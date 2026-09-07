# All four answered, all built — and the bug was worse than you measured

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 7 September 2026
**Re:** `BACKEND_REQUEST_message-filters-and-campaign-search.md`, your `master@87fd4d5`

A sentence per item, as asked, then the detail.

1. **§2, the status filter — fixed and deployed today.** Your diagnosis was exact, including
   the line number and the root cause. It was worse than the numbers in your document, for a
   reason that is not your error: the log moved under you. §1.
2. **§2.4, `read` and `cancelled` — different answers.** Keep `read`; it is real and
   unimplemented. **Delete `cancelled`** — it is unreachable by a decision the two of us
   already made together. §2.
3. **§3, campaign filters — built and deployed today.** Shape is right, nothing to push back
   on, and `make generate` was quiet exactly as you predicted. §3.
4. **§4, abuse-queue ordering — deliberate and guaranteed.** It is an explicit `ORDER BY
   flagged_at DESC` in the SQL, not storage order. §4.

Nothing to push back on this time. The one thing worth arguing about, I have argued in §2
rather than just doing.

---

## 1. §2 — fixed, and the gap was 24,226 rather than 10,226

Your report was right down to `internal/api/messages.go:105` and the `""`-means-no-filter
mechanism. It needed no investigation, only confirmation, which is the most useful shape a
bug report can have.

**The one thing to correct is a number, and it is not your mistake.** You measured 10,226
unreachable rows. When we reproduced it this afternoon it was **24,226**:

```
                 yours        ours (a few hours later)
status=sent     15,637         1,637
status=failed      335           335
unreachable     10,226        24,226
```

The reconciler moved ~24,000 rows from `accepted` to `expired` in between. That is it doing
its job on fixture data — seeded `accepted` rows have no delivery report coming, so they
age out and land as `expired`, which is the honest terminal state for "the carrier never
said anything". Nothing broke; the log is genuinely a moving target.

It matters for your checker rather than for the bug: **your partition assertion is measuring
a collection that changes underneath it**, and your tolerance for drift is doing real work.
Worth knowing that the drift can be tens of thousands of rows, not six.

### 1.1 What it is now

`contractStatusToState` returned one state; it is now `contractStatusToStates` and returns
the set, because the mapping is one-to-many in both directions:

| wire value | internal states |
| --- | --- |
| `queued` | `queued`, `submitting` |
| `sent` | `submitted`, `accepted` |
| `delivered` | `delivered` |
| `failed` | `undelivered`, `carrier_rejected`, `expired` |
| `rejected` | `rejected` |
| `read`, `cancelled` | — (empty set; see §2) |

Your table, adopted as written. The store now filters `status IN (?)`.

**The rule that actually fixes it** is the one you named, and it is now enforced rather than
implied: *a filter either narrows or it matches nothing; it never widens to everything.*
Concretely, `nil` means no filter and a non-nil empty set means "this value covers no
internal state", which returns an empty page without touching the database. The old code
could not express the difference, so the third case collapsed into the first.

We also deleted the doc comment that stated the rule the code broke. It said `"sent" spans
submitted and accepted, so a filter on it must not silently match only one` — correct, and
describing behaviour the function did not have. A comment that documents the intended
behaviour of code that does something else is worse than no comment, because it stops the
next reader looking.

### 1.2 The guard

`TestEveryMessageStatusFilterPartitionsTheLog` seeds one row per internal state — nine
states, deliberately different counts so a mapping that swaps two of them fails rather than
coincidentally summing — and asserts both of your properties.

Broken on purpose, both ways, because your point about either alone is exactly right:

- **unmapped values fall through to no filter** (the live bug) → red on all three arms:
  purity, the whole-log check, and the expected total.
- **`failed` maps to `undelivered` only** (the incomplete one) → **purity stays silent**,
  and only the totals catch it: `total = 7, want 26`, then `the seven sum to 35, the log is
  54 — 19 rows reachable by no filter at all`.

That second mutation is the argument for your two-assertion design, made by the test rather
than by a document.

---

## 2. §2.4 — keep `read`, delete `cancelled`. They are unreachable for different reasons.

You asked rather than instructed, and the two turn out not to have the same answer.

**`read` — keep it.** Genuinely unreachable today, and it should not be. `DeliveryReport`
carries `Delivered bool` and nothing else, so a read receipt has nowhere to land even when a
carrier sends one — and RCS and WhatsApp both do. `CampaignCounts.Read` already exists and is
never populated, from the same gap. This is a feature we have not built, not a state that
cannot exist. Your chip is right and will start matching rows when we build it. Your mock
serving 513 `read` rows is a better guess than our zero.

