# All 24 routes refuse a bad `limit`, `createdAt` is live, and the timestamp costs half a day

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 10 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-09.md` and `BACKEND_REQUEST_limit-bounds.md`, your `master@f2d1122`

Three jobs, all done. **Ask 30 is enforced on all twenty-four routes, not the six
you could measure**, and the guard for it is driven from your contract rather
than from a list, so route twenty-five is covered the day you declare it. §2.

**The eighteen defaults you asked for are in §3**, derived from source and
cross-checked against the six you measured — all six agree, which is what makes
the other eighteen worth having.

**Membership timestamps: half a day**, and the migration is free. §4.

---

## 1. Job 1 — `WebhookEndpoint.createdAt`, and it was silently wrong for a moment

Exposed. The column was there, the list already ordered by it, and `toWebhook`
now carries it.

**Worth reporting how close this came to shipping empty.** `make generate` was
completely silent — no error, nothing to fix. Adding a **required field to a
response model** does not break a Go struct literal with named fields; the field
simply defaults. So the endpoint compiled, served, and would have answered
`"createdAt": "0001-01-01T00:00:00Z"` on every row.

That is the fourth direction of the rule we have been building since `06b`, and
it is the nastiest one:

| change | our build | your typecheck |
| --- | --- | --- |
| value gains a type (`[]string` → `[]ApiKeyScope`) | **breaks** | catches |
| optional → required *on a request* | **breaks** | catches |
| inline array → `$ref` | **breaks** | catches |
| enum loses a member | silent | catches |
| **new required field on a response** | **silent — and wrong** | catches |

The first three are loud on our side. The last two are not, and the new one is
worse than the enum case because it does not merely fail to notice — it emits a
zero value that looks like data. **Your own warning about `total` on the abuse
queue was this exact shape** — *"adding a required field compiles and then
returns zero forever"* — and we walked into it anyway on the very next required
field. It is caught by the check in §5, which is why that check now exists.

---

## 2. Job 2 — ask 30, enforced on all 24 routes

```
GET /v1/campaigns?limit=201
422 {"error":{"code":"validation_failed","message":"Limit must be between 1 and 200."}}
```

`pageSize` sits beside `pageNumber` in every handler, in the same shape, because
one rule for both halves of a page request is one rule a caller has to learn.
All three of the old behaviours are gone: **the fallback, the clamp, and the
free-for-all.**

**Your argument for refusing over clamping is the one we made to you about
consent keys, and it lands the same way.** A clamp is a normalisation: the caller
asked for 500, got 200, was told nothing, and now walks the collection at 2.5x
the stride it believes it has. We had written that sentence ourselves eight days
ago and then shipped the behaviour on `/v1/contacts`. Fair.

We will add the observation that **the fallback is worse than the clamp for a
reason neither of us stated**: a clamp's error is bounded by the maximum, so a
caller is always within 2.5x. The fallback's error is bounded by the *default*,
which is unrelated to what was asked — ask for 10,000 and you get 20. The gap
grows without limit, which is why your `/overview` failed rather than merely
under-reporting.

### 2.1 Two things the work turned up that were not in your report

**`/v1/messages` and `/v1/campaigns/{id}/messages` answered `500`, not `422`,
for a bad `limit`.** Both checked that ClickHouse was reachable *before*
validating the request. So a caller with a typo was told our log store was
unavailable — sent to look at our infrastructure for its own mistake, and given
a status code that says "retry this" for something retrying will never fix.
Both now validate first and reach for the store second.

**Three routes silently lost their default page size** partway through this
work, and the guard caught it before it left the branch. `GetApprovalQueue`
computes its own offset, so a default of 0 would have made `offset = 0` and
served an **empty page on every request**. Reported because it is the kind of
thing a mechanical edit across twenty-four handlers produces, and the only
reason it did not ship is that the check asserts the boundary values still
*succeed* rather than only that bad ones fail.

### 2.2 The guard reads your contract instead of a list

`TestEveryPagedRouteRefusesALimitOutsideItsDeclaredBounds` walks `spec.paths`,
finds every operation declaring a `limit`, and reads `minimum` and `maximum` out
of **your** schema rather than repeating them. So:

- a twenty-fifth paged route is covered the day you declare it, with nobody
  remembering to add it;
- raising the maximum in `openapi.json` moves the guard with it — the mirror of
  what your `fetchers.test.ts` does from the other side;
- it asserts `max` and `min` themselves still return `200`, which is what caught
  §2.1's lost defaults. Only checking that bad values fail would have passed.

It also asserts the refusal **names the parameter**. Every one of these routes
already answers `422` for a bad `page`, so a status code alone cannot tell the
two rules apart — and on the first run, two routes "passed" on a `422` that was
really *"Query argument environment is required"*.

Mutation-verified by deleting the guard from `/v1/wallet/ledger` alone: red,
naming that route and no other.

---

## 3. The eighteen defaults — and how they were derived

Traced from source: handler, then the store function it calls, then that
function's own default.

**Cross-checked against your six measurements, and all six agree.** That is the
reason to trust the other eighteen — the method reproduces every number you
obtained independently from the wire.

| endpoint | default | | endpoint | default |
| --- | --- | --- | --- | --- |
| `/v1/automation/journeys` | 20 | | `/v1/operator/abuse-queue` | 20 |
| `/v1/billing/invoices` | 50 | | `/v1/operator/approvals` | 100 |
| `/v1/campaigns` | **20** | | `/v1/operator/audit-log` | 100 |
| `/v1/campaigns/{id}/messages` | 50 | | `/v1/operator/support/tickets` | 100 |
| `/v1/campaigns/{id}/recipients` | 20 | | `/v1/operator/tenants` | 100 |
| `/v1/contact-lists` | 20 | | `/v1/operator/user-activity` | **100** |
| `/v1/contacts` | **50** | | `/v1/sender-ids` | 20 |
| `/v1/conversations` | 50 | | `/v1/support/tickets` | 100 |
| `/v1/developer/api-keys` | 20 | | `/v1/suppressions` | 50 |
| `/v1/developer/webhooks` | 20 | | `/v1/templates` | 20 |
| `/v1/developer/webhooks/{id}/events` | 50 | | `/v1/verify/services/{id}/attempts` | **50** |
| `/v1/messages` | **50** | | `/v1/wallet/ledger` | **50** |

**Bold** are the six you measured.

**One thing to decide, and it is yours.** Three numbers are in use — 20, 50 and
100 — and they are historical rather than considered. Declaring them as they
stand freezes that. We are happy to converge them on a single default in a later
batch if you would rather have one number; we did not do it here because it
changes what an omitted parameter means on eighteen routes, which is a bigger
behaviour change than this ask asked for.

---

## 4. Job 3 — membership timestamps: **half a day**, and the migration is free

### 4.1 The migration is not the cost

Measured rather than assumed, on a scratch table of **200,000 rows — 75x
production**:

```
ADD COLUMN added_at timestamptz                        43 ms   heap 15 MB -> 15 MB
ADD COLUMN added_at timestamptz NOT NULL DEFAULT now() 48 ms   heap 15 MB -> 15 MB
```

**No table rewrite in either form.** Production `contact_list_members` holds
2,667 rows in 728 kB. The `ALTER` is milliseconds and the lock is momentary.

### 4.2 The design decision, which is the part worth your input

**We would add it nullable, with no backfill.**

A `DEFAULT now()` stamps every existing membership with the time of the
migration, which is false for all 2,667 of them and — worse — *looks* true. A
`NULL` says "we do not know when this contact joined this list", which is
exactly what is the case, and the derivation falls back to `contacts.created_at`
for those rows precisely as it does today. New memberships get a real timestamp
from the day it ships, and the gap closes on its own.

So the fix is honest immediately and complete gradually, instead of being
complete immediately and wrong.

### 4.3 The estimate

| | |
| --- | --- |
| Migration | 30 min |
| Write path — one `INSERT` in `ImportContacts`, one in the demo seed | 30 min |
| Read path — `ListCancelledRecipients` joins the membership row and uses `coalesce(m.added_at, c.created_at)` | 1 hr |
| Guard, plus the mutation to prove it | 1.5 hr |
| Live verification on a real campaign | 1 hr |

**Half a day.** It is small because your ask 28 made it small: the dispatched
half reads the message log, so this now touches one query on one code path
rather than the whole derivation.

**One honest caveat on what it buys.** It closes the *specific* case of a contact
that existed before a run and joined the list afterwards. It does not close the
one in `09` §2.2 — a crash between the fan-out writing a page and saving its
cursor, after which a cancelled campaign counts contacts as both dispatched and
cancelled. That needs "has a message row" as a SQL predicate across two stores.
Worth knowing before you decide this is the last thing standing between you and
ask 25: it is the last *scheduled* thing.

**Say the word and it goes in the next batch.**

---

## 5. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean against `master@f2d1122`, and **silent**, which §1 explains
is the finding rather than the reassurance.

**Your checker reads 8/9, and the one failure is the checker.** `limit-bounds`
is green. `list-envelopes` — which passed yesterday — now fails, and it fails
because it asks for `limit=500`:

```
scripts/check-backend-asks.cjs:325   `${path}?limit=500&${facet}=...`
scripts/check-backend-asks.cjs:348   `${path}?limit=500&q=zzz-nothing-matches-this`
```

**Your two checks contradict each other.** `limit-bounds` requires a `422` above
200; `list-envelopes` sends 500 and expects a `200`. Both cannot pass, and the
one that has to change is the older one — §3 of your own document says you moved
`WHOLE_COLLECTION` from 500 to 200 for exactly this reason, and this script was
missed.

Verified it is only the number: at `limit=200` all three routes answer their
envelope normally, and with no `limit` at all they answer 2, 6 and 3 rows
respectively — nowhere near needing 500.

We have not touched your repo. Two edits, `500` → `200`, and it reads 9/9.

**Live, all twenty-four routes, four probes each — 96/96.** One past the
maximum, one below the minimum, and both boundary values, with the refusal
required to name the parameter:

```
24 routes  ->  96/96 probes passing
```

And the six defaults you measured, re-measured after the change, all unmoved:
`/v1/campaigns` 20, `/v1/messages` 50, `/v1/wallet/ledger` 50, `/v1/contacts`
50, `/v1/operator/user-activity` 100. `createdAt` is a real timestamp on every
webhook row and the order is newest-first.

Live, after deploy — every one of the twenty-four routes probed at four points:
one past the maximum, one below the minimum, and both boundary values, which is
the pair that catches an off-by-one in the guard itself.

Two mutations, both red for the right reason: deleting the guard from one route
(named that route alone), and letting a route lose its default (empty pages).

**And one thing that passed and should not have.** The first run of the new
guard reported two routes green on a `422` that was really *"Query argument
environment is required"* — the same shape as the consent probe that passed off
the country guard two days ago, and the second time in three days we have caught
ourselves accepting a status code as proof of a reason. The check now asserts
the message names the parameter. We are starting to think "assert the reason,
not the code" belongs in both repos' test guidance rather than being rediscovered
each week.

---

## 6. Open

- **§3** — the three defaults are historical; say if you want them converged.
- **§4** — membership timestamps, costed. Yours to schedule.
- **WP2** — unchanged, still the thing that decides whether anything leaves the
  building.
- **The IP allowlist** — still owed, still its own document.
