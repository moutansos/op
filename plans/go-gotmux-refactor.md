# Go and gotmux Refactor Plan

Status: proposed

The gotmux claims in this plan were verified empirically against `gotmux v0.5.0` and `tmux 3.4` on
Linux, not read from documentation. Findings that contradict the library's apparent API are marked
**Verified** and must not be "simplified" back to the obvious-looking call.

## Objective

Replace the C and PowerShell implementations with one Go binary that:

- Owns project discovery, repository operations, command execution, and tmux orchestration.
- Uses `github.com/GianlucaP106/gotmux/gotmux` instead of invoking `tmux` directly throughout the
  application.
- Creates or reconciles a main tmux session whose first window and pane run an `op` dashboard TUI.
- Embeds fzf-style project filtering and selection in one section of that dashboard, without
  requiring the external `fzf` executable.
- Displays host and tmux-owned process statistics in the dashboard.
- Exposes an authenticated server API for remotely cloning repositories and opening projects in new
  tmux windows.
- Retires `Open-Project.ps1`, `native/`, and `scripts/New-GitWorktree.ps1` after the Go
  implementation reaches parity.

The tmux executable remains a runtime dependency. gotmux is a typed Go wrapper around tmux, not a
replacement for the tmux server.

## Current Baseline

The migration needs to account for behavior currently split across two implementations:

- `Open-Project.ps1` is the Windows, WSL, and fallback implementation. It loads `config.json`, lists
  projects and custom entries, delegates both selections to external `fzf`, clones and initializes
  repositories, launches editors and shells, runs custom commands, and manages the `code` tmux
  session.
- `native/main.c` is a Linux-focused partial port. It repeats config, path, process, git, fzf, and
  tmux logic. It still invokes the PowerShell worktree script, so the native path is not independent
  of PowerShell.
- `native/configlib.c` contains a custom JSON parser that can be replaced with `encoding/json` and
  explicit validation.
- `native/fzflib.c` forks the external `fzf` process. The dashboard should instead use an in-process
  fuzzy-filtered list.
- `scripts/New-GitWorktree.ps1` creates a branch and sibling worktree. This must become a Go
  repository operation.
- The current tmux shape is a `code` session, one window per project, a main editor pane, and a
  preferred-shell pane resized to 20 rows.
- A newly created `code` session names its initial window `op`, but that pane currently has no
  dashboard application.
- The PowerShell path supports more Windows and WSL launch variants than the C path. The Go runtime
  target must be made explicit instead of retaining two divergent behavior sets.

### Existing behavior this plan must explicitly keep, replace, or retire

These are implemented today and were previously unaddressed. Each needs a decision, not silent
omission:

- **Window-name project targeting** (`Open-Project.ps1:262` `Get-TmuxWindowProjectTarget`, mirrored
  in `native/main.c`). Running `op` from inside a tmux window whose name matches a project skips
  selection and targets that project. This is the newest feature in the tree (commit `e3bddfc`) and
  must survive. In the Go model, `op` run from inside a project window should act on that project
  directly. `--no-target` exists only because WSL cannot reliably detect whether it is inside tmux
  (commits `929b81f`, `2f2f2e0`); running the binary beside tmux should retire the flag's reason to
  exist.
- **`--continuous` and the `<< Exit >>` keyword**. Both implementations support a loop mode that
  re-prompts after each action. The dashboard subsumes this; the flag is retired rather than ported.
- **Custom command placeholders**. `{{path}}` (`Open-Project.ps1:502`) and `{{oproot}}`
  (`Open-Project.ps1:117`) are substituted before execution, and `runInPreferredShell` selects
  between direct execution and a wrapped interactive shell. All three must be preserved by the
  config migration and are currently absent from the target config shape.
- **`customEntries[].paths` contain PowerShell expressions**, not plain paths. The shipped example
  uses `"$env:LOCALAPPDATA/nvim"` and is resolved with `Invoke-Expression`. A generic `${VAR}`
  expander will not parse `$env:NAME`; migration needs an explicit translation rule.
- **`isServer` only gates the VS Code action** (`Open-Project.ps1:411`). It means "headless host, do
  not offer GUI editors" and has nothing to do with the new HTTP server. Reusing the word `server`
  for both is a naming collision to avoid.
- **`cd-here` and `vs`** are host-shell and GUI behaviors. `cd-here` changes the invoking shell's
  working directory, which a dashboard running inside a tmux pane cannot do; it must become "open a
  shell pane here". `vs` opens a discovered `.sln` on Windows and is out of scope for a Linux/WSL
  runtime.
- The existing `config.example.json` has no `customCommands` key even though both implementations
  read it.

## Scope Decisions

Use these defaults unless implementation work establishes a concrete reason to change them:

1. Run the tmux-owning Go binary on Linux or inside WSL, in the same environment as the tmux
   executable. Do not recreate the PowerShell pattern of driving WSL tmux from a Windows-host
   process. Make sure op remains accessible on the command line in windows.
2. Keep `code` as the default session name and `op` as the dashboard window name, but make both
   configurable.
3. Keep the existing config file usable. Add canonical nested Go configuration while accepting the
   persisted `preferedShell` spelling and current top-level keys during migration.
