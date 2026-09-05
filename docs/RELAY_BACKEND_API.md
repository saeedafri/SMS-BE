# Relay backend — everything the UI team needs

**Base URL:** `https://sms-api.saqibsaeed.cloud`
**Operations:** 177 across 142 paths
**Schemas:** 227
**Date:** 5 September 2026

Regenerate with `make api-reference`. **Do not hand-edit:** the reference half is
read out of `openapi/control.json` — the same file the server is generated from —
and the auth column is parsed out of `internal/api/key_scopes.go`, so neither can
describe an API we do not actually serve. The narrative half lives in
`docs/api-reference-preamble.md`.

---

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


---

## 7. How authentication works

Two credential kinds, and they are not interchangeable.

**Session token** — from `POST /v1/auth/login`, sent as `Authorization: Bearer <token>`.
Authorised by the user's **role** (`owner`, `admin`, `member`). This is what the
dashboard uses.

**API key** — from `POST /v1/developer/api-keys`, prefixed `sk_live_` or `sk_test_`,
sent the same way. Authorised by **scopes**, not by role. A key is only a
credential on the routes marked below as accepting one; on every other route it
answers `401`, deliberately.

The six scopes: `send:sms`, `send:rcs`, `read:messages`, `read:analytics`,
`read:logs`, `webhooks:manage`. Anything else is refused at key creation with `422`.

| Status | Means |
| --- | --- |
| `401` | No credential, or a key on a route that does not accept keys |
| `403` | A valid credential that lacks the scope or role. The message names the missing scope |

**Operator console** routes (`/v1/operator/*`) use a separate session from
`POST /v1/operator/login` against a separate user table, and may additionally be
restricted by IP allowlist.

---

## 8. Index

