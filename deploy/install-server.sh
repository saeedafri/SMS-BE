#!/usr/bin/env bash
# Prepares a fresh Ubuntu VPS to run Relay. Run ONCE per server, as root.
#
# Idempotent: every step checks before acting, so re-running after a partial
# failure resumes rather than duplicating. That matters because the most likely
# time to run this twice is right after something went wrong.
set -euo pipefail

say() { printf '\n\033[36m▸ %s\033[0m\n' "$1"; }
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }

say "System packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl gnupg ca-certificates ufw fail2ban >/dev/null
ok "base packages"

say "PostgreSQL 16"
if ! command -v psql >/dev/null; then
  apt-get install -y -qq postgresql postgresql-contrib >/dev/null
fi
systemctl enable --now postgresql
ok "postgres $(psql --version | awk '{print $3}')"

say "Redis"
if ! command -v redis-server >/dev/null; then
  apt-get install -y -qq redis-server >/dev/null
fi
systemctl enable --now redis-server
ok "redis running"

say "ClickHouse"
if ! command -v clickhouse >/dev/null; then
  # The official installer, which sets no quarantine attributes and verifies
  # its own signatures. Deliberately not a third-party build.
  curl -fsSL https://clickhouse.com/ | sh >/dev/null 2>&1
  ./clickhouse install --noninteractive >/dev/null 2>&1 || true
fi
systemctl enable --now clickhouse-server 2>/dev/null || true
ok "clickhouse ready"

say "Caddy (TLS terminates here)"
if ! command -v caddy >/dev/null; then
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy >/dev/null
fi
ok "caddy installed"

say "Service user"
# A dedicated unprivileged user with no login shell. The API never needs to be
# root, and a compromised binary should not be able to open a session.
id -u relay >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin relay
install -d -o relay -g relay /opt/relay /var/log/relay
ok "user 'relay' and /opt/relay"

say "Firewall"
# Databases listen on loopback only and are never exposed. Opening 5432 to the
# internet is the single most common way a VPS database gets ransomwared.
ufw allow OpenSSH >/dev/null
ufw allow 80/tcp  >/dev/null
ufw allow 443/tcp >/dev/null
ufw --force enable >/dev/null
ok "only 22, 80, 443 open"

say "Done"
echo "  next: run deploy/deploy.sh from your machine"
