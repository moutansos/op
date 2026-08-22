package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/moutansos/op/internal/domain"
)

type commandClient struct {
	raw rawTmux
}

type rawTmux struct {
	executable string
	socket     string
}

func newCommandClient(ctx context.Context, config ManagerConfig) (tmuxClient, bool, error) {
	executable, err := exec.LookPath("tmux")
	if err != nil {
		return nil, false, domain.NewError(domain.ErrorCodeDependency, "tmux.new", "tmux executable was not found", err)
	}
	raw := rawTmux{executable: executable, socket: config.Socket}
	bootstrapped := false
	if config.Socket != "" {
		if _, err := raw.run(ctx, "list-sessions"); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, false, domain.NewError(domain.CodeOf(contextErr), "tmux.new", "probe configured socket", contextErr)
			}
			args := []string{"new-session", "-d", "-s", config.Session}
			if config.StartDirectory != "" {
				args = append(args, "-c", config.StartDirectory)
			}
			dashboardPaneCommand, commandErr := buildPersistentShellCommand(config.PreferredShell, config.DashboardCommand)
			if commandErr != nil {
				return nil, false, domain.NewError(domain.ErrorCodeInvalidArgument, "tmux.new", "build dashboard shell command", commandErr)
			}
			args = append(args, dashboardPaneCommand)
			if _, createErr := raw.runSessionCreation(ctx, args...); createErr != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return nil, false, domain.NewError(domain.CodeOf(contextErr), "tmux.new", "bootstrap configured socket", contextErr)
				}
				return nil, false, domain.NewError(domain.ErrorCodeDependency, "tmux.new", "bootstrap configured socket: "+createErr.Error(), createErr)
			}
			if _, verifyErr := raw.run(ctx, "has-session", "-t", config.Session); verifyErr != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return nil, false, domain.NewError(domain.CodeOf(contextErr), "tmux.new", "verify configured socket bootstrap", contextErr)
				}
				return nil, false, domain.NewError(domain.ErrorCodeDependency, "tmux.new", "configured socket bootstrap was not observable", verifyErr)
			}
			bootstrapped = true
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, false, domain.NewError(domain.CodeOf(err), "tmux.new", "initialize tmux client", err)
	}
	return &commandClient{raw: raw}, bootstrapped, nil
}

func newReadOnlyCommandClient(ctx context.Context, config ManagerConfig) (tmuxClient, bool, error) {
	executable, err := exec.LookPath("tmux")
	if err != nil {
		return nil, false, domain.NewError(domain.ErrorCodeDependency, "tmux.snapshot", "tmux executable was not found", err)
	}
	raw := rawTmux{executable: executable, socket: config.Socket}
	if _, err := raw.run(ctx, "has-session", "-t", config.Session); err != nil {
		if ctx.Err() != nil {
			return nil, false, domain.NewError(domain.CodeOf(ctx.Err()), "tmux.snapshot", "probe managed session", ctx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, false, nil
		}
		return nil, false, domain.NewError(domain.ErrorCodeDependency, "tmux.snapshot", "probe managed session", err)
	}
	return &commandClient{raw: raw}, true, nil
}

func (c *commandClient) Session(ctx context.Context, name string) (*sessionState, error) {
	ids, err := c.raw.listIDs(ctx, validateSessionID, "list-sessions", "-F", "#{session_id}")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, nil
		}
		return nil, err
	}
	for _, id := range ids {
		sessionName, err := c.raw.field(ctx, id, "session_name")
		if err != nil {
			return nil, err
		}
		if sessionName != name {
			continue
		}
		attached, err := c.raw.intField(ctx, id, "session_attached", 0, int(^uint(0)>>1))
		if err != nil {
			return nil, err
		}
		return &sessionState{ID: id, Name: sessionName, Attached: attached > 0}, nil
	}
	return nil, nil
}

func (c *commandClient) CreateSession(ctx context.Context, name, directory, shellCommand string) error {
	args := []string{"new-session", "-d", "-s", name}
	if directory != "" {
		args = append(args, "-c", directory)
	}
	if shellCommand != "" {
		args = append(args, shellCommand)
	}
	_, err := c.raw.runSessionCreation(ctx, args...)
	return err
}

