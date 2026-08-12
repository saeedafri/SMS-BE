# Research findings, revised architecture, and honest costs

Written after reading engineering write-ups, benchmark comparisons, and community discussion
(HN threads, Reddit, vendor TCO analyses), plus a direct read of the frontend's real data layer.
Sources listed at the end.

**The brief changed**: 30M/day is the *start*, target is billions. Cost is no longer the binding
constraint. That genuinely reopens decisions I made under the old brief, and I'm revising two of
them. I'll say clearly which and why.

---

## Part 1 — What I got right, and what I'm changing

### Changing #1: message logs leave Postgres. ClickHouse from day one.

This is the big one, and I under-called it before.

The research is unambiguous. Log/event analytics is ClickHouse's designed purpose: columnar
compression delivers **5–10× better ratios than row storage** on log-shaped data, and it's built
to scan billions of rows with sub-second latency. Cloudflare, Lyft and Contentsquare publish
case studies of **tens of billions of events/day**. Meanwhile, the commonly-cited practical
threshold for write-heavy logs in Postgres is around **10 million rows** before the relational
wall gets real.

Your `messages` table hits 10M rows in **eight hours**.

I proposed partitioned Postgres + rollup tables. That works at 30M/day. It does not survive the
trajectory you just described, and building it that way would mean a painful migration in year
one. So:

- **Postgres** = system of record for the **control plane**: tenants, users, senders, templates,
  campaigns, routes, rate cards, wallet ledger, audit. Strongly relational, transactional,
  RLS-isolated. **This data never gets big** — even at 1B msgs/day you have maybe 100k tenants.
  Postgres handles it forever without sharding.
- **ClickHouse** = **message logs and all analytics**. Every message record and every delivery
  event. This is what `GET /v1/messages`, all `/v1/analytics/*`, and the operator's usage and
  margin screens read from.
- **Redis** = hot live state: in-flight message status, campaign counters, rate limits,
  idempotency keys, abuse throttles.

**The clean split that makes billions tractable**: control-plane data is small and complex;
data-plane data is huge and simple. Put each in the engine designed for it. Almost every failed
scale-up story is someone who kept them in one place too long.

### Changing #2: the ledger records batches, not messages

I need to flag a flaw in my own earlier design. I proposed a ledger entry per message. At 1B/day
that's 1B ledger rows/day in Postgres — which destroys the "control plane stays small" property
that makes this whole architecture work.

Corrected:

- **Wallet ledger** = one entry per **batch settlement** (per campaign chunk, or per tenant per
  minute for API traffic). Millions of rows/year, not billions/day. Stays in Postgres, stays
  append-only, stays auditable.
- **Per-message cost** = a column in ClickHouse. `GET /v1/billing/usage` and the per-message cost
  drill-down read there and reconcile against ledger batch totals.
- The invariant becomes `sum(ledger) == sum(clickhouse per-message costs)` per period, checked
  continuously. Same guarantee, four orders of magnitude fewer rows.

### Holding: Go. The research strengthened this, and here's the honest nuance.

2026 benchmarks put Rust Axum around **307k req/s** vs Go around **180k req/s** — a real gap.
But the same analyses note that for typical API workloads (DB queries, HTTP proxying, queue
processing) the difference "shrinks considerably when the bottleneck is network or disk I/O
rather than CPU," which describes an SMS platform exactly. The stated 2026 community consensus
is **"Go is the right default for almost all backend work; Rust for the narrow set of
systems-level workloads where deterministic latency or tiny footprint matter."**

And the emerging pattern is explicitly hybrid: **Go for application services and control planes,
Rust reserved for hot-path data-plane components.**

That is precisely what Discord did — Rust *only* for the data service in front of ScyllaDB,
everything else unchanged. So the plan: **Go everywhere now; one identified Rust escape hatch**
(§3) for the single hottest component if and when it's measured to need it. Not speculatively.

### Holding: no Kafka yet — but the trigger is now defined, and it's Redpanda

Reference points for what "big" means: **Pinterest runs 800 billion messages/day at 15M
msg/sec across 2,000+ brokers**; **Shopify peaked at 66M msg/sec**. Kafka's own community
guidance is that below roughly tens of thousands of msg/sec, "the overhead often outweighs the
benefit."

30M/day = 350/sec. Even 1B/day = ~12,000/sec — still at the *bottom edge* of where a broker
earns its keep. Kafka becomes right for us on **fan-out and replay**, not throughput.

