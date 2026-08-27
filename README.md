# op

`op` is a project manager for Linux and WSL. The Linux binary discovers repositories, performs Git
operations, manages a tmux workspace, renders an in-process fuzzy project dashboard, reports host
and tmux-owned process statistics, optionally exposes an authenticated remote-control API, and can
notify you when coding-agent sessions go idle or need input.

The managed workspace defaults to a tmux session named `code`. Its first window, `op`, runs the
dashboard. Project windows contain an editor pane and a preferred-shell pane.

## Platform Scope

The full runtime is supported on Linux, including Linux distributions running under WSL. `op`, Git,
and tmux always run together in that Linux environment. The native Windows `op.exe` is a thin proxy:
every invocation from PowerShell, cmd, or Windows Terminal delegates to a Linux `op` in WSL. It does
not run the CLI, app, TUI, Git, or tmux implementation on Windows.

Using `op.exe` requires both binaries: install the Windows proxy on Windows `PATH` and install the
Linux binary in the selected WSL distribution (normally `~/.local/bin/op`, or set `OP_WSL_OP` to its
absolute Linux path). See [Windows-to-WSL delegation](docs/windows-wsl.md).

## Dependencies

Build dependencies:

- Go 1.24.2 or newer.

Linux/WSL runtime dependencies:

- `tmux` (tested with 3.4). `gotmux` v0.5.0 is retained only as a compile-checked compatibility/type
  pin; production execution uses the context-aware adapter-local tmux command layer.
- `git` for discovery state, pulls, clones, repository creation, and worktrees.
- The configured `preferredShell`, `zsh` by default.
- The command configured for the default project opener (`nvim .` by default), plus `nvim` for the
  in-project Neovim action.
- `code` only when `actions.guiEditors` is enabled.

Native Windows proxy dependency:

- Current WSL with `wsl.exe --cd` support, plus a Linux `op` installation in the selected
  distribution. The proxy uses the system directory returned by the Windows API, never
  `SystemRoot` or a `wsl.exe` found on Windows `PATH`.
- The native proxy supports Windows amd64 and arm64. Windows 386 builds are intentionally rejected
  at compile time because Sysnative handling is not implemented.

The external `fzf` executable is not required. Fuzzy filtering is built into the dashboard.

## Install And Build

Build from a checkout:

```sh
go mod download
go build -o op ./cmd/op
install -Dm755 op "$HOME/.local/bin/op"
```

The repository helper can build, test, and run the application from any working directory:

```sh
./build.sh --build --test
./build.sh --run -- projects --json
./build.sh --build-windows
./build.sh --integration
```

Artifacts are written to `.build_output/`. Run `./build.sh --help` for unit, race, integration,
Windows proxy, cleanup, and argument-forwarding options. `VERSION`, `COMMIT`, and `BUILD_DATE` may
override the metadata embedded in builds.

Alternatively, from the checkout, install into `GOBIN` (or `GOPATH/bin`):

```sh
go install ./cmd/op
```

Ensure the installation directory is in `PATH`. Confirm the binary and runtime tools:

```sh
op version
git --version
tmux -V
```

Create the repository root and canonical configuration before first use:

```sh
mkdir -p "$HOME/source/repos" "$HOME/.config/op"
cp config.example.json "$HOME/.config/op/config.json"
op projects
```

