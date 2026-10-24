# op Grok Code plugin

Thin Grok Build plugin that forwards Stop, Notification, and PermissionDenied
hooks to [op](https://code.msyke.dev/mSyke/op). **op
owns notification routing**; this plugin only relays hook payloads.

## How it works

```
Grok hooks  →  forward.sh  →  op HTTP ingest  →  providers
  Notification        POST /v1/grok-code/hook
  Stop
  PermissionDenied
```

Classification is done **server-side** (`mapGrokCodeHook`). The plugin uses a
single matcher-less group per event so handlers are not duplicated.

### Known / provisional mapping

| Grok event | Typical result |
|------------|----------------|
| `Stop` with `reason` empty or `end_turn` | `idle` |
| `Stop` with `channel_closed` / `shutdown` | ignored (session-end observe fire) |
| `Notification` whose type looks idle | ignored (avoids double with Stop) |
| `Notification` types we treat as approval-like\* | `permission` |
| `PermissionDenied` | `permission` (system **deny**, not a user prompt) |
| Other / unknown `Notification` types | **ignored** (same default as Claude) |

\*Types such as `permission_prompt`, `approval_required`, `needs_input` are
**provisional** — Grok’s public hook docs do not enumerate notification types.
Ingest logs `notificationType` on every request so the allowlist can be refined
from real traffic. Until those types are observed, the useful Grok path on many
setups is **`Stop` → idle**.

### Permissions reality check

Grok has **no** Claude-style `PermissionRequest` hook. `PermissionDenied` fires
when the permission *system* rejects a call (e.g. a deny rule), not when the UI
is waiting for you to click Allow. With `[ui] permission_mode = "always-approve"`,
approval UI rarely appears, so permission Discord cards may be uncommon.

`forward.sh` writes **nothing to stdout** (Stop is a decision gate) and
**backgrounds** the HTTP POST so an unreachable notifier cannot stall turn end.

## Prerequisites

1. op running with `ingest.enabled: true` (default port **4100**)
2. `curl` on `PATH`

## Install

```bash
bun run install-grok-plugin
# → ~/.grok/plugins/op (auto-trusted)
```

Optional env: `OC_NOTIFIER_URL` (default `http://127.0.0.1:8787`), `OC_NOTIFIER_TOKEN`.

Restart Grok or `/plugins reload` after install.

## Manual test

```bash
# Idle (Stop)
echo '{"sessionId":"t","cwd":"/tmp","hookEventName":"stop","reason":"end_turn"}' \
  | curl -sS -X POST http://127.0.0.1:8787/v1/grok-code/hook -H 'Content-Type: application/json' -d @-

# Provisional permission notification type
echo '{"sessionId":"t","cwd":"/tmp","hookEventName":"notification","notificationType":"permission_prompt","message":"Allow bash?"}' \
  | curl -sS -X POST http://127.0.0.1:8787/v1/grok-code/hook -H 'Content-Type: application/json' -d @-
```
