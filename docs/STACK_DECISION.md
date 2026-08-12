# Stack decision — what real CPaaS platforms actually run

You asked for the real-world options, not a textbook answer. Here they are, with who runs them
in production and what it would mean for *us* specifically: one Hostinger VPS, ₹0 extra spend,
**30M messages/day**.

---

## What the actual companies run

| Company | Data plane | Notes |
|---|---|---|
| **Twilio** | Java/Scala + Kafka, originally Ruby | Rewrote off Ruby precisely because the data plane outgrew it. Control plane still partly Rails. |
| **Infobip** | Java/Kotlin + Kafka | Classic JVM shop, ~10B+ msgs/yr. |
| **Bird (MessageBird)** | **Go** | Public about Go for connectors and pipeline. |
| **Plivo** | **Go** data plane, Python control plane | The exact split this design proposes. |
| **Telnyx** | **Elixir/OTP** | Genuinely excellent for millions of stateful carrier sessions. Tiny hiring pool. |
| **Sinch / Gupshup / Kaleyra** | Java | Enterprise-typical. |
| **Jasmin SMS Gateway** (OSS) | Python + RabbitMQ + Redis | The reference open-source SMS router. Does thousands of TPS. Worth reading, not adopting. |
| **Kannel** (OSS) | C | The 20-year-old grandfather of SMS gateways. Still deployed. |

**The pattern is unmistakable**: nobody serious runs the high-volume SMS data plane on
Node.js. The split is JVM (older, enterprise, Kafka-heavy) or Go (newer, leaner). Elixir is the
brilliant outlier.

---

## The four options that are actually on the table for us

### Option A — Go everywhere ✅ recommended

Go 1.23 + chi + oapi-codegen + sqlc + Postgres + River + Redis.

- **Memory**: ~50–80 MB per service. On a VPS where RAM is the budget, this is the whole
  argument. Every GB not spent on the runtime is a GB of Postgres `shared_buffers`.
- **Contract enforcement**: `oapi-codegen` generates a Go *server interface* from their
  `openapi.json`. Miss an operation or drift a field → **compile error.** No other option in
  this list enforces the contract at build time.
- **Concurrency**: 10k concurrent SMPP/HTTP connector sockets is a non-event. This is literally
  what goroutines were designed for.
- **Ops**: one static binary. No JVM tuning, no `node_modules`, no GIL, no interpreter.
- **Cost**: fastest path to "handles 30M/day on one box."
- **Against it**: more verbose than TS; your frontend team can't read it as easily; you'd be
  hiring for two languages.

### Option B — TypeScript control plane + Go data plane

NestJS/Fastify for the 151 dashboard endpoints, Go for the pipeline. This is Plivo's shape.

- **For**: frontend team can contribute to the control plane; types generated straight from
  `openapi.json` with the tooling they already use (`openapi-typescript`).
- **Against**: two languages, two build systems, two deploy pipelines — for a *starter team*
  that's a real tax. The control plane is 80% of the code; putting it in the heavier runtime
  costs 1–2 GB of RAM you don't have at 30M/day. And the contract is enforced only by advisory
  types, not compilation.
- **Verdict**: this is the right answer at 20 engineers. At your size it's the worst of both.

### Option C — TypeScript everywhere

- **For**: one language across the whole company, fastest onboarding, biggest ecosystem.
- **Against**: at 30M/day the data plane is ~350 msg/sec sustained with 1000+ DB rows/sec.
  Node can do it — but it needs 3–5× the memory and multiple worker processes to use your
  cores, on a box that doesn't have the headroom. You'd be fighting the runtime within months.
  This is precisely the wall Twilio hit with Ruby.
- **Verdict**: viable only if volume is far below what you stated.

### Option D — Elixir/OTP

- **For**: the *theoretically* best fit. Supervision trees make "reconnect the SMPP bind
  without loss or duplication" nearly free. Telnyx runs on it.
- **Against**: you cannot hire for it in India cheaply, ecosystem for Postgres/OpenAPI codegen
  is thinner, and I'd be handing you a system your next engineer can't maintain.
- **Verdict**: right answer, wrong company stage.

---

## Recommendation

**Option A, Go everywhere.** At 30M/day on a single VPS with ₹0 spend, memory efficiency and
compile-time contract enforcement aren't nice-to-haves — they're what makes the constraint
survivable. Option B becomes correct once you have a backend team big enough to split.

---

## The harder truth about 30M/day on one VPS

Compute is fine. **Disk is the constraint**, and you should hear the math before we start:

- 30M messages/day + ~3 lifecycle events each ≈ **90M rows/day**
- At ~300 bytes/row including indexes ≈ **~25 GB/day**, ~750 GB/month
- Hostinger's largest VPS tops out around 400 GB NVMe

So **~16 days of data fills the biggest box you can buy**, and that's before Postgres WAL,
backups, and the OS. This is not a reason to change the stack — Go/Postgres handles the
throughput. It's a reason to make retention a **day-one architectural feature, not a
setting**:

1. **Daily partitions** on `messages` and `message_events`; drop = instant, not a scanning DELETE.
2. **Hot window of 7–14 days** in Postgres. That's what `GET /v1/messages` searches.
3. **Rollups are permanent** — hourly/daily aggregates per tenant/campaign/route/country are
   tiny (megabytes) and never dropped. All analytics read only these, so the dashboard's
   history stays complete forever even when raw rows are gone.
4. **Cold archive**: partitions past the hot window export to compressed Parquet/CSV.gz
   (~10–20× smaller) on disk or wherever you have cheap space, restorable on demand.
5. `GET/PATCH /v1/data-retention` in the contract becomes the real, user-facing control over
   step 2 — the contract already anticipated this.

**What I need from you**: your exact VPS specs (vCPU / RAM / disk), and whether Postgres and
Redis are on that same box or a different one. That single answer sets partition strategy,
hot-window length, and worker pool sizing. Everything else in the design holds regardless.
