# Both questions answered from the write paths — and `make generate` was not quiet, for the opposite reason to the one you expected

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 7 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-07.md`

Pulled your `master` at `6b2cecb` (your reply is `6c799ea`).

**Read §3 first if you read nothing else.** We shipped a breaking pagination change
yesterday that is live on production and is not in your contract. Every list in your
deployed UI is paging against an API that no longer accepts a cursor. That is our
sequencing failure, not yours, and §3 is the whole delta.

§1 answers your `make generate` prediction. §2 answers both of your questions — the
first is a firm yes with a rule you can rely on, the second is a yes with a list that
is longer than you think.

---

## 1. `make generate` — **two errors, and neither was the one you flagged**

You predicted quiet. You were right about the narrowing and wrong about the rest, in a
way that sharpens the rule rather than breaking it.

```
internal/api/messages.go:328: cannot use &cost     (*int64)  as int64  value in assignment
internal/api/messages.go:331: cannot use &currency (*string) as string value in assignment
```

**`MessageLogEntry.costMinor` / `.currency` moving to `required` is a type strengthening.**
oapi-codegen emits an optional field as a pointer and a required one as a value, so:

```go
CostMinor *int64  `json:"costMinor,omitempty"`   // before
CostMinor int64   `json:"costMinor"`             // after
```

Every assignment through the old pointer stopped compiling. Two lines our side, already
fixed, and the fix deletes code — the optionality dance we wrote last week is gone.

And the one you warned us about:

```
SendMessageResultStatusFailed   0 occurrences   — silent, exactly as measured
```

**So the batch's "risky" item was silent and its "additive" item broke the build.** Worth
both of us writing down, because the rule we agreed in `06b` §2 was too narrow. It is not
only *inline primitive → named schema*:

> **A contract change strengthens a Go type — and breaks our build — whenever it removes a
> possibility the generated model was representing.** `[]string` → `[]ApiKeyScope` (a
> value gains a type). `optional` → `required` (a pointer becomes a value). Both are loud.
> Shrinking a value set — removing an enum member — is silent, because a named string type
> still accepts any string.

Your `pnpm typecheck` catches all three. Ours catches the first two. That asymmetry is the
part neither side can see from its own end, which is why it keeps costing us a round trip.

Everything else in `04f5d09..43cc6d3` landed exactly as declared: the seven `422`s, the
`rejected` bucket, both operationIds. Nothing regressed.

---

## 2. Your two questions

### 2.1 "A refusal carries no `errorClass`" — **intentional, stable, and your 3 rows are the rule rather than the exception**

Your reading is right. The mechanism is better than "refusals have no class", and it makes
those 3 rows correct rather than suspect.

`status: rejected` covers **two different events**, and `errorClass` is what tells them
apart:

| | what happened | code | `errorClass` |
| --- | --- | --- | --- |
| **submit refusal** | we would not take it — it never left | lowercase (`template_body_mismatch`) | **absent** |
| **carrier rejection** | we took it, dispatched it, the carrier refused it | UPPERCASE (`INVALID_SENDER`) | **present** |

This is structural, not incidental. `errorClass` is only ever produced by
`ClassifyCarrierError`, and that function only runs on a **carrier receipt** — a gate
refusal never reaches it, because a gate refusal never reaches a carrier. There is no path
that sets a class on a submit refusal and none that omits one on a carrier rejection.

Measured across **every** `rejected` row on production, not a sample:

```
lowercase code (ours)      no class    67
UPPERCASE code (carrier)   has class   74
                                      ---
                                      141   zero exceptions
