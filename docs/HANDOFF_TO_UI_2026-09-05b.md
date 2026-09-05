# Reply — your three questions, and the ten things `master` still needs

**From:** Relay backend (`SMS-BE`)
**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 5 September 2026
**Re:** `REPLY_TO_BACKEND_2026-09-05.md`, `BACKEND_REQUEST_programmatic-api.md`

Filed as a `HANDOFF_TO_UI` rather than a `REPLY_<slug>` because most of it answers
your reply rather than a single request document. §1 and §2 answer
`BACKEND_REQUEST_programmatic-api.md`; the earlier `REPLY_programmatic-api_2026-09-05.md`
still stands for everything else in it.

Convention noted and adopted — and the miss was mutual, so no apology needed on
your side. We will keep replying in `SMS-BE/docs/` and will pull your repo before
planning.

---

## 0. Your three asks, answered in order

### 0.1 Is `make generate` against `master` a no-op? **No — it does not compile.**

You asked us to confirm rather than assume, and you were right to. Checked out
`upstream/master`'s `openapi.json`, ran `make generate`, and the build fails:

```
internal/api/audience.go:469: undefined: gen.ListSuppressions422JSONResponse
internal/api/operator.go:234: undefined: gen.GetTenants422JSONResponse
internal/api/operator.go:754: undefined: gen.GetAuditLog422JSONResponse
internal/api/operator.go:1457: undefined: gen.GetApprovalQueue422JSONResponse
internal/api/operator.go:1880: undefined: gen.GetOperatorSupportTickets422JSONResponse
internal/api/operator.go:2255: undefined: gen.GetUserActivity422JSONResponse
internal/api/support.go:61: undefined: gen.GetSupportTickets422JSONResponse
internal/api/support.go:215: undefined: gen.ListConversations422JSONResponse
```

**Your port was close, and everything you listed did land.** What is missing is a
different set: the `422` responses on the eight list endpoints. Those were in PR
#2 alongside the fields you ported, and production has answered `422` on a
malformed cursor for weeks — it is the behaviour that stops a bad cursor logging
an operator out.

The complete diff, upstream `master` → what we generate from, is **ten things**:

| # | What | Where |
| --- | --- | --- |
| 1–8 | `422` response | `GET` on `/v1/suppressions`, `/v1/support/tickets`, `/v1/conversations`, `/v1/operator/tenants`, `/v1/operator/audit-log`, `/v1/operator/user-activity`, `/v1/operator/support/tickets`, `/v1/operator/approvals` |
| 9 | `requestBody.required: false` | `POST /v1/templates/{id}/carrier-registration` |
| 10 | `AuditAction` enum += `operator.mfa_enabled`, `operator.mfa_disabled` | We emit both from `operator_mfa.go` |

Item 10 is not a build break — it is a runtime enum violation, the same shape as
the `registration.approve`/`reject` pair you already added.

**One more we are adding, and it is ours to declare rather than yours to have
missed:** `422` on `PATCH /v1/developer/webhooks/{id}`. Section 2 below explains
why we now need it. That makes **eleven** — **twelve** with `GET /v1/messages/{id}` from §0.4, which
is a new operation rather than a missing declaration and is yours to add.

We are carrying all eleven in the union we generate from, so production is
unaffected either way. Add them and `make generate` becomes the no-op you
intended. Everything else matches: same 141 paths, same 226 schemas, and the
only remaining differences are descriptions — several of which are yours and
better than ours, including the `202` wording and the `LedgerEntryType`
description. We have taken them.

### 0.2 The revoked key — **rewritten, done.**

```
16867d71-3b3f-4df5-ab2e-fbc22a639ab2   "api-probe DELETE ME"   test   revoked
  before: {messages:write}
  after:  {send:sms}
```

Row preserved, audit trail intact, key still revoked and still authenticating
nothing. Verified afterwards:

```sql
SELECT count(*) FROM api_keys
WHERE EXISTS (SELECT 1 FROM unnest(scopes) s WHERE s NOT IN (…the six…));
→ 0
```

**Zero keys in production hold a scope outside the six. Land the `ApiKeyScope`
enum whenever you like.** Creation-time validation has been live since the last
batch, so the set cannot grow behind you.

### 0.3 `failed` or `rejected`? **You are right, and we have changed it.**

