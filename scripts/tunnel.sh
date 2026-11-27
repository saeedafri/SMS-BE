#!/usr/bin/env bash
# Forward Relay's datastores on the Hostinger server (189.72.144.98) to
# localhost. They listen on the server's loopback only, so SSH is the only way in.
#
#   local 18080 -> the relay-test-api container, including /v1/dev/*
#   local 15432 -> relay-test-postgres   (databases: sms, sms_test)
#   local 16380 -> relay-test-redis
#   local  8123 -> relay-test-clickhouse HTTP
#   local  9000 -> relay-test-clickhouse native — the port is not negotiable:
#                  store.OpenClickHouse derives the native address as host:9000
#                  from the HTTP URL, so a forwarded port would be ignored.
#
# Needs the `hostinger-test` host in ~/.ssh/config (HostName 189.72.144.98,
# User root, your key in the server's authorized_keys).
#
# Usage: scripts/tunnel.sh [start|stop|status]
set -euo pipefail

HOST=${RELAY_HOST:-hostinger-test}
PIDFILE=${TMPDIR:-/tmp}/relay-tunnel.pid

case "${1:-start}" in
start)
    if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
        echo "already up (pid $(cat "$PIDFILE"))"
        exit 0
    fi
    ssh -f -N -o ExitOnForwardFailure=yes \
        -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
        -L 18080:127.0.0.1:8080 \
        -L 15432:127.0.0.1:15432 \
        -L 16380:127.0.0.1:16380 \
        -L 8123:127.0.0.1:8123 \
        -L 9000:127.0.0.1:9000 \
        "$HOST"
    pgrep -f "15432:127.0.0.1:15432" > "$PIDFILE"
    echo "tunnel up (pid $(cat "$PIDFILE"))"
    ;;
stop)
    if [ -f "$PIDFILE" ]; then kill "$(cat "$PIDFILE")" 2>/dev/null || true; fi
    rm -f "$PIDFILE"
    echo "tunnel down"
    ;;
status)
    for port in 18080 15432 16380 8123 9000; do
        if nc -z 127.0.0.1 "$port" 2>/dev/null; then echo "$port up"; else echo "$port DOWN"; fi
    done
    ;;
esac
