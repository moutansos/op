package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	actionpolicy "github.com/moutansos/op/internal/action"
	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/notify"
	"github.com/moutansos/op/internal/server"
	"github.com/moutansos/op/internal/tui"
)

func (r *runner) runLocal(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return r.runDefault(ctx)
	}
	switch args[0] {
	case "dashboard":
		return r.runDashboard(ctx, args[1:])
	case "tree":
		return r.runTree(ctx, args[1:])
	case "serve":
		return r.runServe(ctx, args[1:])
	case "notify":
		return r.runNotify(ctx, args[1:])
	case "projects":
		return r.runProjects(ctx, args[1:])
	case "open":
		return r.runOpen(ctx, args[1:])
	case "clone":
		return r.runClone(ctx, args[1:])
	case "new":
		return r.runNew(ctx, args[1:])
	case "worktree":
		return r.runWorktree(ctx, args[1:])
	default:
		return usageError("unknown command %q; run 'op help' for usage", args[0])
	}
}

func (r *runner) runDefault(ctx context.Context) error {
	service, err := r.getService(ctx, "git", "tmux")
	if err != nil {
		return err
	}
	if r.options.LookupEnv("TMUX") != "" && !r.globals.noTarget {
		project, found, err := service.ResolveCurrentProject(ctx)
		if err != nil {
			return err
		}
		if found {
			return r.chooseProjectAction(ctx, service, project)
		}
	}
	if !r.globals.noTarget {
		return r.chooseProjectToOpen(ctx, service)
	}
	ensured, err := service.EnsureMainSession(ctx)
	if err != nil {
		return err
	}
	if ensured.StartDashboard {
		return r.runDashboardTUI(ctx, service)
	}
	return service.AttachOrSwitch(ctx)
}

func (r *runner) chooseProjectToOpen(ctx context.Context, service Service) error {
	projects, err := service.ListProjects(ctx)
	if err != nil {
		return err
	}
	project, err := r.options.SelectProject(ctx, "open project", projects, r.options.Stdin, r.options.Stdout)
	if err != nil {
		return domain.NewError(domain.CodeOf(err), "cli.project", "select project", err)
	}
	if project == nil {
		return nil
	}
	result, err := service.OpenProject(ctx, domain.OpenProjectRequest{ProjectID: project.ID, DeferSelection: true})
	if err != nil {
		return err
	}
	if result.Mode == domain.ProjectOpenModeGUI {
		fmt.Fprintf(r.options.Stdout, "Opened %s with %s.\n", result.Project.Name, result.Profile)
		return nil
	}
	return service.AttachOrSwitchTo(ctx, result.Window.ID)
}

func (r *runner) chooseProjectAction(ctx context.Context, service Service, project domain.Project) error {
	actions := []tui.Action{
		{Name: "nvim", ID: actionpolicy.NvimID},
		{Name: "cd-here", ID: actionpolicy.ShellID},
	}
	if r.config.Actions.GUIEditors {
		actions = append(actions, tui.Action{Name: "vs-code", ID: actionpolicy.CodeID})
	}
	for index, command := range r.config.CustomCommands {
		if err := actionpolicy.ValidateCustomName(command.Name); err != nil {
			return domain.FieldError(domain.ErrorCodeConfig, "cli.action", fmt.Sprintf("customCommands[%d].name", index), err.Error())
		}
		actions = append(actions, tui.Action{Name: strings.ToLower(command.Name), ID: command.Name})
	}
	chosen, err := r.options.SelectAction(ctx, "actions for "+project.Name, actions, r.options.Stdin, r.options.Stdout)
	if err != nil {
		return domain.NewError(domain.CodeOf(err), "cli.action", "select project action", err)
	}
	if chosen == nil {
		return nil
	}
	result, err := service.RunProjectAction(ctx, domain.RunProjectActionRequest{ProjectID: project.ID, Action: chosen.ID})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "Started %s for %s.\n", result.Action, result.Project.Name)
	return nil
}

