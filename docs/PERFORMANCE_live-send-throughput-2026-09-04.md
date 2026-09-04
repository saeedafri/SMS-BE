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

## The two levers, in order of return

1. **Batch the single-send ClickHouse write.** Accumulate accepted sends over a short window
   (50–200 ms) and insert them as one batch. This is where the core is going. It needs care: the
   window is a period where an accepted message is not yet durable in the warehouse, so the buffer
   has to be flushed on shutdown and the failure path has to be explicit. Expect the largest gain
   by far.
2. **More cores.** Two, shared with a neighbour, is the floor for a platform advertising 100/s.
   ClickHouse alone wants a core under load.

Neither is done. Both are recommendations, not claims.

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