4. Treat project opening and cloning as application use cases shared by the TUI, CLI, and HTTP
   server. Front ends must not implement their own git or tmux flows.
5. Preserve local custom commands, but do not expose arbitrary command execution through the remote
   API unless configured as a global command.
6. Default the API to `127.0.0.1` and recommend SSH port forwarding for remote access. Non-loopback
   listening requires authentication and TLS configuration. Provide a flag for specifying the host,
   port, and optionally an API key.
7. Use asynchronous jobs for clone operations because they can exceed normal HTTP request timeouts.
8. Cut over to Go as soon as parity exists, not after every feature is built. Phases 1 through 4 are
   the cutover; statistics and the remote server are post-cutover work on a single codebase. The
   stated problem is two divergent implementations and a PowerShell dependency on Linux — carrying
   three implementations while building a dashboard, a metrics collector, and an HTTP API would
   extend exactly the condition this rewrite exists to end.

### Cutover MVP

The minimum that justifies deleting the C and PowerShell code:

- Config load and migration, project catalog, git operations, worktree creation (Phases 1-2).
- gotmux session and project-window management (Phase 3).
- A dashboard with in-process fuzzy selection and the core actions, with no statistics panel and no
  server (a reduced Phase 4).

Everything else — process statistics, `op serve`, the remote client — lands afterward. This also
front-loads the gotmux adapter risks below into the first weeks of work rather than the last.

## Target User Experience

### Startup

- Running `op` initializes dependencies, loads configuration, and ensures the main tmux session
  exists.
- On first creation, the session's first window is named `op`, starts in the repository root, and
  runs `op dashboard` in its first pane. The command is sent to the pane after creation rather than
  passed as the session's shell command; see the verified gotmux limitations below.
- If `op` is run outside tmux, it attaches to `code` after reconciliation.
- If `op` is run inside tmux, it switches the current client to `code` rather than nesting tmux.
- Reconciliation is idempotent. Repeated starts do not create duplicate dashboard windows or project
  windows.
- If an existing `code` session predates the Go migration, reconciliation creates the dashboard
  window if missing and moves it to the configured base index without deleting or replacing user
  windows.
- Expected behavior on run of `op` outside: it should ask a project to start/attach to and then
  switch the current terminal session to attach to the `code` session and switch the session to the
  selected project.
- Expected behavior on run on `op` inside a project window: it should run the command runner, which
  should read the configuration and pull in the commands to be run in the current terminal pane.
  Examples of the actions could be to start opencode, cd the shell to the project directory, open
  neovim, open claude code, etc. These will be configurable by the user in the config file.

### Dashboard Layout

Use a responsive Bubble Tea layout:

```text
+ Projects ----------------------+ System --------------------------+
| > filter text                   | CPU  18%   Memory 11.2 / 32 GiB  |
|   repo-a                        | Load 0.8 0.7 0.6   Uptime 4d     |
|   repo-b                        + Processes -----------------------+
|   nvim-config                   | win/pane  pid  command CPU  RSS  |
|                                 | api       1234 op       1%  28M  |
+ Actions / Status ---------------+ code      1290 nvim     6% 220M  |
| enter open  n new  c clone      |                                  |
| / filter    a actions  q detach |                                  |
+---------------------------------+----------------------------------+
```

- The project section uses Bubbles' filtered list behavior for fzf-style matching, keyboard
  navigation, and selection.
- `Enter` opens the selected project using the configured default tmux profile.
- `a` opens a local action chooser for editor, shell, worktree, and configured commands.
- `n` and `c` open inline forms for repository creation and cloning.
- Status and errors are rendered in the TUI instead of corrupting the terminal with direct stdout
  writes.
- Narrow terminals switch to stacked sections. Very small terminals show a clear minimum-size
  message rather than malformed output.
- Quitting or detaching the client must not accidentally kill project windows. Dashboard process
  failure is reported by reconciliation and can be respawned.
- be cognizant of the wdith of the screen. If things get too tight move to a tabbed layout
  switchable with keyboard shortcuts

### Project Window

The default project profile preserves the current useful shape:

- One window named after a collision-safe project display name.
- Main pane starts in the project directory and runs `nvim .`.
- A vertically split pane starts the configured preferred shell in the project directory.
- The shell pane is resized to the configured row count, initially 20.
- The editor pane is reselected after setup.
- Opening an already-open project selects its existing window by default. An explicit `newInstance`
  option permits a second suffixed window.

Profiles should be modeled as data so later configurations can define shell-only, editor-only, or
custom layouts without adding more platform-specific branches.

## Go Project Structure

Start with one binary and internal packages:

```text
cmd/op/main.go
internal/app/service.go
internal/cli/cli.go
internal/config/config.go
internal/git/repository.go
internal/project/catalog.go
internal/process/runner.go
internal/server/http.go
internal/server/jobs.go
internal/stats/collector.go
internal/tmux/manager.go
internal/tui/model.go
internal/tui/projects.go
internal/tui/stats.go
```

Package responsibilities:

- `config`: locate, decode, default, expand, and validate configuration. Return normalized absolute
  paths to callers.
- `project`: list repository directories and custom entries, resolve stable project IDs to paths,
  and validate names stay under the configured repository root.
- `git`: clone, initialize, inspect cleanliness, pull, and create worktrees with
  `exec.CommandContext`; never construct git commands through a shell.
