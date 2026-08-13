#!/usr/bin/env bash
# Creates the local Postgres databases and the unprivileged application role.
# The app role is deliberately NOT the table owner, so row-level security
# cannot be bypassed — see docs/ARCHITECTURE.md §7.
#
# ClickHouse is not set up here: it is not needed until Stage 5 (message logs).
# See docs/LOCAL_DEV.md for the macOS Gatekeeper issue blocking the brew cask.
set -euo pipefail

for db in sms_dev sms_test; do
  psql -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='$db'" | grep -q 1 \
    || psql -d postgres -c "CREATE DATABASE $db"
done

psql -d postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='sms_app'" | grep -q 1 \
  || psql -d postgres -c "CREATE ROLE sms_app LOGIN PASSWORD 'sms_app_local'"

for db in sms_dev sms_test; do
  psql -d "$db" -c "GRANT USAGE ON SCHEMA public TO sms_app"
done

echo "databases ready: sms_dev, sms_test"