func (c *commandClient) ListWindows(ctx context.Context, sessionName string) ([]windowState, error) {
	session, err := c.Session(ctx, sessionName)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return []windowState{}, nil
	}
	ids, err := c.raw.listIDs(ctx, validateWindowID, "list-windows", "-t", session.ID, "-F", "#{window_id}")
	if err != nil {
		return nil, err
	}
	result := make([]windowState, 0, len(ids))
	for _, id := range ids {
		index, err := c.raw.intField(ctx, id, "window_index", 0, int(^uint(0)>>1))
		if err != nil {
			return nil, err
		}
		windowName, err := c.raw.field(ctx, id, "window_name")
		if err != nil {
			return nil, err
		}
		active, err := c.raw.boolField(ctx, id, "window_active")
		if err != nil {
			return nil, err
		}
		result = append(result, windowState{ID: id, Index: index, Name: windowName, Active: active})
	}
	return result, nil
}

func (c *commandClient) CreateWindow(ctx context.Context, sessionName, name, directory, shellCommand string) (string, error) {
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", sessionName, "-n", name}
	if directory != "" {
		args = append(args, "-c", directory)
	}
	if shellCommand != "" {
		args = append(args, shellCommand)
	}
	output, err := c.raw.runMutation(ctx, args...)
	if err != nil {
		return "", err
	}
	windowID, err := createdWindowID(output)
	if err != nil {
		return "", &windowCreationVerificationError{err: err}
	}
	return windowID, nil
}

func createdWindowID(output string) (string, error) {
	if strings.HasSuffix(output, "\r\n") {
		output = strings.TrimSuffix(output, "\r\n")
	} else {
		output = strings.TrimSuffix(output, "\n")
	}
	if output == "" {
		return "", errors.New("tmux new-window returned no window ID")
	}
	if strings.ContainsAny(output, "\r\n") {
		return "", fmt.Errorf("tmux new-window returned multiple lines instead of one window ID")
	}
	if err := validateWindowID(output); err != nil {
		return "", fmt.Errorf("tmux new-window returned malformed window ID: %w", err)
	}
	return output, nil
}

func validateWindowID(windowID string) error {
	return validateCanonicalID(windowID, '@', "window")
}

func validatePaneID(paneID string) error {
	return validateCanonicalID(paneID, '%', "pane")
}

func validateSessionID(sessionID string) error {
	return validateCanonicalID(sessionID, '$', "session")
}

func validateCanonicalID(id string, prefix byte, kind string) error {
	if len(id) < 2 || id[0] != prefix || (len(id) > 2 && id[1] == '0') {
		return fmt.Errorf("expected canonical tmux %s ID", kind)
	}
	for _, character := range id[1:] {
		if character < '0' || character > '9' {
			return fmt.Errorf("expected canonical tmux %s ID", kind)
		}
	}
	return nil
}

func (c *commandClient) RenameWindow(ctx context.Context, windowID, name string) error {
	_, err := c.raw.runMutation(ctx, "rename-window", "-t", windowID, name)
	return err
}

func (c *commandClient) KillWindow(ctx context.Context, windowID string) error {
	_, err := c.raw.runMutation(ctx, "kill-window", "-t", windowID)
	return err
}

func (c *commandClient) WindowExists(ctx context.Context, windowID string) (bool, error) {
	if err := validateWindowID(windowID); err != nil {
		return false, err
	}
	return c.raw.exactIDExists(ctx, windowID, "list-windows", "-a", "-F", "#{window_id}")
}

func (c *commandClient) SelectWindow(ctx context.Context, windowID string) error {
	_, err := c.raw.runMutation(ctx, "select-window", "-t", windowID)
	return err
}

func (c *commandClient) MoveWindow(ctx context.Context, windowID, session string, index int) error {
	_, err := c.raw.runMutation(ctx, "move-window", "-s", windowID, "-t", fmt.Sprintf("%s:%d", session, index))
	return err
}

func (c *commandClient) SwapWindow(ctx context.Context, windowID, session string, index int) error {
	_, err := c.raw.runMutation(ctx, "swap-window", "-s", windowID, "-t", fmt.Sprintf("%s:%d", session, index))
	return err
}

func (c *commandClient) ListPanes(ctx context.Context, windowID string) ([]paneState, error) {
	if err := validateWindowID(windowID); err != nil {
		return nil, err
	}
	output, err := c.raw.run(ctx, paneRecordArgs(windowID)...)
	if err != nil {
		return nil, err
	}
	return parsePaneRecords(output)
}

