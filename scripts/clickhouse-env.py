"""Prints the export lines clickhouse_migrate.py needs, taken from
TEST_CLICKHOUSE_URL. Eval it: `eval "$(python3 scripts/clickhouse-env.py)"`."""
import os
import shlex
import urllib.parse

url = urllib.parse.urlparse(os.environ["TEST_CLICKHOUSE_URL"])
print(f"export CLICKHOUSE_HTTP={shlex.quote(f'{url.scheme}://{url.hostname}:{url.port}')}")
print(f"export CLICKHOUSE_USER={shlex.quote(url.username or '')}")
print(f"export CLICKHOUSE_PASSWORD={shlex.quote(url.password or '')}")