To invoke the same Linux workspace from Windows, also build or install `op.exe` on Windows `PATH`:

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -o op.exe ./cmd/op
# Put op.exe on the Windows PATH; keep the Linux op installed inside WSL.
op.exe version
```

`OP_WSL_DISTRO` optionally selects a distribution and `OP_WSL_OP` optionally selects an absolute
Linux binary path. With neither set, the default distribution and its default user, `HOME`, config,
and baseline `PATH` are used. Resolution also checks `~/.local/bin/op`, `/usr/local/bin/op`, and
`/usr/bin/op` without loading shell profiles.

## CLI

Global options may appear before or after the subcommand:

- `--config PATH` loads one explicit configuration file.
- `--no-repo-update` prevents the normal fast-forward pull when a clean repository is opened.
- `--no-target` ignores the current tmux project tag/window name and uses normal default startup.

Commands:

| Command                                                                       | Behavior                                                                                                                                                                                    |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `op`                                                                          | In a random shell, fuzzy-select and open a project. Inside a tagged or matching project window, open its action selector instead.                                         |
| `op dashboard`                                                                | Run the dashboard in the current terminal pane. Normally started by session reconciliation.                                                                                                 |
| `op projects [--json]`                                                        | List discovered projects and custom entries.                                                                                                                                                |
| `op open <id-or-exact-name> [--profile NAME] [--new-instance]`                | Open a project with the default or named configured opener. `--new-instance` applies to tmux openers.                                                                                        |
| `op clone <url> [--directory NAME] [--open] [--profile NAME]`                 | Clone an HTTPS, SSH, or SCP-style Git URL. `--profile` requires `--open`.                                                                                                                   |
| `op new <name> [--open] [--profile NAME]`                                     | Initialize a repository under `repoDirectory`.                                                                                                                                              |
| `op worktree <project> <branch> [--directory NAME] [--open] [--profile NAME]` | Create a new branch and sibling worktree.                                                                                                                                                   |
| `op serve`                                                                    | Start the authenticated HTTP API until interrupted. Hosts notification watching and hook ingest when `notifications` is enabled.                                                            |
| `op notify install-claude\|install-grok\|install-codex\|install-copilot`        | Install hook plugins that forward Claude / Grok / Codex / Copilot events to `op serve`.                                                                                                     |
| `op remote projects`                                                          | List projects from a remote server.                                                                                                                                                         |
| `op remote clone <url> [--directory NAME] [--open] [--profile NAME]`          | Queue a remote clone and print its job JSON.                                                                                                                                                |
| `op remote open <project-id> [--profile NAME] [--new-instance]`               | Open/select a remote project window.                                                                                                                                                        |
| `op remote job <job-id>`                                                      | Read an asynchronous job.                                                                                                                                                                   |
| `op version`                                                                  | Print version, commit, and build date.                                                                                                                                                      |

Use `op help`, `op remote --help`, or `op <command> --help` for concise command usage.

### Default Behavior

From an ordinary shell, plain `op` lists the project catalog in an in-process fuzzy selector. Search
matches project names, path names, full paths, and tags. Selecting a project opens it with the
configured default profile. A tmux opener attaches an outside shell, or switches an existing tmux
client, directly to the selected project window; a GUI opener launches without changing tmux.

When plain `op` is run inside a project window, it first resolves the `@op-project-id` window tag,
then falls back to an exact project/window-name match. It opens an in-process fuzzy selector for
`nvim`, `cd-here`, optional `vs-code`, and configured custom actions. Typing filters immediately;
`up`/`down` or `ctrl+p`/`ctrl+n` navigates, `enter` selects, and `esc`/`ctrl+c` cancels. The selector
uses Bubble Tea/Bubbles, invokes no external `fzf`, and restores the terminal before starting the
selected action. `--no-target` bypasses both targeted project actions and random-shell project
selection, returning to the managed dashboard instead.

Opening an existing project selects its healthy tagged window. `--new-instance` creates a suffixed
additional window. A normal local `open` fast-forward pulls a clean Git worktree before opening it
when its current branch has an upstream. Raw folders, unborn repositories, detached HEADs, branches
without upstreams, dirty worktrees, and custom entries open without pulling. Use `--no-repo-update`
to suppress the pull.

## Dashboard

The dashboard contains Projects, System + Processes, Tmux, and Actions / Status sections. At least
44 columns by 14 rows are required. Wide terminals use two columns, medium terminals stack the
panels, and narrow or short terminals use tabs.

Keys:

| Key                  | Action                                                                                   |
| -------------------- | ---------------------------------------------------------------------------------------- |
| `/`                  | Start fuzzy filtering in the project list. Window focus also re-enters filter mode.      |
| `up`/`down`, `j`/`k` | Navigate the focused list, tmux pane tree, or action chooser.                            |
| `enter`              | Open the selected project, submit a form, or switch to the focused tmux pane.            |
| `a`                  | Choose a configured tmux or GUI project opener.                                          |
| `w`                  | Create a worktree for the selected project.                                              |
| `n`                  | Create and open a repository.                                                            |
| `c`                  | Clone and open a repository.                                                             |
| `tab`, `shift+tab`   | Move between dashboard sections or form fields.                                          |
| `1`, `2`, `3`        | Focus Projects, System, or Tmux.                                                         |
| `r`                  | Refresh all snapshots immediately.                                                       |
| `esc`                | Leave filter mode or cancel an action chooser or form.                                   |
| `q`, `ctrl+c`        | Exit the dashboard to its tmux pane's shell. Plain `op` restarts it on the next invocation. |

Filtering uses project names, path names, full paths, and tags. It is fzf-style matching implemented
with Bubble Tea/Bubbles and does not invoke `fzf`. Click a pane in the Tmux tree, or focus the Tmux
section and press `enter` on the highlighted pane, to switch straight to that pane. The dashboard
requests terminal focus reporting so returning to its window restores the Projects section and filter
mode while preserving the current query. `op` enables tmux focus events when it reconciles the
managed session.

Dashboard search openers are separate from actions available when plain `op` targets an already-open
project. Dashboard `enter` and `a` choose how the project should open; the targeted selector keeps
`nvim`, `cd-here`, optional `vs-code`, and custom commands as actions against the current project. Create,
clone, and worktree forms open their result using the configured default profile.

## Configuration

The canonical file is JSON:

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
  "agents": {
    "enabled": true,
    "quietAfter": "1.2s",
    "idleAfter": "90s",
    "scanLines": 24,
    "definitions": []
  },
  "notifications": {
    "enabled": false,
    "debounce": "3s",
    "ignoreDirectories": [],
    "opencode": {
      "baseUrl": "",
      "desktopBaseUrl": "",
      "username": "",
      "password": ""
    },
    "ingest": {
      "enabled": false
    },
    "providers": []
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
  "projectOpeners": [
    {
      "id": "nvim",
      "name": "Neovim in tmux",
      "mode": "tmux",
      "command": "nvim ."
    },
    {
      "id": "vscode",
      "name": "VS Code",
      "mode": "gui",
      "command": "code {{path}}"
    }
  ],
  "customEntries": [
    {
      "name": "nvim-config",
      "paths": {
        "win": "$env:LOCALAPPDATA/nvim",
        "linux": "~/.config/nvim"
      }
    }
  ],
  "customCommands": [
    {
      "name": "Open opencode",
      "command": "cd {{oproot}} && opencode {{path}}",
      "runInPreferredShell": true
    }
  ]
}
```