#### Authentication & session

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `POST` | [`/v1/auth/login`](#post-v1authlogin) | public |  |
| `POST` | [`/v1/auth/logout`](#post-v1authlogout) | public |  |
| `POST` | [`/v1/auth/mfa/challenge`](#post-v1authmfachallenge) | public |  |
| `POST` | [`/v1/auth/mfa/disable`](#post-v1authmfadisable) | public |  |
| `POST` | [`/v1/auth/mfa/enroll`](#post-v1authmfaenroll) | public |  |
| `POST` | [`/v1/auth/mfa/enroll/confirm`](#post-v1authmfaenrollconfirm) | public |  |
| `PATCH` | [`/v1/auth/password`](#patch-v1authpassword) | public |  |
| `POST` | [`/v1/auth/password/forgot`](#post-v1authpasswordforgot) | public |  |
| `POST` | [`/v1/auth/password/reset`](#post-v1authpasswordreset) | public |  |
| `POST` | [`/v1/auth/signup`](#post-v1authsignup) | public |  |
| `POST` | [`/v1/auth/verify-email/confirm`](#post-v1authverify-emailconfirm) | public |  |
| `POST` | [`/v1/auth/verify-email/resend`](#post-v1authverify-emailresend) | public |  |
| `GET` | [`/v1/me`](#get-v1me) | session |  |
| `PATCH` | [`/v1/me`](#patch-v1me) | session |  |
| `GET` | [`/v1/messages`](#get-v1messages) | session **or** API key (`read:messages`) |  |
| `POST` | [`/v1/messages`](#post-v1messages) | session **or** API key (`send:sms / send:rcs, by the sender's channel`) | Send a single message |
| `GET` | [`/v1/messages/{id}`](#get-v1messagesid) | session **or** API key (`read:messages`) | Read one message |
| `GET` | [`/v1/sessions`](#get-v1sessions) | session |  |
| `DELETE` | [`/v1/sessions/{id}`](#delete-v1sessionsid) | session |  |

#### Messages & sending

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/conversations`](#get-v1conversations) | session |  |
| `GET` | [`/v1/conversations/{id}`](#get-v1conversationsid) | session |  |
| `POST` | [`/v1/conversations/{id}/close`](#post-v1conversationsidclose) | session |  |
| `POST` | [`/v1/conversations/{id}/read`](#post-v1conversationsidread) | session |  |
| `POST` | [`/v1/conversations/{id}/reopen`](#post-v1conversationsidreopen) | session |  |
| `POST` | [`/v1/conversations/{id}/reply`](#post-v1conversationsidreply) | session |  |

#### Campaigns

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/campaigns`](#get-v1campaigns) | session **or** API key (`read:logs`) |  |
| `POST` | [`/v1/campaigns`](#post-v1campaigns) | session |  |
| `POST` | [`/v1/campaigns/estimate`](#post-v1campaignsestimate) | session |  |
| `GET` | [`/v1/campaigns/{id}`](#get-v1campaignsid) | session **or** API key (`read:logs`) |  |
| `POST` | [`/v1/campaigns/{id}/cancel`](#post-v1campaignsidcancel) | session | Stop a campaign for good. Recipients not yet dispatched are cancelled and never charged. |
| `GET` | [`/v1/campaigns/{id}/messages`](#get-v1campaignsidmessages) | session **or** API key (`read:logs`) |  |
| `POST` | [`/v1/campaigns/{id}/pause`](#post-v1campaignsidpause) | session | Hold a sending campaign. No further recipients are dispatched until it is resumed. |
| `POST` | [`/v1/campaigns/{id}/resume`](#post-v1campaignsidresume) | session | Resume a paused campaign from exactly where it stopped. |

#### Automation & journeys

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/automation/journeys`](#get-v1automationjourneys) | session **or** API key (`read:logs`) |  |
| `POST` | [`/v1/automation/journeys`](#post-v1automationjourneys) | session |  |
| `GET` | [`/v1/automation/journeys/{id}`](#get-v1automationjourneysid) | session **or** API key (`read:logs`) |  |
| `PATCH` | [`/v1/automation/journeys/{id}`](#patch-v1automationjourneysid) | session |  |
| `POST` | [`/v1/automation/journeys/{id}/activate`](#post-v1automationjourneysidactivate) | session |  |
| `POST` | [`/v1/automation/journeys/{id}/archive`](#post-v1automationjourneysidarchive) | session |  |
| `POST` | [`/v1/automation/journeys/{id}/pause`](#post-v1automationjourneysidpause) | session |  |
| `POST` | [`/v1/automation/journeys/{id}/resume`](#post-v1automationjourneysidresume) | session |  |
| `POST` | [`/v1/automation/journeys/{id}/unarchive`](#post-v1automationjourneysidunarchive) | session | Restore an archived journey. A journey that had never been activated returns to draft; one that had already ru |

#### Audience & suppression

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/contact-lists`](#get-v1contact-lists) | session |  |
| `POST` | [`/v1/contact-lists`](#post-v1contact-lists) | session |  |
| `GET` | [`/v1/contact-lists/{id}`](#get-v1contact-listsid) | session |  |
| `PATCH` | [`/v1/contact-lists/{id}`](#patch-v1contact-listsid) | session |  |
| `DELETE` | [`/v1/contact-lists/{id}`](#delete-v1contact-listsid) | session |  |
| `DELETE` | [`/v1/contact-lists/{id}/members/{contactId}`](#delete-v1contact-listsidmemberscontactid) | session |  |
| `GET` | [`/v1/contacts`](#get-v1contacts) | session **or** API key (`read:messages`) |  |
| `POST` | [`/v1/contacts/import`](#post-v1contactsimport) | session |  |
| `GET` | [`/v1/suppressions`](#get-v1suppressions) | session |  |
| `POST` | [`/v1/suppressions`](#post-v1suppressions) | session |  |
| `DELETE` | [`/v1/suppressions/{identity}`](#delete-v1suppressionsidentity) | session |  |

#### Senders, templates & compliance

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `POST` | [`/v1/rcs/capabilities`](#post-v1rcscapabilities) | session | Ask the RCS carrier which of these handsets can receive RCS |
| `GET` | [`/v1/registrations`](#get-v1registrations) | session |  |
| `POST` | [`/v1/registrations`](#post-v1registrations) | session |  |
| `GET` | [`/v1/registrations/{id}`](#get-v1registrationsid) | session |  |
| `GET` | [`/v1/sender-ids`](#get-v1sender-ids) | session **or** API key (`read:messages`) |  |
| `POST` | [`/v1/sender-ids`](#post-v1sender-ids) | session |  |
| `GET` | [`/v1/sender-ids/{id}`](#get-v1sender-idsid) | session **or** API key (`read:messages`) |  |
| `PATCH` | [`/v1/sender-ids/{id}`](#patch-v1sender-idsid) | session | Correct a sender that has not been verified yet |
| `DELETE` | [`/v1/sender-ids/{id}`](#delete-v1sender-idsid) | session | Remove a sender that nothing is using |
| `POST` | [`/v1/sender-ids/{id}/voice-call`](#post-v1sender-idsidvoice-call) | session |  |
| `POST` | [`/v1/sender-ids/{id}/voice-code`](#post-v1sender-idsidvoice-code) | session |  |
| `GET` | [`/v1/templates`](#get-v1templates) | session **or** API key (`read:messages`) |  |
| `POST` | [`/v1/templates`](#post-v1templates) | session |  |
| `GET` | [`/v1/templates/{id}`](#get-v1templatesid) | session **or** API key (`read:messages`) |  |
| `POST` | [`/v1/templates/{id}/carrier-registration`](#post-v1templatesidcarrier-registration) | session | Submit an RCS template to the carrier for approval, or attach a code from their portal |
| `GET` | [`/v1/verify/services`](#get-v1verifyservices) | session |  |
| `POST` | [`/v1/verify/services`](#post-v1verifyservices) | session |  |
| `GET` | [`/v1/verify/services/{id}`](#get-v1verifyservicesid) | session |  |
| `PATCH` | [`/v1/verify/services/{id}`](#patch-v1verifyservicesid) | session |  |
| `GET` | [`/v1/verify/services/{id}/analytics`](#get-v1verifyservicesidanalytics) | session |  |
| `GET` | [`/v1/verify/services/{id}/attempts`](#get-v1verifyservicesidattempts) | session |  |
| `POST` | [`/v1/verify/services/{id}/verifications`](#post-v1verifyservicesidverifications) | session |  |
| `POST` | [`/v1/verify/services/{id}/verifications/{vid}/check`](#post-v1verifyservicesidverificationsvidcheck) | session |  |

#### Billing & wallet

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `POST` | [`/v1/billing/estimate`](#post-v1billingestimate) | session |  |
| `GET` | [`/v1/billing/invoices`](#get-v1billinginvoices) | session |  |
| `GET` | [`/v1/billing/invoices/{id}`](#get-v1billinginvoicesid) | session |  |
| `GET` | [`/v1/billing/usage`](#get-v1billingusage) | session |  |
| `GET` | [`/v1/pricing`](#get-v1pricing) | session |  |
| `GET` | [`/v1/wallet/auto-recharge`](#get-v1walletauto-recharge) | session |  |
| `PUT` | [`/v1/wallet/auto-recharge`](#put-v1walletauto-recharge) | session |  |
| `GET` | [`/v1/wallet/balances`](#get-v1walletbalances) | session |  |
| `GET` | [`/v1/wallet/ledger`](#get-v1walletledger) | session |  |
| `GET` | [`/v1/wallet/payment-methods`](#get-v1walletpayment-methods) | session |  |
| `POST` | [`/v1/wallet/payment-methods`](#post-v1walletpayment-methods) | session |  |
| `DELETE` | [`/v1/wallet/payment-methods/{id}`](#delete-v1walletpayment-methodsid) | session |  |
| `POST` | [`/v1/wallet/payment-methods/{id}/default`](#post-v1walletpayment-methodsiddefault) | session |  |
| `POST` | [`/v1/wallet/topup`](#post-v1wallettopup) | session |  |

#### Developer API

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/developer/api-keys`](#get-v1developerapi-keys) | session |  |
| `POST` | [`/v1/developer/api-keys`](#post-v1developerapi-keys) | session |  |
| `DELETE` | [`/v1/developer/api-keys/{id}`](#delete-v1developerapi-keysid) | session |  |
| `POST` | [`/v1/developer/api-keys/{id}/rotate`](#post-v1developerapi-keysidrotate) | session |  |
| `GET` | [`/v1/developer/ip-allowlist`](#get-v1developerip-allowlist) | session |  |
| `POST` | [`/v1/developer/ip-allowlist`](#post-v1developerip-allowlist) | session |  |
| `DELETE` | [`/v1/developer/ip-allowlist/{id}`](#delete-v1developerip-allowlistid) | session |  |
| `GET` | [`/v1/developer/rate-limit`](#get-v1developerrate-limit) | session |  |
| `GET` | [`/v1/developer/scopes`](#get-v1developerscopes) | session |  |
| `GET` | [`/v1/developer/webhooks`](#get-v1developerwebhooks) | session **or** API key (`webhooks:manage`) |  |
| `POST` | [`/v1/developer/webhooks`](#post-v1developerwebhooks) | session **or** API key (`webhooks:manage`) |  |
| `GET` | [`/v1/developer/webhooks/{id}`](#get-v1developerwebhooksid) | session **or** API key (`webhooks:manage`) |  |
| `PATCH` | [`/v1/developer/webhooks/{id}`](#patch-v1developerwebhooksid) | session **or** API key (`webhooks:manage`) |  |
| `DELETE` | [`/v1/developer/webhooks/{id}`](#delete-v1developerwebhooksid) | session **or** API key (`webhooks:manage`) |  |
| `GET` | [`/v1/developer/webhooks/{id}/events`](#get-v1developerwebhooksidevents) | session **or** API key (`webhooks:manage`) |  |
| `POST` | [`/v1/developer/webhooks/{id}/events/{eventId}/resend`](#post-v1developerwebhooksideventseventidresend) | session **or** API key (`webhooks:manage`) |  |
| `POST` | [`/v1/developer/webhooks/{id}/test-event`](#post-v1developerwebhooksidtest-event) | session **or** API key (`webhooks:manage`) |  |

#### Analytics & reporting

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/analytics`](#get-v1analytics) | session **or** API key (`read:analytics`) |  |
| `GET` | [`/v1/analytics/reports`](#get-v1analyticsreports) | session **or** API key (`read:analytics`) |  |
| `POST` | [`/v1/analytics/reports`](#post-v1analyticsreports) | session |  |
| `PATCH` | [`/v1/analytics/reports/{id}`](#patch-v1analyticsreportsid) | session |  |
| `DELETE` | [`/v1/analytics/reports/{id}`](#delete-v1analyticsreportsid) | session |  |

#### Team & tenant settings

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/alerts`](#get-v1alerts) | session |  |
| `PATCH` | [`/v1/alerts`](#patch-v1alerts) | session |  |
| `GET` | [`/v1/sso`](#get-v1sso) | session |  |
| `PUT` | [`/v1/sso`](#put-v1sso) | session |  |
| `GET` | [`/v1/support/tickets`](#get-v1supporttickets) | session |  |
| `POST` | [`/v1/support/tickets`](#post-v1supporttickets) | session |  |
| `GET` | [`/v1/support/tickets/{id}`](#get-v1supportticketsid) | session |  |
| `POST` | [`/v1/support/tickets/{id}/messages`](#post-v1supportticketsidmessages) | session |  |
| `GET` | [`/v1/team`](#get-v1team) | session |  |
| `POST` | [`/v1/team/invite`](#post-v1teaminvite) | session |  |
| `PATCH` | [`/v1/team/{id}`](#patch-v1teamid) | session |  |
| `DELETE` | [`/v1/team/{id}`](#delete-v1teamid) | session |  |
| `PATCH` | [`/v1/tenant`](#patch-v1tenant) | session |  |

#### Operator console

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/operator/abuse-queue`](#get-v1operatorabuse-queue) | operator session |  |
| `GET` | [`/v1/operator/approvals`](#get-v1operatorapprovals) | operator session |  |
| `GET` | [`/v1/operator/audit-log`](#get-v1operatoraudit-log) | operator session |  |
| `GET` | [`/v1/operator/connections`](#get-v1operatorconnections) | operator session | List operator SMPP connections |
| `POST` | [`/v1/operator/connections`](#post-v1operatorconnections) | operator session | Add an operator SMPP connection |
| `GET` | [`/v1/operator/connections/{id}`](#get-v1operatorconnectionsid) | operator session |  |
| `PATCH` | [`/v1/operator/connections/{id}`](#patch-v1operatorconnectionsid) | operator session | Change a connection's settings |
| `DELETE` | [`/v1/operator/connections/{id}`](#delete-v1operatorconnectionsid) | operator session | Remove a connection |
| `POST` | [`/v1/operator/connections/{id}/disable`](#post-v1operatorconnectionsiddisable) | operator session | Traffic on every corridor whose route points at this connection falls through to the next priority. |
| `POST` | [`/v1/operator/connections/{id}/enable`](#post-v1operatorconnectionsidenable) | operator session |  |
| `POST` | [`/v1/operator/connections/{id}/test`](#post-v1operatorconnectionsidtest) | operator session | Attempt a bind without enabling the connection |
| `POST` | [`/v1/operator/login`](#post-v1operatorlogin) | public |  |
| `POST` | [`/v1/operator/login/mfa`](#post-v1operatorloginmfa) | public | Complete an operator sign-in with a second factor |
| `GET` | [`/v1/operator/margin`](#get-v1operatormargin) | operator session |  |
| `GET` | [`/v1/operator/me`](#get-v1operatorme) | operator session |  |
| `POST` | [`/v1/operator/mfa/confirm`](#post-v1operatormfaconfirm) | operator session | Confirm enrolment with a code from the authenticator |
| `POST` | [`/v1/operator/mfa/disable`](#post-v1operatormfadisable) | operator session | Turn off the second factor |
| `POST` | [`/v1/operator/mfa/enroll`](#post-v1operatormfaenroll) | operator session | Start second-factor enrolment for the signed-in operator |
| `GET` | [`/v1/operator/rates`](#get-v1operatorrates) | operator session |  |
| `PATCH` | [`/v1/operator/rates/default`](#patch-v1operatorratesdefault) | operator session |  |
| `POST` | [`/v1/operator/rates/overrides`](#post-v1operatorratesoverrides) | operator session |  |
| `PATCH` | [`/v1/operator/rates/overrides/{id}`](#patch-v1operatorratesoverridesid) | operator session |  |
| `DELETE` | [`/v1/operator/rates/overrides/{id}`](#delete-v1operatorratesoverridesid) | operator session |  |
| `POST` | [`/v1/operator/registrations/{id}/approve`](#post-v1operatorregistrationsidapprove) | operator session |  |
| `POST` | [`/v1/operator/registrations/{id}/reject`](#post-v1operatorregistrationsidreject) | operator session |  |
| `GET` | [`/v1/operator/routes`](#get-v1operatorroutes) | operator session |  |
| `POST` | [`/v1/operator/routes`](#post-v1operatorroutes) | operator session | Add a route to a corridor |
| `DELETE` | [`/v1/operator/routes/{id}`](#delete-v1operatorroutesid) | operator session | Remove a route |
| `POST` | [`/v1/operator/routes/{id}/disable`](#post-v1operatorroutesiddisable) | operator session |  |
| `POST` | [`/v1/operator/routes/{id}/enable`](#post-v1operatorroutesidenable) | operator session |  |
| `POST` | [`/v1/operator/routes/{id}/move-down`](#post-v1operatorroutesidmove-down) | operator session |  |
| `POST` | [`/v1/operator/routes/{id}/move-up`](#post-v1operatorroutesidmove-up) | operator session |  |
| `POST` | [`/v1/operator/senders/{id}/approve`](#post-v1operatorsendersidapprove) | operator session |  |
| `POST` | [`/v1/operator/senders/{id}/reject`](#post-v1operatorsendersidreject) | operator session |  |
| `GET` | [`/v1/operator/support/tickets`](#get-v1operatorsupporttickets) | operator session |  |
| `GET` | [`/v1/operator/support/tickets/{id}`](#get-v1operatorsupportticketsid) | operator session |  |
| `POST` | [`/v1/operator/support/tickets/{id}/messages`](#post-v1operatorsupportticketsidmessages) | operator session |  |
| `POST` | [`/v1/operator/support/tickets/{id}/reopen`](#post-v1operatorsupportticketsidreopen) | operator session |  |
| `POST` | [`/v1/operator/support/tickets/{id}/resolve`](#post-v1operatorsupportticketsidresolve) | operator session |  |
| `POST` | [`/v1/operator/templates/{id}/approve`](#post-v1operatortemplatesidapprove) | operator session |  |
| `POST` | [`/v1/operator/templates/{id}/reject`](#post-v1operatortemplatesidreject) | operator session |  |
| `GET` | [`/v1/operator/tenants`](#get-v1operatortenants) | operator session |  |
| `GET` | [`/v1/operator/tenants/{id}`](#get-v1operatortenantsid) | operator session |  |
| `POST` | [`/v1/operator/tenants/{id}/dismiss-flag`](#post-v1operatortenantsiddismiss-flag) | operator session |  |
| `POST` | [`/v1/operator/tenants/{id}/flag-abuse`](#post-v1operatortenantsidflag-abuse) | operator session |  |
| `POST` | [`/v1/operator/tenants/{id}/reinstate`](#post-v1operatortenantsidreinstate) | operator session |  |
| `POST` | [`/v1/operator/tenants/{id}/suspend`](#post-v1operatortenantsidsuspend) | operator session |  |
| `POST` | [`/v1/operator/tenants/{id}/throttle`](#post-v1operatortenantsidthrottle) | operator session | Throttle a tenant to a fixed send rate |
| `GET` | [`/v1/operator/usage`](#get-v1operatorusage) | operator session |  |
| `GET` | [`/v1/operator/user-activity`](#get-v1operatoruser-activity) | operator session |  |

#### Other

| Method | Path | Auth | Summary |
| --- | --- | --- | --- |
| `GET` | [`/v1/data-retention`](#get-v1data-retention) | session |  |
| `PATCH` | [`/v1/data-retention`](#patch-v1data-retention) | session |  |


---

## 9. Operations

### Authentication & session

#### <a id="post-v1authlogin"></a>`POST /v1/auth/login`

**Auth:** public

**Request body** (required) — [`LoginRequest`](#loginrequest)

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |
| `password` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`LoginResult`](#loginresult) — Authenticated session, or an MFA challenge |
| `401` | [`Error`](#error) — Invalid credentials |
| `422` | [`Error`](#error) — Validation error |


#### <a id="post-v1authlogout"></a>`POST /v1/auth/logout`

**Auth:** public

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Session cleared |


#### <a id="post-v1authmfachallenge"></a>`POST /v1/auth/mfa/challenge`

**Auth:** public

**Request body** (required) — [`MfaChallengeRequest`](#mfachallengerequest)

| Field | Type | Required |
| --- | --- | --- |
| `challengeToken` | `string` | **yes** |
| `code` | `string` | **yes** |
| `method` | `totp` \| `recovery_code` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AuthSession`](#authsession) — Second factor verified; authenticated session |
| `401` | [`Error`](#error) — Wrong code |
| `410` | [`Error`](#error) — Challenge expired or already used |


#### <a id="post-v1authmfadisable"></a>`POST /v1/auth/mfa/disable`

**Auth:** public

**Request body** (required) — [`MfaCodeRequest`](#mfacoderequest)

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — MFA disabled |
| `401` | [`Error`](#error) — Wrong code |


#### <a id="post-v1authmfaenroll"></a>`POST /v1/auth/mfa/enroll`

**Auth:** public

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MfaEnrollment`](#mfaenrollment) — TOTP enrollment secret + provisioning material |


#### <a id="post-v1authmfaenrollconfirm"></a>`POST /v1/auth/mfa/enroll/confirm`

**Auth:** public

**Request body** (required) — [`MfaCodeRequest`](#mfacoderequest)

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MfaRecoveryCodes`](#mfarecoverycodes) — MFA activated; one-time recovery codes |
| `401` | [`Error`](#error) — Wrong code |
| `409` | [`Error`](#error) — MFA already enabled |


#### <a id="patch-v1authpassword"></a>`PATCH /v1/auth/password`

**Auth:** public

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `currentPassword` | `string` | **yes** |
| `newPassword` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Password updated |
| `401` | [`Error`](#error) — Current password is incorrect |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="post-v1authpasswordforgot"></a>`POST /v1/auth/password/forgot`

**Auth:** public

**Request body** (required) — [`ForgotPasswordRequest`](#forgotpasswordrequest)

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Reset link dispatched if the account exists (anti-enumeration) |
| `429` | [`Error`](#error) — Too many requests, reset is throttled |


#### <a id="post-v1authpasswordreset"></a>`POST /v1/auth/password/reset`

**Auth:** public

**Request body** (required) — [`ResetPasswordRequest`](#resetpasswordrequest)

| Field | Type | Required |
| --- | --- | --- |
| `token` | `string` | **yes** |
| `password` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Password changed |
| `422` | [`Error`](#error) — Invalid, expired, or used token, or a weak password |


#### <a id="post-v1authsignup"></a>`POST /v1/auth/signup`

**Auth:** public

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `X-Invite-Code` | header | `string` | no |

**Request body** (required) — [`SignupRequest`](#signuprequest)

| Field | Type | Required |
| --- | --- | --- |
| `fullName` | `string` | **yes** |
| `email` | `string(email)` | **yes** |
| `password` | `string` | **yes** |
| `orgName` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AuthSession`](#authsession) — Authenticated session for the new account |
| `403` | [`Error`](#error) — Signup is invite-only on this deployment |
| `409` | [`Error`](#error) — Email already registered |
| `422` | [`Error`](#error) — Validation error |


#### <a id="post-v1authverify-emailconfirm"></a>`POST /v1/auth/verify-email/confirm`

**Auth:** public

**Request body** (required) — [`ConfirmEmailRequest`](#confirmemailrequest)

| Field | Type | Required |
| --- | --- | --- |
| `token` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Email verified |
| `400` | [`Error`](#error) — Invalid or expired token |
| `422` | [`Error`](#error) — Missing token |


#### <a id="post-v1authverify-emailresend"></a>`POST /v1/auth/verify-email/resend`

**Auth:** public

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Verification email dispatched |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `429` | [`Error`](#error) — Too many requests — resend is throttled |


#### <a id="get-v1me"></a>`GET /v1/me`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Me`](#me) — Current authenticated user, tenant, and capability set |
| `401` | [`Error`](#error) — Missing or invalid bearer token |


#### <a id="patch-v1me"></a>`PATCH /v1/me`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Me`](#me) — Updated user |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1messages"></a>`GET /v1/messages`

**Auth:** session **or** API key (`read:messages`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `status` | query | [`MessageStatus`](#messagestatus) | no |
| `channel` | query | [`ChannelId`](#channelid) | no |
| `errorClass` | query | [`MessageErrorClass`](#messageerrorclass) | no |
| `fraudFlag` | query | [`MessageFraudFlag`](#messagefraudflag) | no |
| `campaignId` | query | `string(uuid)` | no |
| `journeyId` | query | `string` | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MessageLogPage`](#messagelogpage) — Messages across every campaign |


#### <a id="post-v1messages"></a>`POST /v1/messages`

Sends one message immediately. Authenticate with an API key (sk_live_... or sk_test_...) carrying the send scope for the sender's channel, or with a dashboard session. The message is recorded in the logs either way, so a rejected send is inspectable rather than silent. Supply an `Idempotency-Key` header to make a retry safe: the first request with a given key is sent, and every later request carrying the same key returns that first result unchanged instead of sending again. A network timeout is therefore safe to retry with the same key. Keys are scoped to the tenant and are remembered for at least 24 hours.

**Auth:** session **or** API key (`send:sms / send:rcs, by the sender's channel`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `Idempotency-Key` | header | `string` | no |

**Request body** (required) — [`SendMessageRequest`](#sendmessagerequest)

| Field | Type | Required |
| --- | --- | --- |
| `senderId` | `string(uuid)` | **yes** |
| `to` | `string` | **yes** |
| `body` | `string` | **yes** |
| `variables` | `object` \| `null` | no |
| `templateId` | `string(uuid)` \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `202` | [`SendMessageResult`](#sendmessageresult) — The request was well-formed and we have decided. Read `status`: `queued` or `sent` means accepted; `rejected` means refused at submit — terminal, carrying `errorCode` and a `costMinor` of 0, with nothing debited. `failed` is not a submit outcome and is retained in the enum only for compatibility: it means a message we accepted later failed in delivery. A refusal is a 202 rather than a 4xx because it carries a real message id that is inspectable in the logs; a malformed body is still 422. |
| `401` | [`Error`](#error) — Missing or invalid credential |
| `403` | [`Error`](#error) — The API key does not carry the scope for this channel |
| `422` | [`Error`](#error) — The request could not be turned into a send |
| `429` | [`Error`](#error) — The tenant's send rate limit for this key's environment was exceeded |


#### <a id="get-v1messagesid"></a>`GET /v1/messages/{id}`

One message's current state, including a submit-time refusal and its errorCode — which is why a refusal carries a real message id. Authorised by read:messages, the same scope as the list, and scoped to the caller's tenant, so another tenant's id is a 404 rather than a leak.

**Auth:** session **or** API key (`read:messages`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MessageLogEntry`](#messagelogentry) — The message |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — The API key does not carry the read:messages scope |
| `404` | [`Error`](#error) — No such message |


#### <a id="get-v1sessions"></a>`GET /v1/sessions`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Session`](#session)[] — Active sessions for the current user |
| `401` | [`Error`](#error) — Missing or invalid bearer token |


#### <a id="delete-v1sessionsid"></a>`DELETE /v1/sessions/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Revoked |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `404` | [`Error`](#error) — No such session |


### Messages & sending

#### <a id="get-v1conversations"></a>`GET /v1/conversations`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `channel` | query | [`ConversationChannelId`](#conversationchannelid) | no |
| `status` | query | [`ConversationStatus`](#conversationstatus) | no |
| `unread` | query | `boolean` | no |
| `limit` | query | `integer` | no |
| `cursor` | query | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConversationPage`](#conversationpage) — The caller's own tenant's conversations, most recently active first |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="get-v1conversationsid"></a>`GET /v1/conversations/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConversationDetail`](#conversationdetail) — The conversation with its full message thread |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such conversation |


#### <a id="post-v1conversationsidclose"></a>`POST /v1/conversations/{id}/close`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConversationDetail`](#conversationdetail) — The conversation, closed |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such conversation |


#### <a id="post-v1conversationsidread"></a>`POST /v1/conversations/{id}/read`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConversationDetail`](#conversationdetail) — The conversation, marked read |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such conversation |


#### <a id="post-v1conversationsidreopen"></a>`POST /v1/conversations/{id}/reopen`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConversationDetail`](#conversationdetail) — The conversation, reopened |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such conversation |


#### <a id="post-v1conversationsidreply"></a>`POST /v1/conversations/{id}/reply`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`NewConversationReplyRequest`](#newconversationreplyrequest)

| Field | Type | Required |
| --- | --- | --- |
| `body` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConversationDetail`](#conversationdetail) — The conversation with the new outbound reply appended |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such conversation |
| `422` | [`Error`](#error) — Empty body, or the contact is currently suppressed on this channel |


### Campaigns

#### <a id="get-v1campaigns"></a>`GET /v1/campaigns`

**Auth:** session **or** API key (`read:logs`)

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Campaign`](#campaign)[] — Campaigns |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="post-v1campaigns"></a>`POST /v1/campaigns`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `Idempotency-Key` | header | `string` | no |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `listId` | `string` | no |
| `senderId` | `string` | **yes** |
| `templateId` | `string` | **yes** |
| `retryOf` | `string` | no |
| `fallback` | [`CampaignFallback`](#campaignfallback) \| `null` | no |
| `scheduledAt` | `string(date-time)` \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Campaign`](#campaign) — Created |
| `401` | [`Error`](#error) — Unauthenticated |
| `402` | [`Error`](#error) — Insufficient wallet balance for the estimated cost |
| `422` | [`Error`](#error) — Validation error |


#### <a id="post-v1campaignsestimate"></a>`POST /v1/campaigns/estimate`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `listId` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `templateId` | `string` | **yes** |
| `fallback` | [`CampaignFallback`](#campaignfallback) \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`CampaignEstimate`](#campaignestimate) — Estimate |
| `401` | [`Error`](#error) — Unauthenticated |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1campaignsid"></a>`GET /v1/campaigns/{id}`

**Auth:** session **or** API key (`read:logs`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Campaign`](#campaign) — The campaign |
| `404` | [`Error`](#error) — Not found |


#### <a id="post-v1campaignsidcancel"></a>`POST /v1/campaigns/{id}/cancel`

Stop a campaign for good. Recipients not yet dispatched are cancelled and never charged.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Campaign`](#campaign) — The campaign, now cancelled. |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — The campaign has already finished. |


#### <a id="get-v1campaignsidmessages"></a>`GET /v1/campaigns/{id}/messages`

**Auth:** session **or** API key (`read:logs`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |
| `status` | query | [`MessageStatus`](#messagestatus) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MessagePage`](#messagepage) — Messages |
| `404` | [`Error`](#error) — Not found |


#### <a id="post-v1campaignsidpause"></a>`POST /v1/campaigns/{id}/pause`

Hold a sending campaign. No further recipients are dispatched until it is resumed.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Campaign`](#campaign) — The campaign, now paused. |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — The campaign is not sending, so there is nothing to hold. |


#### <a id="post-v1campaignsidresume"></a>`POST /v1/campaigns/{id}/resume`

Resume a paused campaign from exactly where it stopped.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Campaign`](#campaign) — The campaign, sending again. |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — The campaign is not paused. |


### Automation & journeys

#### <a id="get-v1automationjourneys"></a>`GET /v1/automation/journeys`

**Auth:** session **or** API key (`read:logs`)

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey)[] — Journeys |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="post-v1automationjourneys"></a>`POST /v1/automation/journeys`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `trigger` | [`JourneyTrigger`](#journeytrigger) | **yes** |
| `steps` | [`JourneyStep`](#journeystep)[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Journey`](#journey) — Created (always starts draft) |
| `401` | [`Error`](#error) — Unauthenticated |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1automationjourneysid"></a>`GET /v1/automation/journeys/{id}`

**Auth:** session **or** API key (`read:logs`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`JourneyDetail`](#journeydetail) — The journey with its derived funnel |
| `404` | [`Error`](#error) — Not found |


#### <a id="patch-v1automationjourneysid"></a>`PATCH /v1/automation/journeys/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | no |
| `trigger` | [`JourneyTrigger`](#journeytrigger) | no |
| `steps` | [`JourneyStep`](#journeystep)[] | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey) — Updated |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — Not found |
| `422` | [`Error`](#error) — Validation error, or steps/trigger edited on a non-draft journey |


#### <a id="post-v1automationjourneysidactivate"></a>`POST /v1/automation/journeys/{id}/activate`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey) — Now active |
| `404` | [`Error`](#error) — Not found |
| `422` | [`Error`](#error) — Only a draft journey can be activated |


#### <a id="post-v1automationjourneysidarchive"></a>`POST /v1/automation/journeys/{id}/archive`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey) — Now archived |
| `404` | [`Error`](#error) — Not found |
| `422` | [`Error`](#error) — Already archived |


#### <a id="post-v1automationjourneysidpause"></a>`POST /v1/automation/journeys/{id}/pause`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey) — Now paused |
| `404` | [`Error`](#error) — Not found |
| `422` | [`Error`](#error) — Only an active journey can be paused |


#### <a id="post-v1automationjourneysidresume"></a>`POST /v1/automation/journeys/{id}/resume`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey) — Now active again |
| `404` | [`Error`](#error) — Not found |
| `422` | [`Error`](#error) — Only a paused journey can be resumed |


#### <a id="post-v1automationjourneysidunarchive"></a>`POST /v1/automation/journeys/{id}/unarchive`

Restore an archived journey. A journey that had never been activated returns to draft; one that had already run returns to paused, so sending never resumes without an explicit resume.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Journey`](#journey) — Restored to draft (never activated) or paused (previously active) |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — Not found |
| `422` | [`Error`](#error) — Only an archived journey can be unarchived |


### Audience & suppression

#### <a id="get-v1contact-lists"></a>`GET /v1/contact-lists`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ContactList`](#contactlist)[] — Lists for the tenant |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="post-v1contact-lists"></a>`POST /v1/contact-lists`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`ContactList`](#contactlist) — Created |
| `401` | [`Error`](#error) — Unauthenticated |
| `422` | [`Error`](#error) — Invalid |


#### <a id="get-v1contact-listsid"></a>`GET /v1/contact-lists/{id}`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ContactList`](#contactlist) — List |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — Not found |


#### <a id="patch-v1contact-listsid"></a>`PATCH /v1/contact-lists/{id}`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ContactList`](#contactlist) — Renamed |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — Not found |


#### <a id="delete-v1contact-listsid"></a>`DELETE /v1/contact-lists/{id}`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Deleted |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — Not found |


#### <a id="delete-v1contact-listsidmemberscontactid"></a>`DELETE /v1/contact-lists/{id}/members/{contactId}`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Removed |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — Not found |


#### <a id="get-v1contacts"></a>`GET /v1/contacts`

**Auth:** session **or** API key (`read:messages`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `listId` | query | `string` | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ContactPage`](#contactpage) — Contacts |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="post-v1contactsimport"></a>`POST /v1/contacts/import`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `Idempotency-Key` | header | `string` | no |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `targetListId` | `string` | no |
| `newListName` | `string` | no |
| `defaultCountry` | [`CountryCode`](#countrycode) | **yes** |
| `consentBasis` | `object` | **yes** |
| `rows` | `object`[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ImportSummary`](#importsummary) — Import applied |
| `401` | [`Error`](#error) — Unauthenticated |
| `422` | [`Error`](#error) — Invalid |


#### <a id="get-v1suppressions"></a>`GET /v1/suppressions`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SuppressionPage`](#suppressionpage) — Suppression list |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="post-v1suppressions"></a>`POST /v1/suppressions`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `msisdns` | `string`[] | no |
| `emails` | `string`[] | no |
| `reason` | [`SuppressionReason`](#suppressionreason) | **yes** |
| `note` | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | `object` — Add summary |

`200` fields:

| Field | Type | Required |
| --- | --- | --- |
| `created` | `integer` | **yes** |
| `skipped` | `integer` | **yes** |
| `invalid` | `integer` | **yes** |


#### <a id="delete-v1suppressionsidentity"></a>`DELETE /v1/suppressions/{identity}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `identity` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Removed |
| `403` | [`Error`](#error) — Protected reason cannot be removed |


### Senders, templates & compliance

#### <a id="post-v1rcscapabilities"></a>`POST /v1/rcs/capabilities`

Capability discovery against the deployment's configured RCS carrier (Airtel IQ or Vi RBM). This is the answer that should drive RCS-versus-SMS fallback: an RCS message to a handset that cannot display it produces no message, no error, and a charge.

Features are only returned when EXACTLY ONE msisdn is checked. Neither carrier's bulk endpoint returns them — bulk answers reachability and nothing else — so featuresIncluded says which kind of answer this is rather than leaving the client to infer it from an empty array.

Feature names are the Google RBM vocabulary and are passed through from the carrier unmapped, so a name not listed in RcsFeature may still appear and clients must not treat the enum as exhaustive.

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `msisdns` | `string`[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`RcsCapabilityReport`](#rcscapabilityreport) — Capability answer |
| `400` | [`Error`](#error) — Empty list, or more than 10,000 numbers |
| `401` | [`Error`](#error) — Unauthenticated |
| `502` | [`Error`](#error) — The RCS carrier could not be reached or refused the request |
| `503` | [`Error`](#error) — This deployment has no RCS carrier configured |


#### <a id="get-v1registrations"></a>`GET /v1/registrations`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Registration`](#registration)[] — Registrations for the caller's tenant |


#### <a id="post-v1registrations"></a>`POST /v1/registrations`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `objectKey` | `string` | **yes** |
| `fields` | `object` | **yes** |
| `registrationId` | `string` \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Registration`](#registration) — Created, pending review |
| `403` | [`Error`](#error) — Member role has no access to compliance. |
| `409` | [`Error`](#error) — Dependency unmet — would be blocked |
| `422` | [`Error`](#error) — Field validation failed |


#### <a id="get-v1registrationsid"></a>`GET /v1/registrations/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Registration`](#registration) — The registration |
| `404` | [`Error`](#error) — Not found |


#### <a id="get-v1sender-ids"></a>`GET /v1/sender-ids`

**Auth:** session **or** API key (`read:messages`)

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SenderId`](#senderid)[] — Registered sender identities for the tenant |


#### <a id="post-v1sender-ids"></a>`POST /v1/sender-ids`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `callerIdNumber` | `string` | no |
| `header` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `wabaId` | `string` | no |
| `displayName` | `string` | no |
| `phoneNumber` | `string` | no |
| `emailDomain` | `string` | no |
| `fromAddress` | `string` | no |
| `fromName` | `string` | no |
| `registrationId` | `string` \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`SenderId`](#senderid) — Created, pending review |
| `409` | [`Error`](#error) — No approved entity for this country |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="get-v1sender-idsid"></a>`GET /v1/sender-ids/{id}`

**Auth:** session **or** API key (`read:messages`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SenderId`](#senderid) — The sender |
| `404` | [`Error`](#error) — Not found |


#### <a id="patch-v1sender-idsid"></a>`PATCH /v1/sender-ids/{id}`

Corrects a sender still working its way through registration. A verified sender's header is bound to the registry entry that approved it (a DLT header registration in India, a TCR campaign in the US), so changing it would leave the platform sending under a header no registry has granted -- verified senders are therefore immutable, and a change means registering a new one. Editing exists for the case it is actually needed: a typo caught before approval, or a correction after a rejection.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Request body** (required) — [`UpdateSenderIdRequest`](#updatesenderidrequest)

| Field | Type | Required |
| --- | --- | --- |
| `header` | `string` | no |
| `registrationId` | `string` \| `null` | no |
| `displayName` | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SenderId`](#senderid) — The updated sender |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — The sender is verified, blocked or expired, so its details are fixed |
| `422` | [`Error`](#error) — A supplied field is empty, malformed for the country's regime, or does not apply to this sender's channel |


#### <a id="delete-v1sender-idsid"></a>`DELETE /v1/sender-ids/{id}`

Removes a sender permanently. Refused while any template, campaign (either as the campaign's own sender or as its fallback leg), journey or Verify service channel still references it, because deleting one would leave those pointing at a sender that no longer exists; the refusal names what is using it so the caller can act on it rather than guess. Unlike editing, deleting is allowed in any status: retiring a verified sender is a legitimate thing to do once nothing depends on it.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Removed |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — Still referenced by a template, campaign, campaign fallback, journey or Verify service |


#### <a id="post-v1sender-idsidvoice-call"></a>`POST /v1/sender-ids/{id}/voice-call`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`VoiceCallResult`](#voicecallresult) — Call placed; the spoken code is returned for the UI to display |
| `404` | [`Error`](#error) — No such Voice sender |


#### <a id="post-v1sender-idsidvoice-code"></a>`POST /v1/sender-ids/{id}/voice-code`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Code matched; sender is now verified |
| `404` | [`Error`](#error) — No such Voice sender |
| `422` | [`Error`](#error) — Incorrect code |


#### <a id="get-v1templates"></a>`GET /v1/templates`

**Auth:** session **or** API key (`read:messages`)

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Template`](#template)[] — Templates for the tenant |


#### <a id="post-v1templates"></a>`POST /v1/templates`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `senderId` | `string(uuid)` | **yes** |
| `body` | `string` \| `null` | no |
| `rcsContent` | [`RcsContent`](#rcscontent) \| `null` | no |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |
| `waContent` | [`WaContent`](#wacontent) \| `null` | no |
| `emailContent` | [`EmailContent`](#emailcontent) \| `null` | no |
| `ctaUrl` | `string` | no |
| `registrationId` | `string` \| `null` | no |
| `dltCategory` | [`DltCategory`](#dltcategory) \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Template`](#template) — Created, pending review |
| `409` | [`Error`](#error) — Sender is not approved |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="get-v1templatesid"></a>`GET /v1/templates/{id}`

**Auth:** session **or** API key (`read:messages`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Template`](#template) — The template |
| `404` | [`Error`](#error) — Not found |


#### <a id="post-v1templatesidcarrier-registration"></a>`POST /v1/templates/{id}/carrier-registration`

An RCS template needs two approvals and this is the second one.

Two modes, because the carriers differ:

- **Send the body with no carrierTemplateId** and Relay submits the template to the carrier's API. Airtel supports this; review takes up to 24 hours and the outcome arrives on their webhook. The template comes back `pending`.
- **Send a carrierTemplateId** to attach a code obtained in the carrier's own portal. This is the only route for Vi, which has no template API at all: templates are created and approved there by a Vi admin. Attaching a code records it as `approved`, because the customer asserting the carrier approved it is the only source of truth Vi offers.

Calling without a code against a carrier that has no template API returns 409 with an explanation, not an error.

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Request body** (optional) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `carrierTemplateId` | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`CarrierTemplateRegistration`](#carriertemplateregistration) — The carrier registration after this call |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — The caller's role does not include template management |
| `404` | [`Error`](#error) — No such template |
| `409` | [`Error`](#error) — This carrier has no template API; create the template in their portal and attach the code |
| `422` | [`Error`](#error) — Not an RCS template, or already registered |
| `502` | [`Error`](#error) — The carrier could not be reached or refused the template |
| `503` | [`Error`](#error) — This deployment has no RCS carrier configured |


#### <a id="get-v1verifyservices"></a>`GET /v1/verify/services`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | `object` — Verify services for the tenant |

`200` fields:

| Field | Type | Required |
| --- | --- | --- |
| `services` | [`VerifyService`](#verifyservice)[] | **yes** |


#### <a id="post-v1verifyservices"></a>`POST /v1/verify/services`

**Auth:** session

**Request body** (required) — [`VerifyServiceCreate`](#verifyservicecreate)

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `channels` | [`VerifyChannelConfig`](#verifychannelconfig)[] | **yes** |
| `fallbackOrder` | [`ChannelId`](#channelid)[] | **yes** |
| `codeLength` | `integer` | **yes** |
| `codeTtlSeconds` | `integer` | **yes** |
| `maxAttempts` | `integer` | **yes** |
| `rateLimit` | [`VerifyRateLimit`](#verifyratelimit) | **yes** |
| `regionAllowlist` | `string`[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`VerifyService`](#verifyservice) — Created |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `409` | [`Error`](#error) — Conflicting configuration |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="get-v1verifyservicesid"></a>`GET /v1/verify/services/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`VerifyService`](#verifyservice) — The Verify service |
| `404` | [`Error`](#error) — Not found |


#### <a id="patch-v1verifyservicesid"></a>`PATCH /v1/verify/services/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Request body** (required) — [`VerifyServiceUpdate`](#verifyserviceupdate)

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `channels` | [`VerifyChannelConfig`](#verifychannelconfig)[] | **yes** |
| `fallbackOrder` | [`ChannelId`](#channelid)[] | **yes** |
| `codeLength` | `integer` | **yes** |
| `codeTtlSeconds` | `integer` | **yes** |
| `maxAttempts` | `integer` | **yes** |
| `rateLimit` | [`VerifyRateLimit`](#verifyratelimit) | **yes** |
| `regionAllowlist` | `string`[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`VerifyService`](#verifyservice) — Updated |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — Conflicting configuration |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="get-v1verifyservicesidanalytics"></a>`GET /v1/verify/services/{id}/analytics`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |
| `range` | query | [`VerifyAnalyticsRange`](#verifyanalyticsrange) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`VerifyAnalytics`](#verifyanalytics) — Analytics |
| `404` | [`Error`](#error) — Not found |


#### <a id="get-v1verifyservicesidattempts"></a>`GET /v1/verify/services/{id}/attempts`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |
| `range` | query | [`VerifyAnalyticsRange`](#verifyanalyticsrange) | no |
| `status` | query | [`VerificationStatus`](#verificationstatus) | no |
| `channel` | query | [`ChannelId`](#channelid) | no |
| `fraudFlag` | query | [`VerificationFraudFlag`](#verificationfraudflag) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`VerificationAttemptPage`](#verificationattemptpage) — Attempts |
| `404` | [`Error`](#error) — Not found |


#### <a id="post-v1verifyservicesidverifications"></a>`POST /v1/verify/services/{id}/verifications`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `msisdn` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Verification`](#verification) — Verification started |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — Conflicting state |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="post-v1verifyservicesidverificationsvidcheck"></a>`POST /v1/verify/services/{id}/verifications/{vid}/check`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string(uuid)` | **yes** |
| `vid` | path | `string(uuid)` | **yes** |

**Request body** (required) — [`VerificationCheck`](#verificationcheck)

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Verification`](#verification) — Check result |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — Not found |
| `409` | [`Error`](#error) — Conflicting state |
| `422` | [`Error`](#error) — Validation failed |


### Billing & wallet

#### <a id="post-v1billingestimate"></a>`POST /v1/billing/estimate`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `recipientCount` | `integer` | **yes** |
| `primaryBody` | `string` | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |
| `fallback` | `object` \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`CampaignEstimate`](#campaignestimate) — Manual what-if cost estimate — same shape as a campaign's pre-send estimate |
| `401` | [`Error`](#error) — Unauthenticated |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1billinginvoices"></a>`GET /v1/billing/invoices`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`InvoicePage`](#invoicepage) — Invoices, newest first, one per calendar month with billed charges |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |


#### <a id="get-v1billinginvoicesid"></a>`GET /v1/billing/invoices/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Invoice`](#invoice) — The invoice, with line items |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |
| `404` | [`Error`](#error) — No such invoice |


#### <a id="get-v1billingusage"></a>`GET /v1/billing/usage`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `range` | query | [`AnalyticsRange`](#analyticsrange) | no |
| `currency` | query | [`CurrencyCode`](#currencycode) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`UsageReport`](#usagereport) — Actual billed spend grouped by channel and by campaign |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="get-v1pricing"></a>`GET /v1/pricing`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`PricingRate`](#pricingrate)[] — Rates |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="get-v1walletauto-recharge"></a>`GET /v1/wallet/auto-recharge`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AutoRechargeConfig`](#autorechargeconfig)[] — Auto-recharge config for every currency it has been set up for |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="put-v1walletauto-recharge"></a>`PUT /v1/wallet/auto-recharge`

**Auth:** session

**Request body** (required) — [`UpdateAutoRechargeRequest`](#updateautorechargerequest)

| Field | Type | Required |
| --- | --- | --- |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `enabled` | `boolean` | **yes** |
| `thresholdMinor` | `integer` | **yes** |
| `topUpMinor` | `integer` | **yes** |
| `paymentMethodId` | `string` \| `null` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AutoRechargeConfig`](#autorechargeconfig) — The saved config |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1walletbalances"></a>`GET /v1/wallet/balances`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`WalletBalance`](#walletbalance)[] — One native balance per currency the tenant has transacted in — no invented FX conversion between currencies |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="get-v1walletledger"></a>`GET /v1/wallet/ledger`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `currency` | query | [`CurrencyCode`](#currencycode) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`LedgerPage`](#ledgerpage) — Append-only ledger, newest first |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="get-v1walletpayment-methods"></a>`GET /v1/wallet/payment-methods`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`PaymentMethod`](#paymentmethod)[] — Mock payment methods |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="post-v1walletpayment-methods"></a>`POST /v1/wallet/payment-methods`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `brand` | [`PaymentMethodBrand`](#paymentmethodbrand) | **yes** |
| `last4` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`PaymentMethod`](#paymentmethod) — Created |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="delete-v1walletpayment-methodsid"></a>`DELETE /v1/wallet/payment-methods/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Removed |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |
| `404` | [`Error`](#error) — No such payment method |


#### <a id="post-v1walletpayment-methodsiddefault"></a>`POST /v1/wallet/payment-methods/{id}/default`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`PaymentMethod`](#paymentmethod) — The now-default payment method |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |
| `404` | [`Error`](#error) — No such payment method |


#### <a id="post-v1wallettopup"></a>`POST /v1/wallet/topup`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `amountMinor` | `integer` | **yes** |
| `paymentMethodId` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`LedgerEntry`](#ledgerentry) — The credit ledger entry |
| `401` | [`Error`](#error) — Unauthenticated |
| `403` | [`Error`](#error) — Member role has no access to billing. |
| `422` | [`Error`](#error) — Validation error |


### Developer API

#### <a id="get-v1developerapi-keys"></a>`GET /v1/developer/api-keys`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `environment` | query | [`Environment`](#environment) | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ApiKey`](#apikey)[] — API keys |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |


#### <a id="post-v1developerapi-keys"></a>`POST /v1/developer/api-keys`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `scopes` | [`ApiKeyScope`](#apikeyscope)[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`ApiKeyCreated`](#apikeycreated) — Key created — secret shown once |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="delete-v1developerapi-keysid"></a>`DELETE /v1/developer/api-keys/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Revoked |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such key |


#### <a id="post-v1developerapi-keysidrotate"></a>`POST /v1/developer/api-keys/{id}/rotate`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ApiKeyCreated`](#apikeycreated) — New secret — shown once |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such key |


#### <a id="get-v1developerip-allowlist"></a>`GET /v1/developer/ip-allowlist`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `environment` | query | [`Environment`](#environment) | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`IpAllowlistEntry`](#ipallowlistentry)[] — Allowed IP ranges |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |


#### <a id="post-v1developerip-allowlist"></a>`POST /v1/developer/ip-allowlist`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `cidr` | `string` | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `label` | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`IpAllowlistEntry`](#ipallowlistentry) — Entry added |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `422` | [`Error`](#error) — Invalid CIDR |


#### <a id="delete-v1developerip-allowlistid"></a>`DELETE /v1/developer/ip-allowlist/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Removed |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such entry |


#### <a id="get-v1developerrate-limit"></a>`GET /v1/developer/rate-limit`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `environment` | query | [`Environment`](#environment) | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`RateLimitTier`](#ratelimittier) — Rate limit tier |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |


#### <a id="get-v1developerscopes"></a>`GET /v1/developer/scopes`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ApiScope`](#apiscope)[] — Available API scopes |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |


#### <a id="get-v1developerwebhooks"></a>`GET /v1/developer/webhooks`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `environment` | query | [`Environment`](#environment) | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`WebhookEndpoint`](#webhookendpoint)[] — Webhook endpoints |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |


#### <a id="post-v1developerwebhooks"></a>`POST /v1/developer/webhooks`

**Auth:** session **or** API key (`webhooks:manage`)

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `url` | `string` | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `subscribedEvents` | [`WebhookEventType`](#webhookeventtype)[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`WebhookEndpointCreated`](#webhookendpointcreated) — Endpoint created — signing secret shown once |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1developerwebhooksid"></a>`GET /v1/developer/webhooks/{id}`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`WebhookEndpoint`](#webhookendpoint) — Webhook endpoint |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such endpoint |


#### <a id="patch-v1developerwebhooksid"></a>`PATCH /v1/developer/webhooks/{id}`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `subscribedEvents` | [`WebhookEventType`](#webhookeventtype)[] | no |
| `status` | [`WebhookStatus`](#webhookstatus) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`WebhookEndpoint`](#webhookendpoint) — Updated endpoint |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such endpoint |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="delete-v1developerwebhooksid"></a>`DELETE /v1/developer/webhooks/{id}`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Deleted |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such endpoint |


#### <a id="get-v1developerwebhooksidevents"></a>`GET /v1/developer/webhooks/{id}/events`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`WebhookEventPage`](#webhookeventpage) — Webhook delivery events, newest first |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such endpoint |


#### <a id="post-v1developerwebhooksideventseventidresend"></a>`POST /v1/developer/webhooks/{id}/events/{eventId}/resend`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |
| `eventId` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`WebhookEvent`](#webhookevent) — New delivery attempt |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such endpoint or event |


#### <a id="post-v1developerwebhooksidtest-event"></a>`POST /v1/developer/webhooks/{id}/test-event`

**Auth:** session **or** API key (`webhooks:manage`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`WebhookEvent`](#webhookevent) — Test event dispatched |
| `403` | [`Error`](#error) — Member role has no access to developer settings. |
| `404` | [`Error`](#error) — No such endpoint |


### Analytics & reporting

#### <a id="get-v1analytics"></a>`GET /v1/analytics`

**Auth:** session **or** API key (`read:analytics`)

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `range` | query | [`AnalyticsRange`](#analyticsrange) | no |
| `channel` | query | [`ChannelId`](#channelid) | no |
| `country` | query | [`CountryCode`](#countrycode) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Analytics`](#analytics) — Analytics |
| `401` | [`Error`](#error) — Missing or invalid bearer token |


#### <a id="get-v1analyticsreports"></a>`GET /v1/analytics/reports`

**Auth:** session **or** API key (`read:analytics`)

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ScheduledReport`](#scheduledreport)[] — Every scheduled report configured for this tenant |
| `401` | [`Error`](#error) — Unauthenticated |


#### <a id="post-v1analyticsreports"></a>`POST /v1/analytics/reports`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `frequency` | [`ReportFrequency`](#reportfrequency) | **yes** |
| `range` | [`AnalyticsRange`](#analyticsrange) | **yes** |
| `recipients` | `string`[] | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`ScheduledReport`](#scheduledreport) — Created |
| `401` | [`Error`](#error) — Unauthenticated |
| `422` | [`Error`](#error) — Validation error |


#### <a id="patch-v1analyticsreportsid"></a>`PATCH /v1/analytics/reports/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `paused` | `boolean` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ScheduledReport`](#scheduledreport) — Updated scheduled report |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — No such scheduled report |


#### <a id="delete-v1analyticsreportsid"></a>`DELETE /v1/analytics/reports/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Deleted |
| `401` | [`Error`](#error) — Unauthenticated |
| `404` | [`Error`](#error) — No such scheduled report |


### Team & tenant settings

#### <a id="get-v1alerts"></a>`GET /v1/alerts`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AlertRules`](#alertrules) — The tenant's configured alert rules |
| `401` | [`Error`](#error) — Missing or invalid bearer token |


#### <a id="patch-v1alerts"></a>`PATCH /v1/alerts`

**Auth:** session

**Request body** (required) — [`AlertRulesPatch`](#alertrulespatch)

| Field | Type | Required |
| --- | --- | --- |
| `lowBalance` | [`LowBalanceRule`](#lowbalancerule)[] | no |
| `deliveryFloor` | [`DeliveryFloorRule`](#deliveryfloorrule) | no |
| `spendCeiling` | [`SpendCeilingRule`](#spendceilingrule) | no |
| `volumeCeiling` | [`VolumeCeilingRule`](#volumeceilingrule) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AlertRules`](#alertrules) — The full, updated AlertRules |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `422` | [`Error`](#error) — Validation error (invalid recipient email) |


#### <a id="get-v1sso"></a>`GET /v1/sso`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SsoConfig`](#ssoconfig) — Tenant SSO configuration |
| `401` | [`Error`](#error) — Missing or invalid bearer token |


#### <a id="put-v1sso"></a>`PUT /v1/sso`

**Auth:** session

**Request body** (required) — [`SsoConfig`](#ssoconfig)

| Field | Type | Required |
| --- | --- | --- |
| `enabled` | `boolean` | **yes** |
| `provider` | `saml` \| `oidc` | **yes** |
| `metadataUrl` | `string` \| `null` | **yes** |
| `entityId` | `string` \| `null` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SsoConfig`](#ssoconfig) — Updated SSO configuration |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `422` | [`Error`](#error) — Validation error |


#### <a id="get-v1supporttickets"></a>`GET /v1/support/tickets`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `status` | query | [`TicketStatus`](#ticketstatus) | no |
| `category` | query | [`TicketCategory`](#ticketcategory) | no |
| `limit` | query | `integer` | no |
| `cursor` | query | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketPage`](#supportticketpage) — The caller's own tenant's support tickets |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="post-v1supporttickets"></a>`POST /v1/support/tickets`

**Auth:** session

**Request body** (required) — [`NewSupportTicketRequest`](#newsupportticketrequest)

| Field | Type | Required |
| --- | --- | --- |
| `subject` | `string` | **yes** |
| `category` | [`TicketCategory`](#ticketcategory) | **yes** |
| `body` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`SupportTicketDetail`](#supportticketdetail) — The newly-created ticket, with its first message |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `422` | [`Error`](#error) — Missing subject, category, or body |


#### <a id="get-v1supportticketsid"></a>`GET /v1/support/tickets/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketDetail`](#supportticketdetail) — The ticket with its full message thread |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such ticket for this tenant |


#### <a id="post-v1supportticketsidmessages"></a>`POST /v1/support/tickets/{id}/messages`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`NewSupportMessageRequest`](#newsupportmessagerequest)

| Field | Type | Required |
| --- | --- | --- |
| `body` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketDetail`](#supportticketdetail) — The ticket with the new message appended (reopens if pending/resolved) |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `404` | [`Error`](#error) — No such ticket for this tenant |
| `422` | [`Error`](#error) — Empty body |


#### <a id="get-v1team"></a>`GET /v1/team`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TeamMemberPage`](#teammemberpage) — The tenant's team roster |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to team management. |


#### <a id="post-v1teaminvite"></a>`POST /v1/team/invite`

**Auth:** session

**Request body** (required) — [`InviteTeamMemberRequest`](#inviteteammemberrequest)

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |
| `role` | `admin` \| `member` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`TeamMember`](#teammember) — The newly-invited member |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to team management. |
| `422` | [`Error`](#error) — Invalid email or role (owner cannot be invited) |


#### <a id="patch-v1teamid"></a>`PATCH /v1/team/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`TeamMemberRoleUpdateRequest`](#teammemberroleupdaterequest)

| Field | Type | Required |
| --- | --- | --- |
| `role` | [`TeamRole`](#teamrole) | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TeamMember`](#teammember) — The updated member |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to team management. |
| `404` | [`Error`](#error) — No such member |
| `422` | [`Error`](#error) — Cannot change the sole Owner's role |


#### <a id="delete-v1teamid"></a>`DELETE /v1/team/{id}`

**Auth:** session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Removed |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to team management. |
| `404` | [`Error`](#error) — No such member |
| `422` | [`Error`](#error) — Cannot remove the sole Owner |


#### <a id="patch-v1tenant"></a>`PATCH /v1/tenant`

**Auth:** session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Me`](#me) — Tenant name updated, reflected in Me.tenantName |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `422` | [`Error`](#error) — Validation error |


### Operator console

#### <a id="get-v1operatorabuse-queue"></a>`GET /v1/operator/abuse-queue`

**Auth:** operator session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AbuseQueuePage`](#abusequeuepage) — Every currently-flagged tenant across all tenants (open flags only) |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="get-v1operatorapprovals"></a>`GET /v1/operator/approvals`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `type` | query | [`ApprovalQueueItemType`](#approvalqueueitemtype) | no |
| `country` | query | [`CountryCode`](#countrycode) | no |
| `status` | query | [`ApprovalStatus`](#approvalstatus) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ApprovalQueuePage`](#approvalqueuepage) — Cross-tenant sender/template approval queue, optionally filtered |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="get-v1operatoraudit-log"></a>`GET /v1/operator/audit-log`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `tenantId` | query | `string(uuid)` | no |
| `action` | query | [`AuditAction`](#auditaction) | no |
| `range` | query | [`AnalyticsRange`](#analyticsrange) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AuditLogPage`](#auditlogpage) — Operator-performed mutating actions, newest first |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="get-v1operatorconnections"></a>`GET /v1/operator/connections`

List operator SMPP connections

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `carrier` | query | [`CarrierId`](#carrierid) | no |
| `environment` | query | [`Environment`](#environment) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConnectionPage`](#connectionpage) — Every configured connection, never including any password |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="post-v1operatorconnections"></a>`POST /v1/operator/connections`

The connection is created disabled. Enabling it is a separate, audited decision — a bind that went live the moment it was typed would put traffic on an untested path. The supplied password is stored encrypted and is never returned.

**Auth:** operator session

**Request body** (required) — [`ConnectionCreate`](#connectioncreate)

| Field | Type | Required |
| --- | --- | --- |
| `label` | `string` | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `host` | `string` | **yes** |
| `port` | `integer` | **yes** |
| `systemId` | `string` | **yes** |
| `systemType` | `string` \| `null` | no |
| `bindType` | [`BindType`](#bindtype) | **yes** |
| `maxTps` | `integer` | **yes** |
| `windowSize` | `integer` | no |
| `enquireLinkSeconds` | `integer` | no |
| `reconnectBackoffSeconds` | `integer` | no |
| `password` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Connection`](#connection) — Connection created, disabled |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `409` | [`Error`](#error) — A connection with this label already exists |
| `422` | [`Error`](#error) — Unknown carrier or environment, or a field failed validation |


#### <a id="get-v1operatorconnectionsid"></a>`GET /v1/operator/connections/{id}`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Connection`](#connection) — The connection, never including its password |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such connection |


#### <a id="patch-v1operatorconnectionsid"></a>`PATCH /v1/operator/connections/{id}`

Change a connection's settings

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`ConnectionUpdate`](#connectionupdate)

| Field | Type | Required |
| --- | --- | --- |
| `label` | `string` | no |
| `carrier` | [`CarrierId`](#carrierid) | no |
| `environment` | [`Environment`](#environment) | no |
| `host` | `string` | no |
| `port` | `integer` | no |
| `systemId` | `string` | no |
| `systemType` | `string` \| `null` | no |
| `bindType` | [`BindType`](#bindtype) | no |
| `maxTps` | `integer` | no |
| `windowSize` | `integer` | no |
| `enquireLinkSeconds` | `integer` | no |
| `reconnectBackoffSeconds` | `integer` | no |
| `password` | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Connection`](#connection) — The updated connection |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such connection |
| `409` | [`Error`](#error) — Another connection already uses this label |
| `422` | [`Error`](#error) — Unknown carrier or environment, or a field failed validation |


#### <a id="delete-v1operatorconnectionsid"></a>`DELETE /v1/operator/connections/{id}`

Remove a connection

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Connection removed |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such connection |
| `409` | [`Error`](#error) — Routes still point at this connection — repoint or remove them first |
| `422` | [`Error`](#error) — The connection is active — disable it before removing it |


#### <a id="post-v1operatorconnectionsiddisable"></a>`POST /v1/operator/connections/{id}/disable`

Traffic on every corridor whose route points at this connection falls through to the next priority.

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Connection`](#connection) — Connection disabled |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such connection |
| `422` | [`Error`](#error) — Connection is already disabled |


#### <a id="post-v1operatorconnectionsidenable"></a>`POST /v1/operator/connections/{id}/enable`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Connection`](#connection) — Connection enabled (status set to active) |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such connection |
| `422` | [`Error`](#error) — Connection is already active, or has no password set |


#### <a id="post-v1operatorconnectionsidtest"></a>`POST /v1/operator/connections/{id}/test`

Opens a bind, reports the result, and closes it. Deliberately does not change `status` — proving a bind works and putting live traffic on it stay two separate decisions.

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`ConnectionTestResult`](#connectiontestresult) — The bind attempt's outcome |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such connection |


#### <a id="post-v1operatorlogin"></a>`POST /v1/operator/login`

**Auth:** public

**Request body** (required) — [`OperatorLoginRequest`](#operatorloginrequest)

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |
| `password` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`OperatorLoginResult`](#operatorloginresult) — A session, or a second-factor challenge when the operator has MFA enabled |
| `401` | [`Error`](#error) — Email or password is incorrect |
| `422` | [`Error`](#error) — Email and password are required |


#### <a id="post-v1operatorloginmfa"></a>`POST /v1/operator/login/mfa`

Complete an operator sign-in with a second factor

**Auth:** public

**Request body** (required) — [`MfaChallengeRequest`](#mfachallengerequest)

| Field | Type | Required |
| --- | --- | --- |
| `challengeToken` | `string` | **yes** |
| `code` | `string` | **yes** |
| `method` | `totp` \| `recovery_code` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`AuthSession`](#authsession) — Operator session issued |
| `401` | [`Error`](#error) — The challenge is unknown, spent, expired, or the code is wrong |


#### <a id="get-v1operatormargin"></a>`GET /v1/operator/margin`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `range` | query | [`AnalyticsRange`](#analyticsrange) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`OperatorMarginReport`](#operatormarginreport) — Revenue vs. cost margin across every tenant for the selected range, grouped by currency |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="get-v1operatorme"></a>`GET /v1/operator/me`

**Auth:** operator session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`OperatorMe`](#operatorme) — The signed-in operator |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="post-v1operatormfaconfirm"></a>`POST /v1/operator/mfa/confirm`

Confirm enrolment with a code from the authenticator

**Auth:** operator session

**Request body** (required) — [`MfaCodeRequest`](#mfacoderequest)

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MfaRecoveryCodes`](#mfarecoverycodes) — MFA enabled; recovery codes shown once |
| `401` | [`Error`](#error) — Missing token, or the code is wrong |
| `409` | [`Error`](#error) — Already enabled, or enrolment was never started |


#### <a id="post-v1operatormfadisable"></a>`POST /v1/operator/mfa/disable`

Requires a current code: turning MFA off is exactly what an attacker holding a stolen session would do first.

**Auth:** operator session

**Request body** (required) — [`MfaCodeRequest`](#mfacoderequest)

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — MFA disabled |
| `401` | [`Error`](#error) — Missing token, or the code is wrong |
| `409` | [`Error`](#error) — MFA is not enabled |


#### <a id="post-v1operatormfaenroll"></a>`POST /v1/operator/mfa/enroll`

Start second-factor enrolment for the signed-in operator

**Auth:** operator session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`MfaEnrollment`](#mfaenrollment) — Secret and QR code; MFA is not on until confirmed |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="get-v1operatorrates"></a>`GET /v1/operator/rates`

**Auth:** operator session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`RateCard`](#ratecard) — The global default rate card and every tenant rate override, each with a read-only cost reference |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="patch-v1operatorratesdefault"></a>`PATCH /v1/operator/rates/default`

**Auth:** operator session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `perSegmentMinor` | `integer` | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`RateCardRow`](#ratecardrow) — Default rate updated |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No default rate exists for this country x channel |
| `422` | [`Error`](#error) — perSegmentMinor must be a non-negative integer |


#### <a id="post-v1operatorratesoverrides"></a>`POST /v1/operator/rates/overrides`

**Auth:** operator session

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `tenantId` | `string(uuid)` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `perSegmentMinor` | `integer` | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`TenantRateOverride`](#tenantrateoverride) — Override created |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |
| `422` | [`Error`](#error) — An override already exists for this tenant and corridor, or perSegmentMinor is invalid |


#### <a id="patch-v1operatorratesoverridesid"></a>`PATCH /v1/operator/rates/overrides/{id}`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — `object`

| Field | Type | Required |
| --- | --- | --- |
| `perSegmentMinor` | `integer` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantRateOverride`](#tenantrateoverride) — Override rate updated |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such override |
| `422` | [`Error`](#error) — perSegmentMinor must be a non-negative integer |


#### <a id="delete-v1operatorratesoverridesid"></a>`DELETE /v1/operator/rates/overrides/{id}`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Override removed — that tenant reverts to the default rate for this corridor |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such override |


#### <a id="post-v1operatorregistrationsidapprove"></a>`POST /v1/operator/registrations/{id}/approve`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Registration`](#registration) — Registration approved |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such registration |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="post-v1operatorregistrationsidreject"></a>`POST /v1/operator/registrations/{id}/reject`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`RejectItemRequest`](#rejectitemrequest)

| Field | Type | Required |
| --- | --- | --- |
| `reason` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Registration`](#registration) — Registration rejected |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such registration |
| `422` | [`Error`](#error) — Validation failed |


#### <a id="get-v1operatorroutes"></a>`GET /v1/operator/routes`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `country` | query | [`CountryCode`](#countrycode) | no |
| `channel` | query | [`ChannelId`](#channelid) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`RoutePage`](#routepage) — Every route on the platform, optionally filtered |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="post-v1operatorroutes"></a>`POST /v1/operator/routes`

The new route is created disabled and last in its country x channel group. Enabling it is a separate, audited decision — a route that went live the moment it was typed would put traffic on an untested path.

**Auth:** operator session

**Request body** (required) — [`RouteCreate`](#routecreate)

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `label` | `string` | **yes** |
| `complianceStanding` | [`ComplianceStanding`](#compliancestanding) | **yes** |
| `costPerSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `connectionId` | `string(uuid)` \| `null` | no |

**Responses**

| Status | Body |
| --- | --- |
| `201` | [`Route`](#route) — Route created, disabled |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `409` | [`Error`](#error) — That corridor already has a route with this label |
| `422` | [`Error`](#error) — Unknown country, channel, carrier or currency |


#### <a id="delete-v1operatorroutesid"></a>`DELETE /v1/operator/routes/{id}`

Removes the route and closes the gap it leaves, so the remaining priorities in that country x channel group stay 1..n with no holes.

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `204` | _no body_ — Route removed |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such route |
| `422` | [`Error`](#error) — The route is active — disable it before removing it |


#### <a id="post-v1operatorroutesiddisable"></a>`POST /v1/operator/routes/{id}/disable`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Route`](#route) — Route disabled |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such route |
| `422` | [`Error`](#error) — Route is already disabled |


#### <a id="post-v1operatorroutesidenable"></a>`POST /v1/operator/routes/{id}/enable`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Route`](#route) — Route enabled (status set to active) |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such route |
| `422` | [`Error`](#error) — Route is already active |


#### <a id="post-v1operatorroutesidmove-down"></a>`POST /v1/operator/routes/{id}/move-down`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Route`](#route) — Priority swapped with the route holding priority + 1 in the same country x channel group. Carrier is NOT part of the group: a corridor is one ordered ladder across carriers, so the neighbour may belong to a different carrier. |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such route |
| `422` | [`Error`](#error) — This route already holds the highest priority number in its group |


#### <a id="post-v1operatorroutesidmove-up"></a>`POST /v1/operator/routes/{id}/move-up`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Route`](#route) — Priority swapped with the route holding priority - 1 in the same country x channel group. Carrier is NOT part of the group: a corridor is one ordered ladder across carriers, so the neighbour may belong to a different carrier. |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such route |
| `422` | [`Error`](#error) — This route already holds priority 1 in its group |


#### <a id="post-v1operatorsendersidapprove"></a>`POST /v1/operator/senders/{id}/approve`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SenderId`](#senderid) — Sender approved |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such sender |


#### <a id="post-v1operatorsendersidreject"></a>`POST /v1/operator/senders/{id}/reject`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`RejectItemRequest`](#rejectitemrequest)

| Field | Type | Required |
| --- | --- | --- |
| `reason` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SenderId`](#senderid) — Sender rejected |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such sender |
| `422` | [`Error`](#error) — A rejection reason is required |


#### <a id="get-v1operatorsupporttickets"></a>`GET /v1/operator/support/tickets`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `tenantId` | query | `string(uuid)` | no |
| `status` | query | [`TicketStatus`](#ticketstatus) | no |
| `category` | query | [`TicketCategory`](#ticketcategory) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketPage`](#supportticketpage) — Support tickets across all tenants |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="get-v1operatorsupportticketsid"></a>`GET /v1/operator/support/tickets/{id}`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketDetail`](#supportticketdetail) — The ticket with its full message thread |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such ticket |


#### <a id="post-v1operatorsupportticketsidmessages"></a>`POST /v1/operator/support/tickets/{id}/messages`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`NewSupportMessageRequest`](#newsupportmessagerequest)

| Field | Type | Required |
| --- | --- | --- |
| `body` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketDetail`](#supportticketdetail) — The ticket with the new message appended (auto-pends if open) |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such ticket |
| `422` | [`Error`](#error) — Empty body |


#### <a id="post-v1operatorsupportticketsidreopen"></a>`POST /v1/operator/support/tickets/{id}/reopen`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketDetail`](#supportticketdetail) — The ticket, now open |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such ticket |


#### <a id="post-v1operatorsupportticketsidresolve"></a>`POST /v1/operator/support/tickets/{id}/resolve`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`SupportTicketDetail`](#supportticketdetail) — The ticket, now resolved |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such ticket |


#### <a id="post-v1operatortemplatesidapprove"></a>`POST /v1/operator/templates/{id}/approve`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Template`](#template) — Template approved |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such template |


#### <a id="post-v1operatortemplatesidreject"></a>`POST /v1/operator/templates/{id}/reject`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`RejectItemRequest`](#rejectitemrequest)

| Field | Type | Required |
| --- | --- | --- |
| `reason` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`Template`](#template) — Template rejected |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such template |
| `422` | [`Error`](#error) — A rejection reason is required |


#### <a id="get-v1operatortenants"></a>`GET /v1/operator/tenants`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `status` | query | [`TenantStatus`](#tenantstatus) | no |
| `country` | query | [`CountryCode`](#countrycode) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantPage`](#tenantpage) — Every tenant on the platform, optionally filtered. If limit is omitted, returns every matching tenant unbounded with nextCursor null. |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


#### <a id="get-v1operatortenantsid"></a>`GET /v1/operator/tenants/{id}`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantDetail`](#tenantdetail) — Full tenant detail |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |


#### <a id="post-v1operatortenantsiddismiss-flag"></a>`POST /v1/operator/tenants/{id}/dismiss-flag`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantDetail`](#tenantdetail) — Abuse flag dismissed as a false positive (no status change); a no-op-but-200 if the tenant was not flagged |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |


#### <a id="post-v1operatortenantsidflag-abuse"></a>`POST /v1/operator/tenants/{id}/flag-abuse`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`RejectItemRequest`](#rejectitemrequest)

| Field | Type | Required |
| --- | --- | --- |
| `reason` | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantDetail`](#tenantdetail) — Tenant flagged for abuse review (idempotent — a second call returns the same flaggedAt/flagReason, the first reason wins) |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |
| `422` | [`Error`](#error) — A flag reason is required |


#### <a id="post-v1operatortenantsidreinstate"></a>`POST /v1/operator/tenants/{id}/reinstate`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantDetail`](#tenantdetail) — Tenant reinstated |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |
| `422` | [`Error`](#error) — Tenant is not suspended |


#### <a id="post-v1operatortenantsidsuspend"></a>`POST /v1/operator/tenants/{id}/suspend`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantDetail`](#tenantdetail) — Tenant suspended |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |
| `422` | [`Error`](#error) — Tenant is already suspended |


#### <a id="post-v1operatortenantsidthrottle"></a>`POST /v1/operator/tenants/{id}/throttle`

Caps the tenant's send rate at ratePerSecond messages per second across every channel, and clears any open abuse flag. The rate is a number, not a flag: an operator throttles to honour a carrier's contracted TPS or to contain a runaway sender, and both need a specific ceiling. Reinstating or suspending the tenant clears the cap.

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `id` | path | `string` | **yes** |

**Request body** (required) — [`ThrottleTenantRequest`](#throttletenantrequest)

| Field | Type | Required |
| --- | --- | --- |
| `ratePerSecond` | `integer` | **yes** |
| `reason` | `string` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`TenantDetail`](#tenantdetail) — Tenant throttled at the requested rate (also clears any open abuse flag) |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `404` | [`Error`](#error) — No such tenant |
| `422` | [`Error`](#error) — Tenant is not active, is already throttled, or ratePerSecond is missing or not a positive integer |


#### <a id="get-v1operatorusage"></a>`GET /v1/operator/usage`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `range` | query | [`AnalyticsRange`](#analyticsrange) | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`OperatorUsageReport`](#operatorusagereport) — Aggregate message volume across every tenant for the selected range |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |


#### <a id="get-v1operatoruser-activity"></a>`GET /v1/operator/user-activity`

**Auth:** operator session

**Parameters**

| Name | In | Type | Required |
| --- | --- | --- | --- |
| `tenantId` | query | `string(uuid)` | no |
| `eventType` | query | [`UserActivityEventType`](#useractivityeventtype) | no |
| `range` | query | [`AnalyticsRange`](#analyticsrange) | no |
| `cursor` | query | `string` | no |
| `limit` | query | `integer` | no |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`UserActivityPage`](#useractivitypage) — Tenant-side security/access-relevant user activity, system-wide, newest first |
| `401` | [`Error`](#error) — Missing or invalid operator bearer token |
| `422` | [`Error`](#error) — Malformed cursor |


### Other

#### <a id="get-v1data-retention"></a>`GET /v1/data-retention`

**Auth:** session

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`DataRetentionSettings`](#dataretentionsettings) — The tenant's message-log retention setting |
| `401` | [`Error`](#error) — Missing or invalid bearer token |


#### <a id="patch-v1data-retention"></a>`PATCH /v1/data-retention`

**Auth:** session

**Request body** (required) — [`DataRetentionSettings`](#dataretentionsettings)

| Field | Type | Required |
| --- | --- | --- |
| `messageLogRetentionDays` | `30` \| `90` \| `180` \| `365` | **yes** |

**Responses**

| Status | Body |
| --- | --- |
| `200` | [`DataRetentionSettings`](#dataretentionsettings) — The updated setting |
| `401` | [`Error`](#error) — Missing or invalid bearer token |
| `403` | [`Error`](#error) — Member role has no access to settings. |
| `422` | [`Error`](#error) — Validation error (invalid retention value) |



---

## 10. Schemas

### <a id="abusequeuepage"></a>`AbuseQueuePage`

| Field | Type | Required |
| --- | --- | --- |
| `items` | [`TenantDetail`](#tenantdetail)[] | **yes** |

### <a id="alertrules"></a>`AlertRules`

| Field | Type | Required |
| --- | --- | --- |
| `lowBalance` | [`LowBalanceRule`](#lowbalancerule)[] | **yes** |
| `deliveryFloor` | [`DeliveryFloorRule`](#deliveryfloorrule) | **yes** |
| `spendCeiling` | [`SpendCeilingRule`](#spendceilingrule) | **yes** |
| `volumeCeiling` | [`VolumeCeilingRule`](#volumeceilingrule) | **yes** |

### <a id="alertrulespatch"></a>`AlertRulesPatch`

| Field | Type | Required |
| --- | --- | --- |
| `lowBalance` | [`LowBalanceRule`](#lowbalancerule)[] | no |
| `deliveryFloor` | [`DeliveryFloorRule`](#deliveryfloorrule) | no |
| `spendCeiling` | [`SpendCeilingRule`](#spendceilingrule) | no |
| `volumeCeiling` | [`VolumeCeilingRule`](#volumeceilingrule) | no |

### <a id="analytics"></a>`Analytics`

| Field | Type | Required |
| --- | --- | --- |
| `summary` | [`AnalyticsSummary`](#analyticssummary) | **yes** |
| `buckets` | [`AnalyticsBucket`](#analyticsbucket)[] | **yes** |
| `deliverability` | [`AnalyticsDeliverabilityRow`](#analyticsdeliverabilityrow)[] | **yes** |

### <a id="analyticsbucket"></a>`AnalyticsBucket`

| Field | Type | Required |
| --- | --- | --- |
| `bucketStart` | `string(date-time)` | **yes** |
| `sent` | `integer` | **yes** |
| `delivered` | `integer` | **yes** |
| `failed` | `integer` | **yes** |
| `read` | `integer` | **yes** |
| `costMinor` | `integer` | **yes** |

### <a id="analyticsdeliverabilityrow"></a>`AnalyticsDeliverabilityRow`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `sent` | `integer` | **yes** |
| `delivered` | `integer` | **yes** |
| `deliveryRate` | `number` | **yes** |

### <a id="analyticslatency"></a>`AnalyticsLatency`

| Field | Type | Required |
| --- | --- | --- |
| `p50Ms` | `integer` | **yes** |
| `p90Ms` | `integer` | **yes** |

### <a id="analyticsrange"></a>`AnalyticsRange`

One of: `7d`, `30d`, `90d`

### <a id="analyticssummary"></a>`AnalyticsSummary`

| Field | Type | Required |
| --- | --- | --- |
| `sent` | `integer` | **yes** |
| `delivered` | `integer` | **yes** |
| `failed` | `integer` | **yes** |
| `read` | `integer` | **yes** |
| `deliveryRate` | `number` | **yes** |
| `costMinor` | `integer` | **yes** |
| `costPerConversionMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `currencyMixed` | `boolean` | **yes** |
| `latency` | [`AnalyticsLatency`](#analyticslatency) | **yes** |
| `fraudCounts` | [`MessageFraudCounts`](#messagefraudcounts) | **yes** |
| `conversations` | `integer` | no |
| `bounced` | `integer` | no |
| `clicked` | `integer` | no |
| `answeredRate` | `number` | no |
| `avgCallDurationSeconds` | `number` | no |

### <a id="apikey"></a>`ApiKey`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `scopes` | [`ApiKeyScope`](#apikeyscope)[] | **yes** |
| `keyPrefix` | `string` | **yes** |
| `status` | [`ApiKeyStatus`](#apikeystatus) | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `lastUsedAt` | `string(date-time)` \| `null` | **yes** |

### <a id="apikeycreated"></a>`ApiKeyCreated`

Type: [`ApiKey`](#apikey) & `object`

### <a id="apikeyscope"></a>`ApiKeyScope`

What an API key is permitted to do. Creation refuses anything outside this set (422), case-sensitively, so the set cannot grow behind the contract.

One of: `send:sms`, `send:rcs`, `read:messages`, `read:analytics`, `read:logs`, `webhooks:manage`

### <a id="apikeystatus"></a>`ApiKeyStatus`

One of: `active`, `revoked`

### <a id="apiscope"></a>`ApiScope`

| Field | Type | Required |
| --- | --- | --- |
| `key` | [`ApiKeyScope`](#apikeyscope) | **yes** |
| `label` | `string` | **yes** |
| `category` | `string` | **yes** |

### <a id="approvalqueueitem"></a>`ApprovalQueueItem`

Type: [`ApprovalQueueSenderItem`](#approvalqueuesenderitem) \| [`ApprovalQueueTemplateItem`](#approvalqueuetemplateitem) \| [`ApprovalQueueRegistrationItem`](#approvalqueueregistrationitem)

### <a id="approvalqueueitemtype"></a>`ApprovalQueueItemType`

One of: `sender`, `template`, `registration`

### <a id="approvalqueuepage"></a>`ApprovalQueuePage`

| Field | Type | Required |
| --- | --- | --- |
| `items` | [`ApprovalQueueItem`](#approvalqueueitem)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |
| `total` | `integer` | **yes** |

### <a id="approvalqueueregistrationitem"></a>`ApprovalQueueRegistrationItem`

A compliance registration awaiting an operator decision. Until this existed the operator queue showed only senders and templates, so a submitted registration sat in pending_review with no path to approval.

| Field | Type | Required |
| --- | --- | --- |
| `itemType` | `registration` | **yes** |
| `id` | `string(uuid)` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `objectKey` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |
| `rejectionReason` | `string` \| `null` | no |
| `registrationId` | `string` \| `null` | no |
| `fields` | `object` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="approvalqueuesenderitem"></a>`ApprovalQueueSenderItem`

| Field | Type | Required |
| --- | --- | --- |
| `itemType` | `sender` | **yes** |
| `id` | `string(uuid)` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `header` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |
| `rejectionReason` | `string` \| `null` | no |
| `registrationId` | `string` \| `null` | no |
| `wabaId` | `string` \| `null` | no |
| `displayName` | `string` \| `null` | no |
| `phoneNumber` | `string` \| `null` | no |
| `emailDomain` | `string` \| `null` | no |
| `fromAddress` | `string` \| `null` | no |
| `fromName` | `string` \| `null` | no |
| `dnsRecords` | [`EmailDnsRecord`](#emaildnsrecord)[] | no |
| `callerIdNumber` | `string` \| `null` | no |
| `voiceVerification` | [`VoiceVerification`](#voiceverification) \| `null` | no |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="approvalqueuetemplateitem"></a>`ApprovalQueueTemplateItem`

| Field | Type | Required |
| --- | --- | --- |
| `itemType` | `template` | **yes** |
| `id` | `string(uuid)` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `name` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `body` | `string` \| `null` | no |
| `rcsContent` | [`RcsContent`](#rcscontent) \| `null` | no |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |
| `waContent` | [`WaContent`](#wacontent) \| `null` | no |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |
| `rejectionReason` | `string` \| `null` | no |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="approvalstatus"></a>`ApprovalStatus`

One of: `draft`, `submitted`, `pending_review`, `approved`, `rejected`, `blocked`, `expired`

### <a id="auditaction"></a>`AuditAction`

One of: `tenant.suspend`, `tenant.reinstate`, `tenant.flag_abuse`, `tenant.throttle`, `tenant.dismiss_flag`, `route.move_up`, `route.move_down`, `route.enable`, `route.disable`, `rate.default_update`, `rate.override_create`, `rate.override_update`, `rate.override_delete`, `sender.approve`, `sender.reject`, `template.approve`, `template.reject`, `ticket.resolve`, `ticket.reopen`, `route.create`, `route.delete`, `connection.create`, `connection.update`, `connection.enable`, `connection.disable`, `connection.delete`, `connection.test`, `registration.approve`, `registration.reject`, `operator.mfa_enabled`, `operator.mfa_disabled`

### <a id="auditlogentry"></a>`AuditLogEntry`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `occurredAt` | `string(date-time)` | **yes** |
| `actor` | `string` | **yes** |
| `action` | [`AuditAction`](#auditaction) | **yes** |
| `tenantId` | `string(uuid)` \| `null` | **yes** |
| `tenantName` | `string` \| `null` | **yes** |
| `targetLabel` | `string` | **yes** |
| `detail` | `string` | **yes** |

### <a id="auditlogpage"></a>`AuditLogPage`

| Field | Type | Required |
| --- | --- | --- |
| `entries` | [`AuditLogEntry`](#auditlogentry)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |
| `total` | `integer` | **yes** |

### <a id="authsession"></a>`AuthSession`

| Field | Type | Required |
| --- | --- | --- |
| `token` | `string` | **yes** |
| `expiresAt` | `string(date-time)` | **yes** |

### <a id="autorechargeconfig"></a>`AutoRechargeConfig`

| Field | Type | Required |
| --- | --- | --- |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `enabled` | `boolean` | **yes** |
| `thresholdMinor` | `integer` | **yes** |
| `topUpMinor` | `integer` | **yes** |
| `paymentMethodId` | `string` \| `null` | **yes** |
| `lastFailureAt` | `string(date-time)` \| `null` | **yes** |
| `lastFailureReason` | `string` \| `null` | **yes** |

### <a id="bindtype"></a>`BindType`

SMPP bind mode. A transmitter submits only, a receiver takes delivery receipts only, a transceiver does both over one session.

One of: `transmitter`, `receiver`, `transceiver`

### <a id="campaign"></a>`Campaign`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `listId` | `string` | **yes** |
| `senderId` | `string` | **yes** |
| `templateId` | `string` | **yes** |
| `fallback` | [`CampaignFallback`](#campaignfallback) \| `null` | no |
| `status` | [`CampaignStatus`](#campaignstatus) | **yes** |
| `scheduledAt` | `string(date-time)` \| `null` | no |
| `recipients` | `integer` | **yes** |
| `segmentsPerMessage` | `integer` | **yes** |
| `costMinorMin` | `integer` | **yes** |
| `costMinorMax` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `delivered` | `integer` | **yes** |
| `failed` | `integer` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `counts` | [`CampaignCounts`](#campaigncounts) | **yes** |
| `retryOf` | `string` \| `null` | **yes** |
| `retriedByCampaignId` | `string` \| `null` | **yes** |
| `sendStartedAt` | `string(date-time)` \| `null` | **yes** |
| `pausedAt` | `string(date-time)` \| `null` | **yes** |
| `cancelledAt` | `string(date-time)` \| `null` | **yes** |

### <a id="campaigncounts"></a>`CampaignCounts`

| Field | Type | Required |
| --- | --- | --- |
| `queued` | `integer` | **yes** |
| `sent` | `integer` | **yes** |
| `delivered` | `integer` | **yes** |
| `failed` | `integer` | **yes** |
| `read` | `integer` | **yes** |
| `cancelled` | `integer` | **yes** |

### <a id="campaignestimate"></a>`CampaignEstimate`

| Field | Type | Required |
| --- | --- | --- |
| `recipients` | `integer` | **yes** |
| `fallbackEligible` | `integer` | **yes** |
| `segmentsPerMessage` | `integer` | **yes** |
| `costMinorMin` | `integer` | **yes** |
| `costMinorMax` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `suppressedExcluded` | `integer` | **yes** |

### <a id="campaignfallback"></a>`CampaignFallback`

| Field | Type | Required |
| --- | --- | --- |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `senderId` | `string` | **yes** |
| `templateId` | `string` | **yes** |

### <a id="campaignstatus"></a>`CampaignStatus`

One of: `scheduled`, `queued`, `sending`, `paused`, `sent`, `failed`, `cancelled`

### <a id="carrierid"></a>`CarrierId`

One of: `JIO`, `AIRTEL`, `VI`, `BSNL`, `VERIZON`, `ATT`, `TMOBILE`, `EE`, `O2`, `VODAFONE_UK`, `THREE`, `ETISALAT`, `DU`

### <a id="carriertemplateregistration"></a>`CarrierTemplateRegistration`

The CARRIER's approval of an RCS template, which is a different approval from the template's own status. Relay approves a template against its compliance rules; Airtel or Vi review it separately, and a send quoting a template the carrier has never seen is refused at the gateway. A template can therefore read approved here and fail every send. Present only on RCS templates.

| Field | Type | Required |
| --- | --- | --- |
| `carrierTemplateId` | `string` \| `null` | no |
| `rejectionReason` | `string` \| `null` | no |
| `status` | `not_submitted` \| `pending` \| `approved` \| `rejected` | **yes** |
| `submittedAt` | `string(date-time)` \| `null` | no |
| `updatedAt` | `string(date-time)` \| `null` | no |
| `vendor` | `airtel` \| `vi` | no |

### <a id="channelid"></a>`ChannelId`

One of: `SMS`, `RCS`, `WHATSAPP`, `EMAIL`, `VOICE`

### <a id="compliancestanding"></a>`ComplianceStanding`

One of: `registered`, `grey`

### <a id="confirmemailrequest"></a>`ConfirmEmailRequest`

| Field | Type | Required |
| --- | --- | --- |
| `token` | `string` | **yes** |

### <a id="connection"></a>`Connection`

One SMPP bind into an operator. Platform-level configuration, never tenant-scoped. A single connection serves every corridor whose Route points at it, so credentials live here once rather than being duplicated per corridor.

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `label` | `string` | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `host` | `string` | **yes** |
| `port` | `integer` | **yes** |
| `systemId` | `string` | **yes** |
| `systemType` | `string` \| `null` | **yes** |
| `bindType` | [`BindType`](#bindtype) | **yes** |
| `maxTps` | `integer` | **yes** |
| `windowSize` | `integer` | **yes** |
| `enquireLinkSeconds` | `integer` | **yes** |
| `reconnectBackoffSeconds` | `integer` | **yes** |
| `passwordSetAt` | `string(date-time)` \| `null` | **yes** |
| `status` | [`ConnectionStatus`](#connectionstatus) | **yes** |
| `health` | [`ConnectionHealth`](#connectionhealth) | **yes** |

### <a id="connectioncreate"></a>`ConnectionCreate`

| Field | Type | Required |
| --- | --- | --- |
| `label` | `string` | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `host` | `string` | **yes** |
| `port` | `integer` | **yes** |
| `systemId` | `string` | **yes** |
| `systemType` | `string` \| `null` | no |
| `bindType` | [`BindType`](#bindtype) | **yes** |
| `maxTps` | `integer` | **yes** |
| `windowSize` | `integer` | no |
| `enquireLinkSeconds` | `integer` | no |
| `reconnectBackoffSeconds` | `integer` | no |
| `password` | `string` | **yes** |

### <a id="connectionhealth"></a>`ConnectionHealth`

Live bind state, reported by the backend. Read-only — the operator console never sets this.

| Field | Type | Required |
| --- | --- | --- |
| `status` | [`ConnectionHealthStatus`](#connectionhealthstatus) | **yes** |
| `lastBoundAt` | `string(date-time)` \| `null` | **yes** |
| `lastError` | `string` \| `null` | **yes** |

### <a id="connectionhealthstatus"></a>`ConnectionHealthStatus`

One of: `bound`, `unbound`, `error`

### <a id="connectionpage"></a>`ConnectionPage`

Unpaginated by design: the connection count is bounded by how many operator binds the platform holds, not by customer growth.

| Field | Type | Required |
| --- | --- | --- |
| `connections` | [`Connection`](#connection)[] | **yes** |

### <a id="connectionstatus"></a>`ConnectionStatus`

Whether this bind may carry traffic. A connection is always created disabled; enabling it is a separate, audited decision.

One of: `active`, `disabled`

### <a id="connectiontestresult"></a>`ConnectionTestResult`

Outcome of a one-off bind attempt. Testing never changes the connection's status — a passing test still leaves it disabled.

| Field | Type | Required |
| --- | --- | --- |
| `ok` | `boolean` | **yes** |
| `status` | [`ConnectionHealthStatus`](#connectionhealthstatus) | **yes** |
| `message` | `string` | **yes** |
| `testedAt` | `string(date-time)` | **yes** |

### <a id="connectionupdate"></a>`ConnectionUpdate`

Every field optional — only the keys present are changed. Omitting `password` leaves the stored one untouched; supplying it replaces it.

| Field | Type | Required |
| --- | --- | --- |
| `label` | `string` | no |
| `carrier` | [`CarrierId`](#carrierid) | no |
| `environment` | [`Environment`](#environment) | no |
| `host` | `string` | no |
| `port` | `integer` | no |
| `systemId` | `string` | no |
| `systemType` | `string` \| `null` | no |
| `bindType` | [`BindType`](#bindtype) | no |
| `maxTps` | `integer` | no |
| `windowSize` | `integer` | no |
| `enquireLinkSeconds` | `integer` | no |
| `reconnectBackoffSeconds` | `integer` | no |
| `password` | `string` | no |

### <a id="consentstate"></a>`ConsentState`

One of: `opted_in`, `opted_out`, `unknown`

### <a id="contact"></a>`Contact`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `msisdn` | `string` | **yes** |
| `email` | `string` \| `null` | no |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `fields` | `object` | **yes** |
| `consent` | `object` | **yes** |
| `consentedAt` | `object` \| `null` | no |
| `createdAt` | `string(date-time)` | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |
| `phoneSuppressed` | `boolean` | no |
| `emailSuppressed` | `boolean` | no |

### <a id="contactlist"></a>`ContactList`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `contactCount` | `integer` | **yes** |
| `consentedCounts` | `object` | **yes** |
| `waSessionActive` | `integer` | no |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="contactpage"></a>`ContactPage`

| Field | Type | Required |
| --- | --- | --- |
| `contacts` | [`Contact`](#contact)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |

### <a id="conversation"></a>`Conversation`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `contactId` | `string(uuid)` | **yes** |
| `contactName` | `string` | **yes** |
| `identity` | `string` | **yes** |
| `channel` | [`ConversationChannelId`](#conversationchannelid) | **yes** |
| `status` | [`ConversationStatus`](#conversationstatus) | **yes** |
| `unread` | `boolean` | **yes** |
| `suppressed` | `boolean` | **yes** |
| `lastMessagePreview` | `string` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |

### <a id="conversationchannelid"></a>`ConversationChannelId`

The subset of ChannelId capable of a two-way conversational thread (registry capabilities.twoWay === true). Voice is a call, not an async text thread, so it is excluded here even though it remains a valid ChannelId for campaigns/senders/templates.

One of: `SMS`, `RCS`, `WHATSAPP`, `EMAIL`

### <a id="conversationdetail"></a>`ConversationDetail`

Type: [`Conversation`](#conversation) & `object`

### <a id="conversationmessage"></a>`ConversationMessage`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `conversationId` | `string(uuid)` | **yes** |
| `direction` | `inbound` \| `outbound` | **yes** |
| `body` | `string` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `keywordMatched` | `string` \| `null` | no |
| `segments` | `integer` \| `null` | no |
| `status` | [`MessageStatus`](#messagestatus) \| `null` | no |

### <a id="conversationpage"></a>`ConversationPage`

| Field | Type | Required |
| --- | --- | --- |
| `conversations` | [`Conversation`](#conversation)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | no |

### <a id="conversationstatus"></a>`ConversationStatus`

One of: `open`, `closed`

### <a id="countrycode"></a>`CountryCode`

One of: `IN`, `US`, `GB`, `AE`

### <a id="currencycode"></a>`CurrencyCode`

One of: `INR`, `USD`, `GBP`, `AED`

### <a id="dataretentionsettings"></a>`DataRetentionSettings`

| Field | Type | Required |
| --- | --- | --- |
| `messageLogRetentionDays` | `30` \| `90` \| `180` \| `365` | **yes** |

### <a id="deliveryfloorrule"></a>`DeliveryFloorRule`

| Field | Type | Required |
| --- | --- | --- |
| `enabled` | `boolean` | **yes** |
| `range` | [`AnalyticsRange`](#analyticsrange) | **yes** |
| `thresholdPercent` | `number` | **yes** |
| `recipients` | `string`[] | **yes** |

### <a id="dltcategory"></a>`DltCategory`

India DLT content-template category, as approved on DLT. Distinct from TemplateCategory, which is Meta's WhatsApp taxonomy: DLT's TRANSACTIONAL is restricted to banking and OTP traffic, and DLT has no equivalent of UTILITY.

One of: `PROMOTIONAL`, `SERVICE_IMPLICIT`, `SERVICE_EXPLICIT`, `TRANSACTIONAL`

### <a id="emailcontent"></a>`EmailContent`

| Field | Type | Required |
| --- | --- | --- |
| `subject` | `string` | **yes** |
| `bodyHtml` | `string` | **yes** |
| `preheader` | `string` \| `null` | no |

### <a id="emaildnsrecord"></a>`EmailDnsRecord`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `SPF` \| `DKIM` \| `DMARC` | **yes** |
| `host` | `string` | **yes** |
| `value` | `string` | **yes** |
| `status` | `pending` \| `verified` \| `failed` | **yes** |

### <a id="environment"></a>`Environment`

One of: `live`, `test`

### <a id="error"></a>`Error`

| Field | Type | Required |
| --- | --- | --- |
| `error` | `object` | **yes** |

### <a id="forgotpasswordrequest"></a>`ForgotPasswordRequest`

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |

### <a id="importsummary"></a>`ImportSummary`

| Field | Type | Required |
| --- | --- | --- |
| `created` | `integer` | **yes** |
| `updated` | `integer` | **yes** |
| `skipped` | `integer` | **yes** |
| `invalid` | `integer` | **yes** |
| `listId` | `string(uuid)` | **yes** |
| `conflicts` | `object`[] | **yes** |

### <a id="inviteteammemberrequest"></a>`InviteTeamMemberRequest`

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |
| `role` | `admin` \| `member` | **yes** |

### <a id="invoice"></a>`Invoice`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `periodStart` | `string(date-time)` | **yes** |
| `periodEnd` | `string(date-time)` | **yes** |
| `status` | [`InvoiceStatus`](#invoicestatus) | **yes** |
| `subtotalMinor` | `integer` | **yes** |
| `taxRatePercent` | `integer` | **yes** |
| `taxMinor` | `integer` | **yes** |
| `totalMinor` | `integer` | **yes** |
| `lineItems` | [`InvoiceLineItem`](#invoicelineitem)[] | **yes** |

### <a id="invoicelineitem"></a>`InvoiceLineItem`

| Field | Type | Required |
| --- | --- | --- |
| `campaignId` | `string` \| `null` | **yes** |
| `campaignName` | `string` \| `null` | **yes** |
| `journeyId` | `string` \| `null` | **yes** |
| `journeyName` | `string` \| `null` | **yes** |
| `channel` | [`ChannelId`](#channelid) \| `null` | **yes** |
| `amountMinor` | `integer` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="invoicepage"></a>`InvoicePage`

| Field | Type | Required |
| --- | --- | --- |
| `invoices` | [`Invoice`](#invoice)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |

### <a id="invoicestatus"></a>`InvoiceStatus`

One of: `current`, `issued`

### <a id="ipallowlistentry"></a>`IpAllowlistEntry`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `cidr` | `string` | **yes** |
| `label` | `string` \| `null` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="journey"></a>`Journey`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `status` | [`JourneyStatus`](#journeystatus) | **yes** |
| `trigger` | [`JourneyTrigger`](#journeytrigger) | **yes** |
| `steps` | [`JourneyStep`](#journeystep)[] | **yes** |
| `recipients` | `integer` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `activatedAt` | `string(date-time)` \| `null` | **yes** |
| `completedCount` | `integer` | **yes** |
| `exitedSuppressedCount` | `integer` | **yes** |

### <a id="journeydetail"></a>`JourneyDetail`

Type: [`Journey`](#journey) & `object`

### <a id="journeyfunnel"></a>`JourneyFunnel`

| Field | Type | Required |
| --- | --- | --- |
| `stepCounts` | [`JourneyStepCount`](#journeystepcount)[] | **yes** |
| `completed` | `integer` | **yes** |
| `exitedSuppressed` | `integer` | **yes** |
| `totalEnrolled` | `integer` | **yes** |

### <a id="journeystatus"></a>`JourneyStatus`

One of: `draft`, `active`, `paused`, `archived`

### <a id="journeystep"></a>`JourneyStep`

Type: [`JourneyStepSend`](#journeystepsend) \| [`JourneyStepWait`](#journeystepwait)

### <a id="journeystepcount"></a>`JourneyStepCount`

| Field | Type | Required |
| --- | --- | --- |
| `stepId` | `string` | **yes** |
| `count` | `integer` | **yes** |

### <a id="journeystepsend"></a>`JourneyStepSend`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `send` | **yes** |
| `id` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `senderId` | `string` | **yes** |
| `templateId` | `string` | **yes** |
| `senderStatus` | [`ApprovalStatus`](#approvalstatus) | no |
| `templateStatus` | [`ApprovalStatus`](#approvalstatus) | no |

### <a id="journeystepwait"></a>`JourneyStepWait`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `wait` | **yes** |
| `id` | `string` | **yes** |
| `durationMinutes` | `integer` | **yes** |

### <a id="journeytrigger"></a>`JourneyTrigger`

Type: [`JourneyTriggerListEntry`](#journeytriggerlistentry) \| [`JourneyTriggerScheduled`](#journeytriggerscheduled)

### <a id="journeytriggerlistentry"></a>`JourneyTriggerListEntry`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `list_entry` | **yes** |
| `listId` | `string` | **yes** |

### <a id="journeytriggerscheduled"></a>`JourneyTriggerScheduled`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `scheduled` | **yes** |
| `listId` | `string` | **yes** |
| `runAt` | `string(date-time)` | **yes** |

### <a id="ledgerentry"></a>`LedgerEntry`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `type` | [`LedgerEntryType`](#ledgerentrytype) | **yes** |
| `amountMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `campaignId` | `string` \| `null` | **yes** |
| `campaignName` | `string` \| `null` | **yes** |
| `journeyId` | `string` \| `null` | **yes** |
| `journeyName` | `string` \| `null` | **yes** |
| `description` | `string` | **yes** |
| `balanceAfterMinor` | `integer` | **yes** |

### <a id="ledgerentrytype"></a>`LedgerEntryType`

charge = debit. topup, auto_recharge and refund = credit. A refund is the release of a hold placed by a send that did not deliver.

One of: `charge`, `topup`, `auto_recharge`, `refund`

### <a id="ledgerpage"></a>`LedgerPage`

| Field | Type | Required |
| --- | --- | --- |
| `entries` | [`LedgerEntry`](#ledgerentry)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |

### <a id="loginmfachallengeresult"></a>`LoginMfaChallengeResult`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `mfa_challenge` | **yes** |
| `challenge` | [`MfaChallenge`](#mfachallenge) | **yes** |

### <a id="loginrequest"></a>`LoginRequest`

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |
| `password` | `string` | **yes** |

### <a id="loginresult"></a>`LoginResult`

Type: [`LoginSessionResult`](#loginsessionresult) \| [`LoginMfaChallengeResult`](#loginmfachallengeresult)

### <a id="loginsessionresult"></a>`LoginSessionResult`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `session` | **yes** |
| `session` | [`AuthSession`](#authsession) | **yes** |

### <a id="lowbalancerule"></a>`LowBalanceRule`

| Field | Type | Required |
| --- | --- | --- |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `enabled` | `boolean` | **yes** |
| `thresholdMinor` | `integer` | **yes** |
| `recipients` | `string`[] | **yes** |

### <a id="me"></a>`Me`

| Field | Type | Required |
| --- | --- | --- |
| `userId` | `string(uuid)` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `name` | `string` | **yes** |
| `email` | `string(email)` | **yes** |
| `capabilities` | `string`[] | **yes** |
| `emailVerified` | `boolean` | **yes** |
| `mfaEnabled` | `boolean` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `role` | [`TeamRole`](#teamrole) | **yes** |

### <a id="message"></a>`Message`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `campaignId` | `string(uuid)` \| `null` | **yes** |
| `journeyId` | `string` \| `null` | **yes** |
| `journeyStepId` | `string` \| `null` | **yes** |
| `msisdn` | `string` | **yes** |
| `email` | `string` \| `null` | no |
| `status` | [`MessageStatus`](#messagestatus) | **yes** |
| `deliveredChannel` | [`ChannelId`](#channelid) \| `null` | no |
| `errorCode` | `string` \| `null` | no |
| `errorClass` | [`MessageErrorClass`](#messageerrorclass) \| `null` | no |
| `segments` | `integer` | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |
| `sentAt` | `string(date-time)` \| `null` | no |
| `deliveredAt` | `string(date-time)` \| `null` | no |
| `fraudFlag` | [`MessageFraudFlag`](#messagefraudflag) | no |
| `tappedLabel` | `string` \| `null` | no |
| `callOutcome` | `answered` \| `no_answer` \| `busy` \| `failed` \| `voicemail` \| `null` | no |
| `durationSeconds` | `integer` \| `null` | no |

### <a id="messageerrorclass"></a>`MessageErrorClass`

One of: `blocked`, `unreachable`, `rejected`, `expired`

### <a id="messagefraudcounts"></a>`MessageFraudCounts`

| Field | Type | Required |
| --- | --- | --- |
| `velocity` | `integer` | **yes** |
| `geoAnomaly` | `integer` | **yes** |
| `blocked` | `integer` | **yes** |

### <a id="messagefraudflag"></a>`MessageFraudFlag`

One of: `none`, `velocity`, `geo_anomaly`, `blocked`

### <a id="messagelogentry"></a>`MessageLogEntry`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `campaignId` | `string(uuid)` \| `null` | **yes** |
| `campaignName` | `string` \| `null` | **yes** |
| `journeyId` | `string` \| `null` | **yes** |
| `journeyName` | `string` \| `null` | **yes** |
| `conversationId` | `string` \| `null` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `msisdn` | `string` | **yes** |
| `email` | `string` \| `null` | no |
| `status` | [`MessageStatus`](#messagestatus) | **yes** |
| `deliveredChannel` | [`ChannelId`](#channelid) \| `null` | no |
| `errorCode` | `string` \| `null` | no |
| `errorClass` | [`MessageErrorClass`](#messageerrorclass) \| `null` | no |
| `segments` | `integer` | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |
| `sentAt` | `string(date-time)` \| `null` | no |
| `deliveredAt` | `string(date-time)` \| `null` | no |
| `fraudFlag` | [`MessageFraudFlag`](#messagefraudflag) | no |
| `callOutcome` | `answered` \| `no_answer` \| `busy` \| `failed` \| `voicemail` \| `null` | no |
| `durationSeconds` | `integer` \| `null` | no |
| `costMinor` | `integer(int64)` | no |
| `currency` | `string` | no |

### <a id="messagelogpage"></a>`MessageLogPage`

| Field | Type | Required |
| --- | --- | --- |
| `messages` | [`MessageLogEntry`](#messagelogentry)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | no |
| `campaignName` | `string` \| `null` | no |
| `journeyName` | `string` \| `null` | no |

### <a id="messagepage"></a>`MessagePage`

| Field | Type | Required |
| --- | --- | --- |
| `messages` | [`Message`](#message)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | no |

### <a id="messagestatus"></a>`MessageStatus`

One of: `queued`, `sent`, `delivered`, `failed`, `read`, `cancelled`, `rejected`

### <a id="mfachallenge"></a>`MfaChallenge`

| Field | Type | Required |
| --- | --- | --- |
| `challengeToken` | `string` | **yes** |
| `methods` | `totp` \| `recovery_code`[] | **yes** |
| `expiresAt` | `string(date-time)` | **yes** |

### <a id="mfachallengerequest"></a>`MfaChallengeRequest`

| Field | Type | Required |
| --- | --- | --- |
| `challengeToken` | `string` | **yes** |
| `code` | `string` | **yes** |
| `method` | `totp` \| `recovery_code` | **yes** |

### <a id="mfacoderequest"></a>`MfaCodeRequest`

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

### <a id="mfaenrollment"></a>`MfaEnrollment`

| Field | Type | Required |
| --- | --- | --- |
| `secret` | `string` | **yes** |
| `otpauthUri` | `string` | **yes** |
| `qrSvg` | `string` | **yes** |

### <a id="mfarecoverycodes"></a>`MfaRecoveryCodes`

| Field | Type | Required |
| --- | --- | --- |
| `recoveryCodes` | `string`[] | **yes** |

### <a id="newconversationreplyrequest"></a>`NewConversationReplyRequest`

| Field | Type | Required |
| --- | --- | --- |
| `body` | `string` | **yes** |

### <a id="newsupportmessagerequest"></a>`NewSupportMessageRequest`

| Field | Type | Required |
| --- | --- | --- |
| `body` | `string` | **yes** |

### <a id="newsupportticketrequest"></a>`NewSupportTicketRequest`

| Field | Type | Required |
| --- | --- | --- |
| `subject` | `string` | **yes** |
| `category` | [`TicketCategory`](#ticketcategory) | **yes** |
| `body` | `string` | **yes** |

### <a id="operatorloginmfachallengeresult"></a>`OperatorLoginMfaChallengeResult`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `mfa_challenge` | **yes** |
| `challenge` | [`MfaChallenge`](#mfachallenge) | **yes** |

### <a id="operatorloginrequest"></a>`OperatorLoginRequest`

| Field | Type | Required |
| --- | --- | --- |
| `email` | `string(email)` | **yes** |
| `password` | `string` | **yes** |

### <a id="operatorloginresult"></a>`OperatorLoginResult`

Type: [`OperatorLoginSessionResult`](#operatorloginsessionresult) \| [`OperatorLoginMfaChallengeResult`](#operatorloginmfachallengeresult)

### <a id="operatorloginsessionresult"></a>`OperatorLoginSessionResult`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `session` | **yes** |
| `session` | [`AuthSession`](#authsession) | **yes** |
| `token` | `string` | no |
| `expiresAt` | `string(date-time)` | no |

### <a id="operatormargingroup"></a>`OperatorMarginGroup`

| Field | Type | Required |
| --- | --- | --- |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `revenueMinor` | `integer` | **yes** |
| `costMinor` | `integer` | **yes** |
| `marginMinor` | `integer` | **yes** |
| `marginPct` | `number` | **yes** |
| `byTenant` | [`OperatorMarginRow`](#operatormarginrow)[] | **yes** |
| `byChannel` | [`OperatorMarginRow`](#operatormarginrow)[] | **yes** |
| `byCountry` | [`OperatorMarginRow`](#operatormarginrow)[] | **yes** |

### <a id="operatormarginreport"></a>`OperatorMarginReport`

| Field | Type | Required |
| --- | --- | --- |
| `range` | [`AnalyticsRange`](#analyticsrange) | **yes** |
| `groups` | [`OperatorMarginGroup`](#operatormargingroup)[] | **yes** |

### <a id="operatormarginrow"></a>`OperatorMarginRow`

| Field | Type | Required |
| --- | --- | --- |
| `key` | `string` | **yes** |
| `label` | `string` | **yes** |
| `revenueMinor` | `integer` | **yes** |
| `costMinor` | `integer` | **yes** |
| `marginMinor` | `integer` | **yes** |
| `marginPct` | `number` | **yes** |

### <a id="operatorme"></a>`OperatorMe`

| Field | Type | Required |
| --- | --- | --- |
| `operatorId` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `email` | `string(email)` | **yes** |
| `role` | `operator` \| `admin` | **yes** |
| `mfaEnabled` | `boolean` | **yes** |

### <a id="operatorusagebychannel"></a>`OperatorUsageByChannel`

| Field | Type | Required |
| --- | --- | --- |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `messageCount` | `integer` | **yes** |

### <a id="operatorusagebycountry"></a>`OperatorUsageByCountry`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `messageCount` | `integer` | **yes** |

### <a id="operatorusagebyday"></a>`OperatorUsageByDay`

| Field | Type | Required |
| --- | --- | --- |
| `date` | `string(date)` | **yes** |
| `messageCount` | `integer` | **yes** |

### <a id="operatorusagebytenant"></a>`OperatorUsageByTenant`

| Field | Type | Required |
| --- | --- | --- |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `messageCount` | `integer` | **yes** |

### <a id="operatorusagereport"></a>`OperatorUsageReport`

| Field | Type | Required |
| --- | --- | --- |
| `range` | [`AnalyticsRange`](#analyticsrange) | **yes** |
| `totalMessages` | `integer` | **yes** |
| `byDay` | [`OperatorUsageByDay`](#operatorusagebyday)[] | **yes** |
| `byChannel` | [`OperatorUsageByChannel`](#operatorusagebychannel)[] | **yes** |
| `byCountry` | [`OperatorUsageByCountry`](#operatorusagebycountry)[] | **yes** |
| `byTenant` | [`OperatorUsageByTenant`](#operatorusagebytenant)[] | **yes** |

### <a id="paymentmethod"></a>`PaymentMethod`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `brand` | [`PaymentMethodBrand`](#paymentmethodbrand) | **yes** |
| `last4` | `string` | **yes** |
| `isDefault` | `boolean` | **yes** |

### <a id="paymentmethodbrand"></a>`PaymentMethodBrand`

One of: `visa`, `mastercard`, `amex`

### <a id="pricingrate"></a>`PricingRate`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `perSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |

### <a id="ratecard"></a>`RateCard`

| Field | Type | Required |
| --- | --- | --- |
| `defaults` | [`RateCardRow`](#ratecardrow)[] | **yes** |
| `overrides` | [`RateOverrideRow`](#rateoverriderow)[] | **yes** |

### <a id="ratecardrow"></a>`RateCardRow`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `perSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `costReferenceMinor` | `integer` \| `null` | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |

### <a id="ratelimittier"></a>`RateLimitTier`

| Field | Type | Required |
| --- | --- | --- |
| `environment` | [`Environment`](#environment) | **yes** |
| `tierName` | `string` | **yes** |
| `requestsPerSecond` | `integer` | **yes** |
| `burst` | `integer` | **yes** |

### <a id="rateoverriderow"></a>`RateOverrideRow`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `perSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |
| `costReferenceMinor` | `integer` \| `null` | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |

### <a id="rcscapability"></a>`RcsCapability`

| Field | Type | Required |
| --- | --- | --- |
| `features` | `string`[] | no |
| `msisdn` | `string` | **yes** |
| `reachable` | `boolean` | **yes** |

### <a id="rcscapabilityreport"></a>`RcsCapabilityReport`

| Field | Type | Required |
| --- | --- | --- |
| `checkedCount` | `integer` | **yes** |
| `featuresIncluded` | `boolean` | **yes** |
| `reachableCount` | `integer` | **yes** |
| `results` | [`RcsCapability`](#rcscapability)[] | **yes** |
| `vendor` | `airtel` \| `vi` | **yes** |

### <a id="rcscard"></a>`RcsCard`

| Field | Type | Required |
| --- | --- | --- |
| `mediaUrl` | `string` | **yes** |
| `title` | `string` | **yes** |
| `description` | `string` | **yes** |

### <a id="rcscardcontent"></a>`RcsCardContent`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `card` | **yes** |
| `card` | [`RcsCard`](#rcscard) | **yes** |
| `suggestions` | [`RcsSuggestion`](#rcssuggestion)[] | **yes** |

### <a id="rcscontent"></a>`RcsContent`

Type: [`RcsTextContent`](#rcstextcontent) \| [`RcsCardContent`](#rcscardcontent)

### <a id="rcsdialsuggestion"></a>`RcsDialSuggestion`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `dial` | **yes** |
| `text` | `string` | **yes** |
| `phoneNumber` | `string` | **yes** |

### <a id="rcsfeature"></a>`RcsFeature`

A Google RBM feature name. Both carriers' phone-level capability APIs speak this vocabulary, which is why one Relay answer means one thing regardless of which carrier served it. NOT exhaustive — carrier-specific names outside this list are passed through, so treat an unrecognised value as an unknown feature rather than an error. PDF_IN_RICH_CARDS and ACTION_OPEN_URL_IN_WEBVIEW are Airtel-only; REVOCATION is Vi-only.

One of: `RICHCARD_STANDALONE`, `RICHCARD_CAROUSEL`, `ACTION_CREATE_CALENDAR_EVENT`, `ACTION_DIAL`, `ACTION_OPEN_URL`, `ACTION_SHARE_LOCATION`, `ACTION_VIEW_LOCATION`, `PDF_IN_RICH_CARDS`, `ACTION_OPEN_URL_IN_WEBVIEW`, `REVOCATION`

### <a id="rcsopenurlsuggestion"></a>`RcsOpenUrlSuggestion`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `open_url` | **yes** |
| `text` | `string` | **yes** |
| `url` | `string` | **yes** |

### <a id="rcsreplysuggestion"></a>`RcsReplySuggestion`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `reply` | **yes** |
| `text` | `string` | **yes** |

### <a id="rcssuggestion"></a>`RcsSuggestion`

Type: [`RcsReplySuggestion`](#rcsreplysuggestion) \| [`RcsOpenUrlSuggestion`](#rcsopenurlsuggestion) \| [`RcsDialSuggestion`](#rcsdialsuggestion)

### <a id="rcstextcontent"></a>`RcsTextContent`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `text` | **yes** |
| `text` | `string` | **yes** |
| `suggestions` | [`RcsSuggestion`](#rcssuggestion)[] | **yes** |

### <a id="registration"></a>`Registration`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `objectKey` | `string` | **yes** |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |
| `rejectionReason` | `string` \| `null` | no |
| `registrationId` | `string` \| `null` | no |
| `fields` | `object` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |

### <a id="rejectitemrequest"></a>`RejectItemRequest`

| Field | Type | Required |
| --- | --- | --- |
| `reason` | `string` | **yes** |

### <a id="reportfrequency"></a>`ReportFrequency`

One of: `daily`, `weekly`, `monthly`

### <a id="resetpasswordrequest"></a>`ResetPasswordRequest`

| Field | Type | Required |
| --- | --- | --- |
| `token` | `string` | **yes** |
| `password` | `string` | **yes** |

### <a id="route"></a>`Route`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `label` | `string` | **yes** |
| `priority` | `integer` | **yes** |
| `complianceStanding` | [`ComplianceStanding`](#compliancestanding) | **yes** |
| `costPerSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `status` | [`RouteStatus`](#routestatus) | **yes** |
| `connectionId` | `string(uuid)` \| `null` | **yes** |

### <a id="routecreate"></a>`RouteCreate`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `carrier` | [`CarrierId`](#carrierid) | **yes** |
| `label` | `string` | **yes** |
| `complianceStanding` | [`ComplianceStanding`](#compliancestanding) | **yes** |
| `costPerSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `connectionId` | `string(uuid)` \| `null` | no |

### <a id="routepage"></a>`RoutePage`

| Field | Type | Required |
| --- | --- | --- |
| `routes` | [`Route`](#route)[] | **yes** |

### <a id="routestatus"></a>`RouteStatus`

One of: `active`, `disabled`

### <a id="scheduledreport"></a>`ScheduledReport`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `frequency` | [`ReportFrequency`](#reportfrequency) | **yes** |
| `range` | [`AnalyticsRange`](#analyticsrange) | **yes** |
| `recipients` | `string`[] | **yes** |
| `paused` | `boolean` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `recentSends` | `string(date-time)`[] | **yes** |
| `nextSendAt` | `string(date-time)` \| `null` | **yes** |

### <a id="sendmessagerequest"></a>`SendMessageRequest`

| Field | Type | Required |
| --- | --- | --- |
| `senderId` | `string(uuid)` | **yes** |
| `to` | `string` | **yes** |
| `body` | `string` | **yes** |
| `variables` | `object` \| `null` | no |
| `templateId` | `string(uuid)` \| `null` | no |

### <a id="sendmessageresult"></a>`SendMessageResult`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` \| `null` | **yes** |
| `status` | `queued` \| `sent` \| `failed` \| `rejected` | **yes** |
| `segments` | `integer` | **yes** |
| `costMinor` | `integer(int64)` | **yes** |
| `currency` | `string` | **yes** |
| `errorCode` | `string` \| `null` | no |

### <a id="senderid"></a>`SenderId`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `header` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |
| `rejectionReason` | `string` \| `null` | no |
| `registrationId` | `string` \| `null` | no |
| `wabaId` | `string` \| `null` | no |
| `displayName` | `string` \| `null` | no |
| `phoneNumber` | `string` \| `null` | no |
| `qualityRating` | [`WaQualityRating`](#waqualityrating) \| `null` | no |
| `messagingTier` | [`WaMessagingTier`](#wamessagingtier) \| `null` | no |
| `emailDomain` | `string` \| `null` | no |
| `fromAddress` | `string` \| `null` | no |
| `fromName` | `string` \| `null` | no |
| `dnsRecords` | [`EmailDnsRecord`](#emaildnsrecord)[] | no |
| `callerIdNumber` | `string` \| `null` | no |
| `voiceVerification` | [`VoiceVerification`](#voiceverification) \| `null` | no |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="session"></a>`Session`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `device` | `string` | **yes** |
| `browser` | `string` | **yes** |
| `location` | `string` | **yes** |
| `ipAddress` | `string` | **yes** |
| `lastActiveAt` | `string(date-time)` | **yes** |
| `current` | `boolean` | **yes** |

### <a id="signuprequest"></a>`SignupRequest`

| Field | Type | Required |
| --- | --- | --- |
| `fullName` | `string` | **yes** |
| `email` | `string(email)` | **yes** |
| `password` | `string` | **yes** |
| `orgName` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |

### <a id="spendceilingrule"></a>`SpendCeilingRule`

| Field | Type | Required |
| --- | --- | --- |
| `enabled` | `boolean` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `thresholdMinor` | `integer` | **yes** |
| `recipients` | `string`[] | **yes** |

### <a id="ssoconfig"></a>`SsoConfig`

| Field | Type | Required |
| --- | --- | --- |
| `enabled` | `boolean` | **yes** |
| `provider` | `saml` \| `oidc` | **yes** |
| `metadataUrl` | `string` \| `null` | **yes** |
| `entityId` | `string` \| `null` | **yes** |

### <a id="supportmessage"></a>`SupportMessage`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `author` | `customer` \| `operator` | **yes** |
| `authorName` | `string` | **yes** |
| `body` | `string` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="supportticket"></a>`SupportTicket`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `subject` | `string` | **yes** |
| `category` | [`TicketCategory`](#ticketcategory) | **yes** |
| `status` | [`TicketStatus`](#ticketstatus) | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |

### <a id="supportticketdetail"></a>`SupportTicketDetail`

Type: [`SupportTicket`](#supportticket) & `object`

### <a id="supportticketpage"></a>`SupportTicketPage`

| Field | Type | Required |
| --- | --- | --- |
| `tickets` | [`SupportTicket`](#supportticket)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |
| `total` | `integer` | **yes** |

### <a id="suppression"></a>`Suppression`

| Field | Type | Required |
| --- | --- | --- |
| `msisdn` | `string` \| `null` | no |
| `email` | `string` \| `null` | no |
| `reason` | [`SuppressionReason`](#suppressionreason) | **yes** |
| `note` | `string` | no |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="suppressionpage"></a>`SuppressionPage`

| Field | Type | Required |
| --- | --- | --- |
| `suppressions` | [`Suppression`](#suppression)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |
| `total` | `integer` | **yes** |

### <a id="suppressionreason"></a>`SuppressionReason`

One of: `opted_out_keyword`, `manual`, `hard_bounce`, `imported_dnc`

### <a id="teammember"></a>`TeamMember`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` \| `null` | **yes** |
| `email` | `string(email)` | **yes** |
| `role` | [`TeamRole`](#teamrole) | **yes** |
| `status` | `active` \| `invited` | **yes** |
| `invitedAt` | `string(date-time)` \| `null` | **yes** |

### <a id="teammemberpage"></a>`TeamMemberPage`

| Field | Type | Required |
| --- | --- | --- |
| `members` | [`TeamMember`](#teammember)[] | **yes** |

### <a id="teammemberroleupdaterequest"></a>`TeamMemberRoleUpdateRequest`

| Field | Type | Required |
| --- | --- | --- |
| `role` | [`TeamRole`](#teamrole) | **yes** |

### <a id="teamrole"></a>`TeamRole`

One of: `owner`, `admin`, `member`

### <a id="template"></a>`Template`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `senderId` | `string(uuid)` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `body` | `string` \| `null` | no |
| `rcsContent` | [`RcsContent`](#rcscontent) \| `null` | no |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |
| `registrationId` | `string` \| `null` | no |
| `dltCategory` | [`DltCategory`](#dltcategory) \| `null` | no |
| `waContent` | [`WaContent`](#wacontent) \| `null` | no |
| `emailContent` | [`EmailContent`](#emailcontent) \| `null` | no |
| `variables` | `string`[] | **yes** |
| `ctaUrl` | `string` \| `null` | no |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |
| `rejectionReason` | `string` \| `null` | no |
| `carrierRegistration` | [`CarrierTemplateRegistration`](#carriertemplateregistration) \| `null` | no |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="templatecategory"></a>`TemplateCategory`

One of: `MARKETING`, `UTILITY`, `AUTHENTICATION`, `TRANSACTIONAL`

### <a id="tenant"></a>`Tenant`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `plan` | `string` | **yes** |
| `status` | [`TenantStatus`](#tenantstatus) | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="tenantcompliancesummary"></a>`TenantComplianceSummary`

| Field | Type | Required |
| --- | --- | --- |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `status` | [`ApprovalStatus`](#approvalstatus) | **yes** |

### <a id="tenantdetail"></a>`TenantDetail`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `plan` | `string` | **yes** |
| `status` | [`TenantStatus`](#tenantstatus) | **yes** |
| `createdAt` | `string(date-time)` | **yes** |
| `compliance` | [`TenantComplianceSummary`](#tenantcompliancesummary)[] | **yes** |
| `usage` | [`TenantUsageSnapshot`](#tenantusagesnapshot) | **yes** |
| `flaggedAt` | `string(date-time)` \| `null` | **yes** |
| `flagReason` | `string` \| `null` | **yes** |
| `throttledRatePerSecond` | `integer` \| `null` | **yes** |

### <a id="tenantpage"></a>`TenantPage`

| Field | Type | Required |
| --- | --- | --- |
| `tenants` | [`Tenant`](#tenant)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |
| `total` | `integer` | **yes** |

### <a id="tenantrateoverride"></a>`TenantRateOverride`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `perSegmentMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `updatedAt` | `string(date-time)` | **yes** |
| `category` | [`TemplateCategory`](#templatecategory) \| `null` | no |

### <a id="tenantstatus"></a>`TenantStatus`

One of: `active`, `throttled`, `suspended`, `pending`

### <a id="tenantusagesnapshot"></a>`TenantUsageSnapshot`

| Field | Type | Required |
| --- | --- | --- |
| `messagesSent30d` | `integer` | **yes** |
| `lastActivityAt` | `string(date-time)` \| `null` | **yes** |

### <a id="throttletenantrequest"></a>`ThrottleTenantRequest`

| Field | Type | Required |
| --- | --- | --- |
| `ratePerSecond` | `integer` | **yes** |
| `reason` | `string` | no |

### <a id="ticketcategory"></a>`TicketCategory`

One of: `billing`, `technical`, `compliance`, `other`

### <a id="ticketstatus"></a>`TicketStatus`

One of: `open`, `pending`, `resolved`

### <a id="updateautorechargerequest"></a>`UpdateAutoRechargeRequest`

| Field | Type | Required |
| --- | --- | --- |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `enabled` | `boolean` | **yes** |
| `thresholdMinor` | `integer` | **yes** |
| `topUpMinor` | `integer` | **yes** |
| `paymentMethodId` | `string` \| `null` | **yes** |

### <a id="updatesenderidrequest"></a>`UpdateSenderIdRequest`

Every field is optional; only those supplied are changed. An empty object is a no-op, not an error.

| Field | Type | Required |
| --- | --- | --- |
| `header` | `string` | no |
| `registrationId` | `string` \| `null` | no |
| `displayName` | `string` | no |

### <a id="usagebycampaign"></a>`UsageByCampaign`

| Field | Type | Required |
| --- | --- | --- |
| `campaignId` | `string` | **yes** |
| `campaignName` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `messageCount` | `integer` | **yes** |
| `amountMinor` | `integer` | **yes** |

### <a id="usagebychannel"></a>`UsageByChannel`

| Field | Type | Required |
| --- | --- | --- |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `messageCount` | `integer` | **yes** |
| `amountMinor` | `integer` | **yes** |

### <a id="usagebyjourney"></a>`UsageByJourney`

| Field | Type | Required |
| --- | --- | --- |
| `journeyId` | `string` | **yes** |
| `journeyName` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `messageCount` | `integer` | **yes** |
| `amountMinor` | `integer` | **yes** |

### <a id="usagereport"></a>`UsageReport`

| Field | Type | Required |
| --- | --- | --- |
| `byChannel` | [`UsageByChannel`](#usagebychannel)[] | **yes** |
| `byCampaign` | [`UsageByCampaign`](#usagebycampaign)[] | **yes** |
| `byJourney` | [`UsageByJourney`](#usagebyjourney)[] | **yes** |

### <a id="useractivityentry"></a>`UserActivityEntry`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `occurredAt` | `string(date-time)` | **yes** |
| `tenantId` | `string(uuid)` | **yes** |
| `tenantName` | `string` | **yes** |
| `userName` | `string` | **yes** |
| `userEmail` | `string(email)` | **yes** |
| `eventType` | [`UserActivityEventType`](#useractivityeventtype) | **yes** |
| `detail` | `string` | **yes** |

### <a id="useractivityeventtype"></a>`UserActivityEventType`

One of: `login`, `team.invite`, `team.role_change`, `api_key.create`, `api_key.rotate`, `api_key.revoke`, `mfa.enroll`, `mfa.disable`, `sso.config_change`, `session.revoke`, `campaign.pause`, `campaign.resume`, `campaign.cancel`

### <a id="useractivitypage"></a>`UserActivityPage`

| Field | Type | Required |
| --- | --- | --- |
| `entries` | [`UserActivityEntry`](#useractivityentry)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |
| `total` | `integer` | **yes** |

### <a id="verification"></a>`Verification`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `serviceId` | `string(uuid)` | **yes** |
| `msisdn` | `string` | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `status` | [`VerificationStatus`](#verificationstatus) | **yes** |
| `attemptsRemaining` | `integer` | **yes** |
| `expiresAt` | `string(date-time)` | **yes** |

### <a id="verificationattempt"></a>`VerificationAttempt`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `serviceId` | `string(uuid)` | **yes** |
| `msisdn` | `string` | **yes** |
| `country` | [`CountryCode`](#countrycode) | **yes** |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `status` | [`VerificationStatus`](#verificationstatus) | **yes** |
| `fraudFlag` | [`VerificationFraudFlag`](#verificationfraudflag) | **yes** |
| `funnelStage` | [`VerifyFunnelStage`](#verifyfunnelstage) | **yes** |
| `costMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="verificationattemptpage"></a>`VerificationAttemptPage`

| Field | Type | Required |
| --- | --- | --- |
| `attempts` | [`VerificationAttempt`](#verificationattempt)[] | **yes** |
| `total` | `integer` | **yes** |
| `nextCursor` | `string` \| `null` | no |

### <a id="verificationcheck"></a>`VerificationCheck`

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

### <a id="verificationfraudflag"></a>`VerificationFraudFlag`

One of: `none`, `velocity`, `geo_anomaly`, `blocked`

### <a id="verificationstatus"></a>`VerificationStatus`

One of: `pending`, `verified`, `incorrect`, `locked`, `expired`, `rate_limited`

### <a id="verifyanalytics"></a>`VerifyAnalytics`

| Field | Type | Required |
| --- | --- | --- |
| `summary` | [`VerifyAnalyticsSummary`](#verifyanalyticssummary) | **yes** |
| `buckets` | [`VerifyAnalyticsBucket`](#verifyanalyticsbucket)[] | **yes** |

### <a id="verifyanalyticsbucket"></a>`VerifyAnalyticsBucket`

| Field | Type | Required |
| --- | --- | --- |
| `bucketStart` | `string(date-time)` | **yes** |
| `requested` | `integer` | **yes** |
| `sent` | `integer` | **yes** |
| `delivered` | `integer` | **yes** |
| `verified` | `integer` | **yes** |

### <a id="verifyanalyticsrange"></a>`VerifyAnalyticsRange`

One of: `24h`, `7d`, `30d`

### <a id="verifyanalyticssummary"></a>`VerifyAnalyticsSummary`

| Field | Type | Required |
| --- | --- | --- |
| `requested` | `integer` | **yes** |
| `sent` | `integer` | **yes** |
| `delivered` | `integer` | **yes** |
| `verified` | `integer` | **yes** |
| `successRate` | `number` | **yes** |
| `fraudCounts` | [`VerifyFraudCounts`](#verifyfraudcounts) | **yes** |
| `costMinor` | `integer` | **yes** |
| `costPerConversionMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |

### <a id="verifychannelconfig"></a>`VerifyChannelConfig`

| Field | Type | Required |
| --- | --- | --- |
| `channel` | [`ChannelId`](#channelid) | **yes** |
| `senderId` | `string(uuid)` | **yes** |
| `body` | `string` | **yes** |

### <a id="verifyfraudcounts"></a>`VerifyFraudCounts`

| Field | Type | Required |
| --- | --- | --- |
| `velocity` | `integer` | **yes** |
| `geoAnomaly` | `integer` | **yes** |
| `blocked` | `integer` | **yes** |

### <a id="verifyfunnelstage"></a>`VerifyFunnelStage`

One of: `requested`, `sent`, `delivered`, `verified`

### <a id="verifyratelimit"></a>`VerifyRateLimit`

| Field | Type | Required |
| --- | --- | --- |
| `maxPerPhone` | `integer` | **yes** |
| `windowSeconds` | `integer` | **yes** |
| `cooldownSeconds` | `integer` | **yes** |

### <a id="verifyservice"></a>`VerifyService`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `name` | `string` | **yes** |
| `channels` | [`VerifyChannelConfig`](#verifychannelconfig)[] | **yes** |
| `fallbackOrder` | [`ChannelId`](#channelid)[] | **yes** |
| `codeLength` | `integer` | **yes** |
| `codeTtlSeconds` | `integer` | **yes** |
| `maxAttempts` | `integer` | **yes** |
| `rateLimit` | [`VerifyRateLimit`](#verifyratelimit) | **yes** |
| `regionAllowlist` | `string`[] | **yes** |
| `status` | `live` \| `setup_needed` | **yes** |
| `createdAt` | `string(date-time)` | **yes** |

### <a id="verifyservicecreate"></a>`VerifyServiceCreate`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `channels` | [`VerifyChannelConfig`](#verifychannelconfig)[] | **yes** |
| `fallbackOrder` | [`ChannelId`](#channelid)[] | **yes** |
| `codeLength` | `integer` | **yes** |
| `codeTtlSeconds` | `integer` | **yes** |
| `maxAttempts` | `integer` | **yes** |
| `rateLimit` | [`VerifyRateLimit`](#verifyratelimit) | **yes** |
| `regionAllowlist` | `string`[] | **yes** |

### <a id="verifyserviceupdate"></a>`VerifyServiceUpdate`

| Field | Type | Required |
| --- | --- | --- |
| `name` | `string` | **yes** |
| `channels` | [`VerifyChannelConfig`](#verifychannelconfig)[] | **yes** |
| `fallbackOrder` | [`ChannelId`](#channelid)[] | **yes** |
| `codeLength` | `integer` | **yes** |
| `codeTtlSeconds` | `integer` | **yes** |
| `maxAttempts` | `integer` | **yes** |
| `rateLimit` | [`VerifyRateLimit`](#verifyratelimit) | **yes** |
| `regionAllowlist` | `string`[] | **yes** |

### <a id="voicecallresult"></a>`VoiceCallResult`

| Field | Type | Required |
| --- | --- | --- |
| `code` | `string` | **yes** |

### <a id="voiceverification"></a>`VoiceVerification`

| Field | Type | Required |
| --- | --- | --- |
| `status` | `unverified` \| `code_sent` \| `verified` | **yes** |
| `codeSentAt` | `string(date-time)` \| `null` | no |
| `verifiedAt` | `string(date-time)` \| `null` | no |

### <a id="volumeceilingrule"></a>`VolumeCeilingRule`

| Field | Type | Required |
| --- | --- | --- |
| `enabled` | `boolean` | **yes** |
| `thresholdCount` | `integer` | **yes** |
| `recipients` | `string`[] | **yes** |

### <a id="wabutton"></a>`WaButton`

Type: [`WaQuickReplyButton`](#waquickreplybutton) \| [`WaCtaUrlButton`](#wactaurlbutton) \| [`WaCtaCallButton`](#wactacallbutton)

### <a id="wabuttoncontent"></a>`WaButtonContent`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `buttons` | **yes** |
| `body` | `string` | **yes** |
| `buttons` | [`WaButton`](#wabutton)[] | **yes** |

### <a id="wacontent"></a>`WaContent`

Type: [`WaTextContent`](#watextcontent) \| [`WaButtonContent`](#wabuttoncontent) \| [`WaListContent`](#walistcontent)

### <a id="wactacallbutton"></a>`WaCtaCallButton`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `cta_call` | **yes** |
| `text` | `string` | **yes** |
| `phoneNumber` | `string` | **yes** |

### <a id="wactaurlbutton"></a>`WaCtaUrlButton`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `cta_url` | **yes** |
| `text` | `string` | **yes** |
| `url` | `string` | **yes** |

### <a id="walistcontent"></a>`WaListContent`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `list` | **yes** |
| `body` | `string` | **yes** |
| `buttonLabel` | `string` | **yes** |
| `sections` | [`WaListSection`](#walistsection)[] | **yes** |

### <a id="walistrow"></a>`WaListRow`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string` | **yes** |
| `title` | `string` | **yes** |
| `description` | `string` \| `null` | no |

### <a id="walistsection"></a>`WaListSection`

| Field | Type | Required |
| --- | --- | --- |
| `title` | `string` | **yes** |
| `rows` | [`WaListRow`](#walistrow)[] | **yes** |

### <a id="wamessagingtier"></a>`WaMessagingTier`

Max distinct WhatsApp contacts messageable per rolling 24h

One of: `250`, `1000`, `10000`, `100000`

### <a id="waqualityrating"></a>`WaQualityRating`

One of: `green`, `yellow`, `red`

### <a id="waquickreplybutton"></a>`WaQuickReplyButton`

| Field | Type | Required |
| --- | --- | --- |
| `type` | `quick_reply` | **yes** |
| `text` | `string` | **yes** |

### <a id="watextcontent"></a>`WaTextContent`

| Field | Type | Required |
| --- | --- | --- |
| `kind` | `text` | **yes** |
| `body` | `string` | **yes** |

### <a id="walletbalance"></a>`WalletBalance`

| Field | Type | Required |
| --- | --- | --- |
| `balanceMinor` | `integer` | **yes** |
| `currency` | [`CurrencyCode`](#currencycode) | **yes** |

### <a id="webhookendpoint"></a>`WebhookEndpoint`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `environment` | [`Environment`](#environment) | **yes** |
| `url` | `string` | **yes** |
| `subscribedEvents` | [`WebhookEventType`](#webhookeventtype)[] | **yes** |
| `signingSecretPrefix` | `string` | **yes** |
| `status` | [`WebhookStatus`](#webhookstatus) | **yes** |

### <a id="webhookendpointcreated"></a>`WebhookEndpointCreated`

Type: [`WebhookEndpoint`](#webhookendpoint) & `object`

### <a id="webhookevent"></a>`WebhookEvent`

| Field | Type | Required |
| --- | --- | --- |
| `id` | `string(uuid)` | **yes** |
| `endpointId` | `string(uuid)` | **yes** |
| `eventType` | [`WebhookEventType`](#webhookeventtype) | **yes** |
| `timestamp` | `string(date-time)` | **yes** |
| `attempt` | `integer` | **yes** |
| `outcome` | [`WebhookEventOutcome`](#webhookeventoutcome) | **yes** |
| `httpStatus` | `integer` \| `null` | **yes** |
| `responseSnippet` | `string` \| `null` | **yes** |

### <a id="webhookeventoutcome"></a>`WebhookEventOutcome`

One of: `succeeded`, `failed`

### <a id="webhookeventpage"></a>`WebhookEventPage`

| Field | Type | Required |
| --- | --- | --- |
| `events` | [`WebhookEvent`](#webhookevent)[] | **yes** |
| `nextCursor` | `string` \| `null` | **yes** |

### <a id="webhookeventtype"></a>`WebhookEventType`

Events an endpoint can subscribe to. message.inbound is the one a two-way integration cannot work without: without it a customer's systems never see a reply or a STOP, so opt-outs and conversations are invisible to everything outside this dashboard.

One of: `message.inbound`, `message.queued`, `message.sent`, `message.delivered`, `message.read`, `message.failed`, `campaign.completed`, `campaign.failed`, `sender.approved`, `sender.rejected`, `template.approved`, `template.rejected`, `wallet.low_balance`, `wallet.depleted`

### <a id="webhookstatus"></a>`WebhookStatus`

One of: `enabled`, `disabled`

