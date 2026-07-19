#!/usr/bin/env bash
#
# detect-key-health.sh - Probe every codex-api-key in config.yaml.
#
# For each entry, the script sends a lightweight probe (GET /v1/models,
# or POST /v1/responses with a 1-token request for entries that point at
# non-OpenAI gateways) and classifies the result.
#
# Classification:
#   healthy            200 OK
#   auth-fail          401 / 403 (key invalid or revoked)
#   payment-required   402 (key valid but balance is zero)
#   rate-limited       429 (temporary, treat as healthy)
#   not-found          404 (path wrong or model not supported)
#   unreachable        connection refused / timeout / DNS error
#   unknown-model      upstream rejected the specific model id
#   misconfigured      excluded-models missing or base-url malformed
#
# Output: human-readable table to stdout, JSON to stderr (for piping).
#
# Usage: ./scripts/detect-key-health.sh [path/to/config.yaml]
# Env:   PROXY_PORT (default 8317) — port where CLIProxyAPI is listening
#        PROBE_TIMEOUT (default 8) — seconds per probe

set -uo pipefail

CONFIG="${1:-./config.yaml}"
PROXY_PORT="${PROXY_PORT:-8317}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-8}"

if [[ ! -f "${CONFIG}" ]]; then
  echo "ERROR: config not found: ${CONFIG}" >&2
  exit 2
fi

# 1. Read API keys & base URLs via Python (handles YAML safely)
mapfile -t ENTRIES < <(python3 - "$CONFIG" <<'PY' 2>/dev/null
import sys, yaml
with open(sys.argv[1]) as f:
    data = yaml.safe_load(f)
for e in (data.get('codex-api-key') or []):
    ak  = (e.get('api-key') or '').strip()
    bu  = (e.get('base-url') or '').strip().rstrip('/')
    excl = e.get('excluded-models') or []
    models = [m.get('name') for m in (e.get('models') or []) if m.get('name')]
    if ak and bu:
        # tab-separated: ak, base_url, excluded_present (0/1), first_model
        print(f"{ak}\t{bu}\t{1 if excl else 0}\t{models[0] if models else ''}")
PY
)

if [[ ${#ENTRIES[@]} -eq 0 ]]; then
  echo "ERROR: no codex-api-key entries found in ${CONFIG}" >&2
  exit 2
fi

# 2. Probe each entry
printf "%-5s %-30s %-40s %-22s %s\n" "IDX" "KEY" "BASE-URL" "STATUS" "DETAIL"
printf "%-5s %-30s %-40s %-22s %s\n" "---" "---" "-------" "------" "------"

declare -A STATUS DETAIL
i=0
for entry in "${ENTRIES[@]}"; do
  IFS=$'\t' read -r api_key base_url has_excl first_model <<<"$entry"
  i=$((i + 1))

  # Try via local CLIProxyAPI proxy first (it has access to all keys),
  # then direct probe if proxy is unreachable.
  probe_url="http://127.0.0.1:${PROXY_PORT}/v1/responses"
  payload=$(printf '{"model":"%s","input":"ping","stream":false,"max_output_tokens":1}' \
            "${first_model:-gpt-5.5}")
  resp=$(curl -sS -o /tmp/probe.body -w "%{http_code}|%{time_total}" \
              --max-time "$PROBE_TIMEOUT" \
              -H "Authorization: Bearer ${api_key}" \
              -H "Content-Type: application/json" \
              -d "$payload" \
              "$probe_url" 2>/dev/null) || resp="000|0"

  http_code="${resp%%|*}"
  time="${resp##*|}"

  # Classify
  case "$http_code" in
    200) status="healthy" ; detail="${time}s" ;;
    401|403) status="auth-fail" ; detail="HTTP $http_code" ;;
    402) status="payment-required" ; detail="HTTP 402" ;;
    404)
      # 404 may mean CLIProxyAPI isn't up — fall back to direct probe
      direct=$(curl -sS -o /tmp/probe.body -w "%{http_code}|%{time_total}" \
                    --max-time "$PROBE_TIMEOUT" \
                    -H "Authorization: Bearer ${api_key}" \
                    "${base_url}/models" 2>/dev/null) || direct="000|0"
      direct_code="${direct%%|*}"
      case "$direct_code" in
        200) status="proxy-down+direct-ok" ; detail="${direct##*|}s" ;;
        401|403) status="auth-fail" ; detail="HTTP $direct_code" ;;
        000) status="unreachable" ; detail="timeout/connfail" ;;
        *) status="not-found" ; detail="HTTP $direct_code" ;;
      esac
      ;;
    429) status="rate-limited" ; detail="HTTP 429" ;;
    000) status="unreachable" ; detail="timeout/connfail" ;;
    *)  status="unknown" ; detail="HTTP $http_code" ;;
  esac

  # Misconfigured check (cheap, no network)
  if [[ "$has_excl" -eq 0 ]]; then
    detail="$detail [NO excluded-models]"
  fi

  STATUS[$i]="$status"
  DETAIL[$i]="$detail"

  key_short="${api_key:0:24}..."
  base_short="${base_url}"
  [[ ${#base_short} -gt 38 ]] && base_short="${base_short:0:35}..."
  printf "%-5d %-30s %-40s %-22s %s\n" "$i" "$key_short" "$base_short" "$status" "$detail"
done

# 3. Summary
echo
healthy=0; auth=0; pay=0; reach=0; other=0; misconf=0
for i in "${!STATUS[@]}"; do
  case "${STATUS[$i]}" in
    healthy|proxy-down+direct-ok) healthy=$((healthy+1)) ;;
    auth-fail) auth=$((auth+1)) ;;
    payment-required) pay=$((pay+1)) ;;
    unreachable) reach=$((reach+1)) ;;
    *) other=$((other+1)) ;;
  esac
  [[ "${DETAIL[$i]}" == *"[NO excluded-models]"* ]] && misconf=$((misconf+1))
done
echo "SUMMARY: healthy=$healthy  auth-fail=$auth  payment-required=$pay  unreachable=$reach  other=$other  missing-excluded=$misconf"

# 4. JSON to stderr
{
  echo "{"
  echo "  \"summary\": {\"healthy\":$healthy,\"auth_fail\":$auth,\"payment_required\":$pay,\"unreachable\":$reach,\"other\":$other,\"missing_excluded\":$misconf},"
  echo "  \"entries\": ["
  first=1
  for i in "${!STATUS[@]}"; do
    [[ $first -eq 0 ]] && echo ","
    printf '    {"idx":%d,"status":"%s","detail":"%s"}' "$i" "${STATUS[$i]}" "${DETAIL[$i]}"
    first=0
  done
  echo
  echo "  ]"
  echo "}"
} >&2

# Exit code: 0 if everything healthy, 1 otherwise
[[ $((auth+pay+reach)) -eq 0 ]] || exit 1