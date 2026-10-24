#!/usr/bin/env bash
# Forward Codex CLI hook JSON from stdin to oc-notifier's ingest API.
#
# CRITICAL: write NOTHING to stdout.
# - Stop expects JSON control output; plain text is invalid.
# - PermissionRequest can auto-allow/deny via JSON decisions — we must not
#   decide; empty stdout leaves the normal Codex approval UI in place.
#
# Codex does not support async command hooks yet, so we background the HTTP
# POST so an unreachable notifier cannot stall turn end or permission prompts.
#
# Requires: bash, curl
# Defaults to http://127.0.0.1:8787; override with OC_NOTIFIER_URL / OC_NOTIFIER_TOKEN.
set -euo pipefail

BASE_URL="${OC_NOTIFIER_URL:-http://127.0.0.1:8787}"
BASE_URL="${BASE_URL%/}"
TOKEN="${OC_NOTIFIER_TOKEN:-}"
ENDPOINT="${BASE_URL}/v1/codex/hook"
LOG_FILE="${OC_NOTIFIER_HOOK_LOG:-${TMPDIR:-/tmp}/oc-notifier-hook.log}"

body=$(cat)

if [[ -z "${body}" ]]; then
  {
    echo "$(date -Iseconds 2>/dev/null || date) SKIP harness=codex empty stdin"
  } >> "${LOG_FILE}" 2>/dev/null || true
  exit 0
fi

event="$(printf '%s' "${body}" | sed -n 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
session="$(printf '%s' "${body}" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
tool="$(printf '%s' "${body}" | sed -n 's/.*"tool_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"

{
  echo "$(date -Iseconds 2>/dev/null || date) INVOKE harness=codex event=${event:-?} tool=${tool:--} session=${session:-?} plugin_root=${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-}}"
} >> "${LOG_FILE}" 2>/dev/null || true

# Background the network call so Stop / PermissionRequest are never blocked.
tmp_body="$(mktemp "${TMPDIR:-/tmp}/oc-notifier-body.XXXXXX" 2>/dev/null || echo "")"
if [[ -z "${tmp_body}" ]]; then
  {
    echo "$(date -Iseconds 2>/dev/null || date) FAIL harness=codex could not create temp file"
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
    echo "${ts} OK harness=codex event=${event:-?} tool=${tool:--} session=${session:-?} http=${status} resp=${resp_body:0:160}" >> "${LOG_FILE}" 2>/dev/null
  else
    {
      echo "${ts} FAIL harness=codex event=${event:-?} tool=${tool:--} session=${session:-?}"
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

# Do not wait — return immediately with empty stdout (continue / no decision).
exit 0
