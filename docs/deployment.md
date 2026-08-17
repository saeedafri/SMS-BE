# Deployment

Relay runs on a **shared** Hostinger VPS. It is not the only thing there, and
almost every decision on this page follows from that.

```
srv1067327  ·  31.97.186.223  ·  Debian 13  ·  2 vCPU  ·  7.9 GB RAM  ·  99 GB disk
```

Two other products were running before Relay arrived:

| Owner | What | Where |
|---|---|---|
| Rentocloud | pm2: `rentocloud` (:4000), `rentocloud-frontend` (:3000) | `api.rentocloud.com`, `frontend.rentocloud.com` |
| EMS | docker: `ems-backend` (:4001), `ems-postgres` (:5432), `ems-redis` (:6379) | `ems-api.saqibsaeed.cloud` |
| **Relay** | **systemd: `relay-api` (:8080); docker: `relay-clickhouse-1`, `relay-redis-1`** | **`sms-api.saqibsaeed.cloud`** |

Everything Relay listens on is bound to **127.0.0.1**. The only public surface
is one new nginx site.

---

## What Relay costs the box

Measured, not estimated — `free -m` immediately before and after bringing the
whole stack up:

| | Before | After |
|---|---|---|
| Used | 1 969 MB | 2 487 MB |
| Available | 5 977 MB | 5 460 MB |

**Total: 518 MB.**

| Component | Resident | Hard limit |
|---|---|---|
| ClickHouse | 334 MB | 1 GB (cgroup) + 900 MB (its own `max_server_memory_usage`) |
| `relay-api` | 150 MB | 512 MB (`MemoryMax`) with `GOMEMLIMIT=400MiB` |
| Redis | 12 MB | 128 MB (cgroup) + 96 MB `maxmemory`, `allkeys-lru` |
| Postgres delta | +9 MB | unchanged — reuses the existing container |

Every one of those limits exists for the same reason: when a box runs out of
memory the kernel kills the **biggest** process, not the guilty one. An
unbounded ClickHouse on this machine is a way to have EMS killed.

---

## Reuse: what is shared and what is not

**PostgreSQL is shared.** Relay uses a separate `sms` database inside the
running `ems-postgres` container. A second Postgres would have cost ~500 MB to
duplicate something already there. Capacity was never in question:
`max_connections` is 100 with 26 in use, and Relay's two pools add about 8.

**Redis is not shared**, despite being the more obvious candidate. `ems-redis`
runs `maxmemory-policy noeviction` on a 200 MB budget, so anything of Relay's
that filled it would start failing **EMS's** writes — a Relay traffic spike
becoming an EMS incident. Relay's own instance evicts (`allkeys-lru`) instead of
erroring, and costs 12 MB. That is a better trade than the 12 MB is worth.

---

## Database roles

Two roles, and the split is load-bearing:

| Role | Used by | Privileges |
|---|---|---|
| `sms_app` | every request | `LOGIN` only — **no** superuser, **no** `BYPASSRLS` |
| `sms_migrator` | migrations, seeding, the dev reset hook | owns the schema, **`BYPASSRLS`** |

!!! warning "`sms_app` must never gain `BYPASSRLS` or superuser"
    Tenant isolation is row-level security. A superuser bypasses RLS entirely,
    so promoting the application role would silently switch off the single
    mechanism keeping tenants apart — with no error and no failing test.

`sms_migrator` needs `BYPASSRLS` for the opposite reason: its whole job is
writing *across* tenants. Every table is `FORCE ROW LEVEL SECURITY`, which binds
even the table owner, so without it the seeder cannot insert a tenant at all:

```
ERROR: new row violates row-level security policy for table "tenants" (SQLSTATE 42501)
```

This is **invisible in local development**, where `DATABASE_ADMIN_URL` points at
the developer's own superuser account and bypasses RLS implicitly. There is no
`sms_migrator` locally at all. It surfaces the first time the migration role is
a real unprivileged role — which is to say, the first real deployment.

---

## Deploying

Once, on a fresh box:

```bash
scp deploy/docker-compose.yml deploy/clickhouse-limits.xml root@HOST:/opt/relay/deploy/
ssh root@HOST 'bash -s' < deploy/install-server.sh
```

Then, for every release, from your machine:

```bash
./deploy/deploy.sh 31.97.186.223
```

`deploy.sh` cross-compiles both binaries, ships `goose` with them (the server
needs no Go toolchain), runs the Postgres **and** ClickHouse migrations, swaps
the binary atomically, and polls `/healthz` until it answers — reporting the
journal and failing if it never does. Migrations run **before** the swap, so a
failed migration leaves the old binary serving.

!!! danger "Do not run the pre-2026-08 `install-server.sh`"
    The original script installed Caddy, PostgreSQL, Redis and ran
    `ufw --force enable`. On this box each is destructive: Caddy would fight
    nginx for :80/:443 and take down all three sites; the databases would
    duplicate ~600 MB of running containers; and `ufw allow OpenSSH` opens 22
    only, while sshd here also listens on **2222** — enabling it removes an SSH
    path on a remote machine with no console. The current script installs
    nothing and changes no firewall.

---

## Publishing

`deploy/nginx-sms-api.conf` → `/etc/nginx/sites-available/`, symlinked into
`sites-enabled`, then `certbot --nginx -d sms-api.saqibsaeed.cloud`.

The config does one thing beyond proxying:

```nginx
location /v1/dev/ { return 403; }
```

`/v1/dev/*` is **unauthenticated by design** — it falls back to the fixture
tenant when no session is present, because Playwright's `request` fixture
carries none of the browser's cookies. One of those routes rebuilds the entire
demo dataset and another changes the caller's own role. The browser suite
reaches them through an SSH tunnel that never passes nginx, so blocking them
publicly costs the suite nothing.

---

## Running the browser suite against the live backend

```bash
ssh -i ~/.ssh/id_ed25519 -f -N -L 127.0.0.1:18080:127.0.0.1:8080 root@31.97.186.223
# SMS-UI/.env.local → NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:18080
pnpm dev && npx playwright test
```

The tunnel, not the public hostname — that is what gets the suite past the
`/v1/dev/` block above.

---

## Email

Transactional mail goes out through **Resend** on the verified
`saqibsaeed.cloud` domain: verification links, password resets, team
invitations. `RESEND_API_KEY` is unset locally and in tests, where the mailer
logs instead of sending — neither should be able to put real mail in a real
inbox by accident.

Campaign traffic deliberately does **not** use this path. Campaigns are
metered, priced and held against a wallet by the sending pipeline; routing them
through a mailer with no ledger entry would let a tenant send for free.

`APP_BASE_URL` is the **frontend's** origin, not the API's — it builds the links
in those emails, and they must land on the pages that read `?token=`.