- `process`: launch local editors, shells, and configured commands. Keep command execution separate
  from tmux control.
- `tmux`: the only package allowed to import gotmux. It exposes application-oriented methods such as
  `EnsureMainSession`, `OpenProjectWindow`, `SelectProjectWindow`, and `Snapshot`.
- `stats`: collect immutable metric snapshots independently of rendering.
- `app`: coordinate project, git, process, tmux, and job operations. This is the shared API used by
  every front end.
- `tui`: Bubble Tea models and views only. Long-running work is returned as commands and represented
  as messages.
- `server`: HTTP transport, authentication, input validation, job representation, and response
  encoding. It delegates all work to `app.Service`.
- `cli`: parse subcommands and flags, invoke `app.Service`, and format human-readable or JSON
  results.

Use narrow interfaces at external boundaries so git, gotmux, stats, and clocks can be replaced with
fakes in tests. Avoid creating interfaces for pure data transformations.

## Dependencies

- Pin a tagged gotmux release, initially `github.com/GianlucaP106/gotmux v0.5.0`, and import its
  `gotmux` package. Re-evaluate only through a deliberate dependency update.
- Use Bubble Tea for the Model-Update-View loop and alternate-screen TUI.
- Use Bubbles list and text-input components for embedded filtering and forms, and Lip Gloss for
  layout and styling.
- Use `github.com/shirou/gopsutil/v4` for host and process metrics.
- Prefer the standard library for JSON, HTTP, logging, command execution, configuration decoding,
  and CLI parsing.

gotmux v0.5.0 covers session creation, window creation, pane listing and splitting, selection, and
key sending. Keep all compatibility handling in `internal/tmux`. Where the typed API does not expose
a needed flag or operation, use `Tmux.Command` as the single escape hatch rather than calling
`exec.Command("tmux", ...)` elsewhere.

### Verified gotmux v0.5.0 limitations

Confirmed by running gotmux v0.5.0 against tmux 3.4 on an isolated socket. Three of these five fail
**silently** — they return `nil` errors and plausible objects while doing nothing — so the adapter
cannot trust gotmux return values as evidence that an operation happened.

1. **`ShellCommand` is broken for any multi-word command.** gotmux wraps the value as
   `fmt.Sprintf("'%s'", cmd)` and passes it through `exec.Command`, with no shell to strip the
   quotes. tmux receives a literal `'op dashboard'`, runs `sh -c "'op dashboard'"`, and searches for
   a binary whose name contains a space. The pane dies immediately and takes the session with it.
   Because `-P` prints before the pane dies, `NewSession` returns a non-nil `*Session` and a `nil`
   error. Observed:

   ```text
   NewSession(ShellCommand: "sleep 300")  -> err=<nil>, session non-nil, NO session created
   NewSession(ShellCommand: "zsh")        -> err=<nil>, session created
   SplitWindow(ShellCommand: "sleep 300") -> err=<nil>, NO pane created
   ```

   Single-token commands work. The current split-pane behavior works only because `preferedShell` is
   one word. Any profile command such as `nvim .` silently no-ops.

2. **A `-:-` sequence in any format value panics the process.** gotmux joins format variables with a
   `-:-` separator and calls `log.Panicln("invalid query output")` on a field-count mismatch —
   inside the library, not as a returned error. A window named `we-:-ird` panics `Session.NewWindow`
   before it returns. Window names derive from directory names under `repoDirectory`, so this is
   reachable from ordinary user input, and any embedded newline in a pane title or path does the
   same. Every list and get call routes through the same `collect()`.

3. **`NewTmux(socketPath)` fails when no server is running on that socket.** It validates by running
   `list-clients`, which errors with "no server running". A configured `tmux.socket` therefore
   cannot bootstrap its own server, and integration tests must start a server on the socket before
   constructing the client.

4. **Missing operations.** There is no `resize-pane` and no `respawn-pane` in the v0.5.0 API, both
   of which this plan requires. `Window.Move` emits `move-window` without `-k` and errors when the
   target index is occupied.

5. **Errors carry no diagnostics.** Every call uses `.Output()`, discarding stderr, and returns a
   fixed string such as `errors.New("failed to create window")`. `Tmux.Command` does the same. The
   typed-error goal cannot be built on top of gotmux error values.

Two smaller confirmed behaviors: `Option.Value` returns with a trailing newline (`"dashboard\n"`),
and `Session.Attach()` with nil options leaves stdout unset so the attach fails — always pass
`AttachSessionOptions{Output: os.Stdout, Error: os.Stderr}`. `checkSessionName` rejects `:` and `.`,
so the configurable session name must be validated at config load rather than at first use.

### Required adapter hardening

`internal/tmux` carries more weight than a thin type wrapper. It must:

- **Verify after every mutation.** Re-query for the session, window, or pane and confirm it exists
  and is not dead. A `nil` error from gotmux is not evidence of success. This is the single most
  important rule in the adapter.
- **Use `ShellCommand` only for single-token commands.** Everything else goes through the
  send-literal-then-Enter helper or `Tmux.Command`.
- **Sanitize names** before they reach tmux: reject or rewrite `-:-`, newlines, `:`, and `.`.
- **Wrap every gotmux call in a `recover()` barrier** that converts a library panic into a typed
  error. A panic in a list call would otherwise kill the dashboard or the server process.