When we do adopt it: **Redpanda, not Kafka.** Single C++ binary, no ZooKeeper, no Schema
Registry, no Cruise Control; reported **3–6× fewer compute resources and up to 10× lower tail
latencies** on identical hardware. The honest counterpoint from the research: **Kafka 4.0 closed
much of the operational gap** and has a far bigger ecosystem. For a small team, Redpanda still
wins on ops simplicity. NATS JetStream is the third option — lighter still (~820k msg/s
benchmarked vs Kafka's 1.2M batched), excellent for service-to-service, but weaker on long
retention and replay, which is exactly what we'd be adopting a bus *for*.

---

## Part 2 — Storage. Real numbers, upfront.

You asked how much production storage this actually needs. Per message, including its ~3
lifecycle events:

| Store | Per message | Why |
|---|---|---|
| **Postgres** (row storage + indexes) | **~900 bytes** | ~250 B row + ~200 B indexes for `messages`, plus ~450 B for 3 event rows |
| **ClickHouse** (columnar, compressed) | **~30 bytes** | Repeated tenant/route/status/country values compress extraordinarily well |

**~30× difference.** That single ratio is the entire argument.

| Daily volume | Postgres/day | ClickHouse/day | ClickHouse, 1 year retained |
|---|---|---|---|
| 30M (today) | ~27 GB | ~0.9 GB | **~330 GB** |
| 100M | ~90 GB | ~3 GB | ~1.1 TB |
| 500M | ~450 GB | ~15 GB | ~5.5 TB |
| **1B** | ~900 GB ❌ | ~30 GB | **~11 TB** |
| 10B | — ❌ | ~300 GB | ~110 TB |

Read the 1B row carefully. In Postgres that's **900 GB/day** — a terabyte of disk daily, and no
single machine survives it. In ClickHouse the same year of data is **11 TB**, which is two disks
in one server. This is not a tuning difference; it's a category difference.

Add for any tier: Postgres control plane stays **under ~200 GB even at 1B/day** (it holds
tenants and batch ledger, not messages). Backups roughly 2× your ClickHouse footprint. Redis
sized to in-flight messages only — a few GB.

---

## Part 3 — The revised target architecture

```
                       ┌──────────────────────────────────────┐
  Browser ─────────────│  Next.js (Vercel)                     │
                       │  RSC + server actions + route handlers│  ← ALL calls server-side
                       │  httpOnly session cookie → Bearer     │
                       └───────────────┬──────────────────────┘
                                       │  (private network / same origin)
   Carriers ──DLR/MO──┐                │
   Customers ──API────┤                │
                      ▼                ▼
              ┌────────────────┐  ┌────────────────┐
              │  public-api    │  │  control-api   │   Go + chi + oapi-codegen
              │  (Go, or Rust) │  │  (Go)          │   ← 151 ops, contract-generated
              └───────┬────────┘  └───────┬────────┘
                      │                   │
        ┌─────────────┼───────────────────┼──────────────┐
        ▼             ▼                   ▼              ▼
   ┌─────────┐  ┌──────────┐      ┌─────────────┐  ┌──────────┐
   │  Redis  │  │ EventBus │      │ PostgreSQL  │  │ClickHouse│
   │ hot     │  │ River →  │      │ control     │  │ message  │
   │ state   │  │ Redpanda │      │ plane (SoR) │  │ logs +   │
   └─────────┘  └────┬─────┘      └─────────────┘  │ analytics│
                     ▼                             └──────────┘
              ┌─────────────┐
              │  workers    │  fan-out · submit · DLR apply ·
              │  (Go)       │  webhook dispatch · reconcile · rollup
              └──────┬──────┘
                     ▼
              ┌─────────────┐
              │ connectors  │  SMPP binds · HTTP aggregators · sandbox
              └─────────────┘
```

### Best ideas stolen from the research

**1. Request coalescing (from Discord).** Their Rust data service dedupes concurrent identical
reads — *"if multiple users request the same row simultaneously, the service queries the
database once"* and fans the result to all waiters. Combined with consistent-hash routing by
key, this shielded them from thundering herds.

Directly applicable: when a 5M-recipient campaign is live, every dashboard on that tenant is
polling the same counters, and the operator console is polling aggregates. Coalescing turns
thousands of identical queries into one. It's ~200 lines in Go (`singleflight` is in the
standard extended library). **We build this in Stage 6.**

