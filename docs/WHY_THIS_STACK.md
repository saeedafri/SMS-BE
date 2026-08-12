# Why Go, why not Node/Python, do we need Kafka — the honest version

You asked me to be honest and to actually understand the product. So this document leads with
the uncomfortable parts, not the reassuring ones.

---

## Part 0 — The honest framing first

**"Fastest, most robust, highly scalable" and "one Hostinger VPS at ₹0" are in direct tension.**

At 30M messages/day, the companies you'd be competing with — Twilio, Infobip, Gupshup — run
fleets of dozens to hundreds of machines. Not because they're wasteful, but because at that
volume you need redundancy, and one box means one power failure is a total outage with no
failover.

I am **not** telling you the constraint is wrong. A starter with no revenue should absolutely
run on one box. What I'm telling you is that the right goal is:

> **Build software whose only limit is the box, so that adding a second box is a config change,
> not a rewrite.**

That is achievable, and it's what the whole design is oriented around. Every choice below is
judged by that standard, not by "what's fastest in a microbenchmark."

Concretely, "scales by adding a box" requires three properties, and they drive everything else:

1. **Stateless API and workers.** No in-memory session state, no local files, no sticky
   routing. Run N copies, they don't coordinate.
2. **All coordination in Postgres or Redis.** Queue claims, rate limit tokens, leader election
   for the reconciler — never in process memory.
3. **Idempotency everywhere.** If two workers race, or one dies mid-send and another retries,
   the outcome must be identical. This is what makes horizontal scaling *safe* rather than
   merely possible.

Get those three right and the language matters less than people think. But it still matters,
so:

---

## Part 1 — Why Go, honestly

### The three reasons that actually decide it

**1. Memory is your real budget, and it's not close.**

You have one VPS. Every megabyte the runtime takes is a megabyte Postgres doesn't get for
`shared_buffers` and page cache — and at 90M rows/day, Postgres page cache is the difference
between queries hitting RAM and queries hitting disk.

Rough steady-state footprint for the same workload:

| Runtime | API service | Worker pool | Why |
|---|---|---|---|
| **Go** | ~50–80 MB | ~100–200 MB | Compiled, no VM, goroutine stacks start at 2 KB |
| Node.js | ~250–500 MB × N processes | ~400 MB+ | V8 heap + single-threaded, so you run one process **per core** to use the CPU |
| Python | ~200–400 MB × N processes | ~500 MB+ | Same GIL problem, worse per-object overhead |
| Java/JVM | ~500 MB–1 GB | ~1 GB+ | JIT is genuinely fast once warm, but the heap floor is brutal |

On a 4-core box, Node means **4 processes** to use your cores. That's 1–2 GB before you've
sent a single message. Go uses all 4 cores in one process.

**2. Goroutines are the exact shape of this problem.**

An SMS platform's data plane is: thousands of simultaneously-open, long-lived, mostly-idle
network connections — SMPP binds to carriers, HTTP calls to aggregators, outbound webhook
deliveries to tenant endpoints — each of which is *blocked waiting on I/O* almost all the time.

Go handles this with 10,000 goroutines at ~4 KB each: ~40 MB, code written in plain
blocking style. Node handles it with an event loop and callbacks — workable, but any accidental
synchronous work (a big JSON parse, a crypto call) stalls **every** connection on that process.
Python needs asyncio and one bad blocking library call poisons the loop the same way.

This isn't theory. Connector lifecycle management — "reconnect the SMPP bind without loss or
duplication," which your own PRD §8 calls out — is where these systems actually break. Go
makes it a `for` loop with a `select`. That's worth a lot.

**3. The contract becomes a compiler error.**

This one is specific to *your* situation and I think it's underrated.

The frontend team handed you 151 operations and a README whose central plea is *"don't
implement something different from what's written here."* Contract drift is the #1 predicted
failure of this project.

`oapi-codegen` reads their `openapi.json` and generates a Go **server interface**. If we miss an
operation, or return the wrong field type, **the build fails**. Not a lint warning, not a
runtime 500 discovered in QA — a red build.

Compare:
- **TypeScript** (`openapi-typescript`): generates *types*. Helps a lot. But types are advisory —
  a wrong `as any`, a handler that returns a slightly different object, and it compiles fine.
- **Python** (FastAPI): generates a spec *from* your code, which is the wrong direction here —
  they own the spec, we implement it.
- **Java**: has good OpenAPI codegen and would also give compile-time enforcement. It's a real
  answer, just a heavier one (see below).

Go is the only option that gives you compile-time contract enforcement *and* a small footprint.

### The honest costs of Go

