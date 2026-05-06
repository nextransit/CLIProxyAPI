#!/bin/zsh
set -euo pipefail

HOST="api.minimaxi.com"
PRIMARY_IF="en0"
STATE_FILE="/var/run/minimax-route-refresh.state"
LOG_PREFIX="[minimax-route-refresh]"

log() {
  printf "%s %s\n" "$LOG_PREFIX" "$1"
}

get_gateway() {
  /usr/sbin/ipconfig getoption "$PRIMARY_IF" router 2>/dev/null | /usr/bin/tr -d '[:space:]'
}

resolve_ipv4s() {
  /usr/bin/dig +short "$HOST" | /usr/bin/grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | /usr/bin/sort -u
}

cleanup_old_routes() {
  if [[ ! -f "$STATE_FILE" ]]; then
    return
  fi

  while IFS= read -r old_ip; do
    [[ -z "$old_ip" ]] && continue
    /sbin/route -n delete -host "$old_ip" >/dev/null 2>&1 || true
  done < "$STATE_FILE"
}

install_routes() {
  local gateway="$1"
  local -a ips=("$@")
  ips=("${ips[@]:1}")

  if [[ ${#ips[@]} -eq 0 ]]; then
    log "no IPv4 resolved for $HOST"
    return 1
  fi

  : > "$STATE_FILE"
  for ip in "${ips[@]}"; do
    /sbin/route -n delete -host "$ip" >/dev/null 2>&1 || true
    /sbin/route -n add -host "$ip" "$gateway" >/dev/null
    printf "%s\n" "$ip" >> "$STATE_FILE"
    log "route set: $ip via $gateway"
  done

  return 0
}

main() {
  local gateway
  gateway="$(get_gateway)"
  if [[ -z "$gateway" ]]; then
    log "failed to get gateway for $PRIMARY_IF"
    exit 1
  fi

  local -a ips
  ips=("${(@f)$(resolve_ipv4s || true)}")

  cleanup_old_routes
  install_routes "$gateway" "${ips[@]}"
}

main "$@"
