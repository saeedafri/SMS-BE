# Project rules — mandatory, before anything else

These come from the project owner and override every default below.

## 1. Pull the UI code first, on EVERY task

Before starting any task, however small, bring the frontend checkout up to date. The API contract
(`openapi/control.json`) is a symlink into it, and the frontend's briefs live in its
`docs/api-contract/`. Working from a stale copy is how asks get built against an old contract.

```bash
cd ../SMS-UI && git fetch upstream && git merge --ff-only upstream/master
```

The contract lives on the remote **`upstream`** (SAQIBJH/sms-platform-frontend), NOT `origin`.
If the working tree is dirty or the merge is not a fast-forward, STOP and say so — never reset,
stash or discard someone else's frontend work to make the pull succeed.

## 2. Test locally, then live

1. **Locally first**, against the REMOTE datastores (Postgres, ClickHouse, Redis on the Hostinger
   server, reached through `scripts/tunnel.sh` / `make tunnel-up`). Never start a database,
   container or any other service on this machine.
   **`make test` is the full suite** — it compiles the test binaries here and runs them on the
   Hostinger server next to the databases (`scripts/remote-test.sh`, ~3 minutes; `RUN=<pattern>`
   narrows it). `make test-local` is the slow path through the tunnel.
2. **Then live**: every push to `main` deploys to the Hostinger server (`.github/workflows/deploy.yml`).
   Confirm the deploy run succeeded and that `http://189.72.144.98:8080/healthz` reports the pushed
   commit, then exercise the change against that public API. A green local suite is not a
   finished task.

## 3. Everything is on the Hostinger server

The only deployment is the Hostinger server **189.72.144.98** (`ssh hostinger-test`, root). Relay
runs there as the docker compose project `relay-test` in `/opt/relay-test` (`deploy/docker-compose.yml`):
containers `relay-test-api` (public :8080), `relay-test-postgres` (127.0.0.1:15432),
`relay-test-clickhouse` (127.0.0.1:8123/9000), `relay-test-redis` (127.0.0.1:16380). Its secrets
live only on the server, in `/opt/relay-test/.env` and `/opt/relay-test/api.env`.

The same server runs things that are NOT Relay's: nginx on :80/:443, postgresql@17 on :5432, a
Next.js app on :3000, `smpp-v3.service` and everything under `/opt/smpp-*`. Never modify, restart
or reconfigure any of them.