I'd rather you hear these from me now than discover them in month three:

- **Verbose.** `if err != nil` everywhere. Roughly 20–30% more lines than equivalent TS.
- **Your frontend team can't read it.** Real cost — no shared code, no one hopping across.
- **Weaker in a few libraries.** PDF invoice generation, some payment gateway SDKs, and CSV/
  Excel handling are all nicer in Node/Python. Mitigation: these are small, non-hot-path jobs;
  worst case a tiny sidecar service handles them.
- **Hiring.** Smaller pool than Node/Python in India, though it's grown a lot and Go devs skew
  toward backend-serious.

### Why not the others — one line each, no hedging

**Node/TypeScript.** Would work for the control plane. Would hurt on the data plane at your
volume and on your box: 4–8× the memory, one process per core, and any accidental sync work
stalls the loop. Twilio started on Ruby and moved off it for exactly this reason; nobody in
this industry runs the high-volume path on Node today. If your volume were 1M/day I'd say use
it and enjoy the one-language simplicity.

**Python.** Same GIL/multi-process memory problem, plus 10–50× slower per-operation than Go on
the CPU-bound parts (template rendering, GSM-7 encoding, segment counting, signature
verification — all of which run per message, 30M times a day). Genuinely excellent for the
data-science and analytics side later. Not for a connector fleet.

**Java/Kotlin.** Honestly, *technically* fine — this is what Twilio, Infobip, Sinch and Gupshup
actually run. Virtual threads (Java 21) even solve the concurrency problem the same way
goroutines do. The reason I'm not recommending it: ~1 GB heap floor per service on a box where
that's a quarter of your RAM, plus JVM tuning is a specialist skill you don't have on staff.
Java wins at 50 engineers and 20 servers. You have neither yet.

**Rust.** Fastest and safest, genuinely. And it would take your team 2–3× longer to ship 151
endpoints. The bottleneck in this project is the network and the database, not CPU — so Rust's
advantage over Go is nearly zero here while its cost is large. Wrong trade.

**Elixir.** The most *elegant* fit — OTP supervision trees make connector reliability almost
free, and Telnyx runs a whole carrier on it. Ruled out purely on hiring: you would not be able
to replace me or yourself on this codebase.

---

## Part 2 — Kafka. Do we need it?

**Short answer: no, not now — and the reason is arithmetic, not preference.**

### The arithmetic

Kafka's design point is millions of events per second across a cluster. Your load:

- 30M messages/day = **~350/sec average**, maybe 3,000/sec at campaign peak.
- Kafka on a single broker handles that without noticing. So would Postgres. So would Redis.
  So would a text file, frankly.

**350/sec is not big data. It only feels big because the daily total has a lot of zeros.**
That's the honest reframe: 30M/day sounds enormous and is a genuinely serious business volume,
but as a *rate* it's modest. Your real engineering challenges are storage and correctness, not
throughput.

### What Kafka would actually cost you here

