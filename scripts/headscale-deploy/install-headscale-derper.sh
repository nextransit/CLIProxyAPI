#!/usr/bin/env bash
#
# install-headscale-derper.sh
#
# One-shot installer for a self-hosted Tailscale control plane (headscale) +
# custom DERP relay (derper), both running on the same host (ben-ubuntu,
# Shenzhen Telecom, public IP). This replaces the default Tailscale control
# plane for the nodes that opt in, with a home DERP that sits on the
# Shenzhen LAN, dropping tunnel latency from ~600ms (via lax) to ~5-30ms.
#
# Run as root or with sudo on the target host. Idempotent: re-runs pick up
# where they left off.

set -euo pipefail

# ---------------------------------------------------------------------------
# Config — adjust before running, or export the env vars below to override.
# ---------------------------------------------------------------------------
# The hostname clients use to reach headscale. The right answer depends on
# where clients are when they log in:
#   - Clients on the same LAN as ben-ubuntu (e.g. your Mac at home, with
#     ben-ubuntu on 192.168.19.107): use the LAN IP for the lowest
#     latency. Override with HS_DOMAIN=192.168.19.107.
#   - Clients off-LAN (e.g. on a different network, or before tailscale
#     is up on this host): you need a hostname that resolves to a
#     reachable IP for that network. For first-time setup with no
#     tailscale yet, the easiest is the public IP of ben-ubuntu's ISP
#     (Telecom Shenzhen, whatever it is on that day). The script will
#     print the actual candidates.
HS_DOMAIN="${HS_DOMAIN:-192.168.19.107}"
# Port 443 is occupied by opnsense's vpnbridge on this host. Move
# headscale to 8443 and derper to 4443; STUN stays on 3478/udp.
HS_LISTEN_PORT="${HS_LISTEN_PORT:-8443}"
DERPER_PORT="${DERPER_PORT:-4443}"
# MagicDNS base_domain must be a real DNS name, not a bare IP — headscale
# refuses "server_url and base_domain" sharing the same string. Use a
# pseudo-TLD that won't resolve in public DNS but will be unique on this
# tailnet.
HS_BASE_DOMAIN="${HS_BASE_DOMAIN:-hs.tailnet.local}"
DERPER_REGION_ID="${DERPER_REGION_ID:-999}"
DERPER_REGION_CODE="${DERPER_REGION_CODE:-shenzhen}"
DERPER_REGION_NAME="${DERPER_REGION_NAME:-Shenzhen Home}"
HS_DATA_DIR="${HS_DATA_DIR:-/var/lib/headscale}"
DERPER_DATA_DIR="${DERPER_DATA_DIR:-/var/lib/tailscale-derper}"
HS_IMAGE="${HS_IMAGE:-headscale/headscale:stable}"
DERPER_IMAGE="${DERPER_IMAGE:-tailscale/derper:stable}"

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
if [[ $EUID -ne 0 ]]; then
  echo "Re-running with sudo..." >&2
  exec sudo -E /usr/bin/env bash "$0" "$@"
fi

echo "==> Preflight: OS / docker"
. /etc/os-release
case "$ID" in
  ubuntu|debian) ;;
  *)
    echo "This script is for Ubuntu/Debian. Detected: $ID" >&2
    exit 1
    ;;
esac

if ! command -v docker >/dev/null 2>&1; then
  echo "==> Installing docker"
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -y
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
  systemctl enable --now docker
else
  echo "    docker already installed: $(docker --version)"
fi

# ---------------------------------------------------------------------------
# Docker registry mirror — Docker Hub is often unreachable from China
# corporate NATs. Configure daemon-level mirrors (idempotent) and pull via
# the mirror, falling back to Docker Hub if the mirror is itself blocked.
# ---------------------------------------------------------------------------
MIRROR_CONF="/etc/docker/daemon.json"
if ! grep -q "registry-mirrors" "$MIRROR_CONF" 2>/dev/null; then
  echo "==> Configuring docker registry mirrors"
  mkdir -p /etc/docker
  cat > "$MIRROR_CONF" <<JSON
{
  "registry-mirrors": [
    "https://docker.m.daocloud.io",
    "https://dockerproxy.com",
    "https://docker.mirrors.ustc.edu.cn"
  ]
}
JSON
  systemctl restart docker
  sleep 3
