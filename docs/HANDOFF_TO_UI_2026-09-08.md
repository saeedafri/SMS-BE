# All three built — and one push-back on §2 that changes what it can honestly promise

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 8 September 2026
**Re:** `BACKEND_REQUEST_recipients-export-and-list-envelopes.md`, your `master@ea7db18`

Your checker read **6/7** when we started, and the failing one was `list-envelopes`.

All three asks are built. **One push-back, in §2, and it is not a refusal** — your derivation
is right about the mechanism and wrong about one case, and the case is common enough that
the endpoint would have quietly lied. We built it with the correction rather than waiting on
a round trip, because the contract you specified is unchanged by it.

Two things we could not build and one we would not: §3 has a missing declaration, §5.

---

## 0. Your correction, and what we owe back

Thank you for §0. You did not have to go and grep your own repo while writing a reply, and
finding that `campaigns-state.ts:187` was rewriting queued messages into `cancelled` rows —
the exact thing you had told us not to create — is the kind of thing that stays hidden for
months.

**We should say the symmetrical thing.** We wrote *"a decision we made together"* in `07c`
§2 and it was not; it was your sentence and we adopted it without checking it. The reason we
did not check is instructive: it agreed with a model we already preferred, so we treated
agreement as evidence. Your wrong sentence and our uncritical adoption were the same
failure from two sides, and the endpoint in §2 is what neither of us noticed was missing.

`cancelled` is gone from `MessageStatus` on your side and `make generate` was silent on
ours, exactly as the enum-narrowing rule predicted. Nothing to do.

---

## 1. `make generate` — three errors, all the `$ref` swap

```
internal/api/journeys.go:122:  cannot convert out ([]gen.Journey)  to gen.ListJourneys200JSONResponse
internal/api/senders.go:107:   cannot convert out ([]gen.SenderId) to gen.ListSenderIds200JSONResponse
internal/api/templates.go:99:  cannot convert out ([]gen.Template) to gen.ListTemplates200JSONResponse
```

The loud direction, as expected, and the `MessageStatus` narrowing beside it was silent. The
rule is now four-for-four across both directions, so we are treating it as settled rather
than as a working hypothesis.

---

## 2. Ask 25 — built, with one correction to the derivation

**The mechanism you read out of our source is right.** No recipients table, `dispatch_cursor`
is a position, fan-out walks the list in a stable order. You can derive it, and we have.

**The case it gets wrong is a contact added to the list after the campaign ran.**

Fan-out walks the audience `created_at DESC, id DESC` — newest first. So a contact created
last week sorts *ahead* of the cursor, into the region your rule calls "already dispatched".
It would be reported as `dispatched`, with `messageId: null`, contradicting the sentence in
your own schema that says a dispatched recipient points at the row it became. Adding
contacts to a list is not a corner case.

**Fixed two ways.** Contacts created after the campaign started are excluded from the
audience entirely — they were not part of the run, and neither of your two states describes
them. And `state` is decided by whether a message row exists rather than by cursor position:
the cursor chooses which slice to page, the message log says what actually happened, and a
row the two disagree about is dropped from a filtered page rather than mislabelled.

Your `messageId` field is what made this cheap, and it is worth naming why: filling it
requires reading the campaign's message rows anyway, so once you have them the cursor stops
being the authority for anything except paging position. **The field you asked for removed
the need for the mechanism you proposed.** It is looked up per page, so a twenty-row page
costs twenty lookups rather than loading every message id a 100,000-recipient campaign
produced.

**What it still cannot see, stated plainly because it is permanent until someone migrates:**
a contact that existed *before* the run and joined the *list* afterwards. `contact_list_members`
records `(list_id, contact_id, tenant_id)` and no timestamp, so there is nothing to compare
against. That recipient is classified by its creation date, which is arbitrary relative to
this campaign. Recording membership time would close it and is a migration, not a query —
say the word if you want it and we will cost it properly.

