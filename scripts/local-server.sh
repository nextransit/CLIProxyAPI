#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_PATH="${BIN_PATH:-$ROOT_DIR/cli-proxy-api}"
CONFIG_PATH="${CONFIG_PATH:-$ROOT_DIR/config.yaml}"
PID_FILE="${PID_FILE:-$ROOT_DIR/temp/cli-proxy-api.pid}"
LOG_FILE="${LOG_FILE:-$ROOT_DIR/logs/local-server.log}"
RUNTIME_CONFIG_PATH="${RUNTIME_CONFIG_PATH:-$ROOT_DIR/temp/local-server.runtime.yaml}"
REWRITE_DOCKER_PROXY="${REWRITE_DOCKER_PROXY:-1}"
LOCAL_PROXY_HOST="${LOCAL_PROXY_HOST:-127.0.0.1}"
MANAGEMENT_PASSWORD="${MANAGEMENT_PASSWORD:-admin123}"

usage() {
  cat <<'USAGE'
Usage:
  scripts/local-server.sh start [--foreground] [--skip-build] [--config <path>] [-- <extra server args>]
  scripts/local-server.sh stop
  scripts/local-server.sh restart [--foreground] [--skip-build] [--config <path>] [-- <extra server args>]
  scripts/local-server.sh status
  scripts/local-server.sh logs

Examples:
  scripts/local-server.sh start
  scripts/local-server.sh start --foreground -- --tui
  scripts/local-server.sh restart --config ./config.yaml
  REWRITE_DOCKER_PROXY=0 scripts/local-server.sh start
  LOCAL_PROXY_HOST=localhost scripts/local-server.sh start
USAGE
}

is_running() {
  if [[ -f "$PID_FILE" ]]; then
    local pid
    pid="$(cat "$PID_FILE")"
    if [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1; then
      return 0
    fi
  fi
  return 1
}

build_binary() {
  echo "Building binary: $BIN_PATH"
  local version="${VERSION:-dev}"
  local commit
  commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo none)"
  local build_date
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)"
  # If BUILD_DATE already set (e.g. overriding with explicit date), include it in version tag
  if [ -n "${BUILD_DATE:-}" ] && [ "${BUILD_DATE}" != "unknown" ]; then
    version="${version}-${BUILD_DATE}"
  fi
  (cd "$ROOT_DIR" && go build -ldflags="-s -w -X 'main.Version=${version}' -X 'main.Commit=${commit}' -X 'main.BuildDate=${build_date}'" -o "$BIN_PATH" ./cmd/server)
}

prepare_runtime_config() {
  local source_config="$1"

  if [[ ! -f "$source_config" ]]; then
    echo "Config file not found: $source_config"
    exit 1
  fi

  mkdir -p "$(dirname "$RUNTIME_CONFIG_PATH")"
  cp "$source_config" "$RUNTIME_CONFIG_PATH"

  if [[ "$REWRITE_DOCKER_PROXY" == "1" ]]; then
    if grep -Eq '^[[:space:]]*proxy-url:[[:space:]]*.*host\.docker\.internal.*$' "$RUNTIME_CONFIG_PATH"; then
      sed -i '' 's|host\.docker\.internal|'"$LOCAL_PROXY_HOST"'|g' "$RUNTIME_CONFIG_PATH"
      echo "Adjusted proxy-url for local run: host.docker.internal -> ${LOCAL_PROXY_HOST}"
    fi

    # Fix auth-dir: Docker mounts ~/.cliproxyapi to /root/.cliproxyapi in container
    # On macOS, rewrite /root/.cliproxyapi to user's home, removing /auths suffix
    if grep -Eq '^[[:space:]]*auth-dir:[[:space:]]*.*/root/.cliproxyapi.*$' "$RUNTIME_CONFIG_PATH"; then
      home_dir="$(eval echo ~)"
      sed -i '' 's|/root/.cliproxyapi/auths|'"$home_dir"'/.cliproxyapi|g' "$RUNTIME_CONFIG_PATH"
      echo "Adjusted auth-dir for local run: /root/.cliproxyapi/auths -> ${home_dir}/.cliproxyapi"
    fi
  fi

  echo "$RUNTIME_CONFIG_PATH"
}

