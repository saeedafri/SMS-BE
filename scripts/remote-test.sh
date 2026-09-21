#!/usr/bin/env bash
# Run the full test suite ON the AWS server, next to its databases.
#
# Why: from this Mac every database round trip to ap-south-1 costs ~26 ms, a
# tenant-scoped write is four of them, and the suite took ~15 minutes waiting
# on the network. On the server the same round trip is ~0.1 ms.
#
# Nothing starts on this machine and Go is not installed on the server: the
# test binaries are cross-compiled HERE, copied over, and run THERE against the
# same test databases the local suite always used (sms_test, the test
# ClickHouse database, Redis) — only the tunnel ports are swapped for the
# server's own loopback ports. They run at the lowest CPU and I/O priority, so
# live traffic always wins.
#
# Not covered: -race. Cross-compiling with the race detector needs cgo; use
# `make test-race` (local, slow) when that is wanted.
#
# Usage: scripts/remote-test.sh [go test -run pattern]
set -euo pipefail

HOST=${RELAY_AWS:-relay-aws}
REMOTE=relay-tests          # in the remote user's home, not under /opt/relay
RUN=${1:-}
cd "$(dirname "$0")/.."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"

start=$(date +%s)
pkgs=$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
echo "compiling $(echo "$pkgs" | wc -l | tr -d ' ') test packages for linux..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o "$work/bin/" $pkgs

# The four variables the tests read, from .env, with the tunnel's local ports
# swapped for the server's loopback ports. Written 0600 and never printed.
umask 077
grep -E '^(TEST_DATABASE_URL|TEST_DATABASE_ADMIN_URL|TEST_CLICKHOUSE_URL|REDIS_URL)=' .env \
  | sed -e 's/:15432/:5432/g' -e 's/:16380/:6380/g' > "$work/test.env"
# A test with no database URL SKIPS and reports ok — a suite that silently
# skips is the one failure this script must never produce.
[ "$(wc -l < "$work/test.env" | tr -d ' ')" = "4" ] || { echo "missing test settings in .env" >&2; exit 2; }

echo "uploading..."
ssh "$HOST" "mkdir -p $REMOTE/bin"
rsync -az --delete "$work/bin/" "$HOST:$REMOTE/bin/"
rsync -a "$work/test.env" "$HOST:$REMOTE/test.env"
compiled=$(date +%s)

echo "running on $HOST..."
ssh "$HOST" "RUN='$RUN' bash -s" <<'EOS'
set -uo pipefail
cd ~/relay-tests
chmod 600 test.env
set -a; . ./test.env; set +a
rm -rf logs; mkdir -p logs
args=(-test.count=1 -test.timeout=15m)
[ -n "$RUN" ] && args+=(-test.run "$RUN")
for b in bin/*.test; do
  n=$(basename "$b" .test)
  ( t0=$(date +%s)
    nice -n 19 ionice -c 3 "./$b" "${args[@]}" > "logs/$n.log" 2>&1
    echo "$? $(( $(date +%s) - t0 ))" > "logs/$n.rc" ) &
done
wait
fail=0
for rc in logs/*.rc; do
  n=$(basename "$rc" .rc); read -r code secs < "$rc"
  if [ "$code" = "0" ]; then printf 'ok    %-14s %4ss\n' "$n" "$secs"
  else printf 'FAIL  %-14s %4ss\n' "$n" "$secs"; fail=1; fi
done
if [ "$fail" != "0" ]; then
  for rc in logs/*.rc; do
    n=$(basename "$rc" .rc); read -r code _ < "$rc"
    [ "$code" = "0" ] || { echo; echo "=== $n ==="; grep -E -- '--- FAIL|_test\.go:[0-9]+:|panic|FAIL' "logs/$n.log" | head -60; }
  done
fi
exit $fail
EOS
status=$?
end=$(date +%s)
echo "compile+upload $((compiled - start))s, run $((end - compiled))s, total $((end - start))s"
exit $status