See [`config.example.json`](config.example.json) for the maintained example.

### Search Order And Paths

Configuration search order is:

1. `--config PATH`, when provided. A missing explicit file is an error and does not fall through.
2. `<user-config-directory>/op/config.json`. On normal Linux systems this is
   `$XDG_CONFIG_HOME/op/config.json`, or `~/.config/op/config.json` when `XDG_CONFIG_HOME` is unset.
3. `config.json` in the current working directory, intended as a development fallback.

Relative configured paths resolve against the directory containing the loaded configuration file.
Paths support `~`, `$NAME`, `${NAME}`, and legacy PowerShell `$env:NAME` expansion. Missing
variables in active Linux paths are errors. The `paths.win` value is retained for migration, but
only `paths.linux` is used by the Linux/WSL runtime.

`repoDirectory` must exist before listing projects. Its immediate real directories become catalog
entries; directory symlinks are not followed. `customEntries` add explicitly named Linux paths
outside that root.

`preferredShell` may contain an executable plus arguments but not shell operators.
On WSL, `pwsh.exe` and `powershell.exe` are launched through Windows interop and use
`-NoExit -Command` wrapping. The Linux dashboard cannot run inside a Windows `.exe`
shell, so that pane is wrapped with `sh` instead.
`actions.guiEditors` controls whether the `code .` action is offered. `server.enabled` records
configuration intent but does not start a background process; invoke `op serve` or install the
systemd user unit.

Unknown JSON fields currently produce warnings and are ignored. Canonical fields take precedence
over legacy aliases.

### Agent Detection

The dashboard tmux tree reports whether an interactive agent in a pane is working or is blocked
waiting on you.