Your assertions should all hold, including the one you called decisive:
`state=cancelled` total against `counts.cancelled`. They are computed by different routes —
yours by subtraction, this by cursor position — which is exactly why it is worth asserting.

**One gap in the declaration.** `GET /v1/campaigns/{id}/recipients` declares `200` and `422`
and no `404`. We answer a campaign that is not yours with an empty page rather than inventing
a status code, which is defensible but not what you want: it makes "no such campaign" and "a
campaign with no cancelled recipients" the same response. Declare a `404` and we will use it.

---

## 3. Ask 26 — the audit export is built. The user-activity one is not declared.

`GET /v1/operator/audit-log/export` streams the whole filtered log as CSV: your columns in
your order, RFC3339 timestamps, `occurred_at DESC, id DESC` matching the paged endpoint,
`attachment` disposition with the dated filename, and `charset=utf-8` declared.

Streamed through a pipe rather than assembled, so a 45,000-row export costs one row of
memory rather than 45,000. A write that fails mid-stream closes the body with an error
rather than truncating the file, because a half-written CSV that looks complete is worse
than a failed download.

**`GET /v1/operator/user-activity/export` is not in your `openapi.json`.** The revision added
it to the prose and not to the contract, so there is nothing to generate against — we
checked rather than assumed. Declare it and it is a copy of the same handler with a different
column list; it is the cheapest of everything in this document.

### 3.1 Your two push-back invitations

**CSV versus lifting the cap on the JSON endpoints — CSV, and not for the reason you gave.**
Memory was not the deciding factor; streaming solves that either way. The deciding factor is
that an uncapped JSON list is a *different contract* for the same URL, where the cost of a
request depends on a parameter, and every client — including ones that are not you — can
trigger it by accident. A separate export endpoint is a separate promise with its own
expectations, and a client that has never heard of it cannot stumble into a 45,000-row
response.

**Operator-only — yes, agreed,** and for the reason you gave: both list endpoints already
are, and an export that is easier to reach than the screen it mirrors is a hole.

**One correction to your reading of the cap.** You quoted `internal/store/operator.go:144`
and read it as "> 500 silently drops to 100". That is right, and it is not the whole of it:
the same branch catches `limit <= 0`, so `?limit=0` also lands on 100. Your 500 was the
right choice and this changes nothing you did — flagging it because your document describes
the clamp as a ceiling behaviour when it is really "anything out of range gets the default".

---

## 4. Ask 7 — built, and the checker should go green

All three lists now answer an envelope with a `total`, page, and filter in the `WHERE` of
both the count and the page query. `q` matches `name` on templates and journeys and
`header` or `displayName` on sender IDs, as specified. Journeys gets no `channel` or
`country`, for the reason you gave. `page < 1` is a `422` on all three.

You were right to call it least urgent, and right that it was yours to unblock first.

The guard is one test over all three, because they are one pattern and the failure is
identical in each. Broken on purpose: a filter applied after the slice — red, naming the
needle that went missing. It also walks every page and asserts the union is the collection
exactly once, which is the assertion that would have caught the message-log shortfall.

---

## 5. What we did not build, and one thing we would not

- **`/v1/operator/user-activity/export`** — not declared. §3.
- **A `404` on the recipients endpoint** — not declared. §2.
- **Membership timestamps on `contact_list_members`** — would close the last gap in §2, and
  it is a migration. Not doing it on our own judgement.

---

## 6. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean against `master@ea7db18` after the three envelope handlers.

**Your checker reads 7/7 against the deployed API**, `list-envelopes` red to green:

```
PASS  messages-status-partition     all 6 partition 45822 rows
PASS  campaigns-filters             PASS  abuse-queue-order    PASS  tenants-q
PASS  tickets-q                     PASS  list-envelopes       PASS  campaigns-envelope
7/7 satisfied
```

Plus 18 live assertions on the two endpoints your checker does not cover — the recipients
join in both directions, and the export's columns, charset, disposition, row count against
the list's total, and header-only response on a filter matching nothing.

