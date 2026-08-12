# Architecture — diagrams and flows

Companion to `BACKEND_DESIGN.md` (decisions), `RESEARCH_AND_SCALE.md` (evidence and scale),
`WHY_THIS_STACK.md` (rationale). This document is the **pictures**: how data moves, what each
box is responsible for, and what happens on every important path.

---

## 1. System context — who talks to what

```mermaid
graph TB
    subgraph Customers
        BIZ["Business user<br/>(dashboard)"]
        DEV["Developer<br/>(REST API)"]
    end
    subgraph Us
        OPS["Operator<br/>(console)"]
    end

    UI["<b>Next.js app</b><br/>Vercel<br/>dashboard + operator console"]

    CAPI["<b>control-api</b> (Go)<br/>151 contract operations<br/>human-paced traffic"]
    PAPI["<b>public-api</b> (Go)<br/>send · verify · status<br/>DLR/MO ingest<br/>high volume"]
    WRK["<b>workers</b> (Go)<br/>fan-out · submit · settle<br/>webhooks · rollups"]

    PG[("<b>PostgreSQL</b><br/>control plane<br/>system of record")]
    CH[("<b>ClickHouse</b><br/>message logs<br/>+ analytics")]
    RD[("<b>Redis</b><br/>hot state<br/>limits · counters")]

    CARR["Carriers / aggregators<br/>SMPP · HTTP"]
    HOOK["Tenant webhook<br/>endpoints"]

    BIZ --> UI
    OPS --> UI
    UI -->|"server-side only<br/>Bearer token"| CAPI
    DEV -->|"API key"| PAPI
    CARR -->|"DLR / MO"| PAPI

    CAPI --> PG
    CAPI --> CH
    CAPI --> RD
    PAPI --> PG
    PAPI --> RD
    PAPI --> CH
    WRK --> PG
    WRK --> CH
    WRK --> RD
    WRK -->|submit| CARR
    WRK -->|signed POST| HOOK

    style CAPI fill:#2d6a4f,color:#fff
    style PAPI fill:#2d6a4f,color:#fff
    style WRK fill:#2d6a4f,color:#fff
    style UI fill:#1d3557,color:#fff
```

**The one rule to remember**: the browser never talks to our Go services. Every dashboard call
goes through the Next.js server, which holds the httpOnly session cookie and attaches it as a
bearer token. Only developers' own API keys and carriers reach `public-api` directly.

---

## 2. Why two databases — the split that makes billions tractable

```mermaid
graph LR
    subgraph SMALL["Control plane — small &amp; complex"]
        direction TB
        A1["tenants · users · teams"]
        A2["sender IDs · templates<br/>registrations"]
        A3["campaigns · lists<br/>suppressions"]
        A4["wallet ledger (batch)<br/>invoices · rate cards"]
        A5["routes · audit log"]
    end

    subgraph BIG["Data plane — huge &amp; simple"]
        direction TB
        B1["messages<br/>~30 bytes each"]
        B2["message_events"]
        B3["hourly / daily rollups"]
    end

    SMALL --> PG[("PostgreSQL<br/>&lt;200 GB even at 1B/day<br/>RLS · transactions · joins")]
    BIG --> CH[("ClickHouse<br/>~11 TB per year at 1B/day<br/>columnar · compressed")]

    style PG fill:#336791,color:#fff
    style CH fill:#faff69,color:#000
```

| | Control plane (Postgres) | Data plane (ClickHouse) |
|---|---|---|
| Row count at 1B msg/day | ~10⁷ total | ~10¹² and growing |
| Growth driver | tenants (slow) | messages (fast) |
| Query shape | joins, transactions | scans, aggregates |
| Needs ACID | **yes** | no |
| Per-record size | irrelevant | **everything** |

**Postgres stays the system of record.** ClickHouse is derived, rebuildable state — if we lost
it we could replay. That property is what makes running two databases safe rather than scary.

---

## 3. The send pipeline — the hot path