This cannot be answered from process state. Agents such as opencode and Claude Code multiplex
terminal input and network sockets through a single event loop, so a process blocked on a keystroke
and a process blocked on an API response are identical from outside: sleeping, parked in `ep_poll`,
consuming no CPU. Detection therefore works from two observations:

- **Which process owns the terminal.** `op` reads `tpgid` from `/proc/<pane-pid>/stat`, the
  foreground process group of the pane's tty. Agents are frequently grandchildren of the pane's
  shell, so this is more accurate than walking the pane's child tree, and it costs the same two file
  reads no matter how large that tree is.
- **What the agent has painted.** Pane contents are hashed each sample. A working agent repaints
  continuously because it animates a spinner or streams tokens, so a screen that is byte-identical
  across samples is producing nothing. Quiescence alone cannot separate "waiting at a prompt" from
  "running a slow tool that prints nothing", so a quiet pane is only reported as blocked when a
  recognized prompt or confirmation pattern is visible. An agent's first settled
  screen is its new-session chrome and is reported idle even when a prompt is showing.

Panes are classified as `working`, `awaiting input`, `awaiting approval`, `idle`, `starting`, or
`unknown`. Blocked agents are counted in the Tmux panel title and named on the status line, so they
are visible in every layout. Approval prompts also show the question that is blocking. Clicking a
waiting pane, or pressing `enter` while it is focused in the Tmux section, selects that pane.

Only panes whose foreground process matches a known agent are captured, so the number of `tmux`
calls per refresh scales with the number of agents, not the number of panes.

`agents.enabled` turns the whole feature off, including pane capture. `agents.quietAfter` is how
long a pane must paint nothing before its screen is trusted as settled rather than as a gap between
frames. `agents.idleAfter` is how long an unrecognized quiet pane stays quiet before it is reported
idle instead of assumed to be mid-task. `agents.scanLines` bounds how many trailing non-empty lines
are pattern matched.

`agents.definitions` replaces the built-in profiles for `opencode`, `claude`, `codex`, `aider`,
`gemini`, and `grok`. Each definition takes a `name`, a `match` list of command names compared
against the pane's foreground process, and optional `busyPatterns`, `promptPatterns`, and
`approvalPatterns` regular expressions. Pattern lists are unioned with op's generic patterns rather
than replacing them, so a definition only needs to carry what is specific to that agent.

```json
{
  "name": "opencode",
  "match": ["opencode", "oc"],
  "promptPatterns": ["(?i)ctrl\\+p\\s+commands"]
}
```

`busyPatterns` win over quiescence, because an agent still offering "esc to interrupt" is mid-task
by its own account. `approvalPatterns` win over everything, because a confirmation dialog blocks
whether or not it just finished rendering.

Agent detection is Linux-only. Elsewhere the pane's tmux-reported command still names the agent, but
no foreground PID is resolved.

### Session Notifications

`op serve` can watch OpenCode's `/global/event` SSE stream and accept hook payloads from Claude Code,
Grok, Codex, and Copilot CLI, then push Discord, Microsoft Teams, generic webhook, or parent-instance
notifications when a session becomes idle, asks a question, or needs permission.

This is independent of observational pane detection. SSE reports accurate idle/question/permission
transitions for OpenCode server-mode sessions, including those with no tmux pane. The dashboard
classifier is unchanged.

`notifications.enabled` turns the whole feature off. When enabled, configure at least
`notifications.opencode.baseUrl` or `notifications.ingest.enabled`. Idle notifications from OpenCode
are sent only on a busy-to-idle transition after `notifications.debounce` (3s by default), and are
cancelled if the session goes busy again. Subagent sessions are skipped. `ignoreDirectories`
suppresses any session whose project path is that directory or below it.

Ingest routes share the `op serve` listener and the same bearer token. Plugin forwarders default to
`http://127.0.0.1:8787`; set `OC_NOTIFIER_URL` / `OC_NOTIFIER_TOKEN` (or the Claude plugin user
config) to the serve address and API token.

```sh
op notify install-claude
op notify install-grok
op notify install-codex
op notify install-copilot
```

Providers: `discord`, `msteams`, `webhook`, and `parent` (forwards a normalized payload to another
`op serve` `/v1/notify`, preserving the child hostname and desktop URL).

### Project Openers