// paneRecordFields is ordered so that free-form values sit last: parsing keeps
// any stray separator inside the trailing field instead of shifting columns.
var paneRecordFields = []string{
	"pane_id",
	"pane_index",
	"pane_pid",
	"pane_active",
	"pane_dead",
	"pane_at_top",
	"pane_at_bottom",
	"pane_height",
	"pane_current_command",
	"pane_current_path",
}

// paneRecordSeparator must survive tmux format output untouched. tmux escapes
// other control bytes octally (a literal separator would come back as "\037"),
// while tabs pass through verbatim in both the format and the expanded values.
// A tab inside a path is therefore possible, so the free-form path is parsed
// last and keeps any extra separators.
const paneRecordSeparator = "\t"

// paneRecordArgs reads every pane field in one tmux call. Querying fields one
// at a time raced with pane death: a pane that exited between calls produced an
// empty value and failed the whole snapshot.
func paneRecordArgs(windowID string) []string {
	formats := make([]string, len(paneRecordFields))
	for i, name := range paneRecordFields {
		formats[i] = "#{" + name + "}"
	}
	return []string{"list-panes", "-t", windowID, "-F", strings.Join(formats, paneRecordSeparator)}
}

func parsePaneRecords(output string) ([]paneState, error) {
	output = trimOneLineEnding(output)
	if output == "" {
		return []paneState{}, nil
	}
	lines := strings.Split(output, "\n")
	result := make([]paneState, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		pane, err := parsePaneRecord(strings.TrimSuffix(line, "\r"))
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[pane.ID]; duplicate {
			return nil, fmt.Errorf("tmux returned duplicate identity %q", pane.ID)
		}
		seen[pane.ID] = struct{}{}
		result = append(result, pane)
	}
	return result, nil
}

func parsePaneRecord(line string) (paneState, error) {
	values := strings.SplitN(line, paneRecordSeparator, len(paneRecordFields))
	if len(values) != len(paneRecordFields) {
		return paneState{}, fmt.Errorf("tmux returned malformed pane record %q", line)
	}
	pane := paneState{ID: values[0], CurrentCommand: values[8], CurrentPath: values[9]}
	if err := validatePaneID(pane.ID); err != nil {
		return paneState{}, fmt.Errorf("tmux returned malformed identity %q: %w", pane.ID, err)
	}
	index, err := parseIntField("pane_index", values[1], 0, int(^uint(0)>>1))
	if err != nil {
		return paneState{}, err
	}
	pid, err := parseIntField("pane_pid", values[2], 0, int(^uint32(0)>>1))
	if err != nil {
		return paneState{}, err
	}
	active, err := parseBoolField("pane_active", values[3])
	if err != nil {
		return paneState{}, err
	}
	dead, err := parseBoolField("pane_dead", values[4])
	if err != nil {
		return paneState{}, err
	}
	atTop, err := parseBoolField("pane_at_top", values[5])
	if err != nil {
		return paneState{}, err
	}
	atBottom, err := parseBoolField("pane_at_bottom", values[6])
	if err != nil {
		return paneState{}, err
	}
	height, err := parseIntField("pane_height", values[7], 0, int(^uint(0)>>1))
	if err != nil {
		return paneState{}, err
	}
	pane.Index, pane.PID, pane.Active, pane.Dead = index, int32(pid), active, dead
	pane.AtTop, pane.AtBottom, pane.Height = atTop, atBottom, height
	return pane, nil
}

func (c *commandClient) SplitPane(ctx context.Context, paneID, directory, shellCommand string) error {
	args := []string{"split-window", "-v", "-t", paneID}
	if directory != "" {
		args = append(args, "-c", directory)
	}
	if shellCommand != "" {
		args = append(args, shellCommand)
	}
	_, err := c.raw.runMutation(ctx, args...)
	return err
}

func (c *commandClient) ResizePane(ctx context.Context, paneID string, rows int) error {
	_, err := c.raw.runMutation(ctx, "resize-pane", "-t", paneID, "-y", strconv.Itoa(rows))
	return err
}

func (c *commandClient) SelectPane(ctx context.Context, paneID string) error {
	_, err := c.raw.runMutation(ctx, "select-pane", "-t", paneID)
	return err
}

func (c *commandClient) RespawnPane(ctx context.Context, paneID, shellCommand string) error {
	args := []string{"respawn-pane", "-k", "-t", paneID}
	if shellCommand != "" {
		args = append(args, shellCommand)
	}
	_, err := c.raw.runMutation(ctx, args...)
	return err
}

func (c *commandClient) KillPane(ctx context.Context, paneID string) error {
	_, err := c.raw.runMutation(ctx, "kill-pane", "-t", paneID)
	return err
}

