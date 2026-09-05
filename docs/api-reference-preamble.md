## What this document is

**Everything the UI team needs from the backend, in one file.** What we built, what
changed underneath you, what is still open, and then the complete reference for
every operation and schema.

The reference half is **generated** from `openapi/control.json` — the same file the
server is generated from — and the auth column is parsed out of the enforcement
code, so neither can describe an API we do not actually serve.

---

## 1. Read this before you integrate

### 1.1 A refused send is `202`, not a 4xx

`POST /v1/messages` answers `202` whenever the request was well-formed and we
reached a decision. Read `status`:

| `status` | Means |
| --- | --- |
| `sent` / `queued` | Accepted |
| `rejected` | **Refused at submit.** Terminal, carries `errorCode`, `costMinor` is `0` |
| `failed` | Reserved for a message that was accepted and then failed in delivery |

A malformed body is still `422`. A refusal is `202` because it carries a real
message id you can look up afterwards; an error body would not.

### 1.2 The send result and the message log use different status words

`SendMessageResult.status` carries `rejected`. `MessageStatus` — what
`GET /v1/messages` and `GET /v1/messages/{id}` return — does not. So **the same
message reads `rejected` in the send response and `failed` in the log.**

That is a real difference between two enums, not a bug. If you want the
distinction visible in the log too, `MessageStatus` needs `rejected`, and that is
a contract change on your side. We would support it.

### 1.3 Submit-refusal codes

`errorCode` on a `rejected` result:

| Code | Cause |
| --- | --- |
| `registered_template_required` | The destination regime requires a registered template and none was named (India) |
| `template_body_mismatch` | The body is not a legal instantiation of the named template |
| `content_not_allowed` | The country's content rules refuse the body — India's public-shortener ban, today |
| `sender_not_approved` | The sender is not approved for this tenant |
| `sender_not_found` | No such sender on this account |
| `template_not_approved` | Our own review has not passed |
| `carrier_template_not_approved` | The carrier's separate review has not passed (RCS) |
| `sender_template_mismatch` | The template is registered against a different sender |
| `recipient_suppressed` | The recipient is on this tenant's suppression list |
| `invalid_recipient` | Not a valid number for this corridor |
| `insufficient_balance` | The wallet cannot cover the send |
| `no_rate` | No price configured for this country/channel |

---

## 2. Three things that changed underneath you

These will look like regressions the first time you hit them. They are not.

**1. Every India send now needs a `templateId`, and the body must match it.**

India's operators do not judge whether a message looks reasonable. They match its
content against a template registered on DLT and drop everything that does not
match — all of it. So we were accepting, charging for, and reporting as `sent`
messages that could not arrive.

A send to a country whose regime requires a registered template must now name one,
and the body must be a **legal instantiation**: the registered text split on its
`{{variables}}`, the remaining fixed segments matched in order, **anchored at both
ends**. Not a substring check.

Registered template `Hi {{first_name}}, your order {{order_id}} has shipped.`:

| Body sent | Result |
| --- | --- |
| `Hi Priya, your order 4821 has shipped.` | **sent** |
| `Hi {{first_name}}, your order {{order_id}} has shipped.` (unfilled) | **sent** — still the template's own text |
| `Totally unrelated text.` | `template_body_mismatch` |
| `Hi Priya, … has shipped. WIN FREE CASH NOW` | `template_body_mismatch` — appended |
| `URGENT! Hi Priya, …` | `template_body_mismatch` — prefixed |
| `Hi Priya, your order 4821 has been cancelled.` | `template_body_mismatch` — fixed text altered |

Two carve-outs: an RCS template keeps its registered text in `rcs_content` rather
than `body` and we resolve it from there; and a send with **no body of its own** is
not compared, because an RCS campaign sends none — the carrier renders the
approved template from the variables we pass.

**2. `POST /v1/auth/signup` refuses countries we do not operate in.**

`GB` and `AE` now answer `422`. A stub regime is refused for the same reason as an
unknown one: it has no registration objects, so a sender could never be approved
and the customer would find out after onboarding. Stop offering them in the picker,
or render the `422`.

