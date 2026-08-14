#!/usr/bin/env bash
# Builds, ships and restarts Relay on the VPS. Run from your machine.
#
#   ./deploy/deploy.sh 203.0.113.10
#
# Safe to re-run. The binary is uploaded beside the running one and swapped
# atomically, so a failed upload never leaves a half-written executable in place.
set -euo pipefail
cd "$(dirname "$0")/.."

HOST=${1:?usage: deploy.sh <vps-ip-or-host> [ssh-key]}
KEY=${2:-$HOME/.ssh/id_ed25519}
SSH="ssh -i $KEY -o ConnectTimeout=15 root@$HOST"

say() { printf '\n\033[36m▸ %s\033[0m\n' "$1"; }
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }

say "Checking the server is reachable"
$SSH 'echo ok' >/dev/null || { echo "cannot reach $HOST over SSH"; exit 1; }
ok "connected"

say "Building for Linux"
# Static build: no glibc version to match against whatever the VPS runs.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /tmp/control-api ./cmd/control-api
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /tmp/seed-demo ./cmd/seed-demo
ok "$(du -h /tmp/control-api | cut -f1) binary"

say "Uploading"
scp -q -i "$KEY" /tmp/control-api "root@$HOST:/opt/relay/control-api.new"
scp -q -i "$KEY" /tmp/seed-demo   "root@$HOST:/opt/relay/seed-demo.new"
scp -q -i "$KEY" -r db            "root@$HOST:/opt/relay/"
scp -q -i "$KEY" deploy/relay-api.service "root@$HOST:/etc/systemd/system/relay-api.service"
ok "uploaded"

say "Running migrations"
$SSH 'cd /opt/relay && \
  export PATH=$PATH:/usr/local/go/bin:/root/go/bin && \
  set -a && . /opt/relay/.env && set +a && \
  goose -dir db/migrations postgres "$DATABASE_ADMIN_URL" up' || {
    echo "migrations failed — the old binary is still running, nothing was swapped"
    exit 1
  }
ok "schema current"

say "Swapping the binary"
# mv on the same filesystem is atomic, so there is no instant where the file
# exists but is incomplete.
$SSH 'set -e
  chown relay:relay /opt/relay/control-api.new /opt/relay/seed-demo.new
  chmod 755 /opt/relay/control-api.new /opt/relay/seed-demo.new
  mv /opt/relay/control-api.new /opt/relay/control-api
  mv /opt/relay/seed-demo.new /opt/relay/seed-demo
  systemctl daemon-reload
  systemctl enable relay-api >/dev/null 2>&1 || true
  systemctl restart relay-api'
ok "service restarted"

say "Verifying it came back"
# Poll rather than sleep-and-hope: report the real state, not an assumption.
for attempt in $(seq 1 15); do
  health=$($SSH 'curl -s --max-time 3 localhost:8080/healthz' 2>/dev/null || true)
  if grep -q '"status":"ok"' <<<"$health"; then
    ok "healthy: $health"
    echo
    printf '\033[32mDEPLOYED\033[0m — %s\n' "$HOST"
    exit 0
  fi
  sleep 2
done

echo
printf '\033[31mDEPLOY FAILED — service did not become healthy\033[0m\n'
$SSH 'journalctl -u relay-api -n 40 --no-pager'
exit 1