- **Shell out directly where error detail matters**, capturing stderr, rather than surfacing
  gotmux's fixed error strings.
- **Trim option values** on read.

If this hardening grows past a comfortable size, vendoring or forking gotmux becomes the cheaper
option; the adapter boundary is what keeps that a local decision.

## Core Domain and Service API

Define stable request and result types before building front ends:

```go
type Project struct {
    ID       string
    Name     string
    Path     string
    Kind     ProjectKind
    GitState GitState
}

type OpenProjectRequest struct {
    ProjectID   string
    Profile     string
    NewInstance bool
}

type CloneRequest struct {
    URL          string
    Directory    string
    OpenOnFinish bool
    Profile      string
}
```

The application service should provide these use cases with `context.Context`:

- `ListProjects`
- `CreateProject`
- `CloneProject`
- `CreateWorktree`
- `OpenProject`
- `RunProjectAction`
- `EnsureMainSession`
- `GetTmuxSnapshot`
- `GetStatsSnapshot`

Rules enforced in the service layer:

- Project IDs resolve through the catalog; callers do not submit arbitrary filesystem paths.
- Repository and worktree destination paths must remain under configured roots after cleaning and
  symlink-aware validation.
- A per-project operation lock prevents duplicate clones, worktrees, and same-name windows from
  concurrent API requests.
- Clone uses a temporary sibling directory and an atomic rename where practical so incomplete
  repositories do not appear as valid projects.
  - NOTE: This method is overkill. If something fails to clone that's fine. Just report the failure
- Window names are normalized for tmux and disambiguated without changing the stable project ID.
- Git pulls occur only when enabled and the worktree is clean, using `git status --porcelain` rather
  than localized human output.
- Every operation returns typed errors that the CLI, TUI, and server map to their own presentation.

## gotmux Integration

### Adapter Boundary

Wrap `*gotmux.Tmux` in a manager rather than exposing gotmux objects to the rest of the codebase.
The manager should support an internal client interface for tests, while the production
implementation uses:

- `gotmux.DefaultTmux` or `gotmux.NewTmux` for the configured socket.
- `Tmux.HasSession`, `Tmux.GetSessionByName`, and `Tmux.NewSession` for session reconciliation.
- `Session.ListWindows`, `Session.NewWindow`, `Session.GetWindowByName`, and window move/select
  operations for project windows.
- `Window.ListPanes`, `Pane.SplitWindow`, `Pane.Select`, and pane IDs for layout construction.
- `Tmux.ListClients`, `Session.ListPanes`, and pane metadata for dashboard and process snapshots.

### Main Session Reconciliation

1. Validate that tmux is installed and that the session name passes `checkSessionName` constraints.
2. Initialize the client. With a configured `tmux.socket`, `NewTmux` fails when no server is running
   there, so start the server first (`tmux -S <path> new-session -d`) and then construct the client.
   `DefaultTmux` has no such problem.
3. Look up the configured session.
4. If absent, create it detached with `SessionOptions{Name, StartDirectory}` and **no**
   `ShellCommand` — `op dashboard` is multi-word and would silently destroy the session (see
   verified limitation 1). Create the session with a plain shell, then start the dashboard in the
   first pane with the send-literal-plus-Enter helper.
5. Re-query the session and confirm it exists before continuing. `NewSession` reports success for
   sessions that were never created.
6. Rename the first window to `op` and tag it with a tmux user option such as `@op-role=dashboard`.
7. If the session exists, locate the dashboard by the user option first and name second. Trim the
   trailing newline from option values before comparing.
8. Create or respawn a missing or dead dashboard pane via `Tmux.Command("respawn-pane", ...)`, then
   place its window at the base index. `Window.Move` fails when that index is occupied, so handle
   the occupied case explicitly with `swap-window` or by leaving the window where it is rather than
   treating the error as fatal.
9. Never delete unrelated windows during reconciliation.
10. Attach or switch the caller's client only after reconciliation succeeds. Attach through
    `AttachSession` with explicit `Output`/`Error` writers.

Do not manually calculate the next free tmux index for normal project creation. Let tmux allocate it
through `Session.NewWindow`, then use the returned `Window` identity. Query the base index only when
positioning the dashboard window.

### Project Window Creation

1. Resolve and validate the project, and normalize its display name for tmux.
2. Check for an existing window tagged with `@op-project-id=<id>`.
3. Create the window detached with its starting directory and final display name.
4. Get its initial pane and start the editor with the adapter's literal-send-plus-Enter helper
   because gotmux v0.5.0 does not expose a shell command on `NewWindowOptions`, and because the
   default `nvim .` is multi-word and cannot use `ShellCommand` anyway.
5. Split from that pane with `SplitWindowOptions{SplitDirection, StartDirectory}`. Pass
   `ShellCommand` only when the configured preferred shell is a single token; otherwise split with
   the default shell and send the command. Then re-list the panes and confirm the split actually
   happened — a failed multi-word split returns `nil`.
6. Identify the new pane, resize it with `Tmux.Command("resize-pane", "-t", id, "-y", rows)` because
   gotmux exposes no resize operation, and reselect the editor pane.
