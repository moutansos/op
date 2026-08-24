# op Codex plugin

Thin Codex CLI integration that forwards **Stop** (turn idle) and
**PermissionRequest** (approval needed) hooks to
[op](https://code.msyke.dev/mSyke/op). **op owns
notification routing**; this package only relays hook payloads.

## How it works

```
Codex hooks  →  forward.sh  →  op HTTP ingest  →  providers
  Stop                   POST /v1/codex/hook
  PermissionRequest
```

| Codex event | op type | Notes |
|-------------|------------------|-------|
| `Stop` | `idle` | Turn finished; ready for input |
| `PermissionRequest` | `permission` | Approval UI is about to appear |

`forward.sh` writes **nothing to stdout** (Stop control / permission decisions
must stay empty so Codex continues normally) and **backgrounds** the HTTP POST
so an unreachable notifier cannot stall the session.

Subagent-scoped payloads (`agent_id` present) are ignored server-side.

## Prerequisites

1. op running with `ingest.enabled: true` (default port **4100**)
2. **`bash` and `curl` on `PATH`** (required; the forwarder is a bash script)
3. Codex CLI with hooks enabled (default: `[features] hooks = true`)

On Windows, install Git Bash or another bash and ensure `bash`/`curl` are
available. The installer sets `commandWindows` to invoke the script via bash.

## Install

```bash
bun run install-codex-plugin
# → installs scripts under ~/.codex/hooks/op
# → merges Stop + PermissionRequest into ~/.codex/hooks.json
```

Then in Codex:

1. Run `/hooks`, review, and **trust** the new op hooks
2. Optional: `export OC_NOTIFIER_URL=http://127.0.0.1:8787` and/or `OC_NOTIFIER_TOKEN`

Re-running the installer is safe: it upgrades the scripts and replaces only
the op entries in `hooks.json` (identified by `statusMessage:
"op"` and path markers). Codex trusts hooks against a definition
hash, so re-install may prompt you to re-trust hooks — including unrelated
entries if the file was reformatted.

### Manual install

```bash
# Copy or symlink the plugin tree
ln -s /path/to/op/codex-plugin ~/.codex/hooks/op

# Point user hooks at the absolute forward script (edit ~/.codex/hooks.json)
```

Example `~/.codex/hooks.json` fragment (paths must be absolute or resolvable
from the session cwd — the installer uses absolute paths):

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/home/YOU/.codex/hooks/op/scripts/forward.sh",
            "timeout": 15
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/home/YOU/.codex/hooks/op/scripts/forward.sh",
            "timeout": 15
          }
        ]
      }
    ]
  }
}
```

## Verify

1. Start op with ingest enabled
2. In Codex, run `/hooks` and confirm Stop / PermissionRequest from op
3. Trust the hooks if prompted
4. Finish a turn or trigger a permission prompt — you should see an ingest log
   line and a provider notification

Hook debug log (default): `/tmp/op-hook.log`

## Manual test

```bash
# Idle (Stop)
echo '{
  "session_id": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "Stop",
  "last_assistant_message": "Done."
}' | curl -sS -X POST http://127.0.0.1:8787/v1/codex/hook \
  -H 'Content-Type: application/json' \
  -d @-

# Permission
echo '{
  "session_id": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "PermissionRequest",
  "tool_name": "Bash",
  "tool_input": { "command": "rm -rf /tmp/build", "description": "clean build" }
}' | curl -sS -X POST http://127.0.0.1:8787/v1/codex/hook \
  -H 'Content-Type: application/json' \
  -d @-
```

## Notes

- **Does not auto-approve.** PermissionRequest hooks that return a decision can
  allow/deny without the UI; this forwarder never returns a decision.
- **Hooks vs `notify`.** Codex also has a legacy `notify` program for
  `agent-turn-complete` only. Hooks cover both idle and permissions, so this
  integration uses hooks.
- Trust is required: Codex skips non-managed hooks until you review them in
  `/hooks`.