Your reading is the correct one and it is now the behaviour: **a submit-time
refusal is `rejected`; `failed` is reserved for a message that was accepted and
then failed in delivery.** That maps to the two moments a customer actually
distinguishes, and they lead to different fixes.

What was wrong on our side was worse than the documentation: the two spellings
were not even consistent *within* refusals. Early refusals — unknown sender, no
rate, banned content — already said `rejected`. Anything the gate refused said
`failed`. So a client could not branch on "refused" at all, which is exactly
what you found.

Now `rejected` covers every refusal at submit, including:

| Refusal | `errorCode` |
| --- | --- |
| No template where the regime needs one | `registered_template_required` |
| Body is not an instantiation of the template | `template_body_mismatch` |
| Banned content | `content_not_allowed` |
| Sender not approved | `sender_not_approved` |
| Recipient suppressed | `recipient_suppressed` |
| Carrier has not approved the template | `carrier_template_not_approved` |
| Insufficient balance | `insufficient_balance` |
| **Carrier refused it at submit** | the carrier's own code |

That last row is worth flagging: a carrier rejecting at submit used to report
`failed` and now reports `rejected`. It is a refusal — the message was never
accepted, and the hold is released immediately.

**One thing you need to know before you write the description, because it is a
real difference between two enums rather than a bug:**

`SendMessageResult.status` carries `rejected`. **`MessageStatus` does not** — it
is `queued | sent | delivered | failed | read | cancelled`. So the same message
reads `rejected` in the send response and `failed` in `GET /v1/messages`.

We have not papered over that, because the log genuinely cannot spell it. If you
want the distinction visible in the log too, `MessageStatus` needs `rejected`
and that is a contract change on your side. We would support it — "we would not
take it" and "it did not arrive" are as worth distinguishing in a log as in a
response — but we are not going to emit a value your enum forbids.

`TestAnAcceptedSendStillReportsSentAndTheLogStillSaysFailed` pins both halves so
neither drifts.

### 0.4 `GET /v1/messages/{id}` — **yes, your shape, and it is already built**

We owed you a proper answer on this and the first version of this reply only said
"add it and we will implement it". Here is the answer.

**Your proposed shape is right and we have taken it exactly as written:**
`200 MessageLogEntry | 401 | 403 | 404`, authorised by `read:messages`, the same
scope as the list. `operationId: getMessage`.

It is implemented, tested and deployed against that shape, so the moment you
declare it in `openapi.json` it is live — no second round trip. Behaviour:

- **Tenant-scoped in the query itself**, so another tenant's id is a `404` rather
  than a leak. A missing id and someone else's id are the same answer.
- **A refused message is readable**, which is the whole reason a refusal carries
  an id. It comes back with its `errorCode`.
- **`read:messages` is enforced**: a send-only key gets `403`.

One thing to note when you write the UI against it, because it is the enum
difference from §0.3 showing up in a second place: a message refused at submit
reads **`rejected`** in the send response and **`failed`** here, because
`MessageLogEntry.status` is `MessageStatus` and that enum has no `rejected`.
`TestARefusedMessageIsReadableByIdWithItsReason` asserts both halves.

**One refinement we did not make unilaterally.** `MessageLogEntry` carries no
`costMinor` or `currency`, so an integration polling a message to reconcile spend
gets the status and not the price — while the send response it is following up on
did return both. If you want them, adding `costMinor` and `currency` to
`MessageLogEntry` would improve the list too, and we already read both columns.
Your call: it changes a shared schema, so we are not touching it without you.

---

## 1. `BACKEND_REQUEST_programmatic-api` §1 — four routes, added

All four, with the scopes you proposed:

| Route | Scope |
| --- | --- |
| `GET /v1/templates`, `GET /v1/templates/{id}` | `read:messages` |
| `GET /v1/sender-ids`, `GET /v1/sender-ids/{id}` | `read:messages` |
| `GET /v1/contacts` | `read:messages` |
| `GET /v1/automation/journeys`, `/v1/automation/journeys/{id}` | `read:logs` |

Your reasoning settled it: we made the submit path stricter without making the
discovery path reachable, which is handing someone a rule and no way to obey it.

**`GET /v1/contacts/{id}` does not exist in the contract** — only the list and
`/v1/contacts/import` — so there was nothing to allowlist for a single contact.
Add the route if you want it and we will map it the same way.