```mermaid
flowchart TD
    START(["POST /v1/messages<br/>or campaign fan-out"]) --> IDEM{"Idempotency-Key<br/>seen before?"}
    IDEM -->|yes| REPLAY["Return stored response<br/>no side effects"]
    IDEM -->|no| GATE

    subgraph GATE["THE GATE — one Postgres transaction"]
        direction TB
        G1["1 · Tenant active?<br/>not suspended/throttled"]
        G2["2 · Compliance regime<br/>sender + template approved<br/>for destination country"]
        G3["3 · Consent + not suppressed"]
        G4["4 · Wallet balance sufficient"]
        G5["5 · Write ledger HOLD"]
        G6["6 · Insert message rows (COPY)"]
        G7["7 · Enqueue batch job"]
        G1 --> G2 --> G3 --> G4 --> G5 --> G6 --> G7
    end

    GATE -->|any check fails| REJECT["ROLLBACK<br/>no money moved<br/>no message queued<br/>error to caller"]
    GATE -->|all pass| COMMIT["COMMIT<br/>atomic"]

    COMMIT --> QUEUE[["Job queue<br/>1 job = 1,000 messages"]]
    QUEUE --> RATE{"Route + tenant<br/>rate limit<br/>(Redis token bucket)"}
    RATE -->|throttled| WAIT["Wait, retry"] --> RATE
    RATE -->|allowed| SUBMIT["Connector submits<br/>SMPP bind / HTTP"]

    SUBMIT --> ACK{"Carrier response"}
    ACK -->|accepted| ACCEPTED["state = accepted<br/>hold still held"]
    ACK -->|rejected| RELEASE1["state = rejected<br/>RELEASE hold"]
    ACK -->|timeout| SUBMITTED["state = submitted<br/>reconciler owns it"]

    ACCEPTED --> DLR{"DLR arrives?"}
    SUBMITTED --> DLR
    DLR -->|delivered| CHARGE["state = delivered<br/><b>HOLD → CHARGE</b>"]
    DLR -->|failed| RELEASE2["state = undelivered<br/><b>RELEASE hold</b>"]
    DLR -->|"never (window expires)"| RELEASE3["state = expired<br/><b>RELEASE hold</b>"]

    style GATE fill:#1d3557,color:#fff
    style CHARGE fill:#2d6a4f,color:#fff
    style RELEASE1 fill:#6a040f,color:#fff
    style RELEASE2 fill:#6a040f,color:#fff
    style RELEASE3 fill:#6a040f,color:#fff
    style REJECT fill:#6a040f,color:#fff
```

**Read the bottom three boxes.** Money converts `hold → charge` on exactly one condition:
a handset-confirmed delivery receipt. Every other terminal outcome releases the hold. That is
"no charge for undelivered" implemented as a state machine, not as a report or a nightly
correction job.

---

## 4. Message state machine

```mermaid
stateDiagram-v2
    [*] --> queued: gate passed, committed
    queued --> submitting: worker claims batch
    submitting --> submitted: sent to carrier, no ack yet
    submitting --> rejected: carrier refused
    submitted --> accepted: carrier ack (NOT delivery)
    submitted --> expired: reconciler, no receipt in window
    accepted --> delivered: handset-confirmed DLR
    accepted --> undelivered: failure DLR
    accepted --> expired: reconciler, DLR window passed

    delivered --> [*]
    undelivered --> [*]
    rejected --> [*]
    expired --> [*]

    note right of accepted
        accepted ≠ delivered.
        Never merge these two.
        This is the product's
        core honesty promise.
    end note

    note right of expired
        No message sits in a
        non-terminal state forever.
        The reconciler guarantees it.
    end note
```

Every transition is idempotent, keyed on `(message_id, to_state)`. A carrier redelivering the
same DLR five times produces one transition and one ledger effect.

---

## 5. Campaign fan-out — why one job is 1,000 messages

```mermaid
sequenceDiagram
    participant U as Dashboard
    participant A as control-api
    participant P as Postgres
    participant Q as Job queue
    participant W as Worker
    participant C as Connector
    participant CH as ClickHouse

    U->>A: POST /v1/campaigns (Idempotency-Key)
    A->>P: BEGIN · validate · insert campaign · enqueue expand
    P-->>A: COMMIT
    A-->>U: 202 campaign accepted

    Q->>W: expand_campaign(campaign_id, cursor=0)
    loop every 10,000 recipients
        W->>P: read audience page, minus suppressions
        W->>P: COPY 10k message rows (queued)
        W->>Q: enqueue 10 × submit_batch(1,000 each)
        W->>P: save cursor (resumable)
    end

    par batches run concurrently
        Q->>W: submit_batch(ids)
        W->>W: Redis token bucket (route + tenant)
        W->>C: submit
        C-->>W: accepted refs
        W->>CH: write message + event rows
        W->>U: counters via SSE
    end

    Note over C,CH: later, asynchronously
    C->>W: DLR batch
    W->>P: settle ledger (batch-level)
    W->>CH: append delivery events
```

**The arithmetic that makes the queue a non-issue**: 5M recipients → 5,000 jobs, not 5,000,000.
At 30M messages/day that's ~30,000 jobs/day ≈ **0.35 jobs/sec**. The queue table stays small
enough to live entirely in RAM. Message rows are bulk-written with `COPY`, never row-by-row.

---

## 6. DLR ingest — where these systems usually die

