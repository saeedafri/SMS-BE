#!/usr/bin/env bash
# Nightly backup of Relay's data, run by relay-backup.timer: the Postgres
# database (sms), the ClickHouse message log (every message and event) and the
# uploaded media (brand assets, identity documents).
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
stamp=$(date +%Y%m%d_%H%M%S)
file="$dir/sms_nightly_$stamp.sql.gz"
notify() { [ -n "${BACKUP_HEALTHCHECK_URL:-}" ] && curl -fsS -m 10 --retry 3 "${BACKUP_HEALTHCHECK_URL}$1" >/dev/null || true; }
trap 'notify /fail; rm -f "$dir"/*.partial' ERR

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

# ClickHouse: each table's schema and its rows in Native format, one archive.
# Restore: run each .sql, then
#   clickhouse-client --query "INSERT INTO sms.<table> FORMAT Native" < <table>.native
ch="${BACKUP_CH_CONTAINER:-relay-clickhouse-1}"
chfile="$dir/clickhouse_nightly_$stamp.tar.gz"
if docker inspect -f '{{.State.Running}}' "$ch" 2>/dev/null | grep -qx true; then
  work=$(mktemp -d)
  for table in $(docker exec "$ch" clickhouse-client --query "SHOW TABLES FROM sms"); do
    docker exec "$ch" clickhouse-client --format TSVRaw --query "SHOW CREATE TABLE sms.$table" > "$work/$table.sql"
    docker exec "$ch" clickhouse-client --query "SELECT * FROM sms.$table FORMAT Native" > "$work/$table.native"
  done
  [ -s "$work/messages.sql" ]
  tar -czf "$chfile.partial" -C "$work" .
  rm -rf "$work"
  gzip -t "$chfile.partial"
  mv "$chfile.partial" "$chfile"
  ls -t "$dir"/clickhouse_nightly_*.tar.gz | tail -n +15 | xargs -r rm -f
fi

# Uploaded media, when this box stores it on disk.
mediafile="$dir/media_nightly_$stamp.tar.gz"
if [ -n "${MEDIA_ROOT:-}" ] && [ -d "$MEDIA_ROOT" ]; then
  tar -czf "$mediafile.partial" -C "$MEDIA_ROOT" .
  gzip -t "$mediafile.partial"
  mv "$mediafile.partial" "$mediafile"
  ls -t "$dir"/media_nightly_*.tar.gz | tail -n +15 | xargs -r rm -f
fi

for extra in "$chfile" "$mediafile"; do
  [ -f "$extra" ] || continue
  if [ -n "${BACKUP_RCLONE_TARGET:-}" ]; then
    rclone copy "$extra" "$BACKUP_RCLONE_TARGET/"
  fi
  # Same prefix as the dumps: the instance role may write only there, and the
  # bucket's 30-day lifecycle rule covers it.
  if [ -n "${BACKUP_S3_URI:-}" ]; then
    aws s3 cp --only-show-errors "$extra" "$BACKUP_S3_URI/"
  fi
done

for done_file in "$file" "$chfile" "$mediafile"; do
  if [ -f "$done_file" ]; then
    echo "backup ok: $done_file ($(du -h "$done_file" | cut -f1))"
  fi
done
notify ""
