# SMS Platform — Backend Design

Status: **proposal, awaiting sign-off.** No code written yet.

Source of truth for the dashboard API: `../SMS-UI/openapi.json` (123 paths, 151 operations,
203 schemas) plus `../SMS-UI/docs/api-contract/`. Product intent: `../SMS-UI/PRD.md`.

---

## 1. What the system actually is

A multi-tenant A2P CPaaS. Two planes, and conflating them is the classic mistake:

**Control plane** — the 151 operations in `openapi.json`. Dashboard + operator console traffic.
Human-paced: a few hundred requests/sec at most, ever. Rich relational data, transactions,
approvals, RBAC, audit. Latency matters, throughput does not.

**Data plane** — *not in `openapi.json` at all.* This is where the 2–3 crore messages live:
public send API, campaign fan-out, compliance/suppression/balance gate, rate-limited queue,
carrier connectors (SMPP/HTTP), delivery-receipt (DLR) ingest, inbound MO/STOP handling,
webhook fan-out. Throughput matters, per-request latency mostly does not.

These get different technology treatment and different scaling knobs. They share one database
and one deployment.

### Contract gap (needs a decision — Q4 below)

`openapi.json` has `GET /v1/messages` but **no send endpoint, no DLR ingest, no MO ingest, no
outbound webhook schema.** That's correct — those aren't dashboard screens — but it means the
highest-volume surface of the product is unspecified. We author a second spec,
`openapi.public.json`, covering:

- `POST /v1/messages` (single send, `Idempotency-Key` required), `GET /v1/messages/{id}`
- `POST /v1/verify/...` programmatic OTP send/check (dashboard already has the config side)
- `POST /v1/hooks/dlr/{connector}` and `/v1/hooks/mo/{connector}` — carrier-facing ingest
- The signed outbound webhook payloads (`message.delivered`, `message.failed`, …) — the
  dashboard's webhook debugger displays these, so their shape is already half-implied

The frontend contract stays untouched and un-hand-edited, exactly as their README demands.

---

## 2. Stack

Chosen against three hard constraints you gave: **one Hostinger VPS, ₹0 additional spend,
starter team.** Every "obvious big-co" answer (Kafka, ClickHouse, K8s, managed queues) is
disqualified by those, and honestly none of them are needed at this volume.

| Layer | Choice | Why this and not the alternative |
|---|---|---|
| Language | **Go 1.23** | Single static binary, ~40–80 MB RSS, no runtime, no `node_modules`. On a small VPS, memory *is* the budget. Goroutines make 10k concurrent connector sockets a non-event. |
| HTTP | **chi** + `net/http` | stdlib-shaped, zero magic, trivial to reason about. |
| Contract binding | **oapi-codegen** | Generates a Go `ServerInterface` **from their `openapi.json`**. If we fail to implement an operation, or drift a field, **it does not compile.** This is the strongest possible enforcement of "follow the contract" — stronger than any TS setup, because it's server-side codegen, not client-side types. Regenerate whenever they bump the spec. |
| DB access | **sqlc** | Hand-written SQL, compile-checked against the real schema, typed structs out. No ORM, no N+1 surprises, no query the DB planner can't see. |
| Database | **PostgreSQL 16** (yours, existing) | Everything relational: tenants, senders, templates, campaigns, ledger, audit. Declaratively partitioned `messages`/`message_events` by time. |
| Job queue | **River** (Postgres-backed) | Decisive reason: PRD §7 requires compliance + suppression + balance check **and enqueue in one transaction.** Only a Postgres-native queue can do that — a Redis queue makes it two systems and reintroduces double-charge/lost-send bugs. River does 10k+ jobs/sec, which is far above what we need. |
| Redis (yours, existing) | rate limiting, idempotency-key dedupe, hot counters, per-tenant throttles, pub/sub for live campaign counters, session/token denylist | Not the source of truth for anything. If Redis dies, we degrade, we don't lose messages. |
| TLS / ingress | **Caddy** | Automatic Let's Encrypt, one config file, zero cost. |
| Deployment | `docker compose` on the VPS, one file, systemd-managed | api, worker, caddy as containers; Postgres and Redis stay as you have them. |
| Frontend hosting | **Vercel free tier** | The Next.js app is not our VPS's problem. Keeps the whole box for backend. Zero cost. |
| Observability | Prometheus text endpoint + **Grafana OSS** on the same box, `slog` JSON to disk with logrotate | No Datadog, no spend. |