- **2–4 GB RAM** for broker + coordination (Redpanda, the C++ Kafka-compatible rewrite, gets
  this to ~1 GB — it's the better choice if we ever do this).
- A **third stateful system** to operate, back up, monitor, and recover at 3 AM.
- **The transactional guarantee you'd lose.** This is the real killer. Your PRD requires
  compliance + suppression + balance check **and** enqueue to happen atomically. Postgres +
  River gives you that in one `BEGIN...COMMIT`. With Kafka, "debit the wallet" and "enqueue the
  send" are in two different systems, and you're now writing an outbox pattern with a relay
  process to get back the guarantee you started with. That's strictly more machinery for
  strictly less safety, at a volume that doesn't need it.

### The design detail that makes Postgres-as-queue comfortably correct

This is the part I want you to push back on if you disagree, because it's the load-bearing
choice:

**Job granularity is the batch, not the message.**

A naive Postgres queue at 30M jobs/day would mean 30M inserts + 30M deletes + the resulting
vacuum churn, on the same database already absorbing 90M message rows. That would be a real
problem, and if I'd designed it that way you'd be right to worry.

Instead: one job per **batch of 1,000 messages**.

- 30M messages/day → **30,000 jobs/day** → **~0.35 jobs/sec.**
- Message rows are written with `COPY` (bulk), not per-row INSERTs.
- The queue table stays small enough to live entirely in RAM.

The queue load drops by three orders of magnitude and stops being an engineering concern at
all. Carriers want batched submits anyway — this matches how SMPP actually works.

### When Kafka/Redpanda *does* become right

I'd revisit the moment any of these is true:

1. **Multiple independent consumers need the same event stream** — e.g. analytics, fraud
   detection, a data warehouse, and a partner integration all want every delivery event.
   That's Kafka's actual superpower: fan-out with independent cursors.
2. **You need replay** — "reprocess last week's DLRs because the rating engine had a bug."
   Postgres can do this, Kafka does it natively.
3. **You go multi-node and want a real event backbone** rather than everything through one DB.
4. **Sustained volume crosses roughly 10× current** — ~5,000/sec sustained.

**So here's the commitment**: all event publishing goes through one narrow interface —

```go
type EventBus interface {
    Publish(ctx context.Context, topic string, events []Event) error
    Subscribe(ctx context.Context, topic, group string, fn Handler) error
}
```

Day one it's backed by Postgres/River. Swapping in Redpanda later is a new implementation of
that interface plus a config flag — days of work, not a rewrite. **We build the exit door now
and don't walk through it until the arithmetic says to.**

Adopting Kafka today would be the expensive kind of wrong: it *looks* like the serious choice,
costs you a third of your RAM and your on-call sanity, and buys nothing measurable at 350/sec.

---

## Part 3 — What I actually understand about this product

So you can check whether I've got it right. These are the things that make this product
*this* product, and each one dictates a piece of the architecture:

1. **The entire pitch is honesty.** "Delivered" must mean handset-confirmed, never
   carrier-accepted. That's why those are **separate states in the schema** and can never be
   merged by a query. If we ever collapse them for convenience, we've become the thing the PRD
   says we're competing against.

2. **"No charge for undelivered" is a ledger state machine, not a report.** Money moves
   `hold → charge` **only** on a handset-confirmed DLR; anything else releases the hold. That's
   why billing lives in the send transaction and not in a nightly job.

3. **A send is gated, never direct.** Compliance (right country regime) + template approved +
   sender approved + consent + not suppressed + sufficient balance — all checked **before**
   enqueue, in one transaction. Any of these failing must fail *loudly* and before money moves.

4. **Compliance is per-country and pluggable, not an if-statement.** India DLT, US 10DLC, UK
   MMA, UAE approval. New country = new adapter file. The PRD calls this the "adaptability
   law" and it's the difference between a platform and a script.

5. **Grey routing is a flagged, permissioned exception — never a silent default.** This is a
   business-survival property. Silently routing DLT-required traffic over a grey route is how
   aggregators get shut down.

6. **Idempotency is a correctness requirement, not a nicety.** A duplicate `POST /v1/campaigns`
   must not double-send to 5 million people or double-charge. Same for every DLR a carrier
   redelivers.

7. **The operator console has real teeth.** Suspend/throttle must actually stop messages
   already in flight within seconds, not "on the next campaign." That means the abuse controls
   live in the send path's hot check, backed by Redis, not just a database flag.

8. **RCS is co-flagship with SMS from day one**, with RCS→SMS fallback. So "channel" is an
   interface from the first commit, not a column we add later.

9. **Tenant isolation is a data-leak defect if it's ever merely conventional.** Hence Postgres
   RLS — a forgotten `WHERE tenant_id` returns zero rows instead of another company's messages.

If any of these nine is wrong or you'd rank them differently, tell me — they're the spine of
every decision in `BACKEND_DESIGN.md`.

---

## Part 4 — What I still need from you

**Blocking (Stage 0 can't start without #1):**

1. **VPS specs** — vCPU, RAM, disk size and type (NVMe or SATA), and whether Postgres and Redis
   are on *that same box* or a different one. Asked before; it's the last true blocker. It sets
   the hot-data window, worker pool sizes, and Postgres tuning.

**Needed soon (Stages 1–3, tell me when convenient):**

2. **Launch countries and currencies** — India-only first, or India + one more? Decides which
   compliance adapters get built in Stage 2 versus stubbed.
3. **Payment gateway for wallet top-ups** — Razorpay, Stripe, PayU, or manual/bank-transfer to
   start? `POST /v1/wallet/topup` needs a real integration eventually; manual is a fine v1.
4. **Domain name** for the API, so Caddy can get TLS certs.
5. **Backup destination** — is there anywhere off the VPS we can put nightly Postgres dumps?
   Even a free-tier object store or a second cheap box. **One VPS with no off-box backup means
   one disk failure ends the company**, and I'd be doing you a disservice not to say that
   plainly. This is the one place I'd argue for spending a few hundred rupees a month.

**Useful context, no rush:**

6. Is 30M/day today's traffic, or the 12-month target? Changes how hard we optimize now versus
   later. Building for it is right either way; knowing the timeline changes sequencing.
7. Team size on backend — is it you, or you plus others? Affects how much I lean on codegen and
   convention versus flexibility.