`projectOpeners` defines the choices shown by dashboard project search. `tmux.defaultProfile` is the
opener ID used by `enter`, create, clone, worktree, and `op open` when no profile is supplied.

- `mode: "tmux"` creates or selects a managed tmux project window. `command` runs in its main pane;
  the configured preferred-shell pane is still created below it. Windows are reused by project ID
  and opener ID, so the same project can have separate Neovim and opencode windows.
- `mode: "gui"` runs `command` in the project directory without creating or selecting a tmux
  window. Set `runInPreferredShell` only when the command needs shell syntax.
- `{{path}}` and `{{oproot}}` use the same shell-safe substitutions as custom commands. Direct GUI
  commands are split into an executable and arguments without shell evaluation.

For WSL, GUI opener commands can call Windows-visible launchers already available from WSL, for
example `code {{path}}` or a configured Visual Studio helper. A native-Windows Neovim tmux layout can
likewise be represented by a tmux opener command that starts the appropriate Windows executable from
WSL; `op` remains responsible for the tmux layout rather than hardcoding editor-specific variants.

### Custom Commands

Custom command names appear as local project actions. Two placeholders are supported:

- `{{path}}` is the selected project's absolute path.
- `{{oproot}}` is the directory containing the loaded configuration file, not the binary directory
  or repository checkout.

Both substitutions are shell-quoted. With `runInPreferredShell: true`, the substituted command is
interpreted by the configured shell. After the command exits, the preferred shell remains open in
the project directory (`-NoExit` for PowerShell-compatible shells; other shells are restarted after
`-ic` completes). With `false`, the command is split into an executable and arguments without shell
evaluation; operators such as `&&`, pipes, and redirections are rejected.

Names are case-sensitive and must not collide with built-in action IDs: `nvim`, `code`, `shell`,
`worktree`, or the reserved `worktree:<branch>` syntax. Labels that merely contain reserved words,
and numeric names such as `3` or `03`, remain valid. Rename any colliding custom command before
starting `op`; configuration loading rejects it with the corresponding `customCommands[].name`
field.

The optional `global` field is accepted for forward-compatible configuration, but custom commands
are local-only and are not exposed by the HTTP API.

### Legacy Migration

The loader accepts the previous configuration for one migration window and prints warnings:

| Legacy item                                                                       | Migration                                                                                                                                            |
| --------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `preferedShell`                                                                   | Accepted as `preferredShell`; the correctly spelled canonical field wins when both exist.                                                            |
| `isServer: true`                                                                  | Maps to `actions.guiEditors: false`; it never enables the HTTP server. The canonical `actions.guiEditors` field wins.                                |
| `wslRepoDirectory`                                                                | Ignored. Set `repoDirectory` to the Linux/WSL path because `op` now runs beside tmux.                                                                |
| `$env:NAME` paths                                                                 | Expanded without evaluating PowerShell. Prefer `${NAME}` in canonical configuration.                                                                 |
| `customEntries[].paths.win`                                                       | Preserved but unused by the Linux/WSL local runtime; configure `paths.linux`.                                                                        |
| `{{path}}`, `{{oproot}}`                                                          | Preserved with the definitions above.                                                                                                                |
| `runInPreferredShell`                                                             | Preserved; set it for commands that require shell syntax.                                                                                            |
| Custom commands named `nvim`, `code`, `shell`, `worktree`, or `worktree:<branch>` | Rename them; these case-sensitive IDs and syntax are reserved for built-in actions and are rejected during configuration loading.                    |
| `--no-target`                                                                     | Retained as an explicit way to bypass current-window targeting.                                                                                      |
| `--no-repo-update`                                                                | Retained.                                                                                                                                            |
| `--continuous`, `<< Exit >>`                                                      | Retired; the dashboard supplies the persistent interaction loop and `q` exits it.                                                                    |
| `--force-native`, `--force-powershell`                                            | Retired because there is one Go implementation.                                                                                                      |
| `cd-here`                                                                         | Opens the configured shell in the project directory. A child process cannot change its parent shell's directory.                                    |
| `vs`, `nvim-win`, and other Windows launch variants                               | Configure them as named `projectOpeners`; native `op.exe` still delegates orchestration to WSL.                                                     |
| PowerShell worktree helper                                                        | Replaced by `op worktree`, which invokes `git worktree add -b` directly.                                                                             |

