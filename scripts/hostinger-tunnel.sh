#!/usr/bin/env bash
# Forward the Hostinger datastores to localhost. They listen on 127.0.0.1 inside
# the VPS and are not published, so SSH is the only way in — which is also why
# there is no reason to run Postgres, Redis or ClickHouse on a laptop.
#
#   local 15432 -> ems-postgres       (databases: sms, sms_test)
#   local 16380 -> relay-redis-1
#   local  8123 -> relay-clickhouse-1 HTTP
#   local  9000 -> relay-clickhouse-1 native — the port is not negotiable:
#                  store.OpenClickHouse derives the native address as host:9000
#                  from the HTTP URL, so a forwarded port would be ignored.
#
# Usage: scripts/hostinger-tunnel.sh [start|stop|status]
set -euo pipefail

HOST=${RELAY_VPS:-root@31.97.186.223}
PIDFILE=${TMPDIR:-/tmp}/relay-hostinger-tunnel.pid

case "${1:-start}" in
start)
    if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
        echo "already up (pid $(cat "$PIDFILE"))"
        exit 0
    fi
    ssh -f -N -o ExitOnForwardFailure=yes \
        -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
        -L 15432:127.0.0.1:5432 \
        -L 16380:127.0.0.1:6380 \
        -L 8123:127.0.0.1:8123 \
        -L 9000:127.0.0.1:9000 \
        "$HOST"
    pgrep -f "15432:127.0.0.1:5432" > "$PIDFILE"
    echo "tunnel up (pid $(cat "$PIDFILE"))"
    ;;
stop)
    if [ -f "$PIDFILE" ]; then kill "$(cat "$PIDFILE")" 2>/dev/null || true; fi
    rm -f "$PIDFILE"
    echo "tunnel down"
    ;;
status)
    for port in 15432 16380 8123 9000; do
        if nc -z 127.0.0.1 "$port" 2>/dev/null; then echo "$port up"; else echo "$port DOWN"; fi
    done
    ;;
esac
