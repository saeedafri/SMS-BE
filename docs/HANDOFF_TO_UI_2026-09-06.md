# Reply — your four questions, one measurement that undercuts the diff technique, and three defects your §0 uncovered

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 6 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-06.md`

Filed as `HANDOFF_TO_UI` rather than `REPLY_<slug>` because it answers your reply
document rather than a single request — the same call we made for `05b`.

Pulled your `master` at `c23183e` before writing any of this.

**The two things you asked us to commit to are done, deployed and verified on
production, not scheduled.** §4 has the evidence. `MessageStatus.rejected` is in
the log now; `costMinor` and `currency` are on every log row now; **please move
both into `required`.**

---

## 0. `make generate` — **not a no-op, and it was your `ApiKeyScope` that broke it**

Not a criticism: the enum is right and we want it. But a closed set is a *named
Go type*, not `[]string`, so the generated models stop matching our handlers:

```
internal/api/developer.go:28:  cannot use key.Scopes (variable of type []string)
                              as []gen.ApiKeyScope value in struct literal
internal/api/developer.go:90:  cannot use request.Body.Scopes (variable of type
                              []gen.ApiKeyScope) as []string value in argument to knownScopes
internal/api/developer.go:96   … same, to store.CreateAPIKey
internal/api/developer.go:104  … and 134
internal/api/key_scopes.go:33: invalid operation: known.Key == scope
                              (mismatched types gen.ApiKeyScope and string)
internal/api/key_scopes.go:50: cannot use scope.Key (gen.ApiKeyScope) as string
```

Seven errors, all ours to absorb, all absorbed. **Fixed in `084e47a`; `make
generate` now runs clean end to end.** The conversion happens at one boundary —
the column stays a Postgres `text[]`, the wire is the closed set — so a scope can
still be renamed in one place.

Worth saying because it generalises: **"the spec diff is empty" and "`make
generate` is a no-op" are different claims.** A `$ref` swap from an inline
`string` to a named schema is invisible as a *value* diff and is a breaking type
change in every generated client. Your own `pnpm typecheck` would have shown you
the same thing in reverse.

### 0.1 The rest of the diff is exactly what you said it was

We ran your technique back at you — extracted the spec from `control.gen.go`,
diffed it leaf by leaf against your `master`. Beyond the operationId casing and
the `required: false` artifact in §1, **three value differences, all three yours
and all three intended**:

| | ours (embedded) | yours |
| --- | --- | --- |
| `MessageStatus.enum` | 6 values | 7, `rejected` appended |
| `POST /v1/messages` `202` description | "`rejected` or `failed` means refused" | your rewrite |
| `GET /v1/messages/{id}` description | ours | yours, longer |

Plus your four additions (`ApiKeyScope` and its three `$ref`s, `MessageLogEntry`
`costMinor`/`currency`, four descriptions) and one removal (`tags: ['Messages']`).
**Nothing we declared has gone missing.** 142 paths both sides; 226 schemas ours,
227 yours, the difference being `ApiKeyScope` itself.

---

## 1. Your §0.1 — you asked, and the answer is worse than "identical"

> *If your source `openapi/control.json` differs from what you embed, that is
> worth knowing — it would mean the embedded spec is not a faithful record of
> what you generate from, which would undercut the diff technique this whole
> reply rests on.*

**It is not a faithful record. Both of us were right, about different documents.**

Three specs, one field:

| | `requestBody.required` on `POST /v1/templates/{id}/carrier-registration` |
| --- | --- |
| your `master` | *undefined* |
| our source at the time we filed item 9 (the PR #2 branch) | **`false`** |
| what we embed | *undefined* |

So our source really did carry it, your master really never did, and the embedded
spec really is byte-identical to yours — because **the embedding erases it.**

Proven rather than reasoned. We took your `master`, set that one field to `false`,
regenerated, and pulled the spec back out of the new `control.gen.go`:

```
fed   requestBody.required = false
round-trip                 = <undefined — ERASED>
```

The mechanism is `kin-openapi`, which oapi-codegen round-trips the document
through: `Required bool` carries `omitempty`, so every explicit `false` vanishes,
and operationIds are rewritten to their Go names. Measured across your whole
document:

- **78 parameters** carry an explicit `required: false` in your source. **0**
  survive into the embedded spec.
- **Every** operationId is rewritten (`getAlerts` → `GetAlerts`), which is the
  179 "differences" our leaf diff reported before we filtered them.

**What this means for the technique:** diffing against our embedded spec is sound
for *presence* — paths, schemas, enum members, response codes, `$ref` targets, the
things you actually checked. It is blind to any field whose JSON zero value is
also its default: `required: false`, `nullable: false`, `deprecated: false`,
`explode: false`. Treat a clean diff as "nothing is missing", never as "nothing
differs".

### 1.1 And item 9 was our error twice over, so please close it

You were right not to make the change, and for a better reason than diff noise.
**OpenAPI 3 defaults `requestBody.required` to `false`**, so your *undefined* is
already the declaration we wanted — and it matches the handler, where a body-less
POST is the whole point of the route (register through the carrier's API; the body
exists only to attach a portal code instead).

We found this because our own generated API reference had started printing
**"Request body (required)"** for that route — our generator was defaulting the
missing key to `true`, the same mistake in the opposite direction. Fixed in this
commit. **Item 9 is closed with no change on either side.**

---

## 2. Your §0.2 — **your declaration is correct and ours was the bug**

Verified on the deployed API rather than read off the handler:

| Request | Response |
| --- | --- |
| `GET /v1/messages/{id}` with no token | **401** |
| with a garbage token | **401** |
| with a session token | **200** |
| with another tenant's id | **404** (not 403 — we do not confirm the id exists) |

`security: [{ BearerAuth: [] }]` is the truthful declaration. Our omission, against
a spec with no global `security` default, said the route was public, which it has
never been. Taken as-is, along with `getMessage` and dropping `tags`.

**One correction, since you raised consistency:** `getMessage` was not the only
PascalCase operationId. Two remain in your `master` —

```
CheckRcsCapabilities          POST /v1/rcs/capabilities
RegisterTemplateWithCarrier   POST /v1/templates/{id}/carrier-registration
```

Both arrived from our request documents, so they are our casing that you adopted.
Rename them if you want the rule to hold; we generate Go names either way and
nothing of ours depends on it.

---

## 3. Your four questions

### 3.1 Should `CampaignCounts` gain a `rejected` bucket? **Yes, please add it — and today we do exactly what you do.**

You asked what our counts report when a fan-out is refused at the template gate.
**They report `failed`.** A refused recipient gets a real ClickHouse row with
`status = 'rejected'`, and `CountCampaignMessages` folds `rejected` in with
`undelivered` and `expired`:

```go
case "undelivered", "rejected", "expired":
    counts.Failed += int(total)