```mermaid
flowchart LR
    C["Carrier<br/>bursts thousands/sec"] --> EP["POST /v1/hooks/dlr/{connector}"]

    subgraph FAST["HTTP handler — target &lt;1 ms"]
        V["verify signature"] --> W["append raw to dlr_inbox"] --> R["return 200"]
    end

    EP --> FAST
    FAST -.->|"never blocks on<br/>business logic"| C

    subgraph ASYNC["Drain worker — batched"]
        D1["read 5,000 rows"] --> D2["map carrier code → our state"]
        D2 --> D3["idempotent transitions"]
        D3 --> D4["settle ledger (batched)"]
        D4 --> D5["append to ClickHouse"]
        D5 --> D6["enqueue tenant webhooks"]
    end

    FAST --> ASYNC

    style FAST fill:#2d6a4f,color:#fff
    style ASYNC fill:#1d3557,color:#fff
```

**The rule**: never do business logic inside the carrier-facing HTTP handler. Accept, persist
raw, return 200 immediately. A slow handler here means carriers time out, retry, and amplify
their own load until the system collapses. This is the single most common failure mode in SMS
platforms.

---

## 7. Tenant isolation — enforced by the database, not by discipline

```mermaid
flowchart TD
    REQ["Request with Bearer token"] --> AUTH["Verify JWT<br/>extract tenant_id"]
    AUTH --> TX["BEGIN<br/>SET LOCAL app.tenant_id = '...'"]
    TX --> Q["Any query the handler runs"]
    Q --> RLS{"Postgres RLS policy<br/>tenant_id = current_setting('app.tenant_id')"}
    RLS -->|matches| ROWS["rows returned"]
    RLS -->|"does not match"| ZERO["<b>zero rows</b><br/>not another tenant's data"]

    OPS["Operator endpoint"] --> OPROLE["separate cross-tenant role<br/>every use audit-logged"]
    OPROLE --> ROWS

    style ZERO fill:#2d6a4f,color:#fff
    style RLS fill:#336791,color:#fff
```

The API's database user is **not** the table owner and cannot bypass RLS. A handler that forgets
`WHERE tenant_id = ...` returns nothing instead of leaking. Cross-tenant access is possible only
through one explicitly-separate role used only by operator endpoints.

---

## 8. Money — the ledger

```mermaid
flowchart TD
    subgraph BATCH["Per batch of ~1,000 messages"]
        H["ledger: HOLD −₹X<br/>(estimated cost)"]
    end

    H --> OUT{"Batch outcomes<br/>settle when DLRs land"}
    OUT --> D["800 delivered"]
    OUT --> F["150 undelivered"]
    OUT --> E["50 expired"]

    D --> S["ledger: SETTLE<br/>CHARGE actual delivered cost<br/>RELEASE the rest"]
    F --> S
    E --> S

    S --> BAL["wallet_balances updated<br/>same transaction"]
    BAL --> INV["invariant checker<br/>sum(ledger) == balance<br/>runs continuously"]

    CHDETAIL[("ClickHouse<br/>per-message cost<br/>for drill-down + usage")]
    D -.-> CHDETAIL
    F -.-> CHDETAIL
    E -.-> CHDETAIL

    style S fill:#2d6a4f,color:#fff
    style INV fill:#1d3557,color:#fff
```

- Ledger entries are **per batch**, not per message — millions of rows per year, not billions
  per day. This is what keeps the control plane small forever.
- Per-message cost detail lives in ClickHouse, reconciled against ledger batch totals.
- All money is `BIGINT` minor units paired with a `currency`. No `NUMERIC`, no float, anywhere.
- The ledger is append-only, enforced by trigger — no `UPDATE`, no `DELETE`, ever.

---

## 9. Extensibility — three interfaces, no rewrites

```mermaid
graph TB
    SEND["Send request"] --> CHAN{"Channel"}
    CHAN --> SMS["sms"]
    CHAN --> RCS["rcs"]
    CHAN -.-> WA["whatsapp (later)"]
    CHAN -.-> EM["email (later)"]

    RCS -->|"capability miss"| FB["RCS → SMS fallback<br/><i>a decorator, not an if</i>"] --> SMS

    SMS --> REG{"Regime<br/>(by destination country)"}
    REG --> IN["india_dlt"]
    REG --> US["us_10dlc"]
    REG -.-> UK["uk_mma"]
    REG -.-> AE["uae"]

    IN --> CONN{"Connector<br/>(by route policy)"}
    US --> CONN
    CONN --> SMPP["smpp bind"]
    CONN --> HTTP["http aggregator"]
    CONN --> SBX["sandbox"]

    style FB fill:#1d3557,color:#fff
    style SBX fill:#2d6a4f,color:#fff
```

