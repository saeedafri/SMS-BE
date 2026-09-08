# Zero production rows — and ask 28 needed a bigger fix than the one you proposed

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 9 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-08.md` and `BACKEND_REQUEST_last-three-list-envelopes.md`, your `master@07b6b26`

**Your open question first, because it is the one that reprioritises everything
else: zero.** No production contact carries a consent key outside `ChannelId`.
§1.

All five items are built. **One push-back, in §2**, and it is the useful kind:
your proposed shape for ask 28 fixes the defect you reported and introduces a
different one, for a reason neither of us could see from a document.

---

## 1. Ask 27 — the count is **zero**, and the boundary is closed

We ran it as the migration role, because RLS makes the application role answer
an audit query with an empty result rather than an error — a distinction that
cost us a wrong answer to you on 5 September, so it is now the first thing we
check when a count comes back zero and zero is convenient.

Every contact on the deployment, grouped by consent key:

```
key         contacts  tenants  in the ChannelId enum
sms             2560        1  NO
RCS              104        2  yes
SMS              104        2  yes
WHATSAPP         104        2  yes
EMAIL            101        2  yes
VOICE            101        2  yes
```

And per tenant, counting contacts whose consent map has **no** enum key at all:

```
tenant                contacts   unreachable-by-construction
Acme Retail               2564                          2560
Concentrix services        100                             0
```

**All 2,560 are on the demo tenant, and they are ours.** Created between
05:54:31 and 05:55:43 on 8 September — seventy-two seconds, which is the
signature of a bulk fixture and not of customer behaviour. They are the 2,500
from our own §7.2 plus the negative-consent seeds. The one real tenant carries
100 contacts and not one bad key.

**So: a boundary fix, no backfill.** Which is the answer that makes your framing
right rather than lucky — you asked for the count precisely because it decides
between a five-line guard and a data migration, and it does.

### 1.1 What the guard is

`POST /v1/contacts/import` now answers `422` naming the offending key:

```
"sms" is not a channel. Consent keys must be one of EMAIL, RCS, SMS, VOICE, WHATSAPP.
```

**Refused, not normalised**, and your own sentence is why: normalising repairs
the one spelling this client happened to use and stays silent on the next, and
the client still believes it recorded something we had actually rewritten.

Three details worth having:

- **A mixed body is refused whole.** `{"SMS": "opted_in", "RCS": "opted_in",
  "sms": "opted_in"}` is a `422`, not a partial success. Accepting four of five
  keys would tell the caller nothing went wrong while a fifth of what it sent
  was unusable — the same silence one layer down.
- **The check is `gen.ChannelId.Valid()`, which is generated from your
  contract.** The day you declare a sixth channel it accepts it without anyone
  editing this code. A hand-written list would have been a second copy of your
  enum that drifts.
- **Keys are sorted before validation.** Go randomises map iteration, so a body
  with two bad keys would otherwise name a different one per request, and an
  error message that moves between identical requests is one you cannot write a
  test against.

### 1.2 The check we wrote first was green for the wrong reason

Worth reporting because it is the failure mode we keep telling each other about.

The first version of the guard's test asserted `422` and passed immediately —
against the **country** guard, which runs earlier in the same handler and which
our fixture was tripping by sending `country` where the contract says
`defaultCountry`. A test that asserts a status code alone cannot tell which
refusal it got. It now asserts the message names the key, and it is red under a
mutation that accepts-and-stores:

```
= 200, want 422 (body {"created":1,...})
```

That `created: 1` is the whole defect in one line: a cheerful success that
records a contact nobody can ever reach.

---

## 2. Ask 28 — built, and your shape needed one correction

**Your diagnosis is exactly right**, including the sentence of ours it
contradicts and the reason the guard is anchored correctly while the predicate
is not. Nothing to add to the analysis.

**The correction is to the fix.** You proposed dropping the audience predicate
from the `dispatched` half and keeping it for `cancelled`. Dropping it is right;
what is left behind is not.

**Because `total` would then describe a different set from the rows.** The
recipients query pages the *audience list*, and the message log only decides
each row's label. Drop the consent predicate and the candidate set becomes every
list member, while the rows returned are still only those with a message row.
For a campaign that ran **after** `15e1917` on a mixed list — the ordinary case
from now on — that is `total: 4` beside two rows.

That is the defect you have objected to three times in this exchange, and you
would have been right to object a fourth.

### 2.1 What we built instead

**`dispatched` is read from the message log, not derived from the list at all.**

```
dispatched  ->  SELECT ... FROM messages FINAL WHERE tenant_id = ? AND campaign_id = ?
cancelled   ->  the audience derivation, with the full rule, unchanged
```

Your own sentence taken literally: *the message row IS the record it was
reached.* Once you say that, the cursor stops being the authority for the
dispatched half and the list stops being its source. `total` is the count of
that campaign's message rows — exact, not approximate — and every row carries a
real `messageId` because the id came from the same read.

**It is smaller than the version you proposed, not larger.** It deleted
`MessageIDsForRecipients` (the per-page id lookup exists no more — the ids
arrive with the rows), and it removed both of the limits we documented in `08`
§2 from this half:

- the list is mutable → irrelevant; the log is not.
- `contact_list_members` has no timestamp → irrelevant; the log is stamped.

**So §5 shrank while you were reading it.** Membership timestamps now matter
only to the `cancelled` half. We will still cost it, but it is a smaller thing
than when you scheduled it third.

### 2.2 One narrower gap, stated because it is reachable

Fan-out writes a page's message rows and **then** saves the cursor, so a process
that dies between the two leaves rows written and no cursor. If that campaign is
later cancelled, the derivation reads "no cursor" as "nothing was reached" and
counts the whole audience as cancelled — while the log correctly reports those
same contacts as dispatched. **They appear in both halves.**

Separating them needs "has a message row" as a SQL predicate, and the rows are
in ClickHouse while the audience is in Postgres, so it cannot be one query. Left
as a known limit rather than papered over, and it is the third one documented on
that function. The dispatched half stays true either way, which is the half a
compliance question asks about.

### 2.3 The consequence you named, confirmed

A campaign that ran before `15e1917` now reports its real dispatched set,
including contacts it should never have sent to. We agree that is right, for the
reason you gave, and we would add one: those are exactly the rows a regulator
would ask about, and an endpoint that hides them because today's rule would have
prevented them is the wrong tool for that question.

### 2.4 Paging across two blocks

The unfiltered page is the two halves back to back — dispatched first — so
`cancelled + dispatched == total` holds by construction rather than by
arithmetic, and a walk of every page sees each recipient once.

**The guard for this took three attempts and the failures are the point.**

1. The first version used a **completed** campaign, where the cancelled half is
   empty by definition. A mutation that deleted the block arithmetic entirely
   stayed green, because the second block never had rows.
2. Rebuilt on a **cancelled** campaign with a real dispatch cursor, so both
   halves are populated. The mutation stayed green *again*: asking both blocks
   for the same window still partitioned the collection with no duplicate and no
   gap — it just handed back twice as many rows as were asked for.
3. The assertion that catches it is the dullest one in the file: **a page never
   exceeds its limit.** Under the mutation, `page 1 returned 6 rows for limit=3`.

We would not have found that by reading. Your "a check needs its own check" now
has a third instance where the check was wrong in a way only the mutation showed.

---

## 3. Ask 29 — built, and `make generate` produced exactly three

```
internal/api/audience.go:52   cannot convert []gen.ContactList     to ListContactLists200JSONResponse
internal/api/developer.go:47  cannot convert []gen.ApiKey          to ListApiKeys200JSONResponse
internal/api/developer.go:232 cannot convert []gen.WebhookEndpoint to ListWebhookEndpoints200JSONResponse
```

Three, in the three places, for the `$ref` swap. **The rule is now five-for-five
across both directions** and we have stopped treating it as a hypothesis.

All three envelope, page from 1, default 20, `422` on `page < 1`, and filter in
the `WHERE` of both the count and the page query. The two developer lists keep
their required `environment` and it filters before the slice.

### 3.1 One push-back: webhooks are ordered `created_at DESC`, not by insertion

You offered insertion order to avoid inventing a sort key, and said to add
`createdAt` to `WebhookEndpoint` if we would rather it were newest-first.
Neither is needed, and insertion order is the one option that does not work:

**`webhook_endpoints` already has a `created_at` column** — it is simply not
exposed in the contract. So we are not guessing at a sort key, we are using the
real one, and it is what the endpoint already ordered by before this change.

**"Insertion order" in Postgres is not an order.** A bare `SELECT` has no
defined ordering, and a row that is updated moves in the heap. Your mock can
honour insertion order because a JavaScript array has one; our table does not.
Asking for it would have got you an order that is stable in testing and
re-shuffles in production after the first `PATCH` — the worst of the options,
because nothing would fail.

So: `created_at DESC, id DESC`, deterministic, tie-broken. **If you want the
timestamp in the contract we will populate it** — say the word and declare
`createdAt` on `WebhookEndpoint`. It is one field and we already have the value.

### 3.2 Ordering is tie-broken everywhere, and that is not cosmetic

All three order by `created_at DESC, id DESC`. Without the second term, rows
sharing a timestamp — which a seeded or scripted create produces constantly —
can appear on two pages and on neither, and `total` is right the whole time.
Your check's point 3 would pass; the walk in ours would not.

---

## 4. The user-activity export and the `404` — both built

**`GET /v1/operator/user-activity/export`** is the audit export's twin: same
streaming through a pipe, same `text/csv; charset=utf-8`, same dated
`attachment`, same `occurred_at DESC, id DESC` as the paged endpoint. Columns in
your order: `occurredAt, tenantId, tenantName, userName, userEmail, eventType,
detail`.

The assertion we kept is the one that matters — the file's row count against the
paged endpoint's `total` under an identical filter — and it is red under a
mutation that drops the filters: *the export holds 235 rows and the screen
reports 9.*

**`404` on `GET /v1/campaigns/{id}/recipients`.** An unknown campaign is now a
`404` with your wording's meaning. Red under a mutation that restores the empty
page: `unknown campaign = 200, want 404 (body {"recipients":[],"total":0})`.

---

## 5. On your §6, and on the two things you are not asking for

**Your interim client-side export writing different headers** — noted, and it is
the right call to land carefully. Ours is the shape you specified; when you cut
over, the file changes and that is visible to a customer. Not our call to
schedule.

**Routes free-text search — agreed, and the reasoning is worth keeping.** A
corridor is `{country, channel}`, the boundary computation spans a corridor, and
a `q` that cuts across one hands the screen a partial corridor and greys out
legal moves. That is a better argument than "it is only 21 rows", because it
stays true when there are 2,100. If routes ever grow, the boundary computation
moves server-side in the same change — agreed, and we would rather do it once
than add `q` now and inherit the bug.

**`/v1/developer/scopes` and the three wallet lists stay unpaged.** Bounded by
configuration, read whole by screens that need every row. Paging them would add
a `total` nobody reads and a page nobody wants.

---

## 6. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean against `master@07b6b26` after the three envelope handlers.

**Your checker reads 8/8 against the deployed API**, `last-three-envelopes` red
to green. We ran it rather than describing our own.

Every guard in this batch was broken on purpose against the specific defect it
claims to catch, and three of the mutations were themselves wrong first:

| mutation | first result | what it took |
| --- | --- | --- |
| dispatched filtered by today's consent | **red**, `total = 0, want 5` | — |
| import accepts an unknown consent key | **red**, `created: 1` | — |
| `total` counted before the environment filter | *500, wrong reason* | a true no-op filter, not an untyped parameter |
| `total` reports the page length | **red**, `total = 1, want 7` | — |
| export drops its filters | **red**, 235 rows against a screen reporting 9 | — |
| an unknown campaign answers an empty page | **red**, `200, want 404` | — |
| the block arithmetic deleted | *green, twice* | both halves populated, then a page-size assertion |
| the campaigns list back to one read per campaign | **red**, `7 queries, want 1` | — |
| a dropped ClickHouse handle sits out the backoff | **red**, naming the unearned window | — |

The first `total` mutation produced a `500` from a parameter the planner could
not type — a red for the wrong reason, which is a green in disguise, and the
second time we have shipped that particular mistake. And one mutation did not
apply at all, silently, because a shell heredoc ate the backticks in the Go
source it was editing. **Every mutation in this batch now asserts its own anchor
matched exactly once before it runs.**

Live, after deploy — the two campaigns you measured, and the endpoints your
checker does not reach:

```
                                          message rows   dispatched  cancelled  unfiltered
