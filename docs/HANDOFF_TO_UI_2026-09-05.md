# Backend handoff — the five-document batch

**From:** Relay backend (`SMS-BE`)
**Date:** 5 September 2026
**Deployed:** `41def99` on `https://sms-api.saqibsaeed.cloud`
**Covers:** `campaign-halt`, `submit-path-compliance`, `programmatic-api`,
`defects-and-page-totals`, `gst-invoice`

---

## Read this first — the one-screen version

| Document | Status | What you have to do |
| --- | --- | --- |
| **campaign-halt** | **Done, deployed, verified live** | Nothing. Declare 3 new `UserActivityEventType` values. |
| **submit-path-compliance** | **Done, deployed, verified live** | Decide `202` vs `422` (we recommend keeping `202`). |
| **programmatic-api** | **Done, deployed, verified live** | Merge the `ApiKeyScope` enum. One stored key needs a nod first. |
| **defects-and-page-totals** | **Done, deployed, verified live** | Declare `total` on `LedgerPage` and `InvoicePage`. |
| **gst-invoice** | **Nothing built, deliberately** | Get two finance answers back to us. |

**Three things that change behaviour you are already relying on.** They are in
"Breaking changes" below and you should read that section before anything else.

Long-form answers to each document's questions are in the four `REPLY_*.md`
files beside this one. This document is the end-to-end view: what exists now,
what it does, and how to drive it.

---

## Breaking changes — read before you test

**1. Every India send now needs a `templateId`, and the body must match it.**

This is the whole point of `submit-path-compliance`, but it will look like a
regression the first time you hit it. Any existing call that sends a bare body to
an Indian sender now comes back:

```json
{"status":"failed","errorCode":"registered_template_required","costMinor":0,
 "currency":"INR","id":"…","segments":1}
```

Your own probes and any mock-derived fixtures that send arbitrary text will need
a template whose registered body the text instantiates.

**2. `POST /v1/auth/signup` refuses `GB` and `AE` with `422`.**

Any country whose regime is unknown or a stub is refused. The picker should stop
offering them, or the form needs to render the `422` sensibly.

**3. An API key now authenticates on read routes.**

Previously every read with a key was `401`. Now it is `200`, `403` or `401`
depending on scope and route. If anything in your code treats "key + read = 401"
as expected, it will now see a different answer.

---

## 1. Campaign pause, resume and cancel

### Routes

```
POST /v1/campaigns/{id}/pause    200 Campaign | 401 | 404 | 409
POST /v1/campaigns/{id}/resume   200 Campaign | 401 | 404 | 409
POST /v1/campaigns/{id}/cancel   200 Campaign | 401 | 404 | 409
```

No request body. Each returns the **full updated `Campaign`**, so you can render
the response directly without refetching.

### The transition matrix, as deployed

|  | scheduled | queued | sending | paused | sent | failed | cancelled |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **pause** | 200 | 200 | 200 | 409 | 409 | 409 | 409 |
| **resume** | 409 | 409 | 409 | 200 | 409 | 409 | 409 |
| **cancel** | 200 | 200 | 200 | 200 | 409 | 409 | 409 |

`404` is checked **before** any of this, so an id that is not yours is
indistinguishable from an id that does not exist.

### Fields

- `pausedAt` — always present, `null` unless paused. Cleared on resume.
- `cancelledAt` — always present, `null` unless cancelled. Never cleared.
- `counts.cancelled` — always present, `0` unless cancelled.

**Cancel while paused keeps both instants.** The earlier one — the pause — is the
campaign's real stop time. We do not clear `pausedAt` on cancel, precisely so you
can tell.

### Error messages you will render

```json
404 {"error":{"code":"not_found","message":"No such campaign."}}
409 {"error":{"code":"conflict","message":"This campaign cannot be paused from its current state."}}
409 {"error":{"code":"conflict","message":"This campaign cannot be resumed from its current state."}}
409 {"error":{"code":"conflict","message":"This campaign cannot be cancelled from its current state."}}
```

### Two behaviours that will surprise you

**Resume returns immediately and dispatches in the background.** It answers
`200` with `status: "sending"` and `pausedAt: null`, and the send continues
after the response. A short campaign may therefore be `sent` a second later, and
a `cancel` you fire straight afterwards will correctly answer `409`. Your §6
script hits this — use a longer list, or read the status you got back.

