#!/usr/bin/env bash
# Run the full test suite ON the Hostinger server, next to its databases.
#
# Why: from this Mac every database round trip crosses the internet, a
# tenant-scoped write is four of them, and the suite spends most of its time
# waiting on the network. On the server the same round trip is ~0.1 ms.
#
# Nothing starts on this machine and Go is not installed on the server: the
# test binaries are cross-compiled HERE, copied over, and run THERE against the
# test databases (sms_test in relay-test-postgres, sms_test in
# relay-test-clickhouse, Redis db 1). The connection settings are the server's
# own /opt/relay-test/tests/test.env, so no database password lives on this
# machine. They run at the lowest CPU and I/O priority, so live traffic wins.
#
# Not covered: -race. Cross-compiling with the race detector needs cgo; use
# `make test-race` (local, slow) when that is wanted.
#
# Usage: scripts/remote-test.sh [go test -run pattern]
set -euo pipefail

HOST=${RELAY_HOST:-hostinger-test}
REMOTE=/opt/relay-test/tests
RUN=${1:-}
cd "$(dirname "$0")/.."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"

start=$(date +%s)
pkgs=$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
echo "compiling $(echo "$pkgs" | wc -l | tr -d ' ') test packages for linux..."
# shellcheck disable=SC2086 # one argument per package
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o "$work/bin/" $pkgs

echo "uploading..."
# tar over ssh: the server has no rsync, and installing packages there is not ours.
COPYFILE_DISABLE=1 tar --no-xattrs -czf - -C "$work" bin | ssh "$HOST" "rm -rf $REMOTE/bin && mkdir -p $REMOTE && tar -xzf - -C $REMOTE"
compiled=$(date +%s)

echo "running on $HOST..."
set +e
ssh "$HOST" "RUN='$RUN' REMOTE='$REMOTE' bash -s" <<'EOS'
set -uo pipefail
cd "$REMOTE"
# A test with no database URL SKIPS and reports ok — a suite that silently
# skips is the one failure this script must never produce.
[ "$(grep -cE '^(TEST_DATABASE_URL|TEST_DATABASE_ADMIN_URL|TEST_CLICKHOUSE_URL|REDIS_URL)=' test.env 2>/dev/null)" = "4" ] \
  || { echo "missing test settings in $REMOTE/test.env" >&2; exit 2; }
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
