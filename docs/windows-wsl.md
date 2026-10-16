# Windows-to-WSL Delegation

The native Windows `op.exe` is only a launcher. Every argument passed from PowerShell, cmd, or
Windows Terminal is delegated to this project's Linux `op` inside WSL. The CLI, configuration
loading, Git operations, tmux orchestration, TUI, server, and remote client all execute in Linux.

## Installation

Both binaries are required:

1. Install the Linux `op` inside the WSL distribution, normally at `~/.local/bin/op`, and ensure its
   baseline `PATH` contains that directory, or rely on the explicit `~/.local/bin/op` fallback.
2. Install the Windows `op.exe` on Windows `PATH`.
3. Install Git, tmux, and the configured editor/shell inside that same WSL distribution.

For example, build the Linux binary from WSL:

```sh
go build -o op ./cmd/op
install -Dm755 op "$HOME/.local/bin/op"
```

Build the proxy from PowerShell or cross-build it from Linux:

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -o op.exe ./cmd/op
```

Go does not need to be installed in WSL. Both binaries can be built from Windows PowerShell, then
the Linux one installed from WSL:

```powershell
$env:CGO_ENABLED = '0'
$env:GOARCH = 'amd64'
$env:GOOS = 'linux'; go build -trimpath -o .build_output/op ./cmd/op
$env:GOOS = 'windows'; go build -trimpath -o .build_output/op.exe ./cmd/op
wsl.exe --cd "$PWD" --exec sh -c 'install -Dm755 .build_output/op "$HOME/.local/bin/op"'
```

Install `wl-clipboard` and `xclip` in the distribution (`sudo apt install wl-clipboard xclip`).
The terminal UI library probes `PATH` for clipboard tools when every `op` process starts. When
none is installed, that probe walks every Windows directory WSL appends to `PATH`, which adds
roughly a second to each invocation.

The Windows proxy supports amd64 and arm64. Windows 386 is deliberately rejected because a 32-bit
process can be redirected away from the native system directory and Sysnative handling is not
implemented.

The binaries should come from compatible source versions. `op.exe version` reports the delegated
Linux binary's version because even help and version requests execute in WSL.

## Selection And Environment

`op.exe` must know which distribution and Linux binary to use before it enters WSL, so these
settings live in a Windows-side file rather than in the Linux `config.json`. The optional file is
`%APPDATA%\op\wsl.json`:

```json
{
  "distro": "Ubuntu-22.04",
  "op": "/home/ben/.local/bin/op"
}
```

| Setting | Variable | Purpose |
| --- | --- | --- |
| `distro` | `OP_WSL_DISTRO` | Selects the WSL distribution passed to `wsl.exe --distribution`. The default WSL distribution is used when unset. |
| `op` | `OP_WSL_OP` | Selects this project's Linux executable. It must be an absolute Linux path. |

A non-empty environment variable overrides the matching setting, which is useful for pointing one
shell at a development build. Both fields are optional. A missing file uses the defaults. Invalid
JSON, a non-string value, or a relative `op` path exits with status 2 and names the file. Unknown
fields only produce a warning.

Setting `op` also skips the `PATH` search below. That search can take a noticeable fraction of a
second when WSL appends many Windows directories to `PATH` and `op` is not on WSL's baseline
`PATH`.

The proxy also forwards these Windows environment variables:

| Variable | Purpose |
| --- | --- |
| `OP_API_TOKEN` | Passed into WSL by augmenting `WSLENV`. |
| `OP_REMOTE_URL` | Passed into WSL by augmenting `WSLENV`. |
| `TMUX`, `TMUX_PANE` | Passed into WSL by augmenting `WSLENV` so `op.exe` inside a tmux pane targets that pane. |

PowerShell example that overrides the file for one session:

```powershell
$env:OP_WSL_OP = '/home/ben/source/repos/op/.build_output/op'
$env:OP_REMOTE_URL = 'https://op.internal.example'
$env:OP_API_TOKEN = 'replace-me'
op.exe projects --json
```

Without an `op` setting, a fixed non-login `/bin/sh -c` resolver checks `command -v op` using WSL's
baseline environment, followed by `$HOME/.local/bin/op`, `/usr/local/bin/op`, and `/usr/bin/op`.
Shell profiles such as `.profile` and `.zshrc` are never loaded, so their output cannot corrupt JSON.
No Windows user value is interpolated into shell source. `wsl.exe` rejects empty arguments, so the
proxy prefixes every argument it passes to the resolver with one character that the resolver
removes; empty arguments such as an unset `op` setting therefore reach Linux unchanged.

Candidates are never executed to determine identity. The resolver reads the executable's ELF magic
and this project's embedded marker before the final `exec`. `.exe` paths, extensionless PE files,
scripts, and unrelated ELF commands named `op` such as the 1Password CLI are rejected. Validation
utilities read only the candidate file and do not inherit stdin for a candidate process.

The proxy does not pass `--user`, so WSL uses the selected distribution's configured default user,
`HOME`, config directory, and baseline environment. Unrelated `WSLENV` entries are retained exactly.
Existing variants of `OP_API_TOKEN`, `OP_REMOTE_URL`, `OP_WSL_PROXY_ACTIVE`, `TMUX`, and `TMUX_PANE`
are removed and clean `/u` (Win32-to-WSL only) entries are appended, preventing inherited `/p` or
`/w` modifiers from changing their values or direction.

## Running op From Windows Shells Inside tmux

Plain `op` inside a project window opens that project's action menu instead of the project picker.
To behave the same from a Windows shell pane (for example a `pwsh.exe` preferred shell) as from a
Linux shell pane, the Linux `op` adds `TMUX` and `TMUX_PANE` to its `WSLENV` when it runs under
WSL. A tmux server started by `op` inherits that, so Windows programs in its panes see both
variables, and `op.exe` forwards them back into WSL.

A tmux server that was already running before `op` started it does not have that `WSLENV`. Either
restart that server (`tmux kill-server`) so `op` starts it, or add the variables in your Linux
shell profile:

```sh
export WSLENV="${WSLENV:+$WSLENV:}TMUX:TMUX_PANE"
```

## Preferred Shell

Set `preferredShell` to `pwsh.exe` (or `powershell.exe`) to use Windows PowerShell inside
WSL tmux panes, as the previous PowerShell implementation did. WSL interop often reports
the pane command as `init` rather than `pwsh.exe`; startup verification accepts that.

The Linux dashboard TUI cannot execute inside Windows PowerShell, so a `pwsh.exe`
preferred shell still wraps the dashboard with `sh`. Project editor wrapping and the
bottom shell pane continue to use `pwsh.exe`.

## Repositories On Windows Drives

`repoDirectory` and custom entries may point at Windows drives mounted into WSL, for example
`/mnt/d/source/repos` for a Dev Drive. `op` reads `/proc/self/mounts` to find those mounts (`9p`
with `aname=drvfs`, or `drvfs`). When `git.exe` is reachable on the WSL `PATH` (the default
`appendWindowsPath` behavior), status, pull, clone, init, and worktree operations below those
mounts use Git for Windows through WSL interop. Everywhere else, and on plain Linux, Linux `git` is
used.

Linux git is a poor fit for Windows drives. Every file access crosses the WSL file-sharing layer,
so a status check on a large repository can take minutes instead of milliseconds. It also cannot
use the Windows credential manager, and it cannot follow worktree links written by Git for
Windows. Git for Windows keeps those repositories exactly as Windows tools see them.

Repository state is refreshed in the background and cached; see "Repository State" in the README.

## Paths And Terminal Behavior

The current Windows working directory is supplied with WSL's documented `--cd` option. This keeps
relative arguments and repository-checkout fallback behavior anchored to the caller's directory.
If WSL reports that `--cd` is unsupported, update WSL.

An explicit `--config` may appear in either form and anywhere before `--`:

```powershell
op.exe --config C:\Users\Ben\op\config.json projects
op.exe projects --config=C:\Users\Ben\op\config.json
```

Absolute Windows config paths are converted by invoking `wslpath -a -u` directly in the selected
distribution. Relative config paths are first resolved against the Windows caller directory with
Windows path semantics and then converted. Linux absolute paths are preserved. Config flags after
`--` are ordinary command arguments and are not converted. Missing, empty, or duplicate config
values retain the normal parser error and exit status.

The final `wsl.exe` process directly inherits stdin, stdout, and stderr. There are no pipes between
the terminal and the delegated command, so tmux attach, Bubble Tea input and resizing, ANSI color,
and redirected JSON retain their normal behavior. Windows Ctrl+C is left for `wsl.exe` and Linux to
handle; the proxy waits and maps the Windows control-C process status to exit code 130.

## Failure Modes

- Exit 7 means `wsl.exe` was unavailable in the absolute system directory returned by
  `windows.GetSystemDirectory`.
- Exit 127 means this project's marked Linux ELF was absent from baseline `PATH` and the common
  install locations.
- Exit 126 means the resolved candidate was missing, non-executable, a Windows binary, or not this
  project's Linux `op`.
- Exit 125 means recursive delegation back into `op.exe` was blocked.
- Other delegated exit codes are returned unchanged; control-C returns 130.

The proxy sets and bridges `OP_WSL_PROXY_ACTIVE` as a recursion guard. Do not set that internal
variable manually.

## Verification

No real WSL instance is required by Linux CI. Invocation planning uses a fake host, and the resolver
is exercised with Linux `/bin/sh` and marker-bearing ELF test binaries. Local verification is
included in the repository's standard commands:

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/op
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go test -run '^$' -exec=/bin/true ./...
GOOS=windows GOARCH=arm64 go build ./...
GOOS=windows GOARCH=arm64 go test -run '^$' -exec=/bin/true ./...
```