**`counts.cancelled` is derived, and no message rows are written for cancelled
recipients.** Fan-out creates a message row when it reaches a recipient, so a
campaign cancelled at 30k of 100k has 30k rows and the other 70k have none —
never dispatched, never charged, never queued. `counts.cancelled` is the
remainder. `counts.queued` is `0` after a cancel, so your funnel's shared track is
never double-counted.

The consequence: **`MessageStatus.cancelled` is in the enum and no message row
currently carries it.** If you want them materialised, say so.

### Not built: there is no scheduler

Pause, resume and cancel all work on a `scheduled` campaign and set the right
state. But nothing in the platform dispatches a campaign at its scheduled time
today. "Resuming re-arms the schedule" is a no-op until a scheduler exists. That
is its own piece of work and is not in this batch.

### Contract change you need to make

`UserActivityEventType` gains three values we now emit:
`campaign.pause`, `campaign.resume`, `campaign.cancel`.

---

## 2. Template binding on the submit path

### What is enforced now

On `POST /v1/messages`, on campaign fan-out, and on the batched send path —
identically, from the shared gate:

1. If the destination country's regime requires a registered template, a send
   with no `templateId` is refused.
2. The `body` must be a **legal instantiation** of that template: the registered
   text split on its `{{variables}}`, the remaining fixed segments matched in
   order, **anchored at both ends**.
3. The template must belong to your tenant and to the sender being used, and be
   approved.
4. All of it happens **before any money moves**. `costMinor` is `0` and the
   wallet does not change.

India requires it. The US and the stub regimes do not. It is a property of the
regime, so adding the UAE later is an entry in that registry, not a branch here.

### New failure codes

| `errorCode` | Meaning |
| --- | --- |
| `registered_template_required` | The regime needs a template and none was named |
| `template_body_mismatch` | The body is not an instantiation of the named template |

Both come back as `202` with `status: "failed"` and `costMinor: 0`, like every
other gate refusal.

### What "legal instantiation" means, concretely

Registered template:

```
Hi {{first_name}}, your order {{order_id}} has shipped. Track: https://acme.example.com/track
```

| Body sent | Result |
| --- | --- |
| `Hi Priya, your order 4821 has shipped. Track: https://acme.example.com/track` | **sent** |
| `Hi {{first_name}}, your order {{order_id}} has shipped. Track: https://…` (unfilled) | **sent** — it is still the template's own text |
| `Totally unrelated text.` | `template_body_mismatch` |
| `Hi Priya, … Track: https://… WIN FREE CASH NOW` | `template_body_mismatch` — appended |
| `URGENT! Hi Priya, …` | `template_body_mismatch` — prefixed |
| `Hi Priya, your order 4821 has been cancelled. Track: …` | `template_body_mismatch` — fixed text altered |

A substring check would pass rows 4 and 5. An Indian operator drops both.

### Two carve-outs, stated rather than hidden

- **RCS templates keep their registered text in `rcs_content`, not `body`.** We
  resolve it from there. If a channel ever keeps it somewhere we cannot read, the
  template requirement still applies but the text comparison is skipped —
  refusing every send on a channel because we could not find its registered text
  would be an outage dressed up as compliance.
- **A send with no body of its own is not compared.** An RCS campaign sends no
  body: the carrier holds the approved template and renders it from the variables
  we pass, so what reaches the handset *is* the registered template.

### `Template.registrationId` and `dltCategory` — already built

Your probe concluded these were missing. They are not:

```
POST /v1/templates {"registrationId":"1207161234567890123","dltCategory":"TRANSACTIONAL", …}
→ 201, both returned exactly as sent; both survive a read.
```

**Both keys are omitted entirely when the value is null**, and every seeded
template has null in both — which is what your probe saw. Neither is in
`Template.required`, so omitting them is contract-legal and your generated type
already has them as optional. If you would rather have the keys always present
with `null`, that is a requiredness change on your side.

**Item D confirmed from our side:** nothing generates a registration id, and
approval never overwrites a supplied one. A trigger used to mint
`REG-<HEADER>-0001`; it was removed and there is a test guarding its return.

### `currency: ""` — fixed

Refusals that never reached a rate now report the regime's currency for the
sender's country, falling back to the tenant's. `currency` is a required
non-null string in the contract, so `null` was not available without a change on
your side.

