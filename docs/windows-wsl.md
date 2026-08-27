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

The Windows proxy supports amd64 and arm64. Windows 386 is deliberately rejected because a 32-bit
process can be redirected away from the native system directory and Sysnative handling is not
implemented.

The binaries should come from compatible source versions. `op.exe version` reports the delegated
Linux binary's version because even help and version requests execute in WSL.

## Selection And Environment

The proxy supports these optional Windows environment variables:

| Variable | Purpose |
| --- | --- |
| `OP_WSL_DISTRO` | Selects the WSL distribution passed to `wsl.exe --distribution`. The default WSL distribution is used when unset. |
| `OP_WSL_OP` | Selects this project's Linux executable. It must be an absolute Linux path such as `/home/ben/.local/bin/op`. |
| `OP_API_TOKEN` | Passed into WSL by augmenting `WSLENV`. |
| `OP_REMOTE_URL` | Passed into WSL by augmenting `WSLENV`. |

Without `OP_WSL_OP`, a fixed non-login `/bin/sh -c` resolver checks `command -v op` using WSL's
baseline environment, followed by `$HOME/.local/bin/op`, `/usr/local/bin/op`, and `/usr/bin/op`.
Shell profiles such as `.profile` and `.zshrc` are never loaded, so their output cannot corrupt JSON.
No Windows user value is interpolated into shell source.

Candidates are never executed to determine identity. The resolver reads the executable's ELF magic
and this project's embedded marker before the final `exec`. `.exe` paths, extensionless PE files,
scripts, and unrelated ELF commands named `op` such as the 1Password CLI are rejected. Validation
utilities read only the candidate file and do not inherit stdin for a candidate process.

The proxy does not pass `--user`, so WSL uses the selected distribution's configured default user,
`HOME`, config directory, and baseline environment. Unrelated `WSLENV` entries are retained exactly.
Existing variants of `OP_API_TOKEN`, `OP_REMOTE_URL`, and `OP_WSL_PROXY_ACTIVE` are removed and clean
`/u` (Win32-to-WSL only) entries are appended, preventing inherited `/p` or `/w` modifiers from
changing their values or direction.

PowerShell example:

```powershell
$env:OP_WSL_DISTRO = 'Ubuntu-24.04'
$env:OP_WSL_OP = '/home/ben/.local/bin/op'
$env:OP_REMOTE_URL = 'https://op.internal.example'
$env:OP_API_TOKEN = 'replace-me'
op.exe projects --json
```

## Preferred Shell

Set `preferredShell` to `pwsh.exe` (or `powershell.exe`) to use Windows PowerShell inside
WSL tmux panes, as the previous PowerShell implementation did. WSL interop often reports
the pane command as `init` rather than `pwsh.exe`; startup verification accepts that.

The Linux dashboard TUI cannot execute inside Windows PowerShell, so a `pwsh.exe`
preferred shell still wraps the dashboard with `sh`. Project editor wrapping and the
bottom shell pane continue to use `pwsh.exe`.

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