**`cancelled` — delete it from `MessageStatus`.** It is unreachable by construction, and by a
decision we made together: in `06` §3.3 you asked us to leave campaign cancellation derived,
and we agreed that no row is the honest representation of a recipient never dispatched. A
message row can therefore never be `cancelled` — there is no row. That is not an unbuilt
feature, it is the design working.

`counts.cancelled` stays exactly as it is. It is a campaign-level number computed by
subtraction, and it is unrelated to `MessageStatus` beyond sharing a word.

So: one chip to keep, one to delete. Both map to the empty set today, so **nothing breaks
either way and there is no rush** — the filter answers `total: 0` rather than the collection.

---

## 3. §3 — built, and your prediction was right

`status`, `channel` and `q` on `GET /v1/campaigns`. Shape adopted as specified:

- `status` takes one `CampaignStatus`, `channel` one `ChannelId`, `q` a case-insensitive
  substring over the campaign **name** only. All three AND.
- **Filtered in the `WHERE` of both the count and the page query** — one clause list shared
  by them, so the total cannot describe a different set from the rows.
- Empty or whitespace-only `q` filters nothing; a term matching nothing is `200` with
  `total: 0`; ordering stays newest-first.

**`make generate` was quiet**, as you predicted — additive optional query parameters, no
`$ref` swap, nothing removed. You called it a guess rather than a promise; it was right, and
the rule it was reasoning from is now three-for-three.

Same test shape as the operator search, because it is the same property: a needle seeded
first so newest-first ordering pushes it off page one, an assertion that it is *not* on page
one unfiltered, then an assertion that `q` returns it on page 1 of the filtered set. Broken
on purpose two ways — a page-scoped filter and an unfiltered total — both red.

Nothing to push back on. It is the third instance of a pattern rather than a third dialect,
which is what makes it cheap.

---

## 4. §4 — deliberate, and it will survive

```sql
FROM tenants WHERE flagged_at IS NOT NULL ORDER BY flagged_at DESC
```

An explicit `ORDER BY` in `ListFlaggedTenants`, not an artefact of storage order. Nothing to
build. Declaring it in the contract is the right move and we will treat it as a promise —
the ordering is now the screen's only source of order, so changing it silently would be a
regression rather than a refactor.

---

## 5. On your checker

Running one executable assertion per item instead of maintaining a status column is the
right call, and the reasoning — *a table nobody maintains cannot drift* — generalises past
this exchange.

It also caught something neither of us would have found by reading: **the log moves by tens
of thousands of rows between measurements** (§1). A hand-written status table would have
recorded 10,226 as a fact about the system rather than a reading taken at a moment.

One note in the same spirit. Your partition assertion sums seven totals taken as seven
separate requests, so a row that changes state mid-check is counted twice or not at all —
which is what your drift tolerance absorbs. That is the correct trade at this size. If it
ever gets noisy, the tighter form is to compare each filtered total against a `GROUP BY
status` taken in one request, so every number comes from one snapshot. Not worth doing yet.

---

## 6. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean and quiet against your `master@87fd4d5`.

**We ran your checker rather than describing our own:**

```
checking https://sms-api.saqibsaeed.cloud
PASS  messages-status-partition     all 7 partition 45822 rows
PASS  campaigns-filters             status, channel and q all narrow the set
PASS  abuse-queue-order             2 rows, newest first
PASS  tenants-q                     filters 28 down to 0 on no match
PASS  tickets-q                     a no-match term returns an empty filtered set
PASS  campaigns-envelope            {campaigns, total: 67}, page=0 refused

6/6 satisfied
```

It read 4/6 when you filed the document. Thank you for shipping the assertions with the
request — it is the first time we have been able to close a handoff by running your check
instead of asking you to trust ours.

Our own live pass agrees: all seven filters pure, `read` and `cancelled` answering
`total: 0` rather than the collection, the seven summing to 45,822 with **zero** unreachable
rows, and every campaign filter narrowing with a matching total.

Both new guards were broken on purpose against the exact defect they claim to catch, and in
the status filter's case against **both** of its symptoms separately, because the second one
is invisible to the first assertion.

---

## 7. Open

- **`cancelled` in `MessageStatus`** — ours to recommend, yours to delete. No hurry.
- **Read receipts** — a real gap, not scheduled. Your chip stays correct and empty until it is.
- **WP2** — unchanged.
- **The IP allowlist** — still owed, still its own document.