### The `202` — our decision

**We are keeping `202` and we want the description to say so.** A refusal is a
recorded message with an id, a failure code, a segment count and a currency; an
`Error` body carries none of that, and a client that got a `422` would have a
message in its log it could not look up.

Your objection stands though — an SDK keying on the status alone records a
refusal as a success. So please write the endpoint description as:

> **202 means the request was well-formed and we have decided. Read `status`:
> `sent`, `queued` or `rejected`. A `rejected` result is terminal and carries
> `errorCode`.**

A malformed body is still `422`, and that distinction stays meaningful. If you
disagree after reading that, say so — it is your contract, and it is a small
change. But make the call knowing the message id goes away with it.

---

## 3. The programmatic API

### An API key is now a credential on reads

| Route | Scope required |
| --- | --- |
| `GET /v1/messages` | `read:messages` |
| `GET /v1/campaigns`, `/v1/campaigns/{id}`, `/v1/campaigns/{id}/messages` | `read:logs` |
| `GET /v1/analytics`, `/v1/analytics/reports`, `/v1/analytics/reports/{id}` | `read:analytics` |
| `GET/POST/PATCH/DELETE /v1/developer/webhooks*` | `webhooks:manage` |
| `POST /v1/messages` | `send:sms` / `send:rcs`, by the sender's channel |

**`GET /v1/messages/{id}` is in your table and does not exist in the contract.**
There is no single-message route. Polling one message's status is the case you
named, so you probably want it — add it and we will implement it.

**Everything not in that table is session-only** and answers `401` to a key
holding every scope there is: team, billing, wallet, senders, suppressions,
templates, contacts, contact lists, verify services, and the developer key list
itself. Verified live against all of them.

### Status codes

- `401` — no credential, or a key on a route where keys are not accepted.
- `403` — a valid key that does not hold the scope. The message names the missing
  scope: `"This API key does not carry the read:messages scope."`
- An **invalid** key is `401`, never `403`.

### Scope validation at creation

```
POST /v1/developer/api-keys {"scopes":["messages:write"], …}
→ 422 "\"messages:write\" is not a scope. Scopes must be one of: send:sms,
       send:rcs, read:messages, read:analytics, read:logs, webhooks:manage."
```

Case-sensitive: `SEND:SMS` is refused. An empty string is refused. All six
published scopes are accepted — verified by walking `GET /v1/developer/scopes`
and creating a key with each.

### The escalation you asked us to check: it was not there

A `read:messages`-only key **could not send**, and cannot. The send path has
always checked the channel scope — that was the one piece of scope enforcement
that already existed. There is now a test that states it as a security property,
and it is verified live.

`send:sms` does not authorise an RCS send.

### `ApiKeyScope` enum — one row stands in the way

Production holds **8 API keys**. Seven hold only valid scopes. One does not:

```
id          16867d71-3b3f-4df5-ab2e-fbc22a639ab2
name        "api-probe DELETE ME"        ← yours, from your §6 script
tenant      Acme Retail (demo)
environment test
scopes      {messages:write}
status      revoked
```

It is already revoked, so it authenticates nothing. **Our recommendation: make
the enum change now.** If your generated types validate stored scopes on read,
we will rewrite that one row's scopes first — we would rather rewrite than
delete, and we want your nod before changing any key's recorded permissions,
even a revoked one's.

### Two answers from Section 3

- **The IP allowlist is NOT consulted on key authentication.** It stores entries
  and nothing reads them. Same class of defect as the others — a security control
  the UI presents as real. Not fixed in this batch because it was not in your
  checklist; it should be its own item and we think it is a real one.
- **The rate limit IS enforced.** 200/second on live, 20/second on test, per
  tenant, one-second window. Over budget is `429`. Measured under load this week:
  at 128 concurrent senders a single tenant lands at ~192 accepted/second and the
  rest are `429`.

---

## 4. Defects and page totals

**`POST /v1/operator/senders/{id}/approve` on an unknown id is now `404`,** with
the message `"No such sender."` byte-for-byte identical to its `reject` sibling.
The cause was a readiness check that runs only on the approve path letting a bare
"no rows" escape to the middleware.

**`total` on the wallet ledger and the invoice list.**

