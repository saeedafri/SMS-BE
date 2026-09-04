# Live send throughput — measured, 4 September 2026

Measured against the **deployed** control-api on the VPS, through the real send path: API-key
auth, the rate limiter, the wallet charge, route selection, the connector, and the ClickHouse
write. Production carries no carrier credentials, so the connector is the sandbox and nothing
reached a handset — every other layer is the one that serves customers.

The load generator ran **on the VPS** (`127.0.0.1:8080`). Running it from a laptop over the SSH
tunnel measured the home network, not the platform: 135 ms p50 there versus 77 ms on the box.

---

## The number

**~19–22 accepted sends per second, sustained, with zero errors and zero losses.**

Two minutes, 4 workers:

| | |
|---|---|
| requests | 2,308 |
| accepted (202) | 2,308 |
| throughput | **19.2 accepted/sec** |
| latency p50 / p95 / p99 / max | 189 ms / 368 ms / 725 ms / 1.16 s |
| non-202 responses | **0** |
| health probes below 200 during the run | **0** |
| accepted vs recorded in ClickHouse | **2,308 / 2,308 — zero misses** |

The reconciliation matters more than the rate: every id the API returned was found in the
warehouse afterwards. A message accepted and then lost would show here as a miss, not as a
success.

## The capacity curve

Concurrency does not buy throughput — it buys queue:

| workers | accepted/sec | p50 | p99 |
|---|---|---|---|
| 1 | 11.5 | 77 ms | 200 ms |
| 2 | 16.4 | 112 ms | 211 ms |
| 4 | 18.2 | 214 ms | 349 ms |
| 8 | 15.9 | 417 ms | 1.48 s |
| 24 | 22.1 | 1.03 s | 1.75 s |

Throughput plateaus in the high teens while latency grows roughly linearly. That is a saturated
server, not a network limit.

## Where the time goes

- `GET /healthz`, which touches no datastore: **1.0 ms p50, ~950 req/s single-threaded.** The Go
  service and HTTP stack are not the constraint.
- A send at concurrency 1: **77 ms.** So ~76 ms of every send is datastore work.
- Under load, `top` on the VPS shows **ClickHouse pinned at 100% CPU** while `control-api` sits
  at ~20%.

The box has **2 cores**, shared with the EMS containers.

## The bottleneck, named

**Per-message ClickHouse inserts.** `InsertMessages` already carries the comment that says why
this hurts:

> ClickHouse is built for batched inserts and punished by row-at-a-time ones, so the send path
> always accumulates a batch before calling this — never one insert per message.

That holds for campaigns, which batch. A **single send is its own batch of one**, so the
transactional API path pays a full insert per message and ClickHouse spends a core on
per-row overhead.

## What this means for the advertised tier

`GetRateLimit` advertises **100/second on live**. The platform currently sustains about a fifth of
that. The limiter is not what stops it — no request in any run was throttled — the hardware and
the insert pattern are.

## Tested and rejected: caching the configuration reads

The six repeated Postgres lookups per send looked like the obvious win, so they were put behind a
two-second cache with immediate invalidation on every path that changes one.

**It moved nothing.** Same key tier, same four workers, same two minutes, against the deployed
API:

| | accepted/sec | p50 |
|---|---|---|
| before | 19.2 | 189 ms |
| after | 18.4 | 185 ms |

Within noise, and if anything slightly worse. Those reads were never the constraint — which the
CPU numbers had already said and the change did not respect: ClickHouse holds a full core while
control-api uses a fifth of one.

The cache is therefore **shipped disabled** (`NewHotCache(0)`). Its code and tests stay, because
Postgres becomes the next wall once the warehouse write is batched, and turning it on is one
number. Carrying a staleness window on tenant standing — a compliance-relevant check — for zero
measured gain is not a trade worth making.

The lesson is the ordinary one: the profile said ClickHouse, and the first change addressed
something else.

## Shipped and measured: coalescing the warehouse inserts

`async_insert=1` with `wait_for_async_insert=1`, batch window 10ms. Concurrent inserts now share
one data part instead of each becoming their own, which is what the merge storm was made of.
`wait_for_async_insert` is what keeps it safe: the INSERT does not return until the rows are
queryable, so the delivery-report path still finds its message.
`TestARowIsQueryableAsSoonAsTheInsertReturns` guards that and fails on the first attempt if the
flag goes back to 0.

Measured on the deployed API, same key tier and duration throughout:

| workers | before | 50ms window | 10ms window (shipped) |
|---|---|---|---|
| 4 | 18.2/s · p50 214ms | 11.2/s · 325ms | 15.6/s · 208ms |
| 24 | 22.1/s | 31.4/s | 26.9/s |
| 64 | — | 36.4/s | 34.6/s |

**The ceiling went from ~22 to ~35 accepted/sec, about +57%.** Low-concurrency throughput is
still slightly below where it started, because even a 10ms window is time a lone caller spends
waiting for company. 50ms bought a marginally higher ceiling and cost far more at the quiet end,
which is why 10ms is the one deployed.

Run-to-run variance on this box is roughly ±15% — it shares two cores with the EMS containers —
so treat single-digit differences as noise.

### What this does not do

It does not get you to thousands. It removes the warehouse from the critical path's worst
behaviour; it leaves a synchronous wallet transaction and a carrier call inside the request.

## The two levers, in order of return

1. ~~Batch the warehouse write.~~ **Done** — see above. +57% ceiling.
2. **Accept-and-enqueue.** Validate, persist one row, return 202, and let a worker pool do route
   selection, the wallet charge and the warehouse write. This is the change that reaches
   thousands, because it takes the datastores off the request path entirely and makes throughput
   a property of the pool rather than of one message's round trips. It is also the change that
   needs real design: a message that is accepted but not yet charged is a state the product does
   not have today, and getting it wrong means either double-charging or sending for free.
3. **More cores.** Two, shared with a neighbour, is the floor for a platform advertising 100/s.
   ClickHouse alone wanted a core under load before this change.

2 and 3 are recommendations, not claims. Neither is done.

## How to reproduce

The generator is `scratchpad/loadtest.go` from the session that produced this document. Build for
the VPS, upload, run with a **live-tier API key** — the limiter is per key, so a session token
measures a different path:

```
GOOS=linux GOARCH=amd64 go build -o loadtest-linux loadtest.go
scp loadtest-linux root@31.97.186.223:/tmp/relay-loadtest
ssh root@31.97.186.223 "IDS_OUT=/tmp/ids.txt /tmp/relay-loadtest \
  -base http://127.0.0.1:8080 -key <live key> -sender <uuid> -workers 4 -duration 120s"
```

Then reconcile `/tmp/ids.txt` against `SELECT count(DISTINCT id) FROM messages WHERE id IN (…)`.

The probe key created for this run was revoked afterwards, and the generator was removed from
the VPS.
