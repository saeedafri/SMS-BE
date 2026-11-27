#!/usr/bin/env bash
# Nightly backup of Relay's data on the Hostinger server, run by
# relay-backup.timer: the Postgres database (sms), the ClickHouse message log
# (every message and event) and the uploaded media (brand assets, identity
# documents). All three live in the relay-test-* containers.
#
# The deploy already dumps Postgres before every migration; this covers the
# days nobody deploys, which is when a disk dies. A dump is kept only if it is a
# complete, readable file: a truncated dump discovered during a restore is not
# a backup.
#
# Off-box copy: set BACKUP_RCLONE_TARGET in /opt/relay-test/.env (for example
# "b2:relay-backups/sms") and install rclone with that remote configured. Until
# then the copies live on this same disk, which protects against mistakes and
# not against losing the machine.
#
# Alerting: set BACKUP_HEALTHCHECK_URL (healthchecks.io, Better Uptime, ...). It
# is pinged on success and on failure, so a job that silently stops running is
# noticed by the service, not by a customer.
set -euo pipefail

set -a; . /opt/relay-test/.env; set +a
dir=/opt/relay-test/backups
mkdir -p "$dir"
stamp=$(date +%Y%m%d_%H%M%S)
file="$dir/sms_nightly_$stamp.sql.gz"
notify() { [ -n "${BACKUP_HEALTHCHECK_URL:-}" ] && curl -fsS -m 10 --retry 3 "${BACKUP_HEALTHCHECK_URL}$1" >/dev/null || true; }
trap 'notify /fail; rm -f "$dir"/*.partial' ERR

docker exec relay-test-postgres pg_dump -U postgres -d sms | gzip > "$file.partial"

gzip -t "$file.partial"
# grep -c reads to the end; grep -q would exit on the first match and break
# the pipe, which pipefail then reports as a failed backup.
zcat "$file.partial" | tail -n 10 | grep -c "PostgreSQL database dump complete" >/dev/null
zcat "$file.partial" | grep -c "^COPY public.wallet_ledger " >/dev/null
mv "$file.partial" "$file"
ls -t "$dir"/sms_nightly_*.sql.gz | tail -n +15 | xargs -r rm -f

# ClickHouse: each table's schema and its rows in Native format, one archive.
# Restore: run each .sql, then
#   clickhouse-client --query "INSERT INTO sms.<table> FORMAT Native" < <table>.native
ch="docker exec relay-test-clickhouse clickhouse-client --user relay --password $CLICKHOUSE_PASSWORD"
chfile="$dir/clickhouse_nightly_$stamp.tar.gz"
work=$(mktemp -d)
for table in $($ch --query "SHOW TABLES FROM sms"); do
  $ch --format TSVRaw --query "SHOW CREATE TABLE sms.$table" > "$work/$table.sql"
  $ch --query "SELECT * FROM sms.$table FORMAT Native" > "$work/$table.native"
done
[ -s "$work/messages.sql" ]
tar -czf "$chfile.partial" -C "$work" .
rm -rf "$work"
gzip -t "$chfile.partial"
mv "$chfile.partial" "$chfile"
ls -t "$dir"/clickhouse_nightly_*.tar.gz | tail -n +15 | xargs -r rm -f

# Uploaded media, from the API container's volume.
mediafile="$dir/media_nightly_$stamp.tar.gz"
docker exec relay-test-api tar -czf - -C /var/lib/relay . > "$mediafile.partial"
gzip -t "$mediafile.partial"
mv "$mediafile.partial" "$mediafile"
ls -t "$dir"/media_nightly_*.tar.gz | tail -n +15 | xargs -r rm -f

for done_file in "$file" "$chfile" "$mediafile"; do
  if [ -n "${BACKUP_RCLONE_TARGET:-}" ]; then
    rclone copy "$done_file" "$BACKUP_RCLONE_TARGET/"
  fi
  echo "backup ok: $done_file ($(du -h "$done_file" | cut -f1))"
done
notify ""
