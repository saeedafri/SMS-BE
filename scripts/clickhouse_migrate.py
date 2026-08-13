"""Applies db/clickhouse/*.sql over ClickHouse's HTTP interface.

ClickHouse has no transactional DDL and no migration bookkeeping here — every
statement is IF NOT EXISTS, so this is idempotent by construction rather than
by tracking what already ran.
"""
import glob
import os
import sys
import urllib.request

database = sys.argv[1] if len(sys.argv) > 1 else "sms_dev"
host = os.environ.get("CLICKHOUSE_HTTP", "http://localhost:8123")

files = sorted(glob.glob("db/clickhouse/*.sql"))
if not files:
    print("no ClickHouse migrations found; run from the repo root", file=sys.stderr)
    sys.exit(1)

for path in files:
    # Comments are stripped BEFORE splitting on ';'. The other order breaks the
    # moment a comment contains a semicolon, and surfaces as a baffling syntax
    # error pointing at prose.
    source = "\n".join(
        line for line in open(path).read().splitlines()
        if not line.strip().startswith("--")
    )
    for raw in source.split(";"):
        body = raw.strip()
        if not body:
            continue
        request = urllib.request.Request(f"{host}/?database={database}", data=body.encode())
        try:
            urllib.request.urlopen(request).read()
        except Exception as error:  # noqa: BLE001 - surface ClickHouse's own message
            detail = getattr(error, "read", lambda: b"")().decode() or str(error)
            print(f"ClickHouse error applying {path}:\n{detail}", file=sys.stderr)
            sys.exit(1)
    print(f"applied {os.path.basename(path)} to {database}")