After migration, rename old fields to the canonical spellings and remove ignored Windows/WSL fields.

## Tmux Behavior

`op` reconciles rather than replaces the configured session:

- A missing session is created with the dashboard at tmux's `base-index` and tagged with
  `@op-role=dashboard`, `@op-owned=1`, and its verified pane identity in `@op-dashboard-pane` plus
  `@op-dashboard-pid`.
- An existing pre-migration session gains a dashboard without unrelated windows being deleted. If
  the base index is occupied, the windows are swapped.
- A missing, dead, or PID-replaced managed dashboard pane is recreated or respawned; foreground
  editor, shell, and custom-action processes descended from the verified pane shell are left running.
  The dashboard runs as a child of that shell, so exiting it preserves the window and plain `op`
  restarts it in the same managed pane.
- Each project window is tagged with project ID, path, profile, and ownership options.
- The default project layout starts the selected tmux opener command (`nvim .` by default) inside the configured preferred shell in the top/main
  pane and starts the preferred shell in a bottom pane resized to `tmux.shellPaneRows` (20 by
  default). The top shell remains in the project directory after the editor exits normally or
  fails, so the pane and window stay reusable. PowerShell-family shells use `-NoExit`; other shells
  run the editor with `-ic` and then replace it with a fresh preferred shell. The editor pane is
  reselected.
- Healthy project windows are reused by stable ID. Unhealthy owned windows may be replaced;
  user-owned unrelated windows are not removed.
- `tmux.socket` may select an explicit socket path. `op` bootstraps a server on that socket when
  necessary.
- When `tmux.socket` is explicit, current-pane targeting and client switching are rejected if `op`
  was invoked from a different tmux server.
- Outside tmux, the session lock is released after resolving the exact target session/window IDs and
  before the interactive attach blocks. Inside tmux, targeted select/switch/verification remains one
  serialized transaction.

Project/session/window names and paths containing control characters or gotmux's `-:-` query
separator are rejected. Session and dashboard names also cannot contain `:` or `.`.

Existing tmux sessions, windows, and pane paths are read by canonical ID and then by one field per
query. They may contain `-:-`, tabs, spaces, or other delimiter-like text without entering gotmux's
parser.

### gotmux Compatibility Pin

The project pins gotmux v0.5.0 and confines its compile-checked integration boundary to
`internal/tmux`. That release has verified behavior requiring defensive handling:

- Dashboard, preferred-shell-wrapped editor, split-shell, and respawn commands are passed directly
  as shell-command arguments to guarded raw tmux creation mutations, then their foreground process
  is re-queried. The manager never injects commands into an existing pane.
- Query values containing `-:-` or newlines can panic inside gotmux, and its subprocess calls do not
  accept contexts. Production queries and mutations therefore use an adapter-local raw compatibility
  layer with strict canonical-ID parsing, single-field reads, stderr capture, and process-bound
  cancellation.
- A custom socket is bootstrapped with the tmux executable before state reconciliation.
- Attach uses the same context-aware layer but receives a grace period to restore terminal state
  before forced termination.
- Mutations, including session bootstrap, are queued behind a live `op` process check using its PID,
  Linux process start identity, and a random nonce. The tmux client is its direct `Pdeathsig` child.
  Ordinary mutations acknowledge after completion; server-destroying mutations acknowledge dispatch
  before execution and are then verified by exact state.
- Every mutation is verified with a fresh tmux query; a nil gotmux error is not treated as proof of
  success.

These caveats are why `tmux` remains a direct runtime dependency and why integration tests use a
real server.

## Statistics

The dashboard samples aggregate host CPU, used/total memory, load averages, and uptime. Its process
panel is scoped to the managed tmux session: for every pane it walks the pane root process and
descendants, then aggregates CPU deltas and resident memory with pane/window identity, command,
uptime, and live/dead state.

The first process sample shows CPU as unavailable until a delta exists. Processes that exit or
cannot be inspected do not fail the whole dashboard. Host/process stats refresh at
`stats.refreshInterval` (2 seconds by default); projects and tmux refresh at
`stats.tmuxRefreshInterval` (5 seconds by default). The last successful snapshot remains visible and
is marked stale if a refresh fails.

