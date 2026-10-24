#!/usr/bin/env bash
# Forward Claude Code hook JSON from stdin to oc-notifier's ingest API.
# Configuration comes from plugin userConfig via CLAUDE_PLUGIN_OPTION_* env vars.
set -euo pipefail

BASE_URL="${CLAUDE_PLUGIN_OPTION_NOTIFIER_URL:-http://127.0.0.1:8787}"
# Strip trailing slash
BASE_URL="${BASE_URL%/}"
TOKEN="${CLAUDE_PLUGIN_OPTION_TOKEN:-}"
ENDPOINT="${BASE_URL}/v1/claude-code/hook"
LOG_FILE="${OC_NOTIFIER_HOOK_LOG:-${TMPDIR:-/tmp}/oc-notifier-hook.log}"

body=$(cat)

# Empty stdin — nothing to forward
if [[ -z "${body}" ]]; then
  exit 0
fi

event="$(printf '%s' "${body}" | sed -n 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
session="$(printf '%s' "${body}" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
notif_type="$(printf '%s' "${body}" | sed -n 's/.*"notification_type"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"

curl_args=(
  -sS
  -X POST
  --max-time 10
  -H "Content-Type: application/json"
  -d "${body}"
  -w "\n%{http_code}"
)

if [[ -n "${TOKEN}" ]]; then
  curl_args+=(-H "Authorization: Bearer ${TOKEN}")
fi

# Capture body + status; never block Claude Code on notifier failures
response="$(curl "${curl_args[@]}" "${ENDPOINT}" 2>"${LOG_FILE}.err" || true)"
status="${response##*$'\n'}"
resp_body="${response%$'\n'*}"

ts="$(date -Iseconds 2>/dev/null || date)"
if [[ "${status}" =~ ^2[0-9][0-9]$ ]] && printf '%s' "${resp_body}" | grep -q '"ok"'; then
  {
    echo "${ts} OK event=${event:-?} type=${notif_type:--} session=${session:-?} http=${status} resp=${resp_body:0:120}"
  } >> "${LOG_FILE}" 2>/dev/null || true
  exit 0
fi

{
  echo "${ts} FAIL event=${event:-?} type=${notif_type:--} session=${session:-?}"
  echo "  endpoint: ${ENDPOINT}"
  echo "  http: ${status:-none}"
  echo "  body: ${resp_body:0:300}"
  if [[ -s "${LOG_FILE}.err" ]]; then
    echo "  curl: $(head -c 300 "${LOG_FILE}.err")"
  fi
} >> "${LOG_FILE}" 2>/dev/null || true

# Non-blocking for Claude Code (exit 0), but log for debugging
exit 0