**Rejected, and why:** NestJS/TypeScript (3–5× the memory for the same work; contract enforced
only by advisory types). Kafka (needs its own box and a broker mindset for volume Postgres
handles fine). ClickHouse (worth revisiting only if Q1 comes back "per day"). Kubernetes
(operational tax with no benefit on one node). Rust (2–3× the build time for a team that
needs to ship 151 endpoints).

---

## 3. Repository shape

```
SMS-BE/
  cmd/
    api/           # control-plane HTTP server (the 151 ops)
    public-api/    # data-plane send API + carrier ingest (separate binary, separate scaling)
    worker/        # River workers: fan-out, submit, dlr-reconcile, webhook-dispatch, rollups
    migrate/       # goose runner
  internal/
    gen/           # oapi-codegen + sqlc OUTPUT. Never hand-edited. Same rule as their doc.
    domain/        # tenant, sender, template, campaign, message, wallet, compliance, abuse
    store/         # sqlc queries + tx helpers
    connector/     # Connector interface + smpp/, http/, sandbox/ adapters
    compliance/    # Regime interface + india_dlt/, us_10dlc/, uk_mma/, uae/ adapters
    channel/       # Channel interface + sms/, rcs/  (whatsapp/, email/, voice/ slot in later)
    billing/       # ledger, wallet, rating, invoicing
    platform/      # auth, rbac, ratelimit, idempotency, audit, telemetry, config
  db/migrations/
  openapi/         # symlinked frontend spec + our openapi.public.json
  deploy/          # compose.yml, Caddyfile, systemd unit, backup script
```

`domain/` never imports `gen/`. Handlers translate generated types ↔ domain types. That one
rule is what lets the frontend re-spec without our business logic caring.

---

## 4. The four architectural spines

### 4.1 Tenant isolation — Postgres RLS, not discipline

Every tenant-owned table carries `tenant_id` and has row-level security enabled. The API's DB
user is **not** the table owner and cannot bypass RLS. Each request opens a tx and runs
`SET LOCAL app.tenant_id = $1` from the verified token; policies read `current_setting`.

Result: a query that forgets its `WHERE tenant_id` returns zero rows instead of leaking. It
turns the PRD's "missing scope is a data-leak defect" from a code-review hope into a
database-enforced invariant. Operator-console endpoints use a separate, explicitly
cross-tenant role — the only place that role is ever used, and every use is audit-logged.

### 4.2 Billing correctness — append-only ledger, charge inside the send tx

`wallet_ledger` is append-only (no UPDATE/DELETE grant, enforced by trigger). Balance is a
materialized `wallet_balances` row updated in the same transaction as every ledger insert, so
reads are O(1) and a periodic job asserts `sum(ledger) == balance` — a mismatch pages us.

Money is `BIGINT` minor units, always paired with `currency`. There is no `NUMERIC`, no float,
anywhere in the schema.

The send path, in one Postgres transaction:
`reserve balance → write ledger hold → insert message row → River enqueue`. Commit or nothing
happened. On terminal failure or non-delivery, a compensating ledger entry releases the hold —
**"no charge for undelivered" is implemented as: we only convert hold→charge on a
handset-confirmed DLR.** That's the product's headline promise, so it lives in the ledger state
machine, not in a report.

### 4.3 Message lifecycle — an explicit state machine with reconciliation

```
queued → gated → submitting → submitted ─┬→ accepted ──┬→ delivered   (hold → charge)
                     │                   │             └→ undelivered (hold → release)
                     └→ rejected         └→ expired    (reconciler, hold → release)
```

`messages` holds current state (partitioned monthly by `created_at`); `message_events` is the
append-only transition log (same partitioning). Every transition is idempotent and keyed by
`(message_id, to_state)`, so a carrier resending a DLR five times is free.

The "submitted but no receipt" limbo the PRD calls out is a scheduled reconciler: anything in
`submitted`/`accepted` past its route's DLR window gets queried or expired, and the hold
released. No message ever sits in a non-terminal state indefinitely.

Carrier-accepted vs handset-confirmed are **separate states**, never merged. That's the
"honest deliverability" differentiator, enforced at the schema level.

### 4.4 Extensibility — three adapter interfaces, per PRD §8

