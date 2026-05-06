#!/bin/zsh
set -euo pipefail

LABEL="com.zhouyong.minimax-route-refresh"
SRC_DIR="/Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI/scripts/macos"
SCRIPT_SRC="$SRC_DIR/minimax-route-refresh.sh"
PLIST_SRC="$SRC_DIR/$LABEL.plist"
SCRIPT_DST="/usr/local/sbin/minimax-route-refresh.sh"
PLIST_DST="/Library/LaunchDaemons/$LABEL.plist"

require_root() {
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    echo "Please run as root: sudo $0" >&2
    exit 1
  fi
}

install_job() {
  require_root

  install -m 755 "$SCRIPT_SRC" "$SCRIPT_DST"
  install -m 644 "$PLIST_SRC" "$PLIST_DST"
  chown root:wheel "$SCRIPT_DST" "$PLIST_DST"

  launchctl bootout system "$PLIST_DST" >/dev/null 2>&1 || true
  launchctl bootstrap system "$PLIST_DST"
  launchctl enable "system/$LABEL"
  launchctl kickstart -k "system/$LABEL"

  echo "Installed and started: $LABEL"
}

uninstall_job() {
  require_root

  launchctl bootout system "$PLIST_DST" >/dev/null 2>&1 || true
  rm -f "$PLIST_DST" "$SCRIPT_DST"

  echo "Uninstalled: $LABEL"
}

status_job() {
  launchctl print "system/$LABEL" 2>/dev/null || true
  launchctl list | grep "$LABEL" || true
}

case "${1:-install}" in
  install)
    install_job
    ;;
  uninstall)
    uninstall_job
    ;;
  status)
    status_job
    ;;
  *)
    echo "Usage: $0 [install|uninstall|status]" >&2
    exit 1
    ;;
esac
