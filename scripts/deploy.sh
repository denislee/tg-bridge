#!/usr/bin/env bash
# Deploy tg-bridge to a remote Linux host over SSH.
# Idempotent: re-running updates the binary and config in place.
#
# Usage:
#   scripts/deploy.sh user@host [--arch=arm64|armv7|amd64] [--config=path]
#
# Environment overrides:
#   DEPLOY_TARGET, DEPLOY_ARCH, DEPLOY_CONFIG
#
# Requirements on the remote host: systemd, openssl, a regular user with
# passwordless sudo (or root login).

set -euo pipefail

# --- parse args --------------------------------------------------------------

TARGET="${DEPLOY_TARGET:-}"
ARCH="${DEPLOY_ARCH:-arm64}"
CONFIG="${DEPLOY_CONFIG:-config.yaml}"

for arg in "$@"; do
  case "$arg" in
    --arch=*)   ARCH="${arg#*=}" ;;
    --config=*) CONFIG="${arg#*=}" ;;
    --help|-h)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *)
      if [[ -z "$TARGET" ]]; then
        TARGET="$arg"
      else
        echo "unknown argument: $arg" >&2
        exit 2
      fi
      ;;
  esac
done

if [[ -z "$TARGET" ]]; then
  echo "error: target host required (e.g. pi@rpi.local)" >&2
  exit 2
fi

case "$ARCH" in
  arm64) BIN_NAME="tg-bridge-linux-arm64"  ;;
  armv7) BIN_NAME="tg-bridge-linux-armv7"  ;;
  amd64) BIN_NAME="tg-bridge"              ;;
  *) echo "error: --arch must be arm64|armv7|amd64 (got $ARCH)" >&2; exit 2 ;;
esac

if [[ ! -f "$CONFIG" ]]; then
  echo "error: $CONFIG not found — cp config.example.yaml $CONFIG and edit it first" >&2
  exit 2
fi

# --- helpers ----------------------------------------------------------------

say() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
ok()  { printf '\033[1;32m[ok]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

rsh() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$TARGET" "$@"; }

# --- preflight --------------------------------------------------------------

say "preflight: checking local toolchain"
command -v go   >/dev/null || die "go not installed locally"
command -v ssh  >/dev/null || die "ssh not installed locally"
command -v scp  >/dev/null || die "scp not installed locally"

say "preflight: checking remote host $TARGET"
rsh 'command -v systemctl >/dev/null' || die "remote host has no systemd"
rsh 'command -v openssl >/dev/null'    || die "remote host needs openssl"

# --- build ------------------------------------------------------------------

say "building $BIN_NAME"
case "$ARCH" in
  arm64) CGO_ENABLED=0 GOOS=linux GOARCH=arm64         go build -trimpath -ldflags="-s -w" -o "bin/$BIN_NAME" ./cmd/tg-bridge ;;
  armv7) CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7   go build -trimpath -ldflags="-s -w" -o "bin/$BIN_NAME" ./cmd/tg-bridge ;;
  amd64) CGO_ENABLED=0 GOOS=linux GOARCH=amd64         go build -trimpath -ldflags="-s -w" -o "bin/$BIN_NAME" ./cmd/tg-bridge ;;
esac
ok "built $(du -h "bin/$BIN_NAME" | awk '{print $1}') binary"

# --- remote: user + layout --------------------------------------------------

say "ensuring /opt/tg-bridge layout and tgbridge user exist"
rsh 'sudo bash -s' <<'REMOTE'
set -euo pipefail
if ! id tgbridge >/dev/null 2>&1; then
  useradd --system --home /opt/tg-bridge --shell /usr/sbin/nologin tgbridge
fi
install -d -o tgbridge -g tgbridge -m 750 /opt/tg-bridge
install -d -o tgbridge -g tgbridge -m 700 /opt/tg-bridge/data
REMOTE
ok "layout ready"

# --- upload ------------------------------------------------------------------

say "uploading binary, config, and systemd unit"
scp -q "bin/$BIN_NAME"                "$TARGET:/tmp/tg-bridge.new"
scp -q "$CONFIG"                      "$TARGET:/tmp/tg-bridge.config.new"
scp -q "systemd/tg-bridge.service"    "$TARGET:/tmp/tg-bridge.service.new"

rsh 'sudo bash -s' <<'REMOTE'
set -euo pipefail
install -o tgbridge -g tgbridge -m 755 /tmp/tg-bridge.new          /opt/tg-bridge/tg-bridge
install -o tgbridge -g tgbridge -m 600 /tmp/tg-bridge.config.new   /opt/tg-bridge/config.yaml
install -m 644                          /tmp/tg-bridge.service.new /etc/systemd/system/tg-bridge.service
rm -f /tmp/tg-bridge.new /tmp/tg-bridge.config.new /tmp/tg-bridge.service.new
systemctl daemon-reload
REMOTE
ok "files installed"

# --- start / restart --------------------------------------------------------

say "enabling and restarting tg-bridge"
rsh 'sudo systemctl enable tg-bridge >/dev/null 2>&1 || true; sudo systemctl restart tg-bridge'

# Give it a moment to generate the cert and start listening.
sleep 2

# --- health + fingerprint ---------------------------------------------------

say "checking service status"
if ! rsh 'systemctl is-active --quiet tg-bridge'; then
  rsh 'sudo journalctl -u tg-bridge -n 50 --no-pager'
  die "tg-bridge is not running; see logs above"
fi
ok "tg-bridge is active"

say "computing cert fingerprint"
FP=$(rsh 'sudo openssl x509 -in /opt/tg-bridge/data/cert.pem -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2 | tr -d :' || true)
if [[ -z "$FP" ]]; then
  echo "warning: could not read cert.pem yet — check logs manually" >&2
else
  ok "cert SHA-256 = ${FP,,}"
fi

# --- summary ----------------------------------------------------------------

LISTEN=$(awk -F: '/^listen:/ { gsub(/[" ]/,"",$0); sub(/^listen:/,"",$0); print }' "$CONFIG" | tr -d '"')
echo
printf '\033[1;32mdeploy complete\033[0m\n'
echo "  host:         $TARGET"
echo "  listen:       ${LISTEN:-:8443}"
[[ -n "$FP" ]] && echo "  sha256 pin:   ${FP,,}"
echo
echo "next steps:"
echo "  - tail logs:     ssh $TARGET 'sudo journalctl -u tg-bridge -f'"
echo "  - run login:     see README 'First-time login' (one-off curl flow)"
echo "  - pin in firmware: bake the sha256 into the ESP32 build"