7. Set tmux user options that record project ID, path, profile, and ownership.
8. On partial failure, remove only the window created by this operation and return a typed setup
   error.

gotmux's `Pane.SendKeys` does not add Enter in v0.5.0, so the adapter encapsulates literal text and
Enter as separate operations. `ShellCommand` is not the preferred path despite appearing to be the
structured one: it is unusable for multi-word commands and fails without an error.

## TUI Implementation

### Model

Keep one root model with focused child sections:

- Project list and filter state.
- Action or form overlay state.
- Latest system and process snapshot.
- Current tmux snapshot.
- Active jobs and status messages.
- Terminal dimensions and selected section.

Commands perform all I/O. `Update` should only transform state and schedule commands. Use distinct
messages for project loads, stats refreshes, tmux refreshes, operation progress, completion, and
errors.

### Refresh Behavior

- Refresh host/process stats every two seconds by default.
- Refresh tmux and project snapshots every five seconds so remote changes appear without restarting
  the dashboard.
- Refresh projects immediately after successful create or clone jobs.
- Keep and render the last successful snapshot if a refresh fails.
- Use one in-flight refresh per source and a context timeout to prevent work from accumulating.
- Do not enumerate and sample every host process on every render.

### Filtering and Selection

Use Bubbles' list filtering as the initial fzf-style implementation. Project names, path basenames,
custom-entry labels, and optional tags contribute to the filter value. Keep actions out of the
project list; use key bindings and an action overlay instead of synthetic entries such as
`<< Clone >>`.

The TUI must remain useful when the server is disabled. It calls the same in-process application
service and does not depend on HTTP for local operations.

## Process Statistics

The default process table should focus on work managed by the main tmux session rather than
duplicating a full `top` implementation:

- Read pane IDs, window names, root PIDs, current commands, and paths from gotmux.
- Use gopsutil to walk each pane root's descendants and aggregate CPU percentage and resident memory
  by pane.
- Display window, pane, root PID, foreground command, CPU percentage, RSS, uptime, and dead/alive
  state.
- Display host CPU, virtual-memory usage, load averages where supported, and uptime above the
  process table.
- Allow a later toggle to show top host processes, but do not make global process enumeration an MVP
  requirement.

Sampling details:

- Build CPU deltas across snapshots; the first sample may show unavailable values rather than
  misleading zeroes.
- Compute CPU percentage from `Process.Times()` deltas kept by the collector, or retain the
  `*process.Process` objects between samples. Calling `Percent(0)` on a freshly constructed process
  returns the since-boot average, which is the usual source of wrong numbers in this kind of panel.
- `Pane.Pid` is the pane's shell PID, not the foreground command's, so the aggregate must walk
  descendants to be meaningful.
- Bound descendant traversal and tolerate permission errors and processes disappearing between
  calls.
- Collect off the Bubble Tea update loop and publish one immutable snapshot message.
- Normalize or omit unsupported platform metrics instead of treating them as fatal.
- Unit test aggregation with a fake process tree and deterministic counters.

## Remote-Control Server

### Lifecycle

Add `op serve` as a long-running process. It loads the same config and constructs the same
application service as the TUI. Document a user-level systemd unit for automatic startup, but keep
service-manager files separate from core behavior.

The server and dashboard may run in separate processes, so correctness cannot rely only on an
in-memory mutex. Use filesystem lock files under the application state directory for clone, create,
worktree, and project-window operations, and make tmux operations idempotent through project tags
and post-create checks.

### API

Start with a versioned JSON API:

| Method | Path                          | Purpose                                      |
| ------ | ----------------------------- | -------------------------------------------- |
| `GET`  | `/v1/health`                  | Liveness, version, and dependency status     |
| `GET`  | `/v1/projects`                | List project IDs and state                   |
| `GET`  | `/v1/tmux`                    | Read the managed session, windows, and panes |
| `GET`  | `/v1/jobs/{id}`               | Read async operation state and errors        |
| `POST` | `/v1/projects`                | Initialize a named local repository          |
| `POST` | `/v1/projects/clone`          | Queue a clone and optionally open it         |
| `POST` | `/v1/projects/{id}/open`      | Ensure or create a project window            |
| `POST` | `/v1/projects/{id}/worktrees` | Create a branch and sibling worktree         |

Return `202 Accepted` and a job resource for clone and worktree operations. Return the selected or
created window identity for open operations.

For v1, keep the concurrency story small: a single-flight lock per project ID and in-memory job
state are sufficient for a single-user tool where `op serve` is the only writer. `Idempotency-Key`
handling, bounded job retention for retries, and the filesystem lock files described above are only
warranted once the dashboard and the server actually run as separate writing processes. Add them
when that happens, not before.

Add non-interactive client commands so remote control does not require handwritten curl calls:

```text
op remote projects
op remote clone <url> [--open]
op remote open <project-id> [--profile default]
op remote job <job-id>
```

Include a generated swagger doc and interface exposed at runtime.

### Security

`POST /v1/projects/{id}/open` starts config-defined commands — an editor and an interactive shell —
in a pane on the host. That is remote code execution by design, and no amount of input validation
changes it; the validation below narrows the blast radius but does not make the endpoint safe to
expose. This is acceptable for a personal tool reached over an SSH tunnel, and it is the reason the
listener defaults to loopback. State it plainly in the README rather than implying that a validated
API is a safe one.