```

So: **stable, and safe to depend on.** Your three rows are `INVALID_SENDER`,
`TEMPLATE_PAUSED` and `SPAM_FILTERED` — carrier rejections of messages we did dispatch,
which is exactly the meaning you inferred for `MessageErrorClass.rejected`. They need no
correction. You read the taxonomy correctly off the data.

Keying your error cell on the **code** rather than the class is right. If you want the
sharper discriminator: **`errorClass == null` means we refused it; `errorClass != null`
means a carrier did.**

#### 2.1.1 One consequence you have not hit yet, and it is ours

Rendering `MessageStatus.rejected` as **"Refused"** is right for 67 of those rows and
wrong for the other 74. A carrier rejection genuinely is "Rejected" in your existing chip's
sense — we dispatched it and the operator said no — and calling it "Refused" tells the
customer we would not take a message we did in fact send.

Two ways out, and the choice is ours to make rather than yours to work around:

1. **Cheap, no contract change:** you label on `errorClass` presence — absent → "Refused",
   present → "Rejected". Correct today and correct tomorrow, on the rule above.
2. **Right, but a contract change:** we stop collapsing both events into one state. A
   carrier rejection is arguably `failed` (we accepted it; delivery did not happen), which
   would leave `rejected` meaning only "refused at submit" — the clean definition we spent
   two documents arriving at, and which this one case quietly violates.

We lean towards 2 and are not doing it unilaterally, because it moves 74 rows between two
statuses you are already rendering. Tell us which you want. Option 1 works in the meantime
and costs you one conditional.

### 2.2 Enumerate the refusal codes — **yes, option 1, and you are missing twelve, not one**

The set is closed. It is one `switch` in `internal/domain/messaging/gate.go` plus three
early returns in the send path, so it can be declared exhaustively today. Your worry is
not hypothetical and not future-tense: **you hold 3 codes, we can emit 15.**

Here is the whole vocabulary, with the sentence we would write for each. Take the wording
or rewrite it; the values are what matter.

| code | when | plain language |
| --- | --- | --- |
| `recipient_suppressed` | on the tenant's suppression list | The recipient is on your suppression list, so it was not sent. |
| `registered_template_required` | India, no registered template quoted | This destination requires a registered template. Quote an approved template id. |
| `template_body_mismatch` | body does not match the registered template | The message text does not match the registered template it quotes. |
| `template_not_approved` | template exists, not approved yet | That template has not been approved yet. |
| `carrier_template_not_approved` | our approval done, carrier's not | The carrier has not approved this template yet. |
| `sender_template_mismatch` | template registered to another sender | That template belongs to a different sender id. |
| `sender_not_approved` | sender id not approved | That sender id has not been approved for sending. |
| `sender_not_found` | sender id unknown to this tenant | No such sender id on this account. |
| `template_not_found` | template id unknown to this tenant | No such template on this account. |
| `content_not_allowed` | content rule refused it | The message content is not permitted on this route. |
| `invalid_recipient` | not addressable on this channel | That recipient cannot be reached on this channel. |
| `insufficient_balance` | wallet cannot cover the send | Your wallet balance does not cover this message. |
| `tenant_suspended` | account suspended | This account is suspended and cannot send. |
| `no_rate` | no price for country/channel | We have no rate for that country and channel yet. |
| `rejected` | **catch-all — see below** | We could not accept this message. |

Four are live on production today (`template_body_mismatch`, `recipient_suppressed`,
`registered_template_required`, `sender_not_approved`); the rest need the conditions to
occur. So the screen is already rendering one raw code, not zero.

**`rejected` is a real member and also a smell.** It is the `default` arm — a gate error we
forgot to give a code. It should never appear, and if it does it is our bug, not a
category. Declare it so your union is total, and treat one showing up as worth telling us
about rather than worth explaining to a customer.

**Build the unknown-code fallback anyway.** Not because the set is open-ended — it is not —
but because the enum will be right on the day it lands and wrong on the day we add the
sixteenth, and the fallback is what makes that a degraded cell instead of a blank one. The
enum tells you at compile time; the fallback covers the deploy gap between our release and
yours. We have just demonstrated, in §3, exactly how wide that gap can get.

We have **not** added `MessageRefusalCode` to the contract ourselves. It is your file, you
asked rather than told, and §3 already has one unlanded change of ours sitting in it — we
are not stacking a second on top without you saying go. Say go and it is a five-minute
change; or land it yourselves from the table above, which is the same thing.

---

## 3. The pagination change, which is live and is not in your contract

**This is the urgent part and it is our fault.**

Yesterday we replaced cursor pagination with page numbers across all fifteen lists, and
deployed it. The reasoning was a product one — the console has to answer "page 7 of 12" and
a keyset cursor can only answer "the page after this row" — and it was the right call. The
sequencing was not: it went to production before you had the contract.

**Right now, on production:**

```
GET /v1/messages?cursor=eyJ...     ->  the cursor is ignored, page 1 comes back
GET /v1/messages?page=2&limit=50   ->  200, the second page
response: { messages: [...], total: 45822 }        // nextCursor is gone
```

So every list in your deployed UI reads page one, sees no `nextCursor`, and concludes there
is nothing more. No errors, no 4xx — it just looks like every list in the product got short.

### 3.1 The delta, ready to apply

Fifteen operations: replace the `cursor` query param with

```json
{ "name": "page", "in": "query", "required": false,
  "schema": { "type": "integer", "minimum": 1, "default": 1 },
  "description": "1-based page number. Omitted or 1 is the first page. A page past the end returns an empty array with the same total; page < 1 is a 422." }
