//go:build linux

package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const mutationGuardPrefix = "mutation-"

type mutationAckMode uint8

const (
	mutationAckAfter mutationAckMode = iota + 1
	mutationAckDispatch
)

type mutationInvocation struct {
	executable  string
	commandArgs []string
	guard       string
	result      string
	condition   string
	ackMode     mutationAckMode
	cleanup     func()
}

func (r rawTmux) runMutation(ctx context.Context, args ...string) (string, error) {
	mode := mutationAckAfter
	if len(args) > 0 && (args[0] == "kill-window" || args[0] == "kill-pane" || args[0] == "kill-session" || args[0] == "kill-server") {
		mode = mutationAckDispatch
	}
	return r.runGuardedMutation(ctx, false, mode, [][]string{args})
}

func (r rawTmux) runSessionCreation(ctx context.Context, args ...string) (string, error) {
	return r.runGuardedMutation(ctx, true, mutationAckAfter, [][]string{args})
}

func (r rawTmux) runGuardedMutation(ctx context.Context, bootstrap bool, mode mutationAckMode, commands [][]string) (string, error) {
	invocation, err := r.newMutationInvocationCommands(ctx, bootstrap, mode, commands)
	if err != nil {
		return "", err
	}
	defer invocation.cleanup()
	output, commandErr := r.runCommand(ctx, invocation.executable, invocation.commandArgs, commands[0])
	if ctx.Err() != nil {
		return "", commandErr
	}
	if mode == mutationAckAfter && commandErr != nil {
		return "", commandErr
	}
	status, err := awaitMutationResult(ctx, invocation.result, mode)
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("tmux mutation guard: exit status %d", status)
	}
	if mode == mutationAckDispatch {
		return output, nil
	}
	return output, commandErr
}

func (r rawTmux) runInteractiveMutation(ctx context.Context, output, errorOutput io.Writer, args ...string) error {
	invocation, err := r.newMutationInvocation(ctx, false, args...)
	if err != nil {
		return err
	}
	defer invocation.cleanup()
	if err := r.runInteractiveCommand(ctx, invocation.executable, invocation.commandArgs, args, output, errorOutput); err != nil {
		return err
	}
	status, err := awaitMutationResult(ctx, invocation.result, mutationAckAfter)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("tmux mutation guard: exit status %d", status)
	}
	return nil
}

func (r rawTmux) newMutationInvocation(ctx context.Context, bootstrap bool, args ...string) (*mutationInvocation, error) {
	mode := mutationAckAfter
	if len(args) > 0 && (args[0] == "kill-window" || args[0] == "kill-pane" || args[0] == "kill-session" || args[0] == "kill-server") {
		mode = mutationAckDispatch
	}
	return r.newMutationInvocationCommands(ctx, bootstrap, mode, [][]string{args})
}

func (r rawTmux) newMutationInvocationCommands(ctx context.Context, bootstrap bool, mode mutationAckMode, commands [][]string) (*mutationInvocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := mutationGuardDirectory()
	if err != nil {
		return nil, err
	}
	if err := cleanStaleMutationGuards(directory); err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate tmux mutation nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	guard := filepath.Join(directory, mutationGuardPrefix+nonce)
	result := guard + ".result"
	file, err := os.OpenFile(guard, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create tmux mutation guard: %w", err)
	}
	started, alive := linuxProcessStart(os.Getpid())
	if !alive {
		_ = file.Close()
		_ = os.Remove(guard)
		return nil, errors.New("read tmux mutation owner start identity")
	}
	if _, err := fmt.Fprintf(file, "%d %s %s\n", os.Getpid(), started, nonce); err != nil {
		_ = file.Close()
		_ = os.Remove(guard)
		return nil, fmt.Errorf("write tmux mutation guard: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(guard)
		return nil, fmt.Errorf("close tmux mutation guard: %w", err)
	}
	resultFile, err := os.OpenFile(result, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(guard)
		return nil, fmt.Errorf("create tmux mutation result: %w", err)
	}
	if err := resultFile.Close(); err != nil {
		_ = os.Remove(guard)
		_ = os.Remove(result)
		return nil, fmt.Errorf("close tmux mutation result: %w", err)
	}
	condition := mutationOwnerCondition(guard, nonce)
	target := mutationCommandTarget(commands[0])
	tmuxArgs := guardedTmuxArgs(condition, tmuxCommandSequence(commands), guard, result, mode, target, bootstrap)
	removeOnCancel := context.AfterFunc(ctx, func() {
		_ = os.Remove(guard)
		_ = os.Remove(result)
	})
	return &mutationInvocation{
		executable: r.executable, commandArgs: r.commandArgs(tmuxArgs), guard: guard, result: result, condition: condition, ackMode: mode,
		cleanup: func() {
			removeOnCancel()
			_ = os.Remove(guard)
			_ = os.Remove(result)
		},
	}, nil
}

func tmuxCommandSequence(commands [][]string) string {
	parts := make([]string, len(commands))
	for i, command := range commands {
		parts[i] = tmuxCommandString(command)
	}
	return strings.Join(parts, " ; ")
}

func guardedTmuxArgs(condition, command, guard, result string, mode mutationAckMode, target string, bootstrap bool) []string {
	successScript := "test ! -e " + quoteTmuxArgument(guard) + " || printf '0\\n' > " + quoteTmuxArgument(result)
	failureScript := "test ! -e " + quoteTmuxArgument(guard) + " || printf '125\\n' > " + quoteTmuxArgument(result)
	failure := tmuxCommandString([]string{"run-shell", "-b", failureScript})
	var authorized string
	if mode == mutationAckDispatch {
		ack := tmuxCommandString([]string{"run-shell", "-t", target, successScript})
		authorized = ack + " ; " + command
	} else {
		ack := tmuxCommandString([]string{"run-shell", "-b", successScript})
		authorized = command + " ; " + ack
	}
	args := []string{"if-shell", condition, authorized, failure}
	if bootstrap {
		args = append([]string{"start-server", ";"}, args...)
	}
	return args
}

func mutationCommandTarget(args []string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-t" {
			return args[index+1]
		}
	}
	return ""
}

