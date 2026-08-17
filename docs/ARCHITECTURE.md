# Architecture, and why

Every choice here has an alternative that a reasonable person would suggest. Each one
lists why it was refused — that is the question you will actually be asked.

## One process, not microservices

!!! success "Chosen: modular monolith"
    One Go binary. Modules separated by package: `internal/sending`,
    `internal/domain/billing`, `internal/store`, `internal/api`.

**The deciding case.** Sending must check the wallet and reserve money in the *same*
transaction that records the send. If those can disagree for a moment, a customer either
sends messages they cannot pay for, or pays for messages that never went. Inside one
process this is one transaction and the problem does not exist.

!!! failure "Rejected: separate billing and sending services"
    That transaction becomes distributed. You need a saga — reserve, send, confirm, and
    a compensating refund for every failure path including the compensator's own. That
    is a great deal of machinery whose only job is to restore a guarantee we already
    had for free.

**What it costs.** Everything scales together. Accepted: a 20,000-message campaign uses
0.48 cores and 124 MB, so the unit being scaled is small.

**The option stays open.** The seams are cut — modules do not touch each other's
tables, and workers are named goroutines started in `main.go`. Extracting one means
giving it its own `main`, not untangling it.

## Postgres for anything that must be correct

Tenants, users, wallets, campaigns, senders, templates, consent. The defining feature is
that **the database enforces tenant isolation**, not application code:

```sql
CREATE POLICY campaigns_tenant_isolation ON campaigns
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
```

!!! warning "The trap in that snippet"
    `USING` governs SELECT, UPDATE and DELETE — **not INSERT**. A policy with only
    `USING` silently rejects every insert, with no error message you can search for.
    Every policy in this system carries both clauses.

!!! failure "Rejected: filtering by tenant in application code"
    Works until one query in four hundred forgets. There is no compiler check for a
    missing `WHERE`, and the failure is showing one customer another's contacts. RLS
    turns a discipline problem into a database guarantee.

**Passwords:** argon2id, 64 MB / 3 iterations / 4 threads. Chosen over bcrypt because
bcrypt is cheap on GPUs; argon2id's memory cost is what makes a cracking rig expensive.
An unknown email still hashes against a dummy value, so timing cannot enumerate accounts.

## ClickHouse for message history

At 30M messages/day the rows are written once, read as aggregates, and dropped
wholesale after 90 days.

!!! success "Chosen: a column store"
    "Delivery rate by carrier for 30 days" reads 3 columns of 28 — a column store reads
    only those 3. Retention is `DROP PARTITION`, not a `DELETE` scanning hundreds of
    millions of rows. Measured ~60 bytes/message; 162 GB at 30M/day × 90 days.

!!! failure "Rejected: keeping messages in Postgres"
    Row storage means an aggregate over 3 columns still reads all 28. Worse, 30M
    rows/day in the same database as the wallet makes analytics and money compete for
    the same buffer cache, and ageing rows out becomes a vacuum problem that hurts the
    transactional side.

!!! danger "The setting that caused a money bug"
    ClickHouse defaults to `async_insert=1` — inserts become readable a moment later.
    Delivery receipts arrived faster: the receipt found no message, treated it as
    untrusted, dropped it, and the hold was never released. Fixed with
    `"async_insert": 0`.

## Redis, deliberately unimportant

Rate-limit counters only. The API boots and serves without it:

```go
rdb, err := store.OpenRedis(ctx, cfg.RedisURL)
if err != nil {
    // Redis backs rate limiting only. Refusing to boot without it would take
    // down messaging, billing and compliance to protect a counter.
    logger.Warn("redis unavailable; rate limiting degraded", "error", err)
}
```

Capped at 96 MB with `allkeys-lru`: when full it evicts rather than grows. Losing a
counter costs one window of limiting; unbounded growth costs the machine.

## The Go toolchain

| Choice | Why | Alternative refused |
|---|---|---|
| **oapi-codegen**, strict server | The contract generates the interface, so an unimplemented operation is a compile error | Hand-written handlers drift from the spec silently |
| **pgx v5** | Native protocol, real `int64`, batch support | `database/sql` adds a translation layer and loses types |
| **chi** | Standard `http.Handler`, no framework lock-in | Gin/Echo bring their own context and middleware idioms |
| **goose** | Plain SQL migrations, ordered, reversible | ORM auto-migration hides what actually ran |