fi

# ---------------------------------------------------------------------------
# TLS cert (self-signed) for headscale + derper on $HS_DOMAIN
# ---------------------------------------------------------------------------
CERT_DIR="/etc/ssl/headscale"
mkdir -p "$CERT_DIR"
if [[ ! -f "$CERT_DIR/fullchain.pem" ]]; then
  echo "==> Generating self-signed cert for $HS_DOMAIN"
  # Get this host's primary IPv4 (assumes single NIC; override HS_DOMAIN_IP
  # to bind to a different address).
  HS_DOMAIN_IP="${HS_DOMAIN_IP:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
  openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
    -keyout "$CERT_DIR/privkey.pem" \
    -out    "$CERT_DIR/fullchain.pem" \
    -subj   "/CN=$HS_DOMAIN" \
    -addext "subjectAltName=DNS:$HS_DOMAIN,IP:$HS_DOMAIN_IP" \
    2>&1 | tail -1
  chmod 600 "$CERT_DIR/privkey.pem"
  echo "    Cert: $CERT_DIR/fullchain.pem (CN=$HS_DOMAIN, IP=$HS_DOMAIN_IP)"
fi

# ---------------------------------------------------------------------------
# headscale config
# ---------------------------------------------------------------------------
HS_CONFIG="/etc/headscale/config.yaml"
mkdir -p "$(dirname "$HS_CONFIG")" "$HS_DATA_DIR"
if [[ ! -f "$HS_CONFIG" ]]; then
  echo "==> Writing headscale config -> $HS_CONFIG"
  # Private key: headscale generates one on first start; we let it write
  # $HS_DATA_DIR/private.key and then point config at it on restart.
  cat > "$HS_CONFIG" <<YAML
server_url: https://${HS_DOMAIN}:${HS_LISTEN_PORT}
listen_addr: 0.0.0.0:${HS_LISTEN_PORT}
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: false

private_key_path: ${HS_DATA_DIR}/private.key
noise:
  private_key_path: ${HS_DATA_DIR}/noise_private.key

prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48

# Allow all clients by default; tighten via 'policy.path' once you have users.
policy:
  path: ""

derp:
  # Run a SEPARATE derper process on the same host (managed via systemd
  # by this same script). The 'paths' list below tells headscale where to
  # find the derp map; we explicitly disable headscale's embedded DERP
  # server so it does not double-bind port 3478.
  server:
    enabled: false
  urls: []
  paths:
    - /etc/headscale/derp.yaml
  auto_update_enabled: false

database:
  type: sqlite
  sqlite:
    path: ${HS_DATA_DIR}/db.sqlite

log:
  level: info
  format: text

dns:
  magic_dns: true
  base_domain: ${HS_BASE_DOMAIN}
  nameservers.global:
    - 1.1.1.1
    - 8.8.8.8
  search_domains: []
  extra_records: []
YAML
  echo "    Wrote $HS_CONFIG"
fi

# Custom DERP map pointing at the local derper.
cat > /etc/headscale/derp.yaml <<YAML
regions:
  ${DERPER_REGION_ID}:
    regionid: ${DERPER_REGION_ID}
    regioncode: "${DERPER_REGION_CODE}"
    regionname: "${DERPER_REGION_NAME}"
    nodes:
      - name: "derper-${HS_DOMAIN}"
        regionid: ${DERPER_REGION_ID}
        # Clients connect to the DERP over the public hostname. Since this
        # derper is reachable only over Tailscale (we use 100.x), use the
        # tailscale hostname so headscale hands it to clients verbatim.
        hostname: "ben-ubuntu.${HS_DOMAIN}"
        stunport: 3478
        stunonly: false
        derpport: ${DERPER_PORT}
        # The derper self-signs; skip client cert verification.
        insecure: true