```

So both halves already roll it into `failed` and the funnel sums. No divergence to
fix — but the same argument you made for `MessageStatus` applies here and we think
it is stronger, not weaker. On the log a customer at least has a per-message status
to fall back on. On the campaign funnel there is nothing else: a campaign refused
wholesale for an unregistered DLT template reads as **"12,000 failed"**, which
sends someone to look at carrier deliverability for a problem they could fix in
five minutes in their own template settings.

**One implementation note, because it will bite silently.** `counts.cancelled` is
derived, not stored — `recipients − (queued + sent + delivered + failed + read)`.
Add a seventh bucket without adding it to that subtraction and every refused
recipient is counted twice, once as rejected and once as cancelled. We will handle
it our side; flagging it because the same trap exists in any funnel you compute
from the parts.

Declare it and we will populate it in the same day. It is one `case` and one
addend.

### 3.2 Should `failed` come out of `SendMessageResult.status`? **Yes. It is unreachable, and we checked exhaustively rather than agreeing with you.**

`SendMessageResult.status` is built at fourteen sites across `service.go` and
`coalesce.go`. Twelve are early refusals, every one a literal `Status: "rejected"`.
The other two map an internal state, and only three states are reachable there —
`accepted`, `rejected`, and (in the coalescer's queued record) `queued`, which
never reaches a caller because the answer is written in `settleMixedBatch` after
submission:

| site | states reachable | emitted |
| --- | --- | --- |
| `service.go:319` | `accepted`, `rejected` | `sent`, `rejected` |
| `coalesce.go:637` / `:692` | `accepted`, `rejected` | `sent`, `rejected` |

`failed` requires `undelivered` or `expired`, and neither is reachable inside a
request — both arrive from a delivery report or the reconciler, long after the
response has been written.

**So the observed set is `{sent, rejected}`, and the declared set is four.** Live,
across thirteen sends: eleven `sent`, two `rejected`, nothing else.

`queued` and `failed` are both unreachable today — but they are not the same kind
of unreachable, and that is the whole answer:

- **`failed` is unreachable by construction.** Every terminal outcome we can reach
  synchronously is a refusal, and a refusal is `rejected`. There is no design in
  which it comes back. **Remove it.**
- **`queued` is unreachable by current design.** It is the slot for
  accept-and-enqueue, which we have deliberately not built — it would introduce
  "accepted but not yet charged", and we would rather answer slowly than answer
  before we know. **Keep it**, and expect it to start appearing without further
  notice if that changes.

We are unaffected either way — we emit neither — so treat this as a recommendation
with a clear conscience rather than a request. Until it moves, our generated
reference documents `failed` as compatibility-only, not as a submit outcome.

### 3.3 `MessageStatus.cancelled` on real rows — agreed, no action

Your reasoning is ours: no row is the honest representation of a recipient never
dispatched, and writing 70,000 of them to say so would make cancelling a large
campaign an expensive write at exactly the moment someone is trying to stop it.
`counts.cancelled` stays derived. Closed.

### 3.4 The IP allowlist — accepted, with your two answers taken as decided

`403` for a key used from an unlisted address; empty means unrestricted. Both are
right and the second is the one that matters — an allowlist that locks you out
when you add your first entry is a feature people disable permanently after one
bad afternoon. It will get its own document. Not in this batch.

---

## 4. The two things you asked us to commit to — **both live, both verified**

Deployed at `084e47a`. Every number below is from the production API and the
production ClickHouse, not from a test.

### 4.1 `MessageStatus.rejected` in the log — **now**

`ContractStatus` no longer collapses `rejected` into `failed`. That also retired
`SubmitStatus`, which existed for one reason: to spell `rejected` in the send
result where the log could not. The two vocabularies have converged, and a second
mapping of the same states is how they drift apart again.

The distinction now survives the whole round trip:

```
POST /v1/messages   body that does not match the registered template
  → 202  { "status": "rejected", "costMinor": 0, "id": "…", "errorCode": "…" }

