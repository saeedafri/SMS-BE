# Local development

Runs the Go backend and the real Next.js dashboard together on one machine, with MSW mocks off.

## Prerequisites

```bash
brew install go
```

Node 20+ and pnpm for the frontend. **No database is installed locally** — Postgres, Redis and
ClickHouse all run on the Hostinger VPS and are reached over an SSH tunnel. See "The datastores
are on the VPS" below.

## First-time setup

```bash
cd /Users/mohdsaeedafri/All-Code-Base/SMS-BE
make tunnel-up            # forwards the VPS Postgres, Redis and ClickHouse
cp .env.example .env      # then point the URLs at the forwarded ports
set -a && source .env && set +a
make migrate-test         # brings the sms_test database up to date
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

## The datastores are on the VPS

Postgres, Redis and ClickHouse run on the Hostinger box and are **not** installed here. None of
the three is published to the internet: they bind to the VPS loopback inside Docker, so SSH is
the only way to reach them.

```bash
make tunnel-up            # or scripts/hostinger-tunnel.sh start
scripts/hostinger-tunnel.sh status
```

| Forwarded port | Reaches |
|---|---|
| 15432 | `ems-postgres` — databases `sms` and `sms_test` |
| 16380 | `relay-redis-1` |
| 8123 | `relay-clickhouse-1` HTTP |
| 9000 | `relay-clickhouse-1` native |

ClickHouse keeps its real port numbers on purpose: `store.OpenClickHouse` derives the native
address as `host:9000` from the HTTP URL, so a renumbered forward would be silently ignored and
every ClickHouse-backed test would fail on a refused connection to `127.0.0.1:9000`.

Tests use the separate `sms_test` databases — never `sms`, which is production. `.env` therefore
carries two sets of URLs, and `TEST_DATABASE_URL` must never point at `sms`.

Redis has no separate test URL, so local runs use db **1** and production keeps db 0. Nothing a
local run writes can touch a live session or rate-limit counter.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Connection refused on 15432/8123/9000 | The tunnel dropped — `make tunnel-up` |
| Tests skip silently | The URLs are missing. `make test` sources `.env` itself; a bare `go test` does not |
| `invalid input syntax for type uuid: ""` | An RLS policy is using raw `current_setting` instead of `current_tenant_id()` — see migration `00002` |
| Port 8080 busy | `lsof -i :8080` then kill the stale process |