- Bind to loopback by default.
- Require a bearer token loaded from `OP_API_TOKEN` or a the config file.
- Apply strict JSON decoding, content-type checks, server read/write/idle timeouts, and bounded
  concurrent jobs.
- Validate clone URL schemes and project names. Reject destination paths, shell commands, tmux
  targets, and arbitrary environment variables from remote callers.
- Use constant-time token comparison and redact authorization data, clone credentials, and URL user
  info from structured logs.
- Keep local custom command execution out of the HTTP API.

## Configuration

Move toward this shape while loading current files during migration:

```json
{
  "repoDirectory": "~/source/repos",
  "preferredShell": "zsh",
  "tmux": {
    "session": "code",
    "dashboardWindow": "op",
    "socket": "",
    "shellPaneRows": 20,
    "defaultProfile": "nvim"
  },
  "stats": {
    "refreshInterval": "2s",
    "tmuxRefreshInterval": "5s"
  },
  "server": {
    "enabled": false,
    "listen": "127.0.0.1:8787",
    "tokenFile": "~/.config/op/server-token",
    "tlsCertFile": "",
    "tlsKeyFile": ""
  },
  "actions": {
    "guiEditors": false
  },
  "customEntries": [],
  "customCommands": []
}
```

`actions.guiEditors` replaces `isServer`, which today only decides whether the VS Code action is
offered. Keeping the word `server` for the HTTP API alone avoids a collision between two settings
that mean opposite things.

Migration behavior:

- Locate config from an explicit `--config`, then the platform user config directory, then the
  current repository-relative location for development.
- Accept current `repoDirectory`, `wslRepoDirectory`, `isServer`, `preferedShell`, `customEntries`,
  and `customCommands` fields for one migration window.
- Normalize `preferedShell` to `preferredShell` in memory and emit a deprecation warning once.
- Ignore `wslRepoDirectory` when running inside Linux or WSL, with a migration note explaining that
  the process now runs beside tmux.
- Map `isServer: true` to `actions.guiEditors: false` and warn once. Do not fold it into the
  `server` block.
- Reject unknown fields only after the example config and migration tooling are updated; initially
  report them as warnings to avoid silently losing user configuration.
- Expand `~` and environment variables without evaluating configuration as shell or PowerShell code.
- Translate the PowerShell `$env:NAME` syntax already present in shipped `customEntries[].paths`
  into environment lookups. A plain `${VAR}` or `$VAR` expander will not match `$env:LOCALAPPDATA`
  and would leave the value broken. Accept both spellings, and normalize on write.
- Preserve `{{path}}` and `{{oproot}}` substitution in `customCommands`, plus the
  `runInPreferredShell` flag. Define `{{oproot}}` explicitly now that config can live outside the
  repository: it resolves to the directory containing the loaded config file, not the binary's
  location. Custom entries continue to carry per-platform `paths`, but only the Linux key is
  consulted by the Linux/WSL runtime.
- Add `customCommands` to `config.example.json`, which currently omits a key both implementations
  read.

## CLI Shape

Keep the default command convenient while providing scriptable subcommands:

```text
op                         ensure and attach/switch to the main session
op dashboard               run only the dashboard TUI in the current pane
op serve                   run the remote-control API
op projects [--json]       list projects
op open <project>          open a local project window
op clone <url> [--open]    clone locally
op new <name> [--open]     initialize a local repository
op worktree <project> <branch>
op remote ...              call a configured remote server
op version
```

Default `op` behavior when run inside tmux: if the current window is tagged `@op-project-id`, or its
name matches a project, act on that project rather than opening the picker. This preserves the
window-name targeting feature and improves on it, since tagged windows no longer depend on the name
surviving a rename. `--no-target` forces the picker.

Retain `--no-repo-update`. Retire `--no-target`'s original justification — it existed because WSL
could not tell whether it was inside tmux, and the binary now runs beside tmux — but keep the flag
as an explicit override. Drop `--continuous`, which the dashboard replaces. Drop `--force-native`,
`--force-powershell`, and the C build-commit-hash mechanism because there will be one
implementation.

## Migration Phases

Phases 1 through 4 are the cutover and end with the C and PowerShell code deleted. Phases 5 through
7 are additive work on a single Go codebase.

### Phase 1: Go Foundation

- Add `go.mod`, `cmd/op`, configuration loading, path normalization, structured logging, version
  metadata, and CLI dispatch.
- Decode the existing config and add tests using `config.example.json` plus migration fixtures.
- Define project, operation, and typed-error models.
- Add dependency checks for git and tmux.

Exit criteria: the Go binary builds on Linux, loads current configuration, lists projects/custom
entries, and reports useful validation errors.

### Phase 2: Repository and Local Action Parity

- Implement project discovery, custom entry resolution, clone, new repository, clean pull, and
  worktree creation in Go.
- Implement local nvim, VS Code, preferred-shell, and custom-command launchers where they are valid
  in the runtime environment.
- Remove the runtime dependency on `scripts/New-GitWorktree.ps1`.
- Add CLI subcommands before introducing the TUI so use cases are independently testable.

Exit criteria: local Go commands cover the non-tmux C workflows and the worktree helper with no
PowerShell source execution.

