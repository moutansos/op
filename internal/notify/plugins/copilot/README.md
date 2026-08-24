# op Copilot CLI plugin

Thin [GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/overview)
integration that forwards **agentStop** (turn idle) and **notification** events
(permission prompts + elicitation dialogs) to
[op](https://code.msyke.dev/mSyke/op). **op owns
notification routing**; this package only relays hook payloads.

## How it works

```
Copilot CLI hooks  →  forward.sh  →  op HTTP ingest  →  providers
  agentStop                       POST /v1/copilot-cli/hook
  notification (permission_prompt
                | elicitation)
```

| Copilot event | Matcher | op type | Notes |
|---------------|---------|------------------|-------|
| `agentStop` | — | `idle` | Main agent finished a turn |
| `notification` | `permission_prompt` | `permission` | CLI is showing a permission UI |
| `notification` | `elicitation_dialog` | `question` | Waiting for additional user input |

### Why not `permissionRequest`?

`permissionRequest` fires **before** the permission service (rules, session
approvals, **auto-allow/auto-deny**, and user prompting). Empty hook output
still falls through — many tool calls never show a UI. Notifying on every
`permissionRequest` would spam for auto-approved tools.

`notification` / `permission_prompt` is the system-notification signal that a
permission UI is actually being shown. The ingest mapper still accepts
`permissionRequest` payloads (richer `toolName` / `toolArgs`) for manual clients.

`agent_completed` is skipped: it means a **background subagent** finished, not
main-turn idle. Main idle is `agentStop` only.

`forward.sh` writes **nothing to stdout** (`agentStop` control and any
permission decisions must stay empty so Copilot continues normally) and
**backgrounds** the HTTP POST (`nohup`-style disown) so an unreachable notifier
cannot stall the session. Exit code is always `0` (exit `2` would deny a
permission request if that hook were registered).

Subagent-scoped payloads (`agentId` present) are ignored server-side.

## Prerequisites

1. op running with `ingest.enabled: true` (default port **4100**)
2. **`bash` and `curl` on `PATH`** (Windows: Git Bash or equivalent — both the
   `bash` and `powershell` hook entries invoke the bash forwarder)
3. [GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/overview) with hooks support

## Install

```bash
bun run install-copilot-plugin
# → installs scripts under ~/.copilot/hooks/op
# → writes ~/.copilot/hooks/op.json
```

If `COPILOT_HOME` is set, hooks are installed under `$COPILOT_HOME/hooks/`
instead.

Then:

1. **Restart Copilot CLI** (hooks load at startup)
2. Optional: `export OC_NOTIFIER_URL=http://127.0.0.1:8787` and/or `OC_NOTIFIER_TOKEN`

Re-running the installer is safe: it upgrades the scripts and rewrites only
`op.json`.

### Manual install

`hooks/hooks.json` in this tree is a **structural template** with placeholder
paths. Copilot does not expand `PLUGIN_ROOT` — use the installer (or copy the
example below with absolute paths).

```bash
# Copy or symlink the plugin tree
ln -s /path/to/op/copilot-plugin ~/.copilot/hooks/op
```

Example `~/.copilot/hooks/op.json`:

```json
{
  "version": 1,
  "hooks": {
    "agentStop": [
      {
        "type": "command",
        "bash": "bash /home/YOU/.copilot/hooks/op/scripts/forward.sh",
        "powershell": "bash /home/YOU/.copilot/hooks/op/scripts/forward.sh",
        "timeoutSec": 15
      }
    ],
    "notification": [
      {
        "type": "command",
        "matcher": "permission_prompt|elicitation_dialog",
        "bash": "bash /home/YOU/.copilot/hooks/op/scripts/forward.sh",
        "powershell": "bash /home/YOU/.copilot/hooks/op/scripts/forward.sh",
        "timeoutSec": 15
      }
    ]
  }
}
```

## Verify

1. Start op with ingest enabled
2. Restart Copilot CLI so hooks reload
3. Finish a turn or trigger a permission prompt — you should see an ingest log
   line and a provider notification

Hook debug log (default): `/tmp/op-hook.log`

## Manual test

```bash
# Idle (agentStop)
echo '{
  "sessionId": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "agentStop",
  "stopReason": "end_turn"
}' | curl -sS -X POST http://127.0.0.1:8787/v1/copilot-cli/hook \
  -H 'Content-Type: application/json' \
  -d @-

# Permission UI (notification / permission_prompt)
echo '{
  "sessionId": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "Notification",
  "notification_type": "permission_prompt",
  "title": "Permission needed",
  "message": "Allow bash: rm -rf /tmp/build"
}' | curl -sS -X POST http://127.0.0.1:8787/v1/copilot-cli/hook \
  -H 'Content-Type: application/json' \
  -d @-

# Elicitation / waiting for input
echo '{
  "sessionId": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "Notification",
  "notification_type": "elicitation_dialog",
  "message": "Which package manager?"
}' | curl -sS -X POST http://127.0.0.1:8787/v1/copilot-cli/hook \
  -H 'Content-Type: application/json' \
  -d @-
```

## Notes

- **Does not auto-approve.** If you add a `permissionRequest` hook yourself,
  returning `behavior: "allow"` / `"deny"` short-circuits the UI; this
  forwarder never returns a decision.
- **Cloud agent:** `permissionRequest` and `notification` do not apply under
  Copilot cloud agent (non-interactive, tools pre-approved). This integration
  targets the local Copilot CLI.
- Docs: [Hooks reference](https://docs.github.com/en/copilot/reference/hooks-reference),
  [Using hooks](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-hooks).