start_server() {
  local foreground="0"
  local skip_build="0"
  local config="$CONFIG_PATH"
  local runtime_config=""
  local extra_args=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --foreground)
        foreground="1"
        shift
        ;;
      --skip-build)
        skip_build="1"
        shift
        ;;
      --config)
        if [[ $# -lt 2 ]]; then
          echo "Missing value for --config"
          exit 1
        fi
        config="$2"
        shift 2
        ;;
      --)
        shift
        extra_args=("$@")
        break
        ;;
      *)
        extra_args+=("$1")
        shift
        ;;
    esac
  done

  mkdir -p "$(dirname "$PID_FILE")" "$(dirname "$LOG_FILE")"

  if is_running; then
    echo "Server is already running (PID: $(cat "$PID_FILE"))"
    return 0
  fi

  if [[ "$skip_build" != "1" ]]; then
    build_binary
  elif [[ ! -x "$BIN_PATH" ]]; then
    echo "Binary not found at $BIN_PATH, building once..."
    build_binary
  fi

  runtime_config="$(prepare_runtime_config "$config")"

  if [[ "$foreground" == "1" ]]; then
    echo "Starting server in foreground..."
  echo "Command: MANAGEMENT_PASSWORD=*** $BIN_PATH --config $runtime_config ${extra_args[*]+"${extra_args[*]}"}"
  cd "$ROOT_DIR"
  exec env MANAGEMENT_PASSWORD="$MANAGEMENT_PASSWORD" "$BIN_PATH" --config "$runtime_config" ${extra_args[@]+"${extra_args[@]}"}
  fi

  echo "Starting server in background..."
  cd "$ROOT_DIR"
  env MANAGEMENT_PASSWORD="$MANAGEMENT_PASSWORD" nohup "$BIN_PATH" --config "$runtime_config" ${extra_args[@]+"${extra_args[@]}"} >>"$LOG_FILE" 2>&1 &
  local pid=$!
  sleep 2
  if kill -0 $pid 2>/dev/null; then
    echo "$pid" > "$PID_FILE"
    echo "Server started (PID: $pid)"
    echo "Log file: $LOG_FILE"
  else
    echo "Server failed to start. Recent logs:"
    tail -n 80 "$LOG_FILE" || true
    rm -f "$PID_FILE"
    exit 1
  fi
}

stop_server() {
  if ! is_running; then
    echo "Server is not running"
    rm -f "$PID_FILE"
    return 0
  fi

  local pid
  pid="$(cat "$PID_FILE")"
  echo "Stopping server (PID: $pid)..."
  kill "$pid" >/dev/null 2>&1 || true

  for _ in {1..20}; do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      rm -f "$PID_FILE"
      echo "Server stopped"
      return 0
    fi
    sleep 0.2
  done

  echo "Force stopping server (PID: $pid)..."
  kill -9 "$pid" >/dev/null 2>&1 || true
  rm -f "$PID_FILE"
  echo "Server stopped"
}

status_server() {
  if is_running; then
    echo "Server is running (PID: $(cat "$PID_FILE"))"
    return 0
  fi
  echo "Server is not running"
  return 1
}

logs_server() {
  mkdir -p "$(dirname "$LOG_FILE")"
  touch "$LOG_FILE"
  echo "Following logs: $LOG_FILE"
  tail -f "$LOG_FILE"
}

command="${1:-start}"
if [[ $# -gt 0 ]]; then
  shift
fi

case "$command" in
  start)
    start_server "$@"
    ;;
  stop)
    stop_server
    ;;
  restart)
    stop_server
    start_server "$@"
    ;;
  status)
    status_server
    ;;
  logs)
    logs_server
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "Unknown command: $command"
    usage
    exit 1
    ;;
esac
