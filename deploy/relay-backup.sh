#!/usr/bin/env bash
# Nightly backup of Relay's own Postgres database (sms), run by relay-backup.timer.
#
# The deploy already dumps before every migration; this covers the days nobody
# deploys, which is when a disk dies. A dump is kept only if it is a complete,
# readable file: a truncated dump discovered during a restore is not a backup.
#
# Off-box copy: set BACKUP_RCLONE_TARGET in /opt/relay/.env (for example
# "b2:relay-backups/sms") and install rclone with that remote configured. Until
# then the copies live on this same disk, which protects against mistakes and
# not against losing the machine.
#
# Alerting: set BACKUP_HEALTHCHECK_URL (healthchecks.io, Better Uptime, ...). It
# is pinged on success and on failure, so a job that silently stops running is
# noticed by the service, not by a customer.
set -euo pipefail

set -a; . /opt/relay/.env; set +a
dir=/opt/relay/backups
mkdir -p "$dir"
file="$dir/sms_nightly_$(date +%Y%m%d_%H%M%S).sql.gz"
notify() { [ -n "${BACKUP_HEALTHCHECK_URL:-}" ] && curl -fsS -m 10 --retry 3 "${BACKUP_HEALTHCHECK_URL}$1" >/dev/null || true; }
trap 'notify /fail; rm -f "$file.partial"' ERR

# Relay's database only: ems-postgres also hosts a neighbour's.
docker exec ems-postgres pg_dump -U ems_user -d sms | gzip > "$file.partial"

gzip -t "$file.partial"
zcat "$file.partial" | tail -n 10 | grep -q "PostgreSQL database dump complete"
zgrep -q "^COPY public.wallet_ledger " "$file.partial"
mv "$file.partial" "$file"

ls -t "$dir"/sms_nightly_*.sql.gz | tail -n +15 | xargs -r rm -f

if [ -n "${BACKUP_RCLONE_TARGET:-}" ]; then
  rclone copy "$file" "$BACKUP_RCLONE_TARGET/"
fi

echo "backup ok: $file ($(du -h "$file" | cut -f1))"
notify ""