```
GET /v1/wallet/ledger?limit=2   → {"entries":[…], "total": 15457, "nextCursor":"…"}
GET /v1/billing/invoices?limit=2 → {"invoices":[…], "total": 1, "nextCursor":null}
```

- The ledger's total **follows the `currency` filter**.
- It is **stable across pages** — it describes the filter, not the page.
- It is an exact `count(*)` in the same transaction as the page. **Do not label it
  approximate.** If either table ever grows enough to hurt we will tell you
  before it does.

Add `total` to `LedgerPage` and `InvoicePage` in `openapi.json` — we have it in
the union we generate from.

**`POST /v1/auth/signup` refuses countries we do not operate in:**

```json
422 {"error":{"code":"validation_failed",
     "message":"Relay does not operate in GB yet. Contact us if you need it."}}
```

A stub regime is refused for the same reason as an unknown one — it has no
registration objects, so a sender could never be approved and the customer would
find out after onboarding.

**The two `API PROBE … DELETE ME` tenants are still there, deliberately.** We
have a standing instruction not to delete anything from the production database.
They are inert and no more can be created. If you want them gone, say so
explicitly and we will get it authorised.

---

## 5. GST invoice — nothing built, and why

You asked us to read and reply before you merge anything. Two of your four
questions are not engineering's to answer, and the first one decides the shape of
everything else.

| Question | Answer |
| --- | --- |
| Tax at top-up or at usage? | **Blocked — finance.** We recommend at usage. |
| Serial scope? | **Per financial year, per place of business.** One series today. |
| Supplier tax identity stored? | **No — nothing exists.** We propose configuration, not data. |
| SAC code? | **Blocked — finance.** Routed, not absorbed. |

Checked against production: the only tax-shaped columns anywhere are
`invoices.tax_rate_percent` and `invoices.tax_minor`, and `tax_rate_percent` is
literally `return 18` for INR. There is **1 invoice** in production, so there is
essentially nothing to migrate — you were right that this is the cheap moment.

`GET /v1/tenant` does not exist and a GSTIN that can be written and never read
back is worse than none. We can ship that one route ahead of the rest if you want
to build the Settings form early — say the word.

Full reasoning in `REPLY_gst-invoice_2026-09-05.md`. **Questions 1 and 4 are
blocked on the same person and should go in one ask.**

---

## How this was verified

**Automated suite:** 15 packages, 0 failures, with `-race`.

**Live, against `https://sms-api.saqibsaeed.cloud` after deploy — 102 assertions:**

- 24 for the defects and template binding, including every mismatch shape in the
  table above
- 64 for the full halt matrix across all seven statuses × three actions, every
  `409` message string, and the whole key/scope surface
- 14 for tenant isolation, concurrency and idempotency

Worth calling out from that last group:

- **Tenant isolation:** a freshly signed-up tenant sees none of Acme's messages or
  campaigns, gets `404` (not `409`) on all three halts against Acme's campaign,
  and cannot send from Acme's sender.
- **Concurrency:** eight simultaneous pauses on one campaign → exactly one `200`
  and seven `409`. Forty concurrent sends → forty `202`s, forty distinct message
  ids, identical cost on every one.
- **Idempotency:** a replayed `Idempotency-Key` returns the same message id.

One note on reproducing the throughput check: the sandbox connector rejects any
recipient ending `000` at submit and fails any ending `001` as
`ABSENT_SUBSCRIBER`. A probe that spans those numbers will see them as failures.
They are fixtures, not misses.

---

## Checklist for you

- [ ] Declare `total` on `LedgerPage` and `InvoicePage`.
- [ ] Declare `campaign.pause` / `campaign.resume` / `campaign.cancel` on
      `UserActivityEventType`.
- [ ] Merge PR #2 — it carries `CarrierTemplateRegistration`, the six page
      `total`s, `OperatorLoginSessionResult.token` and the rest. Until it merges
      we serve a union of your `master` and it.
- [ ] Decide `202` vs `422` on a refused send. We recommend `202` plus an
      explicit description.
- [ ] Make the `ApiKeyScope` enum change; tell us whether to rewrite the one
      revoked key first.
- [ ] Add `GET /v1/messages/{id}` if you want per-message polling.
- [ ] Get answers on GST questions 1 and 4.
- [ ] Tell us if you want `MessageStatus.cancelled` materialised on real rows.
- [ ] Tell us if the IP allowlist should be enforced on key auth — we think yes.