func (r *runner) runDashboard(ctx context.Context, args []string) error {
	usage := func() { fmt.Fprintln(r.options.Stdout, "Usage: op dashboard") }
	positionals, err := parseFlags("dashboard", args, nil, usage, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("dashboard accepts no arguments")
	}
	service, err := r.getService(ctx, "git", "tmux")
	if err != nil {
		return err
	}
	if _, err := service.EnsureMainSession(ctx); err != nil {
		return err
	}
	return r.runDashboardTUI(ctx, service)
}

func (r *runner) runTree(ctx context.Context, args []string) error {
	usage := func() { fmt.Fprintln(r.options.Stdout, "Usage: op tree") }
	positionals, err := parseFlags("tree", args, nil, usage, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("tree accepts no arguments")
	}
	service, err := r.getService(ctx, "tmux")
	if err != nil {
		return err
	}
	return r.options.RunTreeTUI(ctx, service, tui.Options{
		TmuxRefreshInterval:  r.config.Stats.TmuxRefreshInterval.Duration,
		StatsRefreshInterval: r.config.Stats.RefreshInterval.Duration,
		SnapshotCachePath:    r.snapshotCachePath(),
	})
}

func (r *runner) runDashboardTUI(ctx context.Context, service Service) error {
	openers := make([]tui.ProjectOpener, 0, len(r.config.ProjectOpeners))
	for _, opener := range r.config.ProjectOpeners {
		openers = append(openers, tui.ProjectOpener{ID: opener.ID, Name: opener.Name, Mode: opener.Mode})
	}
	return r.options.RunTUI(ctx, service, tui.Options{
		DefaultProfile:         r.config.Tmux.DefaultProfile,
		ProjectOpeners:         openers,
		ProjectRefreshInterval: r.config.Stats.TmuxRefreshInterval.Duration,
		TmuxRefreshInterval:    r.config.Stats.TmuxRefreshInterval.Duration,
		StatsRefreshInterval:   r.config.Stats.RefreshInterval.Duration,
		SnapshotCachePath:      r.snapshotCachePath(),
	})
}

func (r *runner) runServe(ctx context.Context, args []string) error {
	usage := func() { fmt.Fprintln(r.options.Stdout, "Usage: op serve") }
	positionals, err := parseFlags("serve", args, nil, usage, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("serve accepts no arguments")
	}
	token, err := r.apiToken()
	if err != nil {
		return err
	}
	if token == "" {
		return domain.FieldError(domain.ErrorCodeConfig, "cli.serve", "token", "OP_API_TOKEN or a non-empty server token file is required")
	}
	service, err := r.getService(ctx)
	if err != nil {
		return err
	}
	serveCtx, cancel := r.options.Signals(ctx)
	defer cancel()
	options := server.DefaultOptions()
	options.ListenAddress = r.config.Server.Listen
	options.Token = token
	options.TLSCertFile = r.config.Server.TLSCertFile
	options.TLSKeyFile = r.config.Server.TLSKeyFile
	options.Version = r.options.Version.Version
	if r.config.Notifications.Enabled {
		logger := slog.New(slog.NewTextHandler(r.options.Stderr, nil))
		options.Logger = logger
		notifyService, err := notify.New(notifyOptions(r.config.Notifications, logger))
		if err != nil {
			return err
		}
		if r.config.Notifications.Ingest.Enabled {
			options.NotifyIngest = notifyService.Ingest
		}
		if r.config.Notifications.OpenCode.BaseURL != "" {
			go func() {
				if err := notifyService.WatchOpenCode(serveCtx); err != nil && serveCtx.Err() == nil {
					logger.Error("opencode notification watcher stopped", "err", err)
				}
			}()
		}
	}
	return r.options.RunServer(serveCtx, service, options)
}