Consent verification NEGATIVE 2026-09-08          2500         2500          0        2500
Consent verification 2026-09-08                      4            4          0           4
```

**Those are your two numbers, inverted.** You measured 4 rows listing 2
recipients and 2,500 rows listing 0. Both halves now sum to the whole, and every
dispatched row carries a real `messageId`.

The consent boundary, against the deployed API:

```
{"sms": "opted_in"}                  422  "sms" is not a channel. Consent keys must be one of ...
{"SMS": ..., "sms": ...}             422  refused whole, not partially accepted
{"TELEGRAM": "opted_in"}             422  named, not silently dropped
{"SMS": ..., "WHATSAPP": ...}        200  created 1
```

The user-activity export: `text/csv; charset=utf-8`, `attachment;
filename="user-activity-2026-09-08.csv"`, your seven columns in your order, and
**782 data rows against a paged `total` of 782**. An unknown campaign is a `404`
with `not_found`. And the envelopes, where the environment totals differ and so
prove the filter ran before the slice:

```
/v1/developer/api-keys?environment=live&limit=1   total 77  rows 1   (267 bytes, was all 77)
/v1/developer/api-keys?environment=test&limit=1   total 67  rows 1
/v1/contact-lists?limit=1                         total  8  rows 1
page past the end -> rows 0, total 77 (no wrap)   page=0 -> 422 on all three
```

---

## 6a. Two things the load test found, neither of them in your document

We benchmarked the endpoints on the box after deploying, because the endpoint we
changed reads a store the others do not. It found two defects that predate this
batch, and both are on screens you ship.

### 6a.1 One failed ClickHouse query took out every log screen for five seconds

Under 128 concurrent readers of `GET /v1/messages`, **908 of 1,024 requests
returned `500`.**

Not load shedding — the rate limiter answers `429` and did not fire. The
sequence:

1. One query fails under contention.
2. The handler drops the shared ClickHouse handle, which is right: a dropped
   handle is how a restarted ClickHouse gets noticed.
3. The pool then applies its **dial backoff** to the drop. That backoff exists
   to stop a connection storm against a server that is *down*; the server was
   up the whole time.
4. Worse, the error it reported inside that window was **"clickhouse is not
   configured"** — because the successful dial before it had cleared the last
   error, so the branch fell through to the not-configured case.

So a transient error became a five-second total outage of every
ClickHouse-backed screen, reported to customers as a deployment fault.

**Fixed:** a drop now clears the backoff, and the backoff only applies after a
dial that actually failed. Guarded both ways — a dropped handle must redial at
once, and a genuinely failed dial must still back off, or the fix trades an
outage for a connection storm.

**Worth your knowing** because it changes what a `500` from those endpoints
means. It was previously possible to see one on a healthy system.

### 6a.2 `GET /v1/campaigns` cost one ClickHouse read per campaign

The counts were correct and the endpoint was still wrong: rendering a page of
twenty asked the message log twenty separate times.

```
concurrent readers      16        64       128
/v1/campaigns        564.7 req/s  6.6     2.2
/v1/developer/api-keys  631.3     618.7   637.7     <- Postgres only, for scale
```

A 250× collapse, invisible at one reader. With a pool of sixteen connections,
128 concurrent readers put 2,560 queries in flight.

**Fixed:** one `GROUP BY campaign_id, status` for the page. The guard counts
ClickHouse's own `SelectQuery` counter rather than timing anything, so it fails
on a laptop for the same reason it failed in production — and it asserts the
counts are still right, because "one query" is otherwise trivially satisfied by
not querying at all. Under a mutation back to the old shape: *rendering 6
campaigns issued 7 message-log queries, want 1.*

**No contract change, no shape change.** Same response, same numbers.

---

## 7. Open

- **§3.1** — declare `createdAt` on `WebhookEndpoint` if you want newest-first
  stated rather than merely true. One field, and we have the value.
- **§2** — membership timestamps: still ours to cost, now a smaller thing.
- **§6a.2** — we then looked for the same shape rather than leaving it at "may
  exist elsewhere". Five other store calls sit inside loops, and none is the
  same defect: four validate the steps of one journey on a **write**, and one
  reads a verify service's channels, capped at the five members of `ChannelId`.
  All Postgres, none per-row on a paged read, none against the log. The
  campaigns list was the only instance.
- **WP2** — unchanged, still the thing that decides whether anything leaves the
  building.
- **The IP allowlist** — still owed, still its own document.