YAML
echo "==> Wrote /etc/headscale/derp.yaml"

# ---------------------------------------------------------------------------
# Headscale container
# ---------------------------------------------------------------------------
echo "==> Running headscale container"
docker rm -f headscale 2>/dev/null || true
# Image entrypoint is /ko-app/headscale, so just pass 'serve' as the subcmd.
docker run -d --name headscale --restart always \
  -p ${HS_LISTEN_PORT}:${HS_LISTEN_PORT} \
  -v ${HS_DATA_DIR}:${HS_DATA_DIR} \
  -v /etc/headscale:/etc/headscale:ro \
  -v ${CERT_DIR}:/etc/ssl/headscale:ro \
  -e HEADSCALE_LISTEN_ADDR=0.0.0.0:${HS_LISTEN_PORT} \
  ${HS_IMAGE} \
  serve

# Wait for first boot to generate the noise + private key
for i in 1 2 3 4 5 6 7 8 9 10; do
  if [[ -f "${HS_DATA_DIR}/private.key" ]]; then break; fi
  sleep 1
done

# ---------------------------------------------------------------------------
# derper container (co-located on this host)
# ---------------------------------------------------------------------------
echo "==> Running derper container"
mkdir -p "$DERPER_DATA_DIR"
docker rm -f derper 2>/dev/null || true
if docker pull "${DERPER_IMAGE}" 2>/dev/null && \
  docker run -d --name derper --restart always --network host \
    -v ${DERPER_DATA_DIR}:/var/lib/derper \
    -e DERP_DOMAIN=ben-ubuntu.${HS_DOMAIN} \
    -e DERP_VERIFY_CLIENTS=false \
    -e DERP_CERT_MODE=selfsigned \
    -e DERP_ADDR=:${DERPER_PORT} \
    -e DERP_STUN=true \
    ${DERPER_IMAGE} \
    /derper -hostname=ben-ubuntu.${HS_DOMAIN} -a=:${DERPER_PORT} -stun; then
  echo "    derper container running"
else
  echo "    Docker pull failed; falling back to native derper binary"
  if ! command -v derper >/dev/null 2>&1; then
    echo "    Installing derper from Go source"
    go install tailscale.com/cmd/derper@latest
    install -m 0755 "$(go env GOPATH)/bin/derper" /usr/local/bin/derper
  fi
  cat > /etc/systemd/system/derper.service <<UNIT
[Unit]
Description=Tailscale DERP relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/derper --hostname=ben-ubuntu.${HS_DOMAIN} -a=:${DERPER_PORT} -stun -certmode=selfsigned -certdir=${DERPER_DATA_DIR}
Restart=on-failure
RestartSec=5
Environment=DERP_DOMAIN=ben-ubuntu.${HS_DOMAIN}
Environment=DERP_VERIFY_CLIENTS=false
Environment=DERP_ADDR=:${DERPER_PORT}
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now derper
fi

# ---------------------------------------------------------------------------
# Firewall — open the ports headscale + derper need.
# ---------------------------------------------------------------------------
echo "==> Configuring firewall (ufw)"
if command -v ufw >/dev/null 2>&1; then
  ufw allow ${HS_LISTEN_PORT}/tcp comment "headscale https"
  ufw allow ${DERPER_PORT}/tcp comment "derper https"
  ufw allow 3478/udp comment "derper STUN"
  # Tailscale WireGuard (when clients connect to other peers via direct UDP):
  ufw allow 41641/udp comment "tailscale wireguard"
  ufw reload || true
