# op Claude Code plugin

Thin Claude Code plugin that forwards idle, permission, and question events to
[op](https://code.msyke.dev/mSyke/op). **op owns
notification routing** (Discord, Microsoft Teams, generic webhooks); this plugin
only relays Claude Code hook payloads.

## How it works

```
Claude Code hooks  →  forward.sh  →  op HTTP ingest  →  providers
  Notification           POST /v1/claude-code/hook
  PermissionRequest
```

Hooked events:

| Claude Code event | Matcher / tools | op type |
|-------------------|-----------------|------------------|
| `Notification` | `agent_completed` | `idle` |
| `Notification` | `agent_needs_input` | `permission` |
| `Notification` | `elicitation_dialog` | `question` |
| `PermissionRequest` | all tools (incl. `AskUserQuestion`) | `permission` / `question` |
| `Stop` | (always) | `idle` |

**Permissions:** Claude fires a dedicated **`PermissionRequest`** hook when a tool needs
approval (`AskUserQuestion` → `question`, everything else → `permission`).

**Not hooked, on purpose:** `idle_prompt` and `permission_prompt` are the desktop-alert
twins of `Stop` and `PermissionRequest`. Claude Code fires `idle_prompt` about 60 seconds
after `Stop` for the same session, so forwarding both meant two notifications per turn.
The plugin now matches only the immediate event; op drops the twins server-side
too, so an older plugin install cannot reintroduce the duplicate.

Subagent events (`agent_id` present) are ignored by op.

## Prerequisites

1. [op](../README.md) running with ingest enabled
2. `curl` available on your `PATH`
3. Claude Code with plugin support

## Enable ingest on op

Add to your `config.json`:

```json
{
  "ingest": {
    "enabled": true,
    "host": "127.0.0.1",
    "port": 4100,
    "token": "optional-shared-secret"
  },
  "providers": [
    {
      "type": "discord",
      "enabled": true,
      "webhookUrl": "https://discord.com/api/webhooks/..."
    }
  ]
}
```

`opencode` can be omitted if you only use Claude Code.

## Install the plugin

### Recommended: op CLI

From the op repo:

```bash
# Linux/macOS → symlink  |  Windows → copy
# Installs to ~/.claude/skills/op
# Safe to re-run for upgrades — replaces an existing install
bun run install-claude-plugin

# Optional overrides
bun run src/index.ts --install-claude-plugin \
  --plugin-source ./claude-code-plugin \
  --plugin-target ~/.claude/skills/op
```

Then restart Claude Code (or run `/reload-plugins`).

### Session-only (no install)

```bash
claude --plugin-dir /path/to/op/claude-code-plugin
```

### Manual skills-directory install

```bash
# Linux/macOS
ln -s /path/to/op/claude-code-plugin ~/.claude/skills/op

# Windows (PowerShell) — copy instead of symlink
Copy-Item -Recurse .\claude-code-plugin $env:USERPROFILE\.claude\skills\op
```

When enabling, Claude Code prompts for:

| Option | Description | Default |
|--------|-------------|---------|
| **op URL** | Base URL of the ingest API | `http://127.0.0.1:8787` |
| **Auth token** | Optional bearer token matching `ingest.token` | _(empty)_ |

## Verify

1. Start op with ingest enabled
2. In Claude Code, run `/hooks` and confirm `Notification` / `PermissionRequest` hooks from this plugin
3. Let Claude finish a turn or request a permission — you should see an ingest log line and a provider notification

## Manual test

```bash
# Idle notification (Stop is the idle signal; idle_prompt is ignored as a duplicate)
echo '{
  "session_id": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "Stop",
  "last_assistant_message": "Done."
}' | curl -sS -X POST http://127.0.0.1:8787/v1/claude-code/hook \
  -H 'Content-Type: application/json' \
  -d @-

# Permission prompt
echo '{
  "session_id": "test-session",
  "cwd": "/home/you/project",
  "hook_event_name": "PermissionRequest",
  "tool_name": "Bash",
  "tool_input": { "command": "rm -rf /tmp/build", "description": "clean build" }
}' | curl -sS -X POST http://127.0.0.1:8787/v1/claude-code/hook \
  -H 'Content-Type: application/json' \
  -d @-
```
