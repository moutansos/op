#!/usr/bin/env bash
# Forward GitHub Copilot CLI hook JSON from stdin to oc-notifier's ingest API.
#
# CRITICAL: write NOTHING to stdout.
# - agentStop can return decision:block / allow JSON; plain text is invalid.
# - permissionRequest can auto-allow/deny via behavior JSON — we must not
#   decide; empty stdout leaves the normal Copilot approval UI in place.
# - Exit code 2 on permissionRequest is treated as deny — always exit 0.
#
# Copilot hooks are synchronous (no async field). We background the HTTP POST
# so an unreachable notifier cannot stall turn end or permission prompts.
#
# Requires: bash, curl
# Defaults to http://127.0.0.1:8787; override with OC_NOTIFIER_URL / OC_NOTIFIER_TOKEN.
set -euo pipefail

BASE_URL="${OC_NOTIFIER_URL:-http://127.0.0.1:8787}"
BASE_URL="${BASE_URL%/}"
TOKEN="${OC_NOTIFIER_TOKEN:-}"
ENDPOINT="${BASE_URL}/v1/copilot-cli/hook"
LOG_FILE="${OC_NOTIFIER_HOOK_LOG:-${TMPDIR:-/tmp}/oc-notifier-hook.log}"

body=$(cat)

if [[ -z "${body}" ]]; then
  {
    echo "$(date -Iseconds 2>/dev/null || date) SKIP harness=copilot-cli empty stdin"
  } >> "${LOG_FILE}" 2>/dev/null || true
  exit 0
fi

event="$(printf '%s' "${body}" | sed -n 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${event}" ]]; then
  event="$(printf '%s' "${body}" | sed -n 's/.*"hookEventName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi
session="$(printf '%s' "${body}" | sed -n 's/.*"sessionId"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${session}" ]]; then
  session="$(printf '%s' "${body}" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi
tool="$(printf '%s' "${body}" | sed -n 's/.*"toolName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${tool}" ]]; then
  tool="$(printf '%s' "${body}" | sed -n 's/.*"tool_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi
notif_type="$(printf '%s' "${body}" | sed -n 's/.*"notification_type"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${notif_type}" ]]; then
  notif_type="$(printf '%s' "${body}" | sed -n 's/.*"notificationType"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi

{
  echo "$(date -Iseconds 2>/dev/null || date) INVOKE harness=copilot-cli event=${event:-?} type=${notif_type:--} tool=${tool:--} session=${session:-?}"
} >> "${LOG_FILE}" 2>/dev/null || true

# Background the network call so agentStop / permissionRequest never block.
tmp_body="$(mktemp "${TMPDIR:-/tmp}/oc-notifier-body.XXXXXX" 2>/dev/null || echo "")"
if [[ -z "${tmp_body}" ]]; then
  {
    echo "$(date -Iseconds 2>/dev/null || date) FAIL harness=copilot-cli could not create temp file"
  } >> "${LOG_FILE}" 2>/dev/null || true
  exit 0
fi
printf '%s' "${body}" > "${tmp_body}"

# nohup + disown so Copilot reaping the hook process group is less likely to
# kill the in-flight POST before it completes.
(
  set +e
  curl_args=(
    -sS
    -X POST
    --max-time 3
    -H "Content-Type: application/json"
    -d @"${tmp_body}"
    -w "\n%{http_code}"
  )
  if [[ -n "${TOKEN}" ]]; then
    curl_args+=(-H "Authorization: Bearer ${TOKEN}")
  fi

  response="$(curl "${curl_args[@]}" "${ENDPOINT}" 2>"${LOG_FILE}.err" || true)"
  status="${response##*$'\n'}"
  resp_body="${response%$'\n'*}"
  ts="$(date -Iseconds 2>/dev/null || date)"

  if [[ "${status}" =~ ^2[0-9][0-9]$ ]] && printf '%s' "${resp_body}" | grep -q '"ok"'; then
    echo "${ts} OK harness=copilot-cli event=${event:-?} type=${notif_type:--} tool=${tool:--} session=${session:-?} http=${status} resp=${resp_body:0:160}" >> "${LOG_FILE}" 2>/dev/null
  else
    {
      echo "${ts} FAIL harness=copilot-cli event=${event:-?} type=${notif_type:--} tool=${tool:--} session=${session:-?}"
      echo "  endpoint: ${ENDPOINT}"
      echo "  http: ${status:-none}"
      echo "  body: ${resp_body:0:300}"
      if [[ -s "${LOG_FILE}.err" ]]; then
        echo "  curl: $(head -c 300 "${LOG_FILE}.err")"
      fi
    } >> "${LOG_FILE}" 2>/dev/null
  fi
  rm -f "${tmp_body}" "${LOG_FILE}.err" 2>/dev/null
) >/dev/null 2>&1 &
disown $! 2>/dev/null || true

# Do not wait — return immediately with empty stdout (continue / no decision).
exit 0