```go
type Channel    interface { Validate(Msg) error; Segments(Msg) int; Submit(ctx, Msg) (Ref, error) }
type Regime     interface { Country() string; ValidateSender(...); ValidateTemplate(...); ValidateSend(...) }
type Connector  interface { Name() string; Submit(ctx, []Msg) ([]Ref, error); Health() Status }
```

RCS→SMS fallback is a Channel decorator, not an if-statement in the send path. New country =
new `Regime` file + rows in `regimes`. New carrier = new `Connector`. Zero rewrites, which is
the extensibility law their `docs/architecture/extensibility.md` sets.

---

## 5. Throughput plan

Target confirmed: **2–3 crore per day** (~30M/day).

30M/day ≈ **350 msg/sec sustained**, with campaign bursts wanting several thousand. We design
for **sustained 3,000–5,000 msg/sec** on the box and let per-route carrier rate limits, not our
capacity, be the binding constraint. They will be — carriers cap at a few hundred TPS per bind.

**Compute is not the problem at this volume. Disk is.** 90M rows/day ≈ 25 GB/day; the largest
Hostinger VPS holds ~16 days. Full math and consequences in `STACK_DECISION.md` §"The harder
truth". This is why §5's retention design is a day-one feature, not a later setting.

**Campaign fan-out.** Never one job per recipient at insert time. `POST /v1/campaigns` writes
the campaign, then one `expand_campaign` job walks the audience in batches of 10k using
`COPY`-based bulk insert into `messages`, enqueuing one `submit_batch` job per batch. A 5M
campaign is ~500 jobs, not 5M. Expansion is resumable by cursor.

