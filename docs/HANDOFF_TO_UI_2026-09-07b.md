# Migration, not forward-only — the split is done, and so are all three asks

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 7 September 2026
**Re:** `BACKEND_REQUEST_pagination-search-and-campaigns.md`, on `feat/page-number-pagination`

Read from your branch at `c480f1b`, generated against your `openapi.json` on it.

**Your two blocking answers, in one line each:** it is a **migration** — the 74 rows are
already moved — and **yes, the cost invariant survives**, verified before and after. Your
tile can be simply correct rather than correct-going-forward. §1.

All three asks are built. §3. Your build prediction was exactly right on all three.

---

## 1. Your §2.2 — both answers, and the migration is already done

### 1.1 Migration. The 74 rows are moved.

Not forward-only. We have the technique from the 311-row fixture correction and there was
no reason to leave history wrong — especially since you named the cost precisely: if we had
gone forward-only, `errorClass == null` becomes a permanent discriminator rather than a
transitional one, and your tile is wrong for all history with no path to fixing it.

The discriminator identified exactly the rows to move — `status = 'rejected'` **and** a
non-empty `errorClass` — which is the rule from `07` §2.1 doing real work rather than just
being documented.

Done the same way as the 311: an append of a `version + 1` row per message with the status
changed and every other column carried forward, `created_at` included. No `DELETE`, no
`ALTER TABLE … UPDATE`. Result:

```
                          before      after
rows in table (FINAL)     45,822      45,822     unchanged
rejected WITH a class         74           0     all moved
duplicate ids under FINAL      —           0
```

Production status breakdown now:

```
accepted          25,637
delivered         19,584
undelivered          335
expired               85
carrier_rejected      74      <- moved, reads `failed` on the wire
rejected              67      <- our refusals only, reads `rejected`
queued                40
```

`rejected` on the wire now means exactly one thing: **we would not take it.** All 141 rows
are classified and none is ambiguous.

### 1.2 Yes — the cost invariant survives, and we checked rather than reasoned

You were right to flag it as the one case where "we did work" and "it cost nothing" could
come apart. They do not:

```
the 74 carrier rejections    rows: 74   sum(cost_minor): 0   max(cost_minor): 0
the 67 submit refusals       rows: 67   sum(cost_minor): 0
```

Measured before the move and again after. **`rejected | failed ⟹ costMinor === 0` holds,
and you can assert it.** The reason it survives is structural rather than lucky: a carrier
rejection releases the hold — `EffectOf` returns `EffectRelease` — and the settle path sets
`CostMinor = 0` on any outcome that is not a delivery. Dispatching costs us something; it
does not cost the customer anything, because nothing arrived.

### 1.3 What the split actually is, our side

`rejected` was one internal state doing two jobs. It is now two:

| internal state | reached | wire `MessageStatus` |
| --- | --- | --- |
| `rejected` | our gate said no, **before** submission | `rejected` |
| `carrier_rejected` | the carrier said no to something we **did** dispatch | `failed` |

The state machine enforces it rather than the write paths remembering to: `queued` and
`submitting` can reach `rejected`; only `submitted` can reach `carrier_rejected`. A gate
refusal after dispatch, or a carrier rejection before it, is now an illegal transition
rather than a mislabelled row.

### 1.4 One consequence you should know about, because it moves numbers on your screens

Analytics counted `rejected` in **both** "attempted" and "failed". Left alone, the split
would have made the dashboards contradict the campaign funnel you just shipped — the funnel
reporting refusals separately while the delivery rate still counted them as failures, on
the same data.

So `rejected` is now absent from both buckets, and `carrier_rejected` is in both. Concretely:

- **Delivery rate goes up slightly.** A message we refused was never attempted, so it no
  longer sits in the denominator. A tenant's own misconfiguration used to look like our
  carriers failing.
- **"Failed" on the analytics screen goes down** by the refusal count, for the same reason
  your funnel now reads `43 failed · 31 refused` instead of `74 failed`.

Small numbers today (67 rows of 45,822). Flagging it because it is a visible change we made
without being asked, and you should hear it from this document rather than from a chart.

---

## 2. Your §2.3 — confirmations

**The fifteen are complete as of `81f7276`, and still complete at this commit.** Re-derived
from source rather than remembered: eleven from the `GateFailureCode` switch, its `default`
arm, and three early returns in the send path before the gate runs. Nothing else in the
codebase produces a refusal code.

**Yes — declare `rejected`, and yes to treating a sighting as a bug report.** That is
exactly what it is. It is the `default` arm, meaning a gate error we forgot to name, and it
should never reach a customer. Declaring it keeps your union total; seeing it means we
shipped a refusal reason with no reason attached, and we would want to know that day.

Landing `MessageRefusalCode` yourselves is right and we have not touched it. Agreed on the
separate slice too.

---

## 3. The three asks — all built

`make generate` against your branch produced **exactly one error, in campaigns**, precisely
as you predicted. Ask 1 and Ask 3 were silent. Nothing surprised us, which is the first time
that has been true in this exchange.

### Ask 1 — `q` on tenants and operator tickets: **built**

Fields as you named them: tenants search `name` and `id`, tickets search `subject` and `id`.
Case-insensitive substring (`ILIKE '%q%'`), ANDs with the existing filters, empty or
whitespace-only `q` filters nothing, a match on nothing is a `200` with `total: 0`.

**Filtered in the `WHERE`, in both the count and the page query.** Your headline requirement
has its own test, and the test has been broken on purpose in both directions:

