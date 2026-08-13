# Local development

Runs the Go backend and the real Next.js dashboard together on one machine, with MSW mocks off.

## Prerequisites

```bash
brew install go postgresql@16 redis
```

Node 20+ and pnpm for the frontend. ClickHouse is **not** needed until Stage 5 — see
"ClickHouse on macOS" below.

## First-time setup

```bash
cd /Users/mohdsaeedafri/All-Code-Base/SMS-BE
make services-up          # Postgres + Redis via brew services
make db-setup             # creates sms_dev, sms_test, and the sms_app role
cp .env.example .env
set -a && source .env && set +a
make migrate-up && make migrate-test
make generate             # regenerate from ../SMS-UI/openapi.json
make check                # vet + build + test — expect green
```

`make generate` must be re-run whenever the frontend team changes `openapi.json`. Everything
under `internal/gen/` and `internal/api/unimplemented.go` is generated — never hand-edit either.

## Running both sides

```bash
# terminal 1 — backend on :8080
cd /Users/mohdsaeedafri/All-Code-Base/SMS-BE
set -a && source .env && set +a && go run ./cmd/control-api

# terminal 2 — frontend on :3000
cd /Users/mohdsaeedafri/All-Code-Base/SMS-UI
pnpm dev
```

`../SMS-UI/.env.local` (already written, gitignored):

```bash
NEXT_PUBLIC_USE_MOCKS=false
NEXT_PUBLIC_API_BASE_URL=http://localhost:8080
```

To go back to mocked mode, set `NEXT_PUBLIC_USE_MOCKS=true` and restart `pnpm dev`.

## Verifying the wiring

```bash
curl -s localhost:8080/healthz | jq .
# {"checks":{"postgres":"up","redis":"up"},"status":"ok"}

curl -s localhost:8080/v1/me | jq .
# {"error":{"code":"not_implemented","message":"operation GetMe is not implemented yet"}}
```

To watch the frontend's own traffic arrive, tail the backend's structured log while loading a
dashboard page. A session cookie is required — without one the frontend's middleware redirects
to `/login` without ever calling the backend:

```bash
curl -s -o /dev/null -H "Cookie: relay_session=probe" localhost:3000/billing
# backend log then shows GET /v1/me, /v1/wallet/balances, /v1/alerts, /v1/conversations
```

## What to expect at Stage 0

Dashboard pages return **HTTP 500**, because `src/app/(dashboard)/layout.tsx` fetches four
endpoints on every render and `getJson` throws on any non-2xx. That is the correct Stage 0
result: no contract operation is implemented yet. Stage 0 proves the pipe is connected and the
error envelope matches the contract, nothing more.

**The dashboard layout's four fetches are Stage 1's priority set**, since nothing renders until
they succeed:

| Endpoint | Used by |
|---|---|
| `GET /v1/me` | middleware role gate + dashboard layout |
| `GET /v1/wallet/balances` | topbar balance |
| `GET /v1/alerts` | topbar alerts |
| `GET /v1/conversations` | inbox unread badge |

## Known frontend issues to resolve before Stage 1

1. **`src/lib/api/me.ts` sends no credentials.** It is the only client-side fetch that goes
   straight to `NEXT_PUBLIC_API_BASE_URL` from the browser, with no `Authorization` header:
   against a real backend that is a 401 plus a cross-origin failure. Every other call in the app
   goes through the Next.js server, which attaches the httpOnly session cookie as a bearer token.
   Fix: a `/api/me` route handler mirroring `src/app/api/wallet/balances/route.ts`.
2. **Ten `/v1/dev/*` endpoints are absent from the contract.** UI dev tooling calls
   `/v1/dev/advance-campaign`, `/v1/dev/drain-wallet`, `/v1/dev/set-my-role`,
   `/v1/dev/reset-mock-state` and others; none appear in `openapi.json`. Decide: implement them
   for sandbox environments with a hard production kill-switch, or gate the tooling off when
   mocks are disabled.

## ClickHouse on macOS — resolved

**Do not install ClickHouse with Homebrew.** The cask stamps `com.apple.quarantine` on the
binary, so Gatekeeper kills it silently — no error, no log output, just an immediate exit. The
cask is also deprecated for removal on 2026-09-01.

The official installer avoids this entirely: `curl` does not set the quarantine attribute, so
there is nothing for Gatekeeper to block and **no security control is being bypassed**.

```bash
mkdir -p ~/clickhouse-bin && cd ~/clickhouse-bin
curl -sS https://clickhouse.com/ | sh          # ~160 MB single binary

mkdir -p ~/clickhouse-data && cd ~/clickhouse-data
nohup ~/clickhouse-bin/clickhouse server > ch.log 2>&1 &

curl -s http://localhost:8123/ping             # expect: Ok.
for db in sms_dev sms_test; do
  curl -s http://localhost:8123/ --data-binary "CREATE DATABASE IF NOT EXISTS $db"
done
```

Then set in `.env`:

```bash
CLICKHOUSE_URL=http://localhost:8123/sms_dev
TEST_CLICKHOUSE_URL=http://localhost:8123/sms_test
```

`spctl -a` still reports "rejected" for the binary — that is expected and harmless. Gatekeeper
only *enforces* against quarantined files, and this one is not quarantined.

Verified working: ClickHouse 26.8.1.1307 on macOS 26.6 arm64.

None of this affects production: on Linux the standard package works normally.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `pg_isready` fails | `brew services restart postgresql@16` |
| Tests skip silently | You forgot `set -a && source .env && set +a` |
| `invalid input syntax for type uuid: ""` | An RLS policy is using raw `current_setting` instead of `current_tenant_id()` — see migration `00002` |
| Port 8080 busy | `lsof -i :8080` then kill the stale process |