**Submit path.** Worker pool of goroutines per connector, each respecting a Redis token-bucket
keyed `route:{id}` and `tenant:{id}`. SMPP binds are long-lived, multiplexed, auto-reconnecting
with in-flight replay from the `submitting` state (this is where "reconnects without loss or
duplication" is earned).

**DLR ingest.** Carriers hammer this. The ingest endpoint does the absolute minimum — validate
signature, `COPY` into a raw `dlr_inbox` table, return 200 in ~1 ms. A worker drains
`dlr_inbox` in batches and applies state transitions + ledger settlement. Never process a DLR
synchronously in the HTTP handler; that's the single most common way these systems fall over.

**Reads.** `GET /v1/messages` over a 30M-row/month partitioned table is fine with the right
composite indexes and **keyset pagination** — which the contract already mandates (opaque
`nextCursor`). We encode `(created_at, id)`, never `OFFSET`. Analytics never touch `messages`:
workers maintain hourly/daily rollup tables per (tenant, campaign, route, country, status), and
every analytics endpoint reads only rollups.

**Retention — the load-bearing decision at this volume.** `messages` and `message_events` are
partitioned **daily**. Aging out a day is `DETACH`+`DROP` (instant), never a `DELETE` scanning
hundreds of millions of rows.

- **Hot window: 7–14 days** in Postgres — this is what `GET /v1/messages` searches, and what a
  support engineer debugging "where did my OTP go" actually needs.
- **Rollups are permanent.** Hourly/daily aggregates per (tenant, campaign, route, country,
  status) are megabytes, never dropped. Every analytics endpoint reads only rollups — so the
  dashboard's delivery history stays complete forever even after raw rows are gone.
- **Cold archive**: dropped partitions export first to compressed Parquet/CSV.gz (~10–20×
  smaller), restorable on demand for disputes and audits.
- `GET/PATCH /v1/data-retention` in the contract becomes the real user-facing control over the
  hot window. The frontend team anticipated this endpoint; now it means something.

ClickHouse is the natural next step if analytics outgrow the rollup tables — free, OSS, runs on
the same box. Not now: rollups are simpler and sufficient. Revisit when a real query is slow,
not before.

---

## 6. Security

- Bearer JWT only at the contract level, as their README states. Access token 15 min (ES256,
  key ID rotated), refresh token rotated on use with reuse-detection → kill the session family.
  `GET/DELETE /v1/sessions` is backed by a real session table, not a token blob.
- **API keys** are data-plane only: `sms_live_<id>_<secret>`, stored as Argon2id, prefix
  indexed for lookup, scoped per `GET /v1/developer/scopes`, rotation with an overlap window
  (`/rotate` returns the new key while the old one dies on a timer).
- Outbound webhooks signed HMAC-SHA256 over `timestamp.body`, exponential retry with a
  circuit-breaker per endpoint, full attempt history behind `/webhooks/{id}/events`.
- MFA (TOTP + recovery codes), SSO/SAML behind the existing `PUT /v1/sso`.
- Rate limits: Redis sliding window per API key, per tenant, per IP, per route. `429` returns
  the same `Error` envelope the contract defines, plus `Retry-After`.
- Audit log append-only, covering **every** operator action and every tenant mutation — the
  operator console's `GET /v1/operator/audit-log` reads it directly.
- Secrets via env only, `.env` never committed, `sops`-able later.

---

## 7. Build order

Dependency-ordered, and deliberately aligned with the frontend's cutover order so they can flip
domains from MSW to real one at a time. Each stage ends with the frontend able to run a real
slice with `NEXT_PUBLIC_USE_MOCKS=false`.

| # | Stage | Delivers |
|---|---|---|
| 0 | Foundation | repo, compose, migrations, RLS harness, oapi-codegen + sqlc pipeline, CI, health/metrics. **Cross-tenant leak tests that must fail to read.** |
| 1 | Identity | auth (12 ops), `/me`, sessions, team, tenant, RBAC, audit spine |
| 2 | Compliance | sender-ids, templates, registrations, India-DLT + US-10DLC regimes |
| 3 | Money | wallet (9), billing (4), pricing, ledger, rating engine, invariant checker |
| 4 | Audience | contacts, contact-lists, CSV import pipeline, suppressions, STOP handling |
| 5 | **Data plane** | `openapi.public.json`, send API, gate→queue→connector, sandbox connector, DLR ingest, state machine + reconciler, message logs |
| 6 | Campaigns | campaigns, estimate, fan-out, live counters via Redis pub/sub |
| 7 | Developer | api-keys, ip-allowlist, rate-limit, webhooks (17 ops) |
| 8 | Analytics | rollup workers, analytics + reports, alerts, data-retention |
| 9 | Operator | all 34 operator ops, approval/abuse queues, routes, rate cards, margin |
| 10 | Verify | verify services, attempts, analytics, fraud signals |
| 11 | Support & rest | tickets both sides, conversations, automation/journeys |
| 12 | Hardening | load test to target TPS, chaos on connector/Redis/DB, security pass, backups + restore drill |

Stages 5 and 9 are the hard ones. Everything before 5 is well-understood CRUD that codegen
makes fast.

---

## 8. Testing & the non-negotiables

- **Contract tests**: every implemented operation validated against `openapi.json` in CI. Drift
  fails the build.
- **Tenant-isolation tests**: for each table, a test that authenticates as tenant A and asserts
  it cannot read/write tenant B. Required by PRD §8 explicitly.
- **Billing property tests**: random send/DLR sequences must always end with
  `sum(ledger) == balance` and zero charges for undelivered.
- **Idempotency tests**: every `Idempotency-Key` endpoint, concurrent duplicate submits, exactly
  one side effect.
- **Load test** (k6, free) against the sandbox connector at target TPS before any real carrier.

---

## 9. Cost

₹0 beyond your existing Hostinger VPS. Postgres, Redis, Go, Caddy, Grafana, k6, River, sqlc —
all OSS. Frontend on Vercel free. The only future spend that's genuinely unavoidable is
carrier/aggregator connectivity, which is a business cost, not infrastructure.

---

## 10. Decisions taken

1. **Volume: 2–3 crore per day.** Daily partitions, 7–14 day hot window, permanent rollups,
   cold archive. See §5.
2. **Stack: Go everywhere** (`STACK_DECISION.md`) — pending your confirmation.
3. **First connector: sandbox only.** Stage 5 builds the full pipeline against a fake
   connector; the real SMPP/HTTP adapter plugs into the same `Connector` interface when
   commercials land. Nothing blocks on carrier negotiations.
4. **`openapi.public.json` is authored in Stage 0**, before the data plane is built. Same
   generated-not-hand-edited discipline as the frontend's contract. Their `openapi.json` is
   never touched.

## 11. Still open — one question

**Your exact VPS specs**: vCPU, RAM, disk, and whether Postgres and Redis sit on that same box
or a different one. This sets the hot-window length, worker pool sizing, and Postgres tuning.
It's the only thing left before Stage 0 starts.
