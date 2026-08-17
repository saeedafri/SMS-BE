#!/usr/bin/env bash
# Prepares the SHARED Hostinger VPS to run Relay. Run ONCE, as root, on the box.
#
#   ssh root@31.97.186.223 'bash -s' < deploy/install-server.sh
#
# Idempotent: every step checks before acting, so re-running after a partial
# failure resumes rather than duplicating. That matters because the most likely
# time to run this twice is right after something went wrong.
#
# ---------------------------------------------------------------------------
# WHAT THIS DELIBERATELY DOES NOT DO
#
# An earlier version of this script installed PostgreSQL, Redis and Caddy from
# the distro repos and ran `ufw --force enable`. On THIS box every one of those
# is destructive, because Relay is a guest here — Rentocloud and EMS were
# running first:
#
#   * Caddy binds :80 and :443. nginx already holds them for api.rentocloud.com,
#     frontend.rentocloud.com and ems-api.saqibsaeed.cloud. Installing Caddy
#     takes all three sites down.
#   * A second PostgreSQL and a second Redis waste ~600 MB to duplicate the
#     ems-postgres and ems-redis containers already running.
#   * `ufw allow OpenSSH` opens 22 only. sshd here ALSO listens on 2222, so
#     enabling the firewall silently removes an SSH path — on a remote box,
#     with no console, that is how you lose access to a server.
#
# So: no package installs, no firewall changes, no new listeners on shared
# ports. Relay reuses ems-postgres, brings its own ClickHouse and Redis on
# loopback-only ports, and is published through a new nginx site.
# ---------------------------------------------------------------------------
set -euo pipefail

say() { printf '\n\033[36m▸ %s\033[0m\n' "$1"; }
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }
die() { printf '\n\033[31m✗ %s\033[0m\n' "$1" >&2; exit 1; }

say "Checking what is already here"
command -v docker >/dev/null || die "docker is not installed; this script will not install it"
docker ps --format '{{.Names}}' | grep -qx ems-postgres \
  || die "ems-postgres is not running — Relay reuses it and will not create a second Postgres"
command -v nginx >/dev/null || die "nginx is not installed; Relay publishes through it"
ok "docker, ems-postgres and nginx present"

say "Service user and directories"
# An unprivileged user with no login shell. The API never needs to be root, and
# a compromised binary should not be able to open a session.
id -u relay >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin relay
# 755, not 700: systemd sets WorkingDirectory=/opt/relay and the service user
# must be able to traverse it. The secrets inside are what get locked down.
install -d -m 755 /opt/relay /opt/relay/deploy
install -d -o relay -g relay /var/log/relay
ok "user 'relay', /opt/relay, /var/log/relay"

say "Secrets"
# Generated on the server and never leaving it. Guarded so a re-run cannot
# rotate credentials out from under a running service and a live database.
if [ ! -f /opt/relay/.secrets ]; then
  {
    echo "PG_APP_PASSWORD=$(openssl rand -hex 24)"
    echo "PG_MIGRATOR_PASSWORD=$(openssl rand -hex 24)"
    echo "CLICKHOUSE_PASSWORD=$(openssl rand -hex 24)"
    echo "JWT_SIGNING_KEY=$(openssl rand -base64 48 | tr -d '\n')"
  } > /opt/relay/.secrets
  ok "generated"
else
  ok "already present, left alone"
fi
chmod 600 /opt/relay/.secrets
. /opt/relay/.secrets

say "Database and roles inside the existing ems-postgres"
pg() { docker exec -i ems-postgres psql -U ems_user -v ON_ERROR_STOP=1 "$@"; }

# sms_app serves every request and is deliberately unprivileged: tenant
# isolation is enforced by row-level security, and a superuser bypasses RLS
# entirely. Making the app role a superuser would silently disable the single
# mechanism keeping tenants apart.
pg -d postgres >/dev/null <<SQL
DO \$\$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='sms_migrator') THEN
    CREATE ROLE sms_migrator LOGIN PASSWORD '${PG_MIGRATOR_PASSWORD}';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='sms_app') THEN
    CREATE ROLE sms_app LOGIN PASSWORD '${PG_APP_PASSWORD}';
  END IF;
END \$\$;
SQL

# BYPASSRLS on the MIGRATION role only. Every table is FORCE ROW LEVEL
# SECURITY, which binds even the table owner, so without this the seeder cannot
# insert a tenant at all — it fails with "new row violates row-level security
# policy for table tenants".
#
# This is invisible in local development, where DATABASE_ADMIN_URL points at
# the developer's own superuser account and bypasses RLS implicitly. It only
# appears the first time the migrator is a real, unprivileged role.
pg -d postgres -c "ALTER ROLE sms_migrator BYPASSRLS" >/dev/null

pg -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='sms'" | grep -q 1 \
  || pg -d postgres -c "CREATE DATABASE sms OWNER sms_migrator" >/dev/null

# Both are trusted extensions, so the owner could create them — done as the
# superuser anyway so ownership is never in question.
pg -d sms -c "CREATE EXTENSION IF NOT EXISTS pgcrypto" \
          -c "CREATE EXTENSION IF NOT EXISTS citext" \
          -c "GRANT USAGE ON SCHEMA public TO sms_app" >/dev/null
ok "database 'sms', roles sms_app (no RLS bypass) and sms_migrator"

say "ClickHouse and Redis"
[ -f /opt/relay/deploy/docker-compose.yml ] \
  || die "upload deploy/docker-compose.yml and deploy/clickhouse-limits.xml to /opt/relay/deploy first"
cd /opt/relay/deploy
docker compose --env-file /opt/relay/.secrets up -d
for _ in $(seq 1 30); do
  curl -sf --max-time 3 http://127.0.0.1:8123/ping >/dev/null 2>&1 && break
  sleep 3
done
curl -sf --max-time 3 http://127.0.0.1:8123/ping >/dev/null \
  || die "ClickHouse did not become ready; check: docker logs relay-clickhouse-1"

# CLICKHOUSE_DB in the compose file only takes effect on a FIRST start against
# an empty volume. If the very first start failed for any other reason the
# volume already exists, initialisation is skipped, and the database is never
# created — so create it explicitly rather than relying on that.
docker exec relay-clickhouse-1 clickhouse-client --user relay \
  --password "$CLICKHOUSE_PASSWORD" --query "CREATE DATABASE IF NOT EXISTS sms"
ok "clickhouse (loopback :8123/:9000) and redis (loopback :6380)"

say "Done"
cat <<'NEXT'
  next, from your machine:
    ./deploy/deploy.sh 31.97.186.223      # binaries, migrations, systemd unit

  then, to publish it (needs sms-api.saqibsaeed.cloud -> this box in DNS):
    scp deploy/nginx-sms-api.conf root@HOST:/etc/nginx/sites-available/sms-api.saqibsaeed.cloud
    ssh root@HOST 'ln -sf /etc/nginx/sites-available/sms-api.saqibsaeed.cloud \
        /etc/nginx/sites-enabled/ && nginx -t && systemctl reload nginx \
      && certbot --nginx -d sms-api.saqibsaeed.cloud --non-interactive --agree-tos -m you@example.com'
NEXT