func awaitMutationResult(ctx context.Context, path string, mode mutationAckMode) (int, error) {
	timeout := 5 * time.Second
	if mode == mutationAckDispatch {
		timeout = 500 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		value, err := os.ReadFile(path)
		if err == nil && len(value) > 0 {
			status, parseErr := strconv.Atoi(strings.TrimSpace(string(value)))
			if parseErr != nil || (status != 0 && status != 125) {
				return 0, errors.New("invalid tmux mutation acknowledgement")
			}
			return status, nil
		}
		poll := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			poll.Stop()
			return 0, ctx.Err()
		case <-deadline.C:
			poll.Stop()
			return 0, errors.New("tmux mutation acknowledgement timed out")
		case <-poll.C:
		}
	}
}

func mutationOwnerCondition(guard, nonce string) string {
	return "record=$(cat " + quoteTmuxArgument(guard) + " 2>/dev/null) || exit 1; " +
		"set -- $record; [ \"$#\" -eq 3 ] || exit 1; owner=$1; started=$2; token=$3; " +
		"[ \"$token\" = " + quoteTmuxArgument(nonce) + " ] || exit 1; " +
		"case $owner:$started in *[!0-9:]*|:*) exit 1;; esac; " +
		"stat=$(cat \"/proc/$owner/stat\" 2>/dev/null) || exit 1; " +
		"rest=${stat##*) }; set -- $rest; [ \"${20}\" = \"$started\" ]"
}

func mutationGuardDirectory() (string, error) {
	directory := filepath.Join(os.TempDir(), "op-tmux-guards-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create tmux mutation guard directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect tmux mutation guard directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("tmux mutation guard directory is not a private owned directory")
	}
	return directory, nil
}

func cleanStaleMutationGuards(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("list tmux mutation guards: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), mutationGuardPrefix) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if strings.HasSuffix(path, ".result") {
			if _, err := os.Stat(strings.TrimSuffix(path, ".result")); errors.Is(err, os.ErrNotExist) {
				_ = os.Remove(path)
			}
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		record, readErr := os.ReadFile(path)
		pid, started, _, parseErr := parseMutationGuardRecord(string(record))
		if readErr == nil && parseErr == nil {
			current, alive := linuxProcessStart(pid)
			if !alive || current != started {
				_ = os.Remove(path)
				_ = os.Remove(path + ".result")
			}
			continue
		}
		// Do not race another invocation before its owner writes the record.
		if time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(path)
			_ = os.Remove(path + ".result")
		}
	}
	return nil
}

func parseMutationGuardRecord(value string) (int, string, string, error) {
	fields := strings.Fields(value)
	if len(fields) != 3 || len(fields[2]) != 32 {
		return 0, "", "", errors.New("invalid mutation guard record")
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, "", "", errors.New("invalid mutation guard owner")
	}
	if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
		return 0, "", "", errors.New("invalid mutation guard start identity")
	}
	if _, err := hex.DecodeString(fields[2]); err != nil {
		return 0, "", "", errors.New("invalid mutation guard nonce")
	}
	return pid, fields[1], fields[2], nil
}

func linuxProcessStart(pid int) (string, bool) {
	value, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	closing := strings.LastIndex(string(value), ") ")
	if closing < 0 {
		return "", false
	}
	fields := strings.Fields(string(value)[closing+2:])
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}