GET /v1/messages/{that id}
  → 200  { "status": "rejected", "costMinor": 0, "currency": "INR", "errorCode": "…" }
```

Both halves say `rejected`. Before today the second said `failed`.

Rendering it as **"Refused"** against a "Rejected" `errorClass` chip is your call
and a good one — the wire value is what we owe you, and it is unchanged.

### 4.2 `costMinor` and `currency` on every `MessageLogEntry` — **now. Please make them `required`.**

Both are on every row. They were never a schema change our side: `cost_minor` and
`currency` have been columns on the `messages` table since migration `001`, and
the log's `SELECT` simply never read them. `GET /v1/messages/{id}` was already
reading them and dropping them on the floor.

Your caution was right in method and wrong in fact — you probed the shape and the
keys were absent, so optional was the only safe declaration. Here is the fact that
makes them safe as `required`:

```
messages FINAL           45,806 rows
  currency = ''               0
  cost_minor < 0              0
  distinct currencies         INR (45,806)
```

**There is no pre-field cohort.** Please drop the "Absent on a message recorded
before the field existed" sentence with the `omitempty` — it describes a
possibility our schema never allowed.

**What the number means**, because "what this message cost" is ambiguous and the
answer is not the price list:

| row status | `costMinor` |
| --- | --- |
| `queued`, `sent` | the amount **held** — reserved against the wallet, not yet spent |
| `delivered` | the amount **charged** |
| `rejected`, `failed` | **0** — the hold was released, nothing was debited |

It follows the money. Summing `costMinor` over a page gives what that traffic
actually cost, which is the reconciliation the logs page could not previously do.
`currency` is present on every row including refusals, so a 0 is still denominated.

### 4.3 One thing that would have made `required` a lie, found while proving it

All four live write paths zero the cost the moment an outcome releases the hold.
**The demo seeder did not.** It priced `rejected` and `undelivered` fixture rows at
full rate — **41 rows on the demo tenant carrying a cost against a message nobody
was charged for**, on the one dataset you probe to learn the shape of a log entry.

Fixed: the seeder now asks `messaging.EffectOf` for the same verdict the server
uses, so a fixture cannot disagree with production about who was charged.

**Those 41 rows are still there.** We do not rewrite live data without being asked,
and re-seeding would replace the demo tenant's whole history. So if you assert
`costMinor === 0` for every `rejected` row against the demo tenant, **it will fail
on 41 rows dated 5 Aug – 2 Sep**, all with the uppercase carrier codes
`SPAM_FILTERED` and `TEMPLATE_PAUSED` that only the seeder produces. The other
**96** `rejected` rows — every one written by a live path — are `0`. Say the word
and we will correct the fixtures.

---

## 5. What your §0 uncovered: the seven cursor-paged `GET`s that still declare no `422`

Your eight landed and all eight answer `422` on a malformed cursor. We went and
measured the other seven, expecting cosmetics. They are not cosmetics.

**The missing declaration did not just under-document those routes. It changed
their behaviour** — oapi-codegen's strict server only generates the response types
the contract declares, so a handler with no `422` type available had to reach for
something else, or for nothing.

Measured against production this afternoon:

| Route | declares | malformed cursor → | |
| --- | --- | --- | --- |
| `GET /v1/messages` | `200` only | **500** | no error type exists at all |
| `GET /v1/campaigns/{id}/messages` | `200`, `404` | **500** | same |
| `GET /v1/contacts` | `200`, `401` | **401** | body says `validation_failed` |
| `GET /v1/wallet/ledger` | `200`, `401` | **401** | same |
| `GET /v1/billing/invoices` | `200`, `401`, `403` | **401** | same |
| `GET /v1/developer/webhooks/{id}/events` | `200`, `403`, `404` | **200** | cursor ignored entirely |
| `GET /v1/verify/services/{id}/attempts` | `200`, `404` | **200** | same |

Three symptoms, one cause:

1. **Two routes 500.** `/v1/messages` is the most-used list in the product, and a
   corrupted cursor in a URL crashes it.
2. **Three routes answer `401` with a validation body.** The status line says
   "your credential is invalid", the body says "that page cursor is not valid".
   A client that handles `401` by re-authenticating will loop forever on a bad
   cursor — which is the exact failure your own eight `422`s were added to stop.
3. **Two routes ignore the cursor and silently return page one.** Worse than
   either, because nothing looks wrong: `WebhookEventPage.nextCursor` is
   `required` in your contract and we have never once emitted it, so nobody can
   page past the first 50 webhook deliveries at all. That one is ours and we are
   fixing it as a defect, not waiting on you.

**The ask: declare `422` — `"Malformed cursor"`, `$ref: Error` — on all seven.**
Same shape as the eight you already did. We will move the three `401`s and the two
`500`s onto it the day it lands; we cannot do it before, because the response type
does not exist until you declare it.

One inconsistency of ours while we are here: `/v1/operator/audit-log` answers
`"Malformed cursor."` where the other seven say `"That page cursor is not valid."`
Cosmetic, ours, on the list.

---

## 6. Verified

`go test -race ./...` — 15 packages, 0 failures.

`make generate` clean against your `master@c23183e`. `make verify-api-reference`
against production: 63/63 documented `GET` operations exist, 38/38 API-key auth
checks behave as the reference claims.

The claims in §2 and §4 were re-run against the deployed API after the deploy
landed — **23 assertions, 23 passing**: `rejected` present in the log, every one of
200 sampled rows carrying `costMinor` and `currency`, a live refusal reading
`rejected` in both the send result and the log, `GET /v1/messages/{id}` answering
401/401/200/404 across the four credential cases, and `ApiKeyScope` still refusing
`messages:write` and `SEND:SMS` at creation.

Both new regression checks were mutation-verified the way you did yours — reverted
against the bug they claim to catch, confirmed failing, restored:

- collapse `rejected` back into `failed` → `TestEveryMessageStateMapsToAContractStatus` fails
- point a route at a scope outside the catalogue → `TestKeyScopesAgreeWithTheContractEnum` fails

The first of those moved packages deliberately. It used to live beside the state
machine and compare against a hand-copied list of your enum — which is precisely
how it went stale: `rejected` was added and the list still named the six values it
had the day it was written. It now runs where `gen.MessageStatus.Valid()` is
available, so it cannot drift from the contract again. Same idea as your
`ApiScope.key` test, arrived at the same way — by being wrong first.

---

## 7. Still open, on us

- **WP2** — unchanged, and still the thing that decides whether anything leaves
  the building. Eight binds, we terminate ourselves, no contract change to start.
  Waiting on credentials; the configuration reads entirely from the environment,
  so there is nothing to merge when they arrive.
- **The IP allowlist** (§3.4) — its own document.
- **`WebhookEventPage.nextCursor`** (§5) — ours, defect, not blocked on you.
- **Deploys are not zero-downtime** — still a ~1s window where the binary is
  swapped and requests get a 502. Unchanged from `05b`; mentioning it because you
  will see it if you poll during a deploy.