func notifyOptions(config config.NotificationsConfig, logger *slog.Logger) notify.Options {
	providers := make([]notify.ProviderConfig, 0, len(config.Providers))
	for _, provider := range config.Providers {
		if !provider.Enabled {
			continue
		}
		providers = append(providers, notify.ProviderConfig{
			Type:       provider.Type,
			WebhookURL: provider.WebhookURL,
			URL:        provider.URL,
			Method:     provider.Method,
			Headers:    provider.Headers,
			Token:      provider.Token,
			MaxHops:    provider.MaxHops,
			Timeout:    provider.Timeout.Duration,
		})
	}
	return notify.Options{
		Debounce:          config.Debounce.Duration,
		IgnoreDirectories: config.IgnoreDirectories,
		OpenCode: notify.OpenCodeConfig{
			BaseURL:        config.OpenCode.BaseURL,
			DesktopBaseURL: config.OpenCode.DesktopBaseURL,
			Username:       config.OpenCode.Username,
			Password:       config.OpenCode.Password,
		},
		Providers: providers,
		Logger:    logger,
	}
}

func (r *runner) runNotify(ctx context.Context, args []string) error {
	usage := func() {
		fmt.Fprintln(r.options.Stdout, "Usage: op notify install-claude|install-grok|install-codex|install-copilot [--source DIR] [--target DIR]")
	}
	if len(args) == 0 {
		return usageError("notify requires a subcommand")
	}
	kind, ok := notifyInstallKind(args[0])
	if !ok {
		if args[0] == "--help" || args[0] == "-h" {
			usage()
			return errHelp
		}
		return usageError("unknown notify subcommand %q", args[0])
	}
	var source, target string
	positionals, err := parseFlags("notify", args[1:], map[string]optionKind{"--source": valueOption, "--target": valueOption}, usage, func(set *flag.FlagSet) {
		set.StringVar(&source, "source", "", "plugin source directory")
		set.StringVar(&target, "target", "", "install target directory")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("notify %s accepts no arguments", args[0])
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	summary, err := notify.InstallPlugin(notify.InstallOptions{
		Kind:        kind,
		SourceDir:   source,
		TargetDir:   target,
		HomeDir:     home,
		CopilotHome: strings.TrimSpace(r.options.LookupEnv("COPILOT_HOME")),
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(r.options.Stdout, summary)
	fmt.Fprintln(r.options.Stdout)
	fmt.Fprintln(r.options.Stdout, notifyInstallNextSteps(kind, r.config.Server.Listen))
	return nil
}

func notifyInstallKind(command string) (notify.InstallKind, bool) {
	switch command {
	case "install-claude":
		return notify.InstallClaude, true
	case "install-grok":
		return notify.InstallGrok, true
	case "install-codex":
		return notify.InstallCodex, true
	case "install-copilot":
		return notify.InstallCopilot, true
	default:
		return "", false
	}
}

func notifyInstallNextSteps(kind notify.InstallKind, listen string) string {
	if listen == "" {
		listen = "127.0.0.1:8787"
	}
	url := "http://" + listen
	switch kind {
	case notify.InstallClaude:
		return "Next steps:\n  1. Enable notifications.ingest and run op serve\n  2. Restart Claude Code or run /reload-plugins\n  3. Set the plugin notifier URL to " + url + " and the token to the op API token"
	case notify.InstallGrok:
		return "Next steps:\n  1. Enable notifications.ingest and run op serve\n  2. Restart Grok or run /plugins reload\n  3. export OC_NOTIFIER_URL=" + url + " OC_NOTIFIER_TOKEN=<op API token>"
	case notify.InstallCodex:
		return "Next steps:\n  1. Enable notifications.ingest and run op serve\n  2. In Codex, run /hooks and trust the oc-notifier Stop + PermissionRequest hooks\n  3. export OC_NOTIFIER_URL=" + url + " OC_NOTIFIER_TOKEN=<op API token>"
	default:
		return "Next steps:\n  1. Enable notifications.ingest and run op serve\n  2. Restart Copilot CLI so hooks reload\n  3. export OC_NOTIFIER_URL=" + url + " OC_NOTIFIER_TOKEN=<op API token>"
	}
}

func (r *runner) apiToken() (string, error) {
	if token := strings.TrimSpace(r.options.LookupEnv("OP_API_TOKEN")); token != "" {
		return token, nil
	}
	if r.config.Server.TokenFile == "" {
		return "", nil
	}
	data, err := r.options.ReadFile(r.config.Server.TokenFile)
	if err != nil {
		return "", domain.ResourceError(domain.ErrorCodeConfig, "cli.token", r.config.Server.TokenFile, "read API token file", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (r *runner) runProjects(ctx context.Context, args []string) error {
	var jsonOutput bool
	usage := func() { fmt.Fprintln(r.options.Stdout, "Usage: op projects [--json]") }
	positionals, err := parseFlags("projects", args, map[string]optionKind{"--json": boolOption}, usage, func(set *flag.FlagSet) {
		set.BoolVar(&jsonOutput, "json", false, "write projects as JSON")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("projects accepts no arguments")
	}
	service, err := r.getService(ctx, "git")
	if err != nil {
		return err
	}
	projects, err := service.ListProjects(ctx)
	if err != nil {
		return err
	}
	if jsonOutput {
		if projects == nil {
			projects = []domain.Project{}
		}
		return writeJSON(r.options.Stdout, projects)
	}
	writer := tabwriter.NewWriter(r.options.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tKIND\tPATH")
	for _, project := range projects {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", project.ID, project.Name, project.Kind, project.Path)
	}
	return writer.Flush()
}

func (r *runner) runOpen(ctx context.Context, args []string) error {
	var profile string
	var newInstance bool
	usage := func() {
		fmt.Fprintln(r.options.Stdout, "Usage: op open <project ID or exact name> [--profile NAME] [--new-instance]")
	}
	positionals, err := parseFlags("open", args, map[string]optionKind{"--profile": valueOption, "--new-instance": boolOption}, usage, func(set *flag.FlagSet) {
		set.StringVar(&profile, "profile", "", "project opener profile")
		set.BoolVar(&newInstance, "new-instance", false, "create another project window")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return usageError("open requires exactly one project ID or exact name")
	}
	dependencies := []string{"git"}
	if r.profileUsesTmux(profile) {
		dependencies = append(dependencies, "tmux")
	}
	service, err := r.getService(ctx, dependencies...)
	if err != nil {
		return err
	}
	project, err := resolveProject(ctx, service, positionals[0])
	if err != nil {
		return err
	}
	result, err := service.OpenProject(ctx, domain.OpenProjectRequest{ProjectID: project.ID, Profile: profile, NewInstance: newInstance})
	if err != nil {
		return err
	}
	if result.Mode == domain.ProjectOpenModeGUI {
		fmt.Fprintf(r.options.Stdout, "Opened %s with %s", result.Project.Name, result.Profile)
		fmt.Fprintln(r.options.Stdout)
		return nil
	}
	fmt.Fprintf(r.options.Stdout, "Opened %s in window %s", result.Project.Name, result.Window.Name)
	if result.Reused {
		fmt.Fprint(r.options.Stdout, " (existing)")
	}
	fmt.Fprintln(r.options.Stdout)
	return nil
}

func (r *runner) runClone(ctx context.Context, args []string) error {
	var directory, profile string
	var open bool
	usage := func() {
		fmt.Fprintln(r.options.Stdout, "Usage: op clone <url> [--directory NAME] [--open] [--profile NAME]")
	}
	positionals, err := parseFlags("clone", args, map[string]optionKind{"--directory": valueOption, "--open": boolOption, "--profile": valueOption}, usage, func(set *flag.FlagSet) {
		set.StringVar(&directory, "directory", "", "destination directory name")
		set.BoolVar(&open, "open", false, "open after cloning")
		set.StringVar(&profile, "profile", "", "project opener profile")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return usageError("clone requires exactly one repository URL")
	}
	if err := validateProfileOption(profile, open, "cli.clone"); err != nil {
		return err
	}
	dependencies := []string{"git"}
	if open && r.profileUsesTmux(profile) {
		dependencies = append(dependencies, "tmux")
	}
	service, err := r.getService(ctx, dependencies...)
	if err != nil {
		return err
	}
	result, err := service.CloneProject(ctx, domain.CloneRequest{URL: positionals[0], Directory: directory, OpenOnFinish: open, Profile: profile})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "Cloned %s to %s.\n", result.Project.Name, result.Project.Path)
	return nil
}

func (r *runner) runNew(ctx context.Context, args []string) error {
	var profile string
	var open bool
	usage := func() { fmt.Fprintln(r.options.Stdout, "Usage: op new <name> [--open] [--profile NAME]") }
	positionals, err := parseFlags("new", args, map[string]optionKind{"--open": boolOption, "--profile": valueOption}, usage, func(set *flag.FlagSet) {
		set.BoolVar(&open, "open", false, "open after creating")
		set.StringVar(&profile, "profile", "", "project opener profile")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return usageError("new requires exactly one project name")
	}
	if err := validateProfileOption(profile, open, "cli.new"); err != nil {
		return err
	}
	dependencies := []string{"git"}
	if open && r.profileUsesTmux(profile) {
		dependencies = append(dependencies, "tmux")
	}
	service, err := r.getService(ctx, dependencies...)
	if err != nil {
		return err
	}
	result, err := service.CreateProject(ctx, domain.CreateProjectRequest{Name: positionals[0], OpenOnFinish: open, Profile: profile})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "Created %s at %s.\n", result.Project.Name, result.Project.Path)
	return nil
}

func (r *runner) runWorktree(ctx context.Context, args []string) error {
	var directory, profile string
	var open bool
	usage := func() {
		fmt.Fprintln(r.options.Stdout, "Usage: op worktree <project> <branch> [--directory NAME] [--open] [--profile NAME]")
	}
	positionals, err := parseFlags("worktree", args, map[string]optionKind{"--directory": valueOption, "--open": boolOption, "--profile": valueOption}, usage, func(set *flag.FlagSet) {
		set.StringVar(&directory, "directory", "", "worktree directory name")
		set.BoolVar(&open, "open", false, "open after creating")
		set.StringVar(&profile, "profile", "", "project opener profile")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return usageError("worktree requires a project and branch")
	}
	if err := validateProfileOption(profile, open, "cli.worktree"); err != nil {
		return err
	}
	dependencies := []string{"git"}
	if open && r.profileUsesTmux(profile) {
		dependencies = append(dependencies, "tmux")
	}
	service, err := r.getService(ctx, dependencies...)
	if err != nil {
		return err
	}
	project, err := resolveProject(ctx, service, positionals[0])
	if err != nil {
		return err
	}
	result, err := service.CreateWorktree(ctx, domain.CreateWorktreeRequest{
		ProjectID: project.ID, Branch: positionals[1], Directory: directory, OpenOnFinish: open, Profile: profile,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "Created worktree %s at %s.\n", result.Project.Name, result.Project.Path)
	return nil
}

func validateProfileOption(profile string, open bool, op string) error {
	if profile != "" && !open {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "profile", "requires --open")
	}
	return nil
}

func (r *runner) profileUsesTmux(profile string) bool {
	if profile == "" {
		profile = r.config.Tmux.DefaultProfile
	}
	for _, opener := range r.config.ProjectOpeners {
		if opener.ID == profile {
			return opener.Mode == domain.ProjectOpenModeTmux
		}
	}
	return false
}

func resolveProject(ctx context.Context, service domain.Service, value string) (domain.Project, error) {
	projects, err := service.ListProjects(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	for _, project := range projects {
		if project.ID == value {
			return project, nil
		}
	}
	for _, project := range projects {
		if project.Name == value {
			return project, nil
		}
	}
	return domain.Project{}, domain.ResourceError(domain.ErrorCodeNotFound, "cli.resolve_project", value, "project ID or exact name was not found", nil)
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