## Server And Remote Client

Create a high-entropy token file readable only by your user:

```sh
install -d -m700 "$HOME/.config/op"
umask 077
openssl rand -hex 32 > "$HOME/.config/op/server-token"
op serve
```

`OP_API_TOKEN` takes precedence over `server.tokenFile`. A non-empty token is required even on
loopback, and every `/v1/...` route requires `Authorization: Bearer <token>`. OpenAPI and Swagger
documentation routes are public but do not perform operations.

The listener defaults to `127.0.0.1:8787`. A non-loopback listener, including `0.0.0.0`, requires
all of the following or configuration/startup fails:

- A non-empty bearer token.
- Both `server.tlsCertFile` and `server.tlsKeyFile`.
- Clients using HTTPS and trusting the configured certificate.

TLS certificate and key paths must be configured together. TLS 1.2 or newer is enforced by the
server.

> **Host remote-code-execution warning:** `POST /v1/projects/{id}/open` starts the configured editor
> and shell commands on the server host. This is remote code execution by design. Authentication,
> TLS, and input validation reduce exposure but do not make this endpoint safe for public access.

Keep the server on loopback and use SSH forwarding for remote access:

```sh
# Keep this running on the client machine.
ssh -N -L 8787:127.0.0.1:8787 user@op-host

# Supply the host's token securely on the client.
export OP_REMOTE_URL=http://127.0.0.1:8787
export OP_API_TOKEN='replace-with-the-host-token'
op remote projects
```

Remote connection precedence is `--base-url`, then `OP_REMOTE_URL`, then the configured listener.
Token precedence is `--token`, then `OP_API_TOKEN`, then `server.tokenFile`. The default request
timeout is 30 seconds and can be changed with `--timeout DURATION`. The Windows proxy bridges
`OP_REMOTE_URL` and `OP_API_TOKEN` into WSL through an augmented `WSLENV`; it does not run a separate
native remote client.

Clone and worktree requests are asynchronous and return `202 Accepted`, a job ID, and a `Location`
header. Poll with `op remote job <id>`. The API accepts an optional `Idempotency-Key` for those
requests; reusing it with the same payload returns the original job, while a different payload
conflicts.

### API Routes

| Method | Path                          | Result                                      |
| ------ | ----------------------------- | ------------------------------------------- |
| `GET`  | `/v1/health`                  | Version and project/tmux dependency health. |
| `GET`  | `/v1/projects`                | Project catalog.                            |
| `GET`  | `/v1/tmux`                    | Managed tmux session snapshot.              |
| `GET`  | `/v1/jobs/{id}`               | Clone/worktree job state and result.        |
| `POST` | `/v1/projects`                | Create a local repository (`201 Created`).  |
| `POST` | `/v1/projects/clone`          | Queue a clone (`202 Accepted`).             |
| `POST` | `/v1/projects/{id}/open`      | Open/select a project window (`200 OK`).    |
| `POST` | `/v1/projects/{id}/worktrees` | Queue a branch/worktree (`202 Accepted`).   |
| `POST` | `/v1/notify`                  | Ingest a normalized notification.           |
| `POST` | `/v1/claude-code/hook`        | Ingest a Claude Code hook payload.          |
| `POST` | `/v1/grok-code/hook`          | Ingest a Grok hook payload.                 |
| `POST` | `/v1/codex/hook`              | Ingest a Codex hook payload.                |
| `POST` | `/v1/copilot-cli/hook`        | Ingest a Copilot CLI hook payload.          |

Runtime API documentation:

- OpenAPI JSON: `http://127.0.0.1:8787/openapi.json` or `/v1/openapi.json`.
- Swagger UI: `http://127.0.0.1:8787/docs` or `/swagger`.

The Swagger UI loads its static assets from `unpkg.com`; the OpenAPI JSON remains available without
internet access.

### systemd User Service

An example unit is provided at [`docs/op.service`](docs/op.service). It assumes the binary is at
`~/.local/bin/op`, the config is at `~/.config/op/config.json`, and the token is supplied by
`server.tokenFile`.