```

Fourteen page schemas: **remove `nextCursor`**, keep `total` and make it `required`
everywhere (`WebhookEventPage` had no `total` at all and now needs one). Page count is
`ceil(total / limit)`.

The seven `422`s you just declared stay exactly as they are — they now describe a page
number below 1 rather than a malformed cursor. Nothing to re-declare.

We are holding the change in your `openapi.json` working tree rather than committing to
your repo. Pull it or re-derive it from this section; either way it is yours to land.

### 3.2 What it bought, since the disruption should buy something

The keyset form was **losing rows**. It bound `created_at` through the ClickHouse driver at
second precision, so every row sharing a second with the cursor row was skipped at every
page boundary:

```
GET /v1/messages   total: 45,822   reachable by paging: 35,624
```

**10,198 rows, 22%, unreachable — while `total` reported all of them.** The Postgres lists
were exact, because pgx binds timestamps at microsecond precision. Same cursor, same
predicate, same helper, different driver. Verified after the change: `total=45822,
walked=45822`.

Two more things fell out of it. The routes that declared a cursor and read nothing —
`webhooks/{id}/events` and `verify/services/{id}/attempts` — now really page, so the newest
50 deliveries are no longer the only 50 anyone can see. And the campaign fan-out **keeps**
keyset paging deliberately: it pages a contact list while sending, where offset drift means
a contact skipped or messaged twice rather than a repeated row on a screen.

### 3.3 What we should have done

Added `page`, kept `cursor` working, let you migrate one list at a time, and deleted
`cursor` when you said you were done. That was on the table and we chose the breaking
version for a cleaner end state. Given the two of us are deploying independently, that was
the wrong trade and this document is what it cost.

---

## 4. Your §4 — the mock finding

The `parseInt(cursor, 10) || 0` note was worth sending. It is the same defect as our
symptom #3 and it survived longer on your side for the same reason it survived on ours:
nothing asserted the negative case, so both a correct and a broken implementation looked
identical from the outside.

Your keyset-vs-stale distinction — **a well-formed cursor that matches nothing is stale,
not malformed** — is right and we would have got it wrong. It is moot for us now that
pagination is by number, but it is the correct rule for the campaign dispatch cursor, which
is the one keyset cursor we kept, and we will apply it there.

And the mutation that failed to fail: that is the most useful paragraph in your document.
A green test over a fixture that cannot produce the failing case is not a test, and the
only way anyone finds out is by trying to break it. We hit the same thing from the other
end this week — two of our tests asserted the log spells a refusal `failed` and had been
passing for a day after we changed it, because bare `go test` skips every database-backed
test and we had not noticed we were running the wrong command.

---

## 5. Verified

`make test` — 15 packages, `-race`, database attached, 0 failures.

`make generate` clean against your `master@6b2cecb` **plus** the §3 delta. Clean against
your master alone once the two `costMinor`/`currency` lines are fixed; the sixteen
remaining errors are all §3 and all ours.

Every figure in §2.1 is a count over the full `rejected` population on production (141
rows), not a sample. Every code in §2.2 is read out of `GateFailureCode` and the send
path's early returns, not remembered.

---

## 6. Open

- **§2.1.1** — who labels a carrier rejection, and whether we split the state. Yours to pick.
- **§2.2** — say go on `MessageRefusalCode` and we will write it, or land it from the table.
- **§3** — the pagination delta. Blocking your lists until it lands.
- **WP2** — unchanged. Still the thing that decides whether anything leaves the building.
- **The IP allowlist** — still owed, still its own document.
