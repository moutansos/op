#!/usr/bin/env bash
# Forward Grok hook JSON from stdin to oc-notifier's ingest API.
#
# CRITICAL: write NOTHING to stdout. Stop is a blocking decision gate; any
# stdout is parsed as stop-control JSON. All logging goes to the log file.
#
# Grok waits for Stop hooks (no documented async field). We background the
# HTTP POST so an unreachable notifier cannot stall every turn end.
#
# Defaults to http://127.0.0.1:8787; override with OC_NOTIFIER_URL / OC_NOTIFIER_TOKEN.
set -euo pipefail

BASE_URL="${OC_NOTIFIER_URL:-${CLAUDE_PLUGIN_OPTION_NOTIFIER_URL:-http://127.0.0.1:8787}}"
BASE_URL="${BASE_URL%/}"
TOKEN="${OC_NOTIFIER_TOKEN:-${CLAUDE_PLUGIN_OPTION_TOKEN:-}}"
ENDPOINT="${BASE_URL}/v1/grok-code/hook"
LOG_FILE="${OC_NOTIFIER_HOOK_LOG:-${TMPDIR:-/tmp}/oc-notifier-hook.log}"

body=$(cat)

{
  echo "$(date -Iseconds 2>/dev/null || date) INVOKE harness=grok event=${GROK_HOOK_EVENT:-?} session=${GROK_SESSION_ID:-?} root=${GROK_PLUGIN_ROOT:-}"
} >> "${LOG_FILE}" 2>/dev/null || true

if [[ -z "${body}" ]]; then
  {
    echo "$(date -Iseconds 2>/dev/null || date) SKIP empty stdin"
  } >> "${LOG_FILE}" 2>/dev/null || true
  exit 0
fi

event="$(printf '%s' "${body}" | sed -n 's/.*"hookEventName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${event}" ]]; then
  event="$(printf '%s' "${body}" | sed -n 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi
session="$(printf '%s' "${body}" | sed -n 's/.*"sessionId"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
if [[ -z "${session}" ]]; then
  session="$(printf '%s' "${body}" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi
event="${GROK_HOOK_EVENT:-$event}"
session="${GROK_SESSION_ID:-$session}"

# Background the network call so Stop is never blocked on oc-notifier latency.
# Capture body in a temp file for the background job (avoid huge argv).
tmp_body="$(mktemp "${TMPDIR:-/tmp}/oc-notifier-body.XXXXXX" 2>/dev/null || echo "")"
if [[ -z "${tmp_body}" ]]; then
  {
    echo "$(date -Iseconds 2>/dev/null || date) FAIL harness=grok could not create temp file"
  } >> "${LOG_FILE}" 2>/dev/null || true
  exit 0
fi
printf '%s' "${body}" > "${tmp_body}"

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
    echo "${ts} OK harness=grok event=${event:-?} session=${session:-?} http=${status} resp=${resp_body:0:160}" >> "${LOG_FILE}" 2>/dev/null
  else
    {
      echo "${ts} FAIL harness=grok event=${event:-?} session=${session:-?}"
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

# Do not wait — return immediately so Stop is not blocked.
exit 0