```sh
mkdir -p "$HOME/.config/systemd/user"
cp docs/op.service "$HOME/.config/systemd/user/op.service"
systemctl --user daemon-reload
systemctl --user enable --now op.service
systemctl --user status op.service
journalctl --user -u op.service -f
```

Adjust the unit's `PATH` if the configured shell, editor, Git, or tmux is installed elsewhere. To
run the user service after logout, an administrator or the user (where permitted) can run
`loginctl enable-linger "$USER"`.

## Troubleshooting

- **Configuration file not found:** create `~/.config/op/config.json`, pass `--config`, or run from
  a checkout containing `config.json`. The error lists every attempted path.
- **Repository root not found:** create `repoDirectory`; `op` intentionally does not infer or
  silently create the catalog root.
- **Dependency executable not found:** put `git`, `tmux`, the preferred shell, and action programs
  in `PATH`. systemd uses the explicit `PATH` in the example unit.
- **Dashboard says the terminal is too small:** resize to at least 44x14. Use `1`, `2`, and `3` in
  tabbed mode.
- **An editor/shell pane exits during setup:** run the configured command directly and verify it
  remains alive. Project-window creation rolls back if startup cannot be observed.
- **Unexpected Git pull failure:** normal `op open` uses `git pull --ff-only` only for clean,
  upstream-tracking branches. Use `--no-repo-update` when opening offline.
- **Duplicate project name/conflict:** repository directory names and custom-entry names must be
  unique. Use IDs from `op projects` for scripts and remote calls.
- **Unsafe name/path error mentioning `-:-`:** rename the configured directory or value; managed
  inputs retain this compatibility restriction even though existing tmux state is read by the raw
  parser.
- **Server rejects non-loopback configuration:** configure both TLS files and a token, or return
  `server.listen` to `127.0.0.1:8787` and use SSH forwarding.
- **Remote `401 Unauthorized`:** verify exactly one Bearer token is being sent and that the client
  token matches `OP_API_TOKEN` or the server token file without extra whitespace.
- **Swagger page is blank offline:** use `/openapi.json`; Swagger UI assets are fetched from
  `unpkg.com`.
- **Statistics initially show `-` CPU:** process CPU is delta-based and becomes available after the
  second successful sample.
- **Windows proxy cannot find WSL or Linux `op`:** update/install WSL, verify the selected
  distribution, and install this project's Linux binary in a baseline/common location. If another
  program named `op` (notably 1Password) wins resolution, set
  `OP_WSL_OP=/absolute/linux/path/to/op`.
- **Windows `--config` fails:** Linux absolute paths pass through unchanged. Relative and absolute
  Windows paths are resolved against the Windows caller directory and converted with the selected
  distribution's `wslpath`; verify the file is accessible from that distribution.

## Development And Verification

Formatting, vetting, unit tests, race tests, builds, and isolated tmux integration:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
go build -o /tmp/op-linux ./cmd/op
GOOS=windows GOARCH=amd64 go build -o /tmp/op-windows-amd64.exe ./cmd/op
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go test -run '^$' -exec=/bin/true ./...
GOOS=windows GOARCH=arm64 go build -o /tmp/op-windows-arm64.exe ./cmd/op
GOOS=windows GOARCH=arm64 go build ./...
GOOS=windows GOARCH=arm64 go test -run '^$' -exec=/bin/true ./...
OP_TMUX_INTEGRATION=1 go test ./internal/tmux -run '^TestIntegration' -count=1 -v
git diff --check
```

The tmux integration tests require `tmux`. Every test reserves a unique short `/tmp/op-tmux-*`
socket, kills only that explicitly addressed server during cleanup, and never uses the default tmux
socket. Test data remains under `t.TempDir()`; do not move socket paths back into those long names.

CI runs the same classes of checks in [`.github/workflows/ci.yml`](.github/workflows/ci.yml),
installing tmux only for the isolated integration job.

### Release Build Metadata

The Linux main package exposes version, commit, and UTC build date through linker variables. The
Windows proxy delegates `version`, so it reports this Linux build metadata:

```sh
VERSION=v1.0.0
COMMIT=$(git rev-parse --verify HEAD)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
go build -trimpath \
  -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
  -o dist/op ./cmd/op
dist/op version
```

Without release ldflags, `op version` reports `dev`, `unknown`, and `unknown`.