func (c *commandClient) PaneExists(ctx context.Context, paneID string) (bool, error) {
	if err := validatePaneID(paneID); err != nil {
		return false, err
	}
	return c.raw.exactIDExists(ctx, paneID, "list-panes", "-a", "-F", "#{pane_id}")
}

func (c *commandClient) SetWindowOption(ctx context.Context, windowID, key, value string) error {
	_, err := c.raw.runMutation(ctx, "set-option", "-w", "-t", windowID, key, value)
	return err
}

func (c *commandClient) WindowOption(ctx context.Context, windowID, key string) (string, bool, error) {
	return c.option(ctx, []string{"show-options", "-qv", "-w", "-t", windowID, key})
}

func (c *commandClient) SetServerOption(ctx context.Context, key, value string) error {
	_, err := c.raw.runMutation(ctx, "set-option", "-s", key, value)
	return err
}

func (c *commandClient) ServerOption(ctx context.Context, key string) (string, bool, error) {
	return c.option(ctx, []string{"show-options", "-qv", "-s", key})
}

func (c *commandClient) SessionOption(ctx context.Context, session, key string) (string, bool, error) {
	return c.option(ctx, []string{"show-options", "-qv", "-t", session, key})
}

func (c *commandClient) CurrentWindow(ctx context.Context, paneID string) (string, error) {
	args := []string{"display-message", "-p"}
	if paneID != "" {
		args = append(args, "-t", paneID)
	}
	args = append(args, "#{window_id}")
	value, err := c.raw.run(ctx, args...)
	if err != nil {
		return "", err
	}
	value = trimOneLineEnding(value)
	if err := validateWindowID(value); err != nil {
		return "", err
	}
	return value, nil
}

func (c *commandClient) CurrentWindowName(ctx context.Context, paneID string) (string, error) {
	args := []string{"display-message", "-p"}
	if paneID != "" {
		args = append(args, "-t", paneID)
	}
	args = append(args, "#{window_name}")
	value, err := c.raw.run(ctx, args...)
	return trimOneLineEnding(value), err
}

func (c *commandClient) ListClients(ctx context.Context) ([]clientState, error) {
	value, err := c.raw.run(ctx, "list-clients", "-F", "#{pane_id}")
	if err != nil {
		return nil, err
	}
	value = trimOneLineEnding(value)
	if value == "" {
		return []clientState{}, nil
	}
	lines := strings.Split(value, "\n")
	clients := make([]clientState, 0, len(lines))
	for _, line := range lines {
		activePane := strings.TrimSuffix(line, "\r")
		if err := validatePaneID(activePane); err != nil {
			return nil, fmt.Errorf("unexpected list-clients pane identity: %w", err)
		}
		filter := fmt.Sprintf("#{==:#{pane_id},%s}", activePane)
		name, err := c.raw.run(ctx, "list-clients", "-f", filter, "-F", "#{client_name}")
		if err != nil {
			return nil, err
		}
		name = trimOneLineEnding(name)
		if name == "" {
			return nil, fmt.Errorf("client for pane %s disappeared", activePane)
		}
		clients = append(clients, clientState{Name: name, ActivePane: activePane})
	}
	return clients, nil
}

func (c *commandClient) ClientSession(ctx context.Context, clientName string) (string, error) {
	clients, err := c.ListClients(ctx)
	if err != nil {
		return "", err
	}
	activePane := ""
	for _, client := range clients {
		if client.Name == clientName {
			activePane = client.ActivePane
			break
		}
	}
	if activePane == "" {
		return "", fmt.Errorf("target client was not found")
	}
	value, err := c.raw.run(ctx, "display-message", "-p", "-c", clientName, "-t", activePane, "#{client_session}")
	return trimOneLineEnding(value), err
}

func (c *commandClient) SwitchClient(ctx context.Context, clientName, session string) error {
	_, err := c.raw.runMutation(ctx, "switch-client", "-c", clientName, "-t", session)
	return err
}

func (c *commandClient) Attach(ctx context.Context, sessionID, windowID string, output, errorOutput io.Writer) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := validateWindowID(windowID); err != nil {
		return err
	}
	return c.raw.runInteractiveMutation(ctx, output, errorOutput, "attach-session", "-t", sessionID+":"+windowID)
}