**3. An API key now authenticates on read routes.**

Previously every read with a key was `401`. Now it is `200`, `403` or `401`
depending on scope and route. If anything treats "key + read = 401" as expected,
it will see a different answer.

---

## 3. What we built

### 3.1 Campaign pause, resume and cancel

The three routes that did not exist. There was no brake: a campaign of 900,000
recipients with a wrong link ran to completion and the only available action was
to watch.

The transition matrix, as deployed:

|  | scheduled | queued | sending | paused | sent | failed | cancelled |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **pause** | 200 | 200 | 200 | 409 | 409 | 409 | 409 |
| **resume** | 409 | 409 | 409 | 200 | 409 | 409 | 409 |
| **cancel** | 200 | 200 | 200 | 200 | 409 | 409 | 409 |

`404` is checked **before** any of that, so an id that is not yours is
indistinguishable from one that does not exist.

- `pausedAt` / `cancelledAt` — always present, `null` when unset. `pausedAt` is
  cleared on resume; **cancel keeps both**, because the earlier instant is the
  campaign's real stop time.
- `counts.cancelled` — always present, `0` unless cancelled.

**Two behaviours worth knowing.** Resume returns immediately and dispatches in the
background, so a short campaign may be `sent` a second later and a `cancel` fired
straight after correctly answers `409`. And `counts.cancelled` is **derived** — fan
out writes a message row when it reaches a recipient, so a campaign cancelled at
30k of 100k has 30k rows and the other 70k have none. Writing 70,000 rows to say
so adds nothing the subtraction does not carry. `MessageStatus.cancelled` is
therefore in the enum and carried by no row; say the word if you want them
materialised.

**There is no scheduler.** Pause/resume/cancel all work on a `scheduled` campaign,
but nothing dispatches a campaign at its scheduled time today, so "resuming re-arms
the schedule" is a no-op until one exists.

### 3.2 The programmatic API

A key is a credential on the routes marked in the reference below, and on nothing
else. Absence is deliberate: the team roster, billing, the wallet, suppressions,
contact lists and the developer key list itself stay session-only and answer `401`
to a key holding every scope there is.

Scopes are validated at creation (`422`, naming the offender) and enforced at call
time (`403`, naming the missing scope). `send:sms` does not authorise an RCS send.

**The escalation you asked us to check was not there.** A `read:messages`-only key
could not spend the wallet, and cannot. The send path has always checked the
channel scope.

### 3.3 Defects fixed

- `POST /v1/operator/senders/{id}/approve` on an unknown id answered `500`; now
  `404`, with the message byte-identical to its `reject` sibling.
- `total` on `GET /v1/wallet/ledger` and `GET /v1/billing/invoices`. The ledger's
  total follows the `currency` filter and is stable across pages. It is an exact
  `count(*)` in the same transaction as the page — **do not label it approximate.**
- A refusal reported `currency: ""`. It now reports the regime's currency.
- `POST /v1/developer/webhooks` accepted a body with no `subscribedEvents` and
  minted a signing secret for an endpoint that presented as enabled and was
  permanently silent. Now `422`. **The same hole was on `PATCH`** — it accepted an
  empty subscription *and* did not validate event names at all, so a typo'd event
  was refused on create and stored on update. Both fixed.

### 3.4 GST — nothing built, deliberately

Two of your four questions are finance decisions and the first one decides the
shape of everything else. Answering what we could:

| Question | Answer |
| --- | --- |
| Tax at top-up or at usage? | **Blocked — finance.** We recommend at usage |
| Serial scope? | **Per financial year, per place of business.** One series today |
| Supplier tax identity stored? | **No — nothing exists.** We propose configuration, not data |
| SAC code? | **Blocked — finance.** Routed, not absorbed |

The only tax-shaped columns in the database are `invoices.tax_rate_percent` and
`invoices.tax_minor`, and the rate is literally `return 18` for INR. There is **one
invoice in production**, so there is nothing to migrate.

---

## 4. What you need to do

### 4.1 `make generate` against your `master` does not compile

We checked rather than assumed. Twelve things are missing:

| # | What | Where |
| --- | --- | --- |
| 1–8 | `422` response | `GET` on `/v1/suppressions`, `/v1/support/tickets`, `/v1/conversations`, `/v1/operator/tenants`, `/v1/operator/audit-log`, `/v1/operator/user-activity`, `/v1/operator/support/tickets`, `/v1/operator/approvals` |
| 9 | `422` response | `PATCH /v1/developer/webhooks/{id}` |
| 10 | `requestBody.required: false` | `POST /v1/templates/{id}/carrier-registration` |
| 11 | `AuditAction` enum += `operator.mfa_enabled`, `operator.mfa_disabled` | We emit both |
| 12 | The whole operation | `GET /v1/messages/{id}` — built to your proposed shape, live, undeclared |

Items 1–9 are build breaks. Item 11 is a runtime enum violation. Item 12 is a new
operation and yours to add.

We carry all twelve in the union we generate from, so production is unaffected
either way.

### 4.2 The rest of the checklist

- **Declare `total`** on `LedgerPage` and `InvoicePage`.
- **Declare `campaign.pause` / `campaign.resume` / `campaign.cancel`** on
  `UserActivityEventType` — the three halt endpoints emit them.
- **Merge PR #2.** It carries `CarrierTemplateRegistration`, the six page `total`s,
  `OperatorLoginSessionResult.token` and the rest. Until it merges we serve a union
  of your `master` and it.
- **Land the `ApiKeyScope` enum.** The one offending row is rewritten:
  `16867d71-…` (your revoked `api-probe DELETE ME` key) is now `{send:sms}`. **Zero
  keys in production hold a scope outside the six.**
- **Decide `202` vs `422`** on a refused send. We recommend keeping `202` plus the
  explicit description — the message id is worth more than the status code.
- **Optional:** add `costMinor` and `currency` to `MessageLogEntry`. Polling a
  message to reconcile spend currently gets the status and not the price, while the
  send response it is following up on returned both. We read both columns already.
- **Optional:** add `GET /v1/contacts/{id}` if you want it. Only the list and
  `/v1/contacts/import` exist, so there was nothing to allowlist for a single
  contact.

---

## 5. Known, and not fixed

- **The IP allowlist is stored and never consulted** on key authentication.
  `POST /v1/developer/ip-allowlist` writes entries and the auth path never reads
  them. Agreed as its own item; not done. When you want it, the open question is
  what happens to a key used from an unlisted address — `403` is obvious but it
  locks people out of their own integration, so it wants a deliberate default.
- **Deploys are not zero-downtime.** The binary swap leaves roughly a second where
  the proxy answers `502`. Observed during this batch, not yet addressed.
- **The delivery plane has not started.** SMS resolves to an in-process sandbox.
  The `connections` table already stores every SMPP bind field encrypted — host,
  port, `system_id`, `system_type`, `bind_type`, password, `max_tps`,
  `window_size` — plus health fields. Nothing dials them. That is the item that
  decides whether the product can carry traffic at all.
- **No TRAI time-band enforcement and no DND/NCPR scrubbing.** Our suppression list
  is a tenant-level opt-out and must not be mistaken for the national registry.

---

## 6. How this was verified

**Automated suite:** 15 packages, 0 failures, with `-race`.

**Against the deployed API, 206 assertions:**

- **105 behavioural** — the campaign halt matrix across all seven statuses, every
  `409` message string, the template-binding rules including each mismatch shape
  above, the whole key and scope surface, tenant isolation, concurrency
  (eight simultaneous pauses resolve to exactly one winner; forty concurrent sends
  give forty distinct ids at identical cost), and idempotency.
- **101 reference-conformance** — every documented `GET` route probed for existence
  (**63/63 exist**), and every route this document says accepts a key under a scope
  probed with a key that holds it and one that does not (**38/38 correct**).

Reproduce the second with `make verify-api-reference` in `SMS-BE`.

One fixture quirk if you reproduce the throughput checks: the sandbox connector
rejects any recipient ending `000` at submit and fails any ending `001` as
`ABSENT_SUBSCRIBER`. Those are fixtures, not misses.