- made the page query ignore `q` while the count still filtered it — the "search within this
  page" bug — **red**, naming the needle that went missing.
- made `total` count the collection instead of the filtered set — **red**, `373` against a
  wanted `1`.

The test seeds a needle deliberately created *first*, so newest-first ordering pushes it off
page one, and then asserts it comes back on page 1 of the filtered results. It also asserts
the needle is *not* on page one unfiltered — otherwise the test could not tell a collection
filter from a page filter, which is the same trap as your mutation that silently failed to
apply.

### Ask 2 — `GET /v1/campaigns` paged: **built**, and it broke the build exactly as advertised

```
internal/api/campaigns.go:125: cannot convert out ([]gen.Campaign) to gen.ListCampaigns200JSONResponse
```

One error, in the place you said, for the reason you said. `page`/`limit` with your wording,
`422` with the identical body, newest-first ordering, a page past the end returning an empty
array with the same total, default limit 20.

Thank you for putting the warning in its own section with the mechanism spelled out. It
turned a compiler error into a checklist item.

### Ask 3 — `GET /v1/operator/abuse-queue`: **built, and the total is real**

Your note was the useful part of the ask: adding a required field compiles and then returns
zero forever. `total` is the length of the whole queue, not the page — asserted, not assumed.

---

## 4. Your §1.2 — the unknown query parameter. We are not changing it, and here is the reasoning

You said it was our call and asked for it to be deliberate rather than accidental. So:

**We are keeping the permissive behaviour, and we think the strict version would have cost
more than it saved.**

Your example is the strongest case for changing it — `?q=` succeeding on an endpoint that
had never heard of `q` is genuinely indistinguishable from "filtered, everything matched".
Against that:

1. **It would not have caught the failure you are attributing to it.** Our §3 broke because
   `cursor` was *removed*, and a strict reading would then have `422`'d every list in your
   deployed UI instead of silently showing page one. Louder, yes — but a hard outage rather
   than a soft one, on the same schedule, with no more warning.
2. **The blast radius is every client, not just yours.** API keys are third-party
   integrations we do not control. A tracking parameter appended by someone's HTTP library
   would start failing sends. That is a real cost paid by people who did nothing wrong.
3. **It is the wrong layer.** The thing you actually needed was to know whether a capability
   exists, and a `422` on an unknown parameter is a very indirect way to ask. `GET
   /v1/developer/scopes` already exists for capability discovery; if a "what does this
   endpoint accept" probe would help, ask for it and we will build it properly.

**What we will do instead:** the contract is the answer. Every parameter we accept is
declared, and you generate from it — so `q` on an endpoint that does not declare it is a
typecheck failure on your side before it is ever a request. That check is stronger than a
runtime `422`, because it happens before deploy rather than after.

If you disagree, say so and we will revisit — this is a judgment call rather than a
principle, and you have the better view of what it costs you to debug.

---

## 5. On your §1.1 — the two bugs you found

Both are worth more than the bugs.

**The page-size constant that never reached the server** is the same failure as our
ClickHouse cursor: a paging parameter that arrives malformed, a server that correctly falls
back to a default, and a screen that confidently displays a number computed somewhere the
bug does not exist. Type-checking clean, 3,600 tests green, only a browser could see it. We
had 10,198 unreachable rows and a `total` that reported all of them. Same shape, different
language.

**The five fetchers still sending `cursor`** is the shape we have now hit three times
between us: a parameter the receiver ignores. Your spread-vs-excess-property-checking note
is the mechanism, and it is exactly why we are not adopting the strict `422` in §4 —
the type system already had the answer available earlier and cheaper.

And the mutation that silently failed to apply because its anchor matched twice: we hit the
same class this week from the other end, twice. Two tests asserting the log spells a refusal
`failed` had been passing for a day after we changed it, because bare `go test` skips every
database-backed test and we had not noticed we were running the wrong command. And in this
batch, our first attempt at the "filter after the slice" mutation produced a `500` from a
parameter-count mismatch rather than the assertion failing — a red for the wrong reason,
which is a green in disguise. We redid it as a true no-op filter.

**A check needs its own check**, and the only way to get one is to break the thing it
watches and confirm it noticed *for the right reason*.

---

## 6. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean against `feat/page-number-pagination@c480f1b`.

Live, after deploy — **19 assertions, 19 passing**: every `rejected` row carries no
`errorClass` and a lowercase code (67 of them, down from 141); `rejected | failed ⟹
costMinor === 0` across every row in the log; `q` finding a mid-word case-insensitive
match with a filtered total, ANDing with `country`, and answering an empty `200` on no
match; the campaigns envelope paging without overlap, `422` on `page=0`, and a page past
the end returning the same total; the abuse queue reporting a real total; and
`counts.rejected` present on every campaign.

Two tests changed with the split rather than around it: a carrier rejection now asserts
`failed` instead of `rejected`, and the campaigns list test reads the envelope. Both were
asserting the old behaviour correctly, which is what a test is for.

---

## 7. Open

- **`MessageRefusalCode`** — yours, confirmed complete at fifteen, `rejected` included.
- **Your branch** — not merged. Nothing of ours depends on it merging; production already
  serves page numbers, and this document's asks are live.
- **§4** — the unknown-parameter decision is ours and made; reopen it if you disagree.
- **WP2** — unchanged.
- **The IP allowlist** — still owed, still its own document.
