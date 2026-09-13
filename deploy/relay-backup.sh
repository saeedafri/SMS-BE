#!/usr/bin/env bash
# Nightly backup of Relay's own Postgres database (sms), run by relay-backup.timer.
#
# The deploy already dumps before every migration; this covers the days nobody
# deploys, which is when a disk dies. A dump is kept only if it is a complete,
# readable file: a truncated dump discovered during a restore is not a backup.
#
# Off-box copy: set BACKUP_S3_URI on AWS, or BACKUP_RCLONE_TARGET in /opt/relay/.env (for example
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

# Relay's database only. On Hostinger it lives in the shared ems-postgres; on
# AWS in relay-postgres — set BACKUP_PG_CONTAINER / BACKUP_PG_USER in .env.
docker exec "${BACKUP_PG_CONTAINER:-ems-postgres}" pg_dump -U "${BACKUP_PG_USER:-ems_user}" -d sms | gzip > "$file.partial"

gzip -t "$file.partial"
# grep -c reads to the end; grep -q would exit on the first match and break
# the pipe, which pipefail then reports as a failed backup.
zcat "$file.partial" | tail -n 10 | grep -c "PostgreSQL database dump complete" >/dev/null
zcat "$file.partial" | grep -c "^COPY public.wallet_ledger " >/dev/null
mv "$file.partial" "$file"

ls -t "$dir"/sms_nightly_*.sql.gz | tail -n +15 | xargs -r rm -f

if [ -n "${BACKUP_RCLONE_TARGET:-}" ]; then
  rclone copy "$file" "$BACKUP_RCLONE_TARGET/"
fi
# On AWS: BACKUP_S3_URI=s3://bucket/postgres, written with the instance role.
if [ -n "${BACKUP_S3_URI:-}" ]; then
  aws s3 cp --only-show-errors "$file" "$BACKUP_S3_URI/"
fi

echo "backup ok: $file ($(du -h "$file" | cut -f1))"
notify ""