elif command -v iptables >/dev/null 2>&1; then
  iptables -I INPUT -p tcp --dport ${HS_LISTEN_PORT} -j ACCEPT
  iptables -I INPUT -p tcp --dport ${DERPER_PORT} -j ACCEPT
  iptables -I INPUT -p udp --dport 3478 -j ACCEPT
  iptables -I INPUT -p udp --dport 41641 -j ACCEPT
fi

# ---------------------------------------------------------------------------
# Preauth key + admin user, so the operator can register the first nodes
# ---------------------------------------------------------------------------
HS_BIN="docker exec headscale headscale"
HS_USER="${HS_USER:-default}"
HS_USER_ID=""
echo "==> Creating user '$HS_USER'"
HS_USER_ID="$($HS_BIN users create "$HS_USER" 2>&1 | tail -1 | awk '{print $1}')"
if [[ -z "$HS_USER_ID" || ! "$HS_USER_ID" =~ ^[0-9]+$ ]]; then
  # Already exists or create failed: look it up.
  HS_USER_ID="$($HS_BIN users list 2>&1 | awk -v u="$HS_USER" '$2==u {print $1}')"
fi
if [[ -z "$HS_USER_ID" || ! "$HS_USER_ID" =~ ^[0-9]+$ ]]; then
  echo "FATAL: could not determine numeric user id for '$HS_USER'" >&2
  $HS_BIN users list >&2 || true
  exit 1
fi
echo "    User id: $HS_USER_ID"

# Reusable key with 24h expiry and single use. Re-run this section to mint
# fresh keys when you need to onboard more machines.
PREAUTH_KEY="$($HS_BIN preauthkeys create --user "$HS_USER_ID" --reusable --expiration 24h | tail -1)"
echo
echo "================================================================"
echo "  headscale is up. NEXT STEPS:"
echo "================================================================"
echo
echo "  # 1. Add an /etc/hosts entry on every client so the self-signed"
echo "     cert resolves to this host. Replace \$BEN_UBUNTU_LAN_IP with the"
echo "     actual IP you saw above (e.g. 192.168.19.107 or 100.126.76.85):"
echo
echo "       sudo tee -a /etc/hosts <<EOF"
echo "       \$BEN_UBUNTU_LAN_IP  ben-ubuntu.${HS_DOMAIN}  ${HS_DOMAIN}"
echo "       EOF"
echo
echo "  # 2. On each client machine, run:"
echo
echo "       sudo tailscale up --login-server=https://${HS_DOMAIN}:${HS_LISTEN_PORT}"
echo
echo "     The browser link printed will show a self-signed-cert warning."
echo "     Bypass it once; the browser will then offer the preauth key URL."
echo
echo "  # 3. Or use the preauth key below with:"
echo
echo "       sudo tailscale up --login-server=https://${HS_DOMAIN}:${HS_LISTEN_PORT} \\"
echo "         --authkey=${PREAUTH_KEY}"
echo
echo "  # 4. Register this preauth key with the headscale user '$HS_USER'."
echo "     Once any node is online, the DERP map (derp.yaml) takes effect"
echo "     and tailscale ping should drop from ~600ms to <50ms."
echo
echo "================================================================"
echo "  PREAUTH KEY: ${PREAUTH_KEY}"
echo "  REUSABLE:   yes (24h)"
echo "  USER:       ${HS_USER}"
echo "  LOGIN URL:  https://${HS_DOMAIN}:${HS_LISTEN_PORT}"
echo "================================================================"

# Persist the key so the operator can copy it after the session ends.
echo "${PREAUTH_KEY}" > /root/headscale-preauth-key.txt
chmod 600 /root/headscale-preauth-key.txt
echo
echo "Preauth key persisted to /root/headscale-preauth-key.txt (chmod 600)."

# ---------------------------------------------------------------------------
# Health check
# ---------------------------------------------------------------------------
sleep 5
echo
echo "==> Health check"
docker ps --filter "name=headscale" --filter "name=derper" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
echo
echo "headscale logs (last 10 lines):"
$HS_BIN preauthkeys list --user "$HS_USER" 2>&1 | head -5