### Phase 3: gotmux Session and Window Manager

Do this phase early and against real tmux. Three of the five verified gotmux limitations fail
silently, so a fake-only test suite will report success on operations that never happened.

- Add the gotmux adapter and fake client boundary.
- Implement the adapter hardening rules: post-mutation verification, name sanitization, the
  `recover()` barrier, single-token-only `ShellCommand`, option-value trimming, and direct shell-out
  where stderr matters.
- Implement main-session reconciliation and dashboard window tagging, including socket bootstrap
  when `tmux.socket` is configured.
- Implement idempotent project window creation, splitting, resizing via `Tmux.Command`, selection,
  and rollback.
- Add integration tests using an isolated tmux socket or server name so developer sessions are never
  touched. Start a server on the socket before constructing the client.

Exit criteria: a fresh isolated session has the dashboard command running in its first pane
_verified by re-querying tmux_, project windows match the editor-plus-shell layout, and a project
directory named with `-:-` produces a typed error instead of a panic.

### Phase 4: Dashboard TUI and Cutover

Reduced scope: no statistics panel, no server. This phase ends the migration.

- Build the responsive root model and project filtering section.
- Add action selection, create/clone forms, progress, errors, and operation completion handling.
- Add periodic catalog and tmux refreshes.
- Make the first session pane run `op dashboard` and add dead-pane reconciliation.
- Implement window-name and `@op-project-id` targeting for `op` run inside tmux.
- Update `README.md` with install, configuration, tmux, dashboard, and migration instructions.
- Replace the C make flow with standard Go build and release commands, and add CI for formatting,
  vetting, tests, and Linux builds.
- Run the manual acceptance items that do not depend on statistics or the server.
- Delete `Open-Project.ps1`, `native/`, and `scripts/New-GitWorktree.ps1`.

Exit criteria: external `fzf` is not required, project selection and all core local operations work
inside the first pane without blocking rendering, and the repository contains one maintained
implementation with no launch path selecting C or PowerShell code.

### Phase 5: Statistics

- Add host metrics and tmux pane process-tree aggregation.
- Render the system summary and process table with stale/error indicators.
- Add configurable refresh intervals, cancellation, and bounded sampling.
- Verify behavior under rapid process exit, permission errors, and no-load-average platforms.

Exit criteria: CPU and memory values refresh without visible input lag or leaked goroutines, and
process failures do not terminate the dashboard.

### Phase 6: Remote Server and Client

- Add authenticated HTTP handlers, middleware, async jobs, and per-project single-flight locking.
- Add the remote CLI client and JSON response models.
- Add loopback defaults, TLS enforcement, redacted structured logs, and systemd documentation.
- Document plainly that opening a project executes configured commands on the host.
- Make dashboard polling surface projects and windows created remotely.

Exit criteria: an authenticated remote client can list projects, queue a clone, monitor it, and open
the result in a tmux window; unauthorized and unsafe requests are rejected.

### Phase 7: Hardening and Release

- Extend CI with race tests and release builds.
- Exercise the full acceptance suite against a disposable repository root and tmux socket.
- Extend `README.md` with the API and remote client sections.
- Revisit whether cross-process filesystem locks and `Idempotency-Key` are warranted now that the
  dashboard and server both run.

Exit criteria: the full acceptance list passes end to end and the release build is reproducible.

## Verification Strategy

### Unit Tests

- Configuration defaults, old-key migration, validation, path expansion, and config search order.
- Project discovery, stable IDs, custom entries, name/path traversal rejection, and window-name
  normalization.
- Clone name derivation for HTTPS, SSH, SCP-like, trailing-slash, and `.git` URLs.
- Git clean/dirty decisions and worktree command construction using a fake command runner.
- App-service idempotency, per-project locking, rollback, and typed errors.
- Tmux reconciliation and project-window state machines using a fake gotmux-facing client. Fakes
  must be able to model gotmux's silent-success behavior, or the tests will not catch the failures
  that actually occur.
- Name sanitization: `-:-`, newlines, `:`, `.`, spaces, and quotes in project and session names.
- Config migration of `$env:NAME` paths, `{{path}}` and `{{oproot}}` substitution,
  `runInPreferredShell`, and `isServer` to `actions.guiEditors`.
- Stats deltas and process-tree aggregation using fake snapshots.
- Bubble Tea model updates for focus, filtering, resize, success, failure, and stale refresh
  messages.
- HTTP authentication, strict decoding, limits, status codes, idempotency, job transitions, and
  redaction using `httptest`.

### Integration Tests

- Use a unique disposable tmux socket/server and temporary repository directory. Start the tmux
  server on that socket before constructing the gotmux client.
- Verify session creation, first dashboard window placement, user-option tags, window reuse, pane
  split direction, start directories, pane sizing, and cleanup.
- Assert that the dashboard pane is actually alive after `EnsureMainSession`, not merely that the
  call returned without error. This is the regression test for the silent `ShellCommand` failure.
- Open a project whose directory name contains `-:-` and assert a typed error rather than a process
  panic.
- Clone a local bare repository fixture to avoid network dependence.
- Create a real worktree and verify branch/destination behavior.
- Run the API against the real app service with isolated git and tmux dependencies.
- Run `go test -race ./...` for service, jobs, stats, and server concurrency.