Every decisive assertion mutation-verified: the export dropping its filters (red — 1,392
rows against a list total of 120), the template list filtering after its slice (red — the
needle unreachable), and the catalogue pager reaching every row it counts.

One of our own test assumptions was wrong and the test caught it rather than us: we asserted
that `action=connection.create` matched nothing, and it matched 147 rows another test had
written. The assertion was replaced with a filter that is guaranteed empty — an unused
tenant id — rather than weakened. Your point about a check needing its own check keeps
being the most reusable thing in this exchange.

---

## 7. Two things live verification found, one of which is not ours to sit on

Neither is in your document. Both came out of proving §2 against real data rather than
against a fixture, which is the only reason we know.

### 7.1 `counts.cancelled` and the recipients endpoint CAN disagree, and your assertion will catch it

You called `state=cancelled` total `== counts.cancelled` the decisive assertion. It is, and it
already fails on one campaign on the demo tenant:

```
Halt verification 2026-09-05    recipients 500   counts.cancelled 500   recipients endpoint 0
```

**Both endpoints are correct.** That campaign has no `list_id` and no `send_started_at` — it
was written straight into SQL as a fixture with `recipients = 500`. `counts.cancelled` is
`recipients − recorded`, so it reports 500 from a number nothing backs. The recipients
endpoint derives from the actual list, and there is no list.

The general form matters more than the fixture: **the two numbers come from different
sources.** `counts.cancelled` reads a stored integer frozen at creation; the recipients
endpoint reads the list as it is now. They agree when the campaign has a real list that has
not changed, and they diverge when it does not. Your assertion is the right one to write —
just expect it to find data like this, and treat a disagreement as "one of these is
describing something that no longer exists" rather than as a backend bug.

On a real campaign the two agree and the shape holds: we launched a 2,500-recipient campaign
on production, and `cancelled + dispatched == total` with every dispatched row carrying a real
`messageId` and every cancelled row carrying `null`.

### 7.2 Campaign fan-out did not check per-channel consent — FIXED, `15e1917`

> **Amended 8 September, after this document was sent.** This section was
> written as an open defect we were not going to fix without asking. We fixed it
> twenty-five minutes later, in `15e1917`, and the section stayed as written —
> so for a day it read as a live compliance hole on an India A2P product. The
> frontend caught it. **The defect below is closed**; the account of it is kept
> because the measurement is worth having and because a document that quietly
> edits its own history is worse than one that shows the correction.

**What was wrong.** `EstimateCampaign` counted only contacts who opted in on the
channel — `consent ->> 'SMS' = 'opted_in'`. The fan-out that actually sends did
not: `ListContactsAfter` filtered on list membership and the dispatch cursor,
and the gate checked suppression, sender, template, balance and addressability.
**Consent was not among them.**

Measured, not read: we seeded 2,500 contacts with no `SMS` consent key, created
a campaign that quoted **0 recipients**, launched it, and it dispatched to all
2,500. A campaign could quote zero and send to everyone.

**What fixed it.** One SQL fragment, `reachableOnChannel`, now shared by the
estimate, the fan-out and the recipients endpoint — the three that have to agree
about who a campaign's audience is, and which had three different answers.
Verified on production afterwards: that same list quotes 0 and sends to 0, and a
mixed list quotes 2 and sends exactly 2, where it had sent 4.

**Two things it cost, both worth recording.** It introduced a `500` on
`/v1/campaigns/{id}/recipients` — the reshaped query named two placeholders it
did not supply — which the full suite missed because every campaign in it had no
list and returned before building any SQL. Fixed in `de2f306`. And putting the
audience rule into that endpoint's `WHERE` put it upstream of where `state` is
decided, so the endpoint began omitting recipients that demonstrably received a
message. That is the frontend's ask 28, and it is fixed in the 9 September
batch.

## 8. Open

- **§3** — declare `user-activity/export` and it lands the same day.
- **§2** — declare the `404`; tell us if you want membership timestamps costed.
- **WP2** — unchanged, still the thing that decides whether anything leaves the building.
- **The IP allowlist** — still owed, still its own document.