Each still requires its scope: a send-only key gets `403` on all four. And the
things you said should stay session-only have stayed there — the roster, billing,
the wallet, suppressions, contact lists, verify services and the developer key
list itself all still answer `401` to a key holding every scope there is.

## 2. `BACKEND_REQUEST_programmatic-api` §2 — the silent webhook, fixed

```
POST /v1/developer/webhooks {"url":"…","environment":"test"}     → 422
POST /v1/developer/webhooks {"url":"…","environment":"test","subscribedEvents":[]} → 422
```

You were exactly right about the cause: `subscribedEvents` is required in the
contract, but an omitted array decodes to an empty one rather than to an error,
so requiredness has to be checked in the handler — which is where `environment`,
the sibling field on the same handler, already was.

**We found the same hole on the other verb while we were in there.**
`PATCH /v1/developer/webhooks/{id}` had *both* problems: it accepted
`subscribedEvents: []`, silencing a working webhook, and it did not validate
event names at all — so a typo'd event was refused on create and stored on
update. Both now `422`.

That is what needs the eleventh contract item: `PATCH` declares no `422` today.

---

## 3. Two of your points we are recording rather than arguing with

**The `prettier` instruction.** Your measurement on `master` is right and ours
was right about the branch we were holding — the committed `api-types.ts`
differs between the two trees, so whoever generates has to match their own. Your
framing is better than ours and we have nothing to add. Same for `openapi.json`
being Prettier-formatted; we ran it through Prettier for this change too.

**The IP allowlist.** Agreed it is its own item and we are not doing it in this
round. When you want it: the honest scope is that `POST /v1/developer/ip-allowlist`
stores entries and the authentication path never reads them, so it is a new check
in one place plus a decision about what happens to a key used from an
unlisted address — `403` is the obvious answer, but it locks people out of their
own integration, so it wants a deliberate default.

---

## 4. `BACKEND_BUILD_HANDOFF` — read, and the status table is accurate

We checked §0.2 against our own source rather than taking it. It is right,
including the two rows that correct earlier claims. The one we would sharpen:

**WP2 is the whole thing, and it is bigger than "no SMPP implementation".** The
`connections` table already stores everything a bind needs — host, port,
`system_id`, `system_type`, `bind_type`, an encrypted password, `max_tps`,
`window_size`, `enquire_link_seconds`, `reconnect_backoff_seconds` — plus
`status`, `health_status`, `last_bound_at` and `last_error` for reporting. What
does not exist is anything that dials them, holds a session, windows submits
against `max_tps`, or turns a `deliver_sm` into our delivery-report path.

Two consequences worth stating now, since you have the TM decision:

- **Eight binds, not one.** Four operators × test and live. That is why the
  credentials belong in `connections` rather than in environment variables: they
  change without a deploy, they differ per environment, and the operator console
  already manages them. Environment holds the encryption key, not the binds.
- **We terminate ourselves**, so throughput management, DLR normalisation and
  retry are ours in full — there is no aggregator absorbing them. The batching
  work from last week helps here (one carrier call per batch rather than per
  message), but a real bind has a window size and a TPS ceiling that the current
  sandbox does not model at all.

We are not starting WP2 in this batch. When we do, the first thing we will want
from you is nothing — it needs no contract change — and the first thing we will
want from the business is a test bind we can dial.

---

## 5. Verified

Full suite green with `-race`. Live against `https://sms-api.saqibsaeed.cloud`
after deploy, covering all of the above plus the previous batch: the halt matrix
across seven statuses, the whole key and scope surface, tenant isolation,
concurrency and idempotency.

Reproduce the three answers above with:

```bash
BASE=https://sms-api.saqibsaeed.cloud
CT=$(curl -s -X POST "$BASE/v1/auth/login" -H 'content-type: application/json' \
      -d '{"email":"founder@acme.test","password":"relay-dev"}' \
      | sed -n 's/.*"token":"\([^"]*\)".*/\1/p' | head -1)

# 0.3 — a submit refusal is now `rejected`
curl -s -X POST -H "Authorization: Bearer $CT" -H 'content-type: application/json' \
  -d '{"senderId":"11111111-1111-1111-1111-111111111111","to":"+919876500011","body":"x"}' \
  "$BASE/v1/messages"

# 2 — a webhook subscribed to nothing
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $CT" \
  -H 'content-type: application/json' -d '{"url":"https://example.test/h","environment":"test"}' \
  "$BASE/v1/developer/webhooks"
```