### Manual Acceptance

1. With no tmux server, run `op` and confirm `code:op` is the first window and its first pane is the
   dashboard.
2. Filter projects with partial and out-of-order text, navigate, cancel, and open a selection
   without external `fzf` installed.
3. Confirm host metrics and tmux-owned process metrics refresh while keyboard input stays
   responsive.
4. Open a project and confirm nvim and the preferred shell start in the correct directory and pane
   layout.
5. Run `op` repeatedly and confirm no duplicate dashboard or project windows are created.
6. Clone and create repositories from the dashboard and CLI, including names with spaces where
   supported.
7. Create a worktree without PowerShell installed.
8. Start `op serve`, verify unauthenticated requests fail, then remotely clone and open a project
   with the client.
9. Interrupt a clone, restart the service, and confirm partial state is reported or cleaned safely
   rather than listed as a complete project.
10. Restart the dashboard and server independently and confirm both reconcile with existing tmux
    state.

## Risks and Mitigations

| Risk                                                                                       | Mitigation                                                                                                                                                                                                  |
| ------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| gotmux v0.x API gaps or behavior changes                                                   | Pin the version, isolate it in one adapter, cover required operations with isolated tmux integration tests, and use `Tmux.Command` only inside that adapter.                                                |
| gotmux `ShellCommand` silently discards multi-word commands and returns success (verified) | Never pass a multi-word `ShellCommand`; send literal text plus Enter instead, and verify by re-querying tmux after every mutation. Covered by an integration test.                                          |
| gotmux panics the process on `-:-` or newlines in format values (verified)                 | Sanitize all names reaching tmux and wrap every gotmux call in a `recover()` barrier that returns a typed error.                                                                                            |
| gotmux errors carry no stderr, so failures are undiagnosable (verified)                    | Shell out directly from the adapter where error detail matters; do not build typed errors on gotmux error values.                                                                                           |
| Adapter workarounds outgrow their value                                                    | Keep them in `internal/tmux` only; if the boundary keeps growing, vendor or fork gotmux as a local decision that does not touch callers.                                                                    |
| Dashboard process exits and removes the only pane/window                                   | Tag and reconcile the dashboard, detect dead/missing panes, support respawn, and never couple dashboard lifetime to project-window lifetime.                                                                |
| Concurrent TUI and API operations create duplicates                                        | Use project user-option tags and post-create reconciliation, with a per-project single-flight lock. Escalate to filesystem locks and idempotency keys only if the dashboard and server both become writers. |
| Clone credentials leak in logs or job output                                               | Redact URL user info and authorization headers; expose bounded sanitized progress instead of raw command lines.                                                                                             |
| Process collection is expensive or racy                                                    | Scope to tmux pane process trees, sample asynchronously at a bounded interval, cache the last snapshot, and tolerate vanished processes.                                                                    |
| Existing Windows-host workflow loses features                                              | State Linux/WSL runtime support clearly, preserve desktop launchers only when present in that environment, and verify required workflows before deleting PowerShell.                                        |
| Config migration breaks a persisted local setup                                            | Parse current keys, warn on deprecated fields, ship an updated example, and test realistic migration fixtures before cutover.                                                                               |

## Implementation Questions to Confirm

These do not block the foundation, but should be confirmed before finalizing phases 4 through 6:

1. Process scope: default to tmux-owned pane process trees, with an optional host-top view later.

- No need for host process view. Host cpu, memory etc. that's aggregated would be useful

2. Remote transport: default to bearer-authenticated HTTP. We expect to be on a secure internal
   network only
3. Duplicate open behavior: select an existing project window unless the caller explicitly requests
   a new instance.
4. Default project profile: preserve `nvim` plus a 20-row preferred-shell pane.
5. Platform scope: run the full application in Linux/WSL beside tmux rather than controlling WSL
   tmux from a native Windows process. Make sure the op command still works in windows
6. gotmux disposition: start with the pinned upstream release plus the adapter hardening above.
   Revisit vendoring or forking if the workarounds keep growing, since several of the limitations
   are library bugs rather than missing features.
7. Windows-side actions: `vs` and the `nvim-win*` variants have no home in a Linux/WSL runtime.
   Confirm they are genuinely unused before Phase 4 deletes them along with the PowerShell script.

## Definition of Done

### Cutover (end of Phase 4)

- One Go binary implements the dashboard, CLI, repository operations, and gotmux orchestration.
- A new main session always starts with the dashboard running in its first pane, verified by
  re-querying tmux rather than by a `nil` error.
- Embedded fuzzy project selection works without `fzf`.
- Window-name and tag-based project targeting work when `op` is run inside tmux.
- Current configuration has a documented migration path, including `$env:` paths, command
  placeholders, and `isServer`.
- Hostile project names produce typed errors, not panics.
- Automated tests cover service logic, TUI updates, git integration, and isolated tmux behavior.
- `Open-Project.ps1`, `native/`, and `scripts/New-GitWorktree.ps1` are deleted.

### Complete (end of Phase 7)

- Dashboard metrics update safely and remain responsive.
- Remote clone and project-window operations are authenticated, validated, and observable as jobs,
  with the host-execution consequence documented.
- Automated tests additionally cover HTTP security and concurrency under `-race`.