```go
type Channel   interface { Validate(Msg) error; Segments(Msg) int; Encode(Msg) (Payload, error) }
type Regime    interface { Country() string; ValidateSender(Sender) error
                           ValidateTemplate(Template) error; ValidateSend(Msg) error }
type Connector interface { Name() string; Submit(ctx, []Msg) ([]Ref, error); Health() Status }
```

New country = one `Regime` file. New carrier = one `Connector` file. New channel = one `Channel`
file. Nothing else changes. That's the "adaptability law" from the PRD, made concrete.

---

## 10. Local development topology (what we build first)

```mermaid
graph LR
    subgraph MAC["Your Mac — native, no Docker"]
        NEXT["Next.js :3000<br/>NEXT_PUBLIC_USE_MOCKS=false<br/>API_BASE=http://localhost:8080"]
        CAPI["control-api :8080"]
        PAPI["public-api :8081"]
        WRK["worker"]
        PG[("Postgres 16 :5432<br/>brew services")]
        RD[("Redis 8 :6379<br/>brew services")]
        CH[("ClickHouse :8123<br/>brew")]
    end

    NEXT --> CAPI
    CAPI --> PG & RD & CH
    PAPI --> PG & RD & CH
    WRK --> PG & RD & CH
    WRK --> SBX["sandbox connector<br/>simulates carrier + DLRs"]
    SBX --> PAPI

    style NEXT fill:#1d3557,color:#fff
    style SBX fill:#2d6a4f,color:#fff
```

The **sandbox connector** is what makes end-to-end testing possible with no carrier: it accepts
submissions, then feeds realistic DLRs back into the ingest endpoint on a configurable delay and
success rate. Every stage gets tested against the real UI, locally, before anything touches
Hostinger.

---

## 11. Production topology (Hostinger, later)

```mermaid
graph TB
    NET["Internet"] --> CADDY["Caddy<br/>TLS · rate limit · routing"]
    CADDY --> CAPI["control-api<br/>(N replicas)"]
    CADDY --> PAPI["public-api<br/>(N replicas)"]

    CAPI & PAPI --> PG[("Postgres 16")]
    CAPI & PAPI --> RD[("Redis")]
    CAPI & PAPI --> CH[("ClickHouse")]

    WRK["workers<br/>(N replicas)"] --> PG & RD & CH
    WRK --> CARR["Carriers"]

    PG -.->|"nightly dump"| BAK["<b>OFF-BOX BACKUP</b><br/>required"]
    CH -.->|"nightly"| BAK

    PROM["Prometheus"] --> GRAF["Grafana"]
    CAPI & PAPI & WRK -.->|/metrics| PROM

    style BAK fill:#6a040f,color:#fff
    style CADDY fill:#1d3557,color:#fff
```

Every green box is **stateless and horizontally scalable** — adding capacity is running another
copy. All coordination lives in Postgres and Redis. That property, established on day one, is
what makes the scaling ladder in `RESEARCH_AND_SCALE.md` §3 a config change rather than a
rewrite.

The red box is not optional.

---

## 12. Request lifecycle — a dashboard read, end to end

```mermaid
sequenceDiagram
    autonumber
    participant B as Browser
    participant N as Next.js server
    participant G as control-api (Go)
    participant R as Redis
    participant P as Postgres
    participant C as ClickHouse

    B->>N: GET /messages (page navigation)
    N->>N: read httpOnly session cookie
    N->>G: GET /v1/messages?cursor=… <br/>Authorization: Bearer …
    G->>G: verify JWT → tenant_id, scopes
    G->>R: rate limit check
    G->>P: BEGIN; SET LOCAL app.tenant_id
    G->>C: SELECT … WHERE tenant_id = … <br/>keyset paginated
    C-->>G: rows + next cursor
    G-->>N: 200 MessagePage {items, nextCursor}
    N-->>B: server-rendered HTML

    Note over G,C: singleflight coalescing —<br/>concurrent identical queries<br/>hit ClickHouse once
```

---

## Diagram index

| # | Diagram | Answers |
|---|---|---|
| 1 | System context | Who talks to what |
| 2 | Two-database split | Why this scales to billions |
| 3 | Send pipeline | How a message becomes money |
| 4 | State machine | What "delivered" actually means |
| 5 | Campaign fan-out | How 5M recipients don't melt the queue |
| 6 | DLR ingest | Where these systems usually die |
| 7 | Tenant isolation | Why a forgotten WHERE can't leak |
| 8 | Ledger | Why we never charge for undelivered |
| 9 | Extensibility | How new countries/carriers/channels slot in |
| 10 | Local topology | What we're building this week |
| 11 | Production topology | Where it goes on Hostinger |
| 12 | Request lifecycle | The full round trip |