**2. Shard-per-core thinking (from ScyllaDB/Redpanda).** Both beat their predecessors mainly by
pinning data and work to specific cores instead of sharing across threads. We can't rewrite our
DB, but we can apply the principle: **pin each connector's goroutines and their message batches
to a consistent shard by route** so a hot route never contends with a cold one.

**3. No garbage-collector on the hottest path (Discord's actual Cassandra pain).** Their worst
outages were **JVM GC pauses** — bad enough that operators manually rebooted nodes. Go's GC is
far better than the JVM's for this, but it exists. This is the concrete reason the **Rust escape
hatch** is reserved for the DLR ingest path specifically: it's the one component where a
100 ms pause under load turns into carrier-visible packet loss.

**4. Postgres stays the system of record (from the CDC pattern the research calls common).**
"Postgres stays the system of record while ClickHouse handles analytics" is the dominant modern
pattern. We follow it exactly — nothing is *only* in ClickHouse that we'd cry over losing;
ClickHouse is derived, rebuildable state.

### The scaling ladder — when each thing changes

| Volume | What changes | What does NOT change |
|---|---|---|
| **≤50M/day** | Single box. PG + ClickHouse + Redis + Go. River as queue. | — |
| **50–200M/day** | Split DB onto its own machine; add PG read replica; ClickHouse gets a second node. | Code. Config only. |
| **200M–1B/day** | Introduce **Redpanda** as the event bus (fan-out + replay). Add worker machines. ClickHouse 3-node cluster. | Application code — `EventBus` interface swaps implementation. |
| **1B–10B/day** | Shard Postgres by tenant (**Citus**) if the control plane ever gets big — likely never. Consider **ScyllaDB** for live message state. Rust for DLR ingest. | The contract. The domain model. |
| **10B+/day** | Multi-region, geo-routed connectors, ClickHouse tiered to object storage. | You are now Twilio. |

**The point of this table**: from today to ~1B/day, nothing is a rewrite. Every step is adding a
machine or swapping an implementation behind an interface that exists on day one. That is what
"highly scalable" actually means in practice — not choosing exotic tech now, but not painting
yourself into a corner.

One honest caution from the research: **Citus migration is a real project — plan 2–4 weeks** for
a large database. Which is why we keep the control plane small enough to never need it.

---

## Part 4 — UI + backend compatibility. I read the actual code.

You asked this be perfect. Here's the factual state, not an assumption.

### Very good news: the frontend already uses the secure server-side pattern

I traced every call site. **Essentially all API traffic goes through the Next.js server** — RSC
fetchers (`src/lib/api/fetchers.ts`), ~30 server-action modules, route handlers, and middleware.
The session token lives in an **httpOnly cookie** the browser cannot read; the server attaches
it as `Authorization: Bearer` (`src/lib/api/fetchers.ts:11-14`).

This is the BFF pattern the security research recommends, already built. It means:

- **No CORS configuration needed** for the dashboard — the browser never calls our Go API
  directly.
- **No token in JavaScript** — XSS can't steal a session.
- Our Go API only needs to accept `Authorization: Bearer`, exactly as their contract states.
- The frontend can sit on Vercel and reach our API over the public internet with mTLS or a
  shared secret header, or we can put both behind one domain. Either works.

They did this properly. Credit where due.

### Three real integration defects I found

**1. `src/lib/api/me.ts` breaks the moment mocks are off.** It's the one client-side fetch that
goes straight to `NEXT_PUBLIC_API_BASE_URL` **from the browser with no `Authorization` header**:

```ts
const base = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:4000";
const res = await fetch(`${base}/v1/me`);   // ← no auth, and cross-origin
```

Under MSW the mock doesn't check auth, so it passes. Against a real backend it's a **401 plus a
CORS failure**. Fix is ~10 lines — a `/api/me` route handler mirroring the wallet one — but it
must be done before cutover, and the UI team should own it since they said they're done.

**2. There are `/v1/dev/*` endpoints the contract doesn't contain.** Ten route handlers call
`/v1/dev/advance-campaign`, `/v1/dev/drain-wallet`, `/v1/dev/set-my-role`,
`/v1/dev/reset-mock-state` and others. **None are in `openapi.json`** — they're MSW-only test
hooks powering the UI's demo controls. Decision needed: either the backend implements them
(sandbox environments only, hard-disabled in production), or the frontend gates that tooling off
when mocks are off. I'd implement them for sandbox — they're genuinely useful for testing the
real pipeline, and a `/v1/dev/advance-campaign` that drives real state is a great QA tool. But
it needs a spec entry and a production kill-switch.

**3. The contract has no send endpoint** — covered previously. `openapi.public.json` in Stage 0.

### Making the join fast and smooth

- **Same-origin via Vercel rewrite.** Point `NEXT_PUBLIC_API_BASE_URL` at `/api-proxy` and add a
  Vercel rewrite to our Go API. Kills CORS permanently, including for the `me.ts` case, and
  hides the backend hostname.
- **Keyset pagination is already in the contract** (opaque `nextCursor`). Against ClickHouse
  this is the *only* pagination that stays fast at billions of rows. They chose correctly.
- **`ETag`/`If-None-Match` on list endpoints** — polling dashboards get `304`s instead of
  payloads. Cheap, big win, invisible to the frontend.
- **Server-Sent Events for live campaign counters**, replacing the 30-second poll on
  `useWalletBalance` and campaign progress. Additive — no contract change.
- **Contract tests in CI on both sides** against the same `openapi.json`, so drift fails a build
  in either repo.

---

## Part 5 — Cost. Every tier, honestly.

Provider recommendation: **Hetzner** for the heavy compute. Research consistently puts it at
**3–5× cheaper than AWS** for equivalent compute, with **20 TB egress included per server** —
egress alone would be **$1,800+/month on AWS** at list rate. For a messaging platform pushing
DLR and webhook traffic, that matters. AWS/GCP only for things genuinely irreplaceable.

Prices are approximate 2026 Hetzner rates and should be re-checked at purchase.

### Tier 0 — Today. ≤50M/day. **~$0–60/month**
Your existing Hostinger VPS, or one Hetzner AX42-class box (~€49/mo: 64 GB RAM, 2× NVMe).
Everything co-located: Go services, Postgres, ClickHouse, Redis, Caddy.
**Honest caveat**: no redundancy. One disk failure ends the company. Add **~$10/mo off-box
backup storage** — non-negotiable, and the cheapest insurance you will ever buy.

### Tier 1 — Real production, HA. 50–200M/day. **~$200–300/month**
- 2× app servers (Go API + workers), ~€49 each
- 1× Postgres primary + 1× replica, ~€60 each
- 1× ClickHouse, ~€60
- Backup storage box, ~€12
Survives any single machine failing. **This is where I'd get to within ~6 months of real
traffic**, and it's roughly the cost of a mid-range laptop per year.

### Tier 2 — Serious scale. 200M–1B/day. **~$800–1,500/month**
Add: 3-node Redpanda cluster, 3-node ClickHouse cluster (+3 Keeper nodes — the research
specifically flags Keeper as the cost everyone forgets), more app/worker nodes, load balancers.
Roughly 12–15 machines.
For comparison: **3-node ClickHouse self-hosted on Hetzner ≈ $135/mo vs $2,000–4,000/mo on
ClickHouse Cloud.** Same workload.

### Tier 3 — Billions/day. **~$3,000–6,000/month**
25–40 machines: multi-region app tier, ScyllaDB cluster if live message state outgrows Postgres,
larger ClickHouse with tiered object storage, full observability stack.
At this volume you're sending ~365B messages/year. At even ₹0.10 revenue/message that's ₹36
crore/year of revenue. **Infrastructure is well under 1% of revenue.** The costs stop being the
interesting question long before here.

### The cost nobody quotes you

Self-hosting has a labour cost the research is blunt about: ClickHouse alone is cited at
**~2–4 weeks initial setup** and **4–8 hours/week ongoing** once stable. That's real. It's still
the right call versus $2–4k/month for managed at Tier 2 — but budget the hours, and revisit
managed ClickHouse the moment engineering time is scarcer than money.

---

## Part 6 — What I need from you

**Blocking Stage 0:**
1. **VPS specs** — vCPU, RAM, disk, and whether Postgres/Redis share the box. (Third ask.)
2. **Is adding ClickHouse now acceptable?** It's the one meaningful change to what you approved.
   Free, self-hosted, runs alongside Postgres on the same box today. I strongly recommend yes —
   retrofitting it at 500M/day is a migration; adding it now is a config file.

**Soon:**
3. Launch countries/currencies. 4. Payment gateway. 5. API domain. 6. **Off-box backup target**
   — please don't skip this one.

**Worth a conversation:**
7. **Who fixes `me.ts` and decides the `/v1/dev/*` question** — frontend team or us?
8. **Realistic 12-month volume.** "Billions eventually" is the right thing to architect for;
   knowing whether that's 18 months or 5 years changes what we build now versus what we merely
   leave room for.

---

## Sources

- [How Discord Stores Trillions of Messages](https://discord.com/blog/how-discord-stores-trillions-of-messages) · [HN discussion](https://news.ycombinator.com/item?id=35048410) · [ScyllaDB migration talk](https://www.scylladb.com/tech-talk/how-discord-migrated-trillions-of-messages-from-cassandra-to-scylladb/) · [InfoQ](https://www.infoq.com/news/2023/06/discord-cassandra-scylladb/)
- [ClickHouse vs PostgreSQL for analytics](https://www.tinybird.co/blog/clickhouse-vs-postgres) · [ClickHouse is winning the observability wars](https://matduggan.com/clickhouse-is-winning-the-observability-wars/) · [When to move logs out of a relational DB](https://lorbic.com/clickhouse-vs-postgres-log-storage/) · [PostHog: ClickHouse vs Postgres](https://posthog.com/blog/clickhouse-vs-postgres)
- [Postgres vs Kafka for event queues (Dagster)](https://dagster.io/blog/skip-kafka-use-postgres-message-queue) · [HN: Postgres as a better message queue](https://news.ycombinator.com/item?id=33083661) · [RudderStack: queueing on PostgreSQL](https://rudderstack.medium.com/kafka-vs-postgresql-how-we-implemented-our-queueing-system-using-postgresql-ec128650e3e)
- [Redpanda vs Kafka (Conduktor)](https://www.conduktor.io/glossary/redpanda-vs-kafka) · [Kafka vs Redpanda 2026 decision matrix](https://datacouch.io/blog/kafka-vs-redpanda-2026-enterprise-decision-matrix/) · [Kafka vs Pulsar vs NATS](https://bytewax.io/blog/kafka-vs-pulsar-vs-nats/) · [Kafka scaling best practices](https://factorhouse.io/articles/kafka-scaling-best-practices/) · [Tuning Kafka for a million msg/sec](https://oneuptime.com/blog/post/2026-01-25-tune-kafka-million-messages-per-second/view)
- [Go vs Rust 2026 honest backend comparison](https://levelupgo.dev/blog/go-vs-rust-2026-honest-backend-comparison) · [Rust vs Go benchmarks 2026](https://www.danilchenko.dev/posts/rust-vs-go/) · [High-performance hybrid architectures](https://crazyimagine.com/blog/high-performance-hybrid-architectures-rust-vs-go-in-2026/) · [Java vs Go vs Rust backend](https://www.index.dev/skill-vs-skill/backend-java-spring-vs-go-vs-rust)
- [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) · [gosmpp](https://github.com/linxGnu/gosmpp) · [fiorix/go-smpp](https://github.com/fiorix/go-smpp)
- [Hetzner vs AWS 2026](https://gartsolutions.com/hetzner-vs-aws/) · [AWS vs DigitalOcean vs Hetzner](https://www.forasoft.com/blog/article/aws-vs-digitalocean-vs-hetzner-1302) · [Self-hosting ClickHouse on Hetzner](https://aitorcto.substack.com/p/self-hosting-clickhouse-on-hetzner) · [Self-hosted ClickHouse cost](https://www.tinybird.co/blog/self-hosted-clickhouse-cost) · [ClickHouse TCO](https://oneuptime.com/blog/post/2026-03-31-clickhouse-estimate-total-cost-of-ownership/view)
- [BFF pattern with Next.js](https://medium.com/digigeek/bff-backend-for-frontend-pattern-with-next-js-api-routes-secure-and-scalable-architecture-d6e088a39855) · [Secure Next.js BFF sessions](https://www.cybersierra.co/blog/secure-nextjs-bff-sessions) · [Auth0: what devs get wrong about BFF](https://auth0.com/blog/things-developers-get-wrong-about-the-backend-for-frontend-pattern/)
- [Carrier-grade SMS gateway architecture](https://www.enabld.tech/blog/build-a-carrier-grade-sms-gateway/) · [SMPP vs HTTP API](https://www.enabld.tech/blog/smpp-vs-http-sms-api-comparison/) · [Understanding partitioning and sharding in Postgres and Citus](https://www.citusdata.com/blog/2023/08/04/understanding-partitioning-and-sharding-in-postgres-and-citus/)