func (c *commandClient) option(ctx context.Context, args []string) (string, bool, error) {
	value, err := c.raw.run(ctx, args...)
	if err != nil {
		return "", false, err
	}
	value = trimLineEndings(value)
	return value, value != "", nil
}

func (r rawTmux) run(ctx context.Context, args ...string) (string, error) {
	return r.runCommand(ctx, r.executable, r.commandArgs(args), args)
}

func (r rawTmux) commandArgs(args []string) []string {
	commandArgs := make([]string, 0, len(args)+2)
	if r.socket != "" {
		commandArgs = append(commandArgs, "-S", r.socket)
	}
	return append(commandArgs, args...)
}

func (r rawTmux) runCommand(ctx context.Context, executable string, commandArgs, shownArgs []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, executable, commandArgs...)
	configureCommandCancellation(command, false)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", fmt.Errorf("tmux %s: %w", strings.Join(shownArgs, " "), contextErr)
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return stdout.String(), nil
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(shownArgs, " "), err, detail)
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(shownArgs, " "), err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", fmt.Errorf("tmux %s: %w", strings.Join(shownArgs, " "), contextErr)
	}
	return stdout.String(), nil
}

func (r rawTmux) runInteractive(ctx context.Context, output, errorOutput io.Writer, args ...string) error {
	return r.runInteractiveCommand(ctx, r.executable, r.commandArgs(args), args, output, errorOutput)
}

func (r rawTmux) runInteractiveCommand(ctx context.Context, executable string, commandArgs, shownArgs []string, output, errorOutput io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, commandArgs...)
	configureCommandCancellation(command, true)
	command.Stdin = os.Stdin
	command.Stdout = output
	var stderr bytes.Buffer
	if errorOutput == nil {
		command.Stderr = &stderr
	} else {
		command.Stderr = io.MultiWriter(errorOutput, &stderr)
	}
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("tmux %s: %w", strings.Join(shownArgs, " "), contextErr)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("tmux %s: %w: %s", strings.Join(shownArgs, " "), err, detail)
		}
		return fmt.Errorf("tmux %s: %w", strings.Join(shownArgs, " "), err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("tmux %s: %w", strings.Join(shownArgs, " "), contextErr)
	}
	return nil
}

func tmuxCommandString(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteTmuxArgument(arg)
	}
	return strings.Join(quoted, " ")
}

func quoteTmuxArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (r rawTmux) field(ctx context.Context, target, name string) (string, error) {
	value, err := r.run(ctx, fieldArgs(target, name)...)
	if err != nil {
		return "", err
	}
	return trimOneLineEnding(value), nil
}

func fieldArgs(target, name string) []string {
	format := "#{" + name + "}"
	if validatePaneID(target) == nil {
		return []string{"list-panes", "-t", target, "-f", "#{==:#{pane_id}," + target + "}", "-F", format}
	}
	return []string{"display-message", "-p", "-t", target, format}
}

func (r rawTmux) intField(ctx context.Context, target, name string, minimum, maximum int) (int, error) {
	value, err := r.field(ctx, target, name)
	if err != nil {
		return 0, err
	}
	return parseIntField(name, value, minimum, maximum)
}

func parseIntField(name, value string, minimum, maximum int) (int, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, fmt.Errorf("tmux returned invalid %s %q", name, value)
	}
	return int(parsed), nil
}

func (r rawTmux) boolField(ctx context.Context, target, name string) (bool, error) {
	value, err := r.field(ctx, target, name)
	if err != nil {
		return false, err
	}
	return parseBoolField(name, value)
}

func parseBoolField(name, value string) (bool, error) {
	switch value {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("tmux returned invalid %s %q", name, value)
	}
}

func (r rawTmux) listIDs(ctx context.Context, validate func(string) error, args ...string) ([]string, error) {
	output, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	output = trimOneLineEnding(output)
	if output == "" {
		return []string{}, nil
	}
	lines := strings.Split(output, "\n")
	ids := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		id := strings.TrimSuffix(line, "\r")
		if err := validate(id); err != nil {
			return nil, fmt.Errorf("tmux returned malformed identity %q: %w", id, err)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("tmux returned duplicate identity %q", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func trimOneLineEnding(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
}

func (r rawTmux) exactIDExists(ctx context.Context, id string, args ...string) (bool, error) {
	output, err := r.run(ctx, args...)
	if err != nil {
		return false, err
	}
	output = trimLineEndings(output)
	if output == "" {
		return false, nil
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSuffix(line, "\r") == id {
			return true, nil
		}
	}
	return false, nil
}
