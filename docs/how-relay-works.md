# How Relay works, end to end

Every path a message takes, from someone creating an account to a handset
confirming delivery — and what happens to the money at each step.

Written from the code. Where this and the code disagree, the code is right and
this is stale; the comments in `internal/domain/messaging/gate.go`,
`internal/sending/service.go` and `docs/rcs-carrier-integration.md` are the
closest thing to a second opinion.

---

## What Relay is

A multi-tenant A2P messaging platform — businesses send SMS, RCS, WhatsApp,
Email and Voice to their customers through it. Two pieces of software: a Go API
(`SMS-BE`) and a Next.js dashboard (`SMS-UI`), talking over a shared OpenAPI
contract of 133 endpoints.

The product's whole argument is **honesty about delivery and money**. Incumbents
report "sent" and bill for it. Relay separates *a carrier accepted this* from
*a handset received this*, keeps them apart in the type system, and only
converts a hold into a charge when a handset confirms. Almost every decision
below follows from that one commitment.

```
                       ┌──────────────────────────────────────────┐
   Dashboard  ───────▶ │  Control API (Go, chi, oapi-codegen)     │
   API keys   ───────▶ │    identity ▸ gate ▸ money ▸ carrier     │
                       └───┬──────────────┬───────────────┬───────┘
                    ┌──────▼─────┐  ┌─────▼──────┐  ┌─────▼──────┐
                    │ PostgreSQL │  │ ClickHouse │  │   Redis    │
                    │ 47 tables  │  │ messages,  │  │ rate limit │
                    │ RLS forced │  │ events,    │  │            │
                    │  = truth   │  │ rollups    │  │            │
                    └────────────┘  └────────────┘  └────────────┘
                           │
                    ┌──────▼──────────────────────────────────────┐
                    │ Carriers: sandbox · Airtel IQ · Vi RBM      │
                    └─────────────────────────────────────────────┘
```

- **PostgreSQL** — everything transactional and everything that must be exactly
  right. 34 of its 47 tables carry forced row-level security.
- **ClickHouse** — the message warehouse: every message, every state
  transition, hourly rollups.
- **Redis** — send rate limiting only. If it is down the limiter fails *open*;
  a dead cache must not stop a customer sending.

---

## 1 · Getting an account

Signing up creates three rows in one transaction: a **tenant** (the
organisation), a **user** (the person), and a **tenant_users** row joining them
with the role `owner`. A session is issued immediately and a verification email
goes out in the same request.

```
POST /v1/auth/signup   { fullName, email, password, orgName, country }
   │
   ├─ SIGNUP_INVITE_CODE set?  ──▶ header x-invite-code must match, else refused
   ├─ password hashed (argon2)
   ├─ tenant + user + membership(owner) created atomically
   ├─ verification email sent   (failure logged, NOT fatal — the account exists)
   └─ session token returned
```

Self-registration is gated by an invite code where one is configured. Empty
means open, which is right for a development instance and wrong on the public
internet — a stranger signing up and then funding their own wallet for nothing
was a real go-live blocker.

### Logging in

`POST /v1/auth/login` answers with *either* a session or an MFA challenge — a
discriminated union, so the client cannot mistake one for the other.

Every failure returns the identical 401, and the password is verified even when
the email is unknown, hashed against a dummy value so an unknown address costs
the same time as a known one with a wrong password. Both matter: a different
response, or a materially faster one, lets someone enumerate which addresses
have accounts here.

### The rest of identity

| | |
|---|---|
| MFA | TOTP. Enroll → confirm → recovery codes, hashed and single-use. `/v1/auth/mfa/*` |
| Email verification | Token link; resend available |
| Password reset | Forgot → emailed token → reset. Single-use |
| Sessions | Listed with the device they were minted on; any one revocable |
| SSO | Per-tenant, `/v1/sso` |

---

## 2 · Roles and the team

| Role | Can do | Cannot |
|---|---|---|
| `owner` | Everything, including billing and deleting the account | — |
| `admin` | Everything operational: senders, templates, campaigns, developer, billing, team | — |
| `member` | Day-to-day sending and reading | Settings, billing, compliance, developer, team |

**The role gate is enforced twice, on purpose.** The dashboard's middleware
hides pages a member may not use, but a Server Action is dispatched by its own
id — not by the page URL that rendered the form — so a member replaying a
captured mutation would bypass a URL-based gate entirely. Every gated action
calls `blockedForMember()` before any validation or fetch, and the API refuses
independently with a real 403.

---

## 3 · Money: the wallet and the ledger

Relay is prepaid. A tenant holds a balance per currency (INR, USD, GBP, AED) and
every movement is a row in an **append-only ledger** — enforced by a database
trigger that rejects any UPDATE or DELETE, so a balance can never be quietly
edited.

```sql
-- one movement, one transaction, one lock
INSERT wallet_balances (tenant, currency) ON CONFLICT DO NOTHING  -- create on first use
SELECT balance FROM wallet_balances ... FOR UPDATE                -- serialise
   credit ▸ balance += amount
   debit  ▸ balance <  amount ? ErrInsufficientFunds : balance -= amount
UPDATE wallet_balances
INSERT wallet_ledger (..., balance_after_minor)                   -- running total, stored
```

Every amount is an integer of minor units — paise, cents. No floats anywhere in
the money path. `balance_after_minor` is written into each ledger row, so the
statement reconstructs without re-summing and a discrepancy is visible rather
than inferred.

### How a message spends it

1. **Hold** — a `charge` entry is written *before* anything is handed to a
   carrier. A carrier can never receive a message we have not reserved payment
   for.
2. **Settle** — a handset-confirmed delivery leaves the money spent. Anything
   else (undelivered, rejected, expired) writes a `refund`.

"No charge for undelivered" is a state machine, not a promise in a report — see
§6.

---

## 4 · Permission to send

A2P messaging is regulated differently in every country, and Relay models that
as **data behind one interface** rather than branches in shared code. A new
country is a new file in `internal/domain/compliance` plus a registry entry — no
handler changes. The interface deliberately gives handlers no way to ask "which
country is this?".

| Country | Regime | Currency | Registers |
|---|---|---|---|
| IN | DLT | INR | Principal entity (PE/RTM) → DLT header → content template |
| US | TCR / 10DLC | USD | Brand → campaign (campaign depends on brand) |
| GB | MMA *(stub)* | GBP | Nothing yet |
| AE | — *(stub)* | AED | Nothing yet |

A **stub** regime is not the same as an unsupported country. A tenant in GB gets
"no registrations are required here yet", not "we do not operate in your
country" — different facts, and only one of them is true.

### Three tiers, in order

- **Entity** — who you legally are. India: PE/RTM with PAN and entity type.
  US: a TCR brand.
- **Sender** — the name on the handset. India: a six-character DLT header, typed
  transactional / promotional / service.
- **Template** — the exact message shape, pre-approved.

Each object carries a **remediation string** — what to actually fix — so a
rejection says "headers are six alphanumeric characters and must already be
registered against your principal entity" rather than just "rejected".

---

## 5 · Senders and templates

**Sender IDs** are a header + channel + country, starting `pending_review`.
Nothing is approved on arrival. Voice senders additionally need a **verified
caller ID** (`/v1/sender-ids/{id}/voice-call`, then `/voice-code`).

**Templates** belong to a sender and *inherit its channel and country* rather
than declaring their own — a template claiming a different country from the
sender it goes out through would be unenforceable at send time. Variables are
parsed as distinct `{{named}}` tokens in first-seen order; that order matters
later.

SMS and Voice carry a plain body. RCS, WhatsApp and Email carry structured
content in their own `jsonb` columns, with a database constraint that each
belongs to exactly one channel — without it a template could claim to be an SMS
with a WhatsApp button payload attached, and whichever renderer looked first
would win.

India rejects shortened CTA URLs at creation time. DLT's real control is the
operator's CTA whitelist; catching `bit.ly` here means a tenant learns
immediately rather than at rejection, days later.

### RCS templates are approved twice

```
templates.status              = approved      ← Relay's review
templates.carrier_status      = pending       ← the carrier's, separately
templates.carrier_template_id = 01kct02n...   ← their id, unique per vendor
templates.carrier_rejection_reason            ← their words, not ours
```

Collapsing the two would send a customer arguing with the wrong team about a
template we approved last week. The gate has `carrier_template_not_approved` as
a refusal distinct from `template_not_approved` for the same reason.

---

## 6 · The send path

One code path serves both the public API and campaign fan-out, deliberately — so
the rules cannot drift between "a developer sent this" and "a campaign sent
this".

```
POST /v1/messages  ·  or campaign fan-out
  │
  1 resolve sender        must belong to this tenant
  2 normalise recipient   country rule, then E.164 fallback
  3 price it              segments × rate — same arithmetic as the estimate
  4 gather gate inputs    template, suppression, balance, tenant status
  5 ── THE GATE ──        nothing charged, nothing sent yet
  6 hold the money        BEFORE the carrier sees anything
  7 pick the path         carrier + route recorded
  8 record as queued
  9 submit to carrier
 10 apply the receipt     accepted → wait · rejected → release the hold now
      ⋮  later, asynchronously
 11 delivery report       webhook (real) or drained (sandbox)
 12 settle                delivered → charge stands · anything else → refund
```

Everything that could refuse the send happens before money moves and before
anything reaches a carrier. A message that reached a carrier but failed to be
recorded is unrecoverable — the recipient got it and we have no idea.

### The gate

| # | Check | Failure code |
|---|---|---|
| 1 | Tenant not suspended | `tenant_suspended` |
| 2 | Recipient addressable on this channel | `invalid_recipient` |
| 3 | Sender is approved | `sender_not_approved` |
| 4 | Template is approved | `template_not_approved` |
| 5 | Template belongs to this sender | `sender_template_mismatch` |
| 6 | Carrier has approved the template *(RCS, when a carrier is configured)* | `carrier_template_not_approved` |
| 7 | Recipient not suppressed | `recipient_suppressed` |
| 8 | Balance covers the cost | `insufficient_balance` |

**The order is the design.** Compliance failures are reported before money,
because telling someone "insufficient balance" when the real problem is an
unapproved sender sends them to fix the wrong thing. Suppression is checked
before balance so an opted-out recipient is never billed for, not even
momentarily.

A refused send is still recorded, with its reason, and costs nothing. Someone
asking "why didn't this arrive?" deserves an answer.

### States, and what each does to the money

```
queued ──▶ submitting ──▶ submitted ──▶ accepted ──▶ delivered     charge stands
   │            │             │             ├──────▶ undelivered   refund
   └────────────┴─────────────┴─ rejected                          refund
                              └─ expired  ◀──────────┘             refund
```

Terminal states go nowhere. A carrier replaying a receipt cannot walk a message
backwards out of one — which is what makes ingest idempotent without a dedupe
table.

`accepted` means a carrier took it. `delivered` means a handset confirmed it.
The dashboard contract collapses eight internal states to five, and `accepted`
maps to **"sent"**, never **"delivered"**. That single mapping is where the
product's honesty claim is kept or broken.

### Segment arithmetic

From GSM 03.38, and not adjustable preferences — a 161-character GSM-7 message
really is billed as two segments.

| Encoding | One segment | Per segment when concatenated |
|---|---|---|
| GSM-7 | 160 | 153 (7 septets go to the UDH) |
| UCS-2 | 70 | 67 |

A single smart quote pasted from a word processor forces the whole message to
UCS-2 — the most common way a 160-character message silently becomes a
70-character one, and doubles its cost.

---

## 7 · The five channels

| Channel | Addressed by | Content | Needs |
|---|---|---|---|
| SMS | msisdn | plain body | Approved sender; in India a DLT header + registered template |
| RCS | msisdn | text or card | Approved sender **and** a carrier-approved template |
| WhatsApp | msisdn | text / buttons / list | Approved sender with quality tier; 24-hour session window |
| Email | email address | subject + HTML | Verified domain with published DNS records |
| Voice | msisdn | script | Approved sender with a verified caller ID |

Email is the one addressed by something other than a phone number, and the send
path knows it: an Email send to a contact with no email address is not a
delivery failure to be retried, it is a message that was never sendable — so it
is neither charged for nor counted against the delivery rate.

### RCS and the carriers

Airtel IQ and Vi RBM, both Indian routes to Google's RBM. They agree on almost
nothing:

| | Airtel IQ | Vi RBM |
|---|---|---|
| Auth | Static Basic credential, never expires | OAuth token from a separate host, 60 mints/min |
| Reachability | Single + bulk; bulk **refuses under 500 numbers** | Single + bulk, no floor |
| "Not reachable" | A *failed* envelope wrapping a Google 404 | HTTP 200 with `{}` |
| Templates | API; review up to 24 h; decision on a webhook | **No API** — portal only, paste the code back |
| Placeholders | Positional `{{1}}` | Named `[NAME]`, as a JSON *string* |
| Correlation | Their `messageRequestId` — nothing of ours | Accepts our own uuid as `messageId` |
| Revoke | Via TTL only | Explicit |

Both vendors' phone-level capability APIs speak Google's RBM vocabulary, so
feature names pass through unmapped. A name Relay does not recognise reaches the
caller intact rather than being dropped by a filter written before the carrier
added it.

Delivery outcomes, template decisions and inbound messages all arrive on
`POST /v1/carrier-webhooks/rcs/{vendor}/{token}`. Neither vendor signs its
callbacks and neither lets us set a header, so the shared secret travels in the
path — paired with an optional IP allowlist, and the routes are not mounted at
all without a token.

See `docs/rcs-carrier-integration.md` for the full comparison, and
`scripts/rcs-stubs/` to run either vendor locally.

---

## 8 · Campaigns and journeys

**Campaigns** are a sender + template + list, launched once. Fan-out is paged at
500 recipients so a million-contact list never has to fit in memory, and
everything identical across recipients is resolved once rather than per message.
The unbatched path cost about eight Postgres round trips and six ClickHouse
inserts per message and capped out near 68 messages a second.

What is deliberately *not* batched is correctness: every recipient still passes
the same gate with the same rules, and the money still moves before anything
reaches a carrier. One wallet movement covers the page, so a wallet that runs
dry mid-page refuses the rest instead of going negative.

Bodies are personalised per contact from that contact's `fields`. An unknown
placeholder is left as-is rather than blanked: sending "Hi {{name}}" is an
obvious bug someone reports, while sending "Hi " looks deliberate and ships to
the whole list unnoticed.

**Journeys** are multi-step flows with a trigger and a steps array:

```
draft ──activate──▶ active ⇄ paused ──archive──▶ archived
                                                     │
                       unarchive: never straight back to active
                       never activated ──▶ draft     ◀┘
                       had run          ──▶ paused
```

Restoring a journey and resuming it are two decisions, made separately. Coming
back *active* would resume sending to a live list the instant the button is
pressed. `activated_at` is never rewritten either — the enrollment funnel and
per-step counts are computed from elapsed time since the original activation.

---

## 9 · Verify — one-time codes

A hosted OTP service: define a **verify service** with its channels in fallback
order, code length, TTL, attempt limit and per-phone rate limits; then start a
verification and check a code against it.

- Codes come from `crypto/rand`, never `math/rand` — a predictable OTP is not an
  OTP. Each digit is drawn independently so every code of the length is equally
  likely, *including ones with leading zeros*: trimming those would shrink the
  keyspace and bias the result.
- Stored hashed, compared in constant time.
- Expired, incorrect and **locked** are three different answers. Locked means the
  attempt budget is spent and the verification is dead even if the next guess
  would have been right — that is the entire point of an attempt limit.

---

## 10 · Inbox

Inbound messages thread into a **conversation**, unique per contact × channel.
Conversations are open or closed, carry an unread flag, and can be replied to,
closed and reopened. A reply is a real send: same gate, same rate, and it appears
in the ledger.

Inbound RCS is **not** threaded yet — it arrives on the carrier webhook and is
logged, but does not reach the inbox.

---

## 11 · The API and the developer surface

**API keys** are prefixed `sk_live_` / `sk_test_`, shown once, stored hashed,
rotatable and revocable. Keys carry scopes (`send:sms`, `send:rcs`, …) and the
send path checks the scope matching the sender's channel.

Scopes apply to keys only. A dashboard session is authorised by role, and
treating a session as "a key with every scope" would silently grant send rights
that the key model exists to limit.

**Rate limits** are per tenant, per environment: 200/second live, 20/second
test, in Redis. The window is anchored to the tenant's *first* request rather
than to wall-clock seconds — a clock-anchored window splits a burst across two
counters and lets through double. If Redis is unavailable the limiter fails
open.

**Outbound webhooks** are per environment with a signing secret revealed once.
Every attempt is recorded — event type, payload, attempt number, outcome, HTTP
status and a response snippet — and is replayable.

---

## 12 · Analytics and the message log

Three ClickHouse tables: `messages` (one row per version, replacing),
`message_events` (append-only transition log), `message_rollup_hourly`.

Rollups are permanent while raw rows age out, so every analytics read comes from
the rollup. That is what keeps a year-old chart truthful after the messages
behind it have been retained-out.

The logs explorer filters by channel, country, status, campaign and error class —
where error class separates *the number is unreachable* from *we blocked it
ourselves*, which are different problems with different fixes.

---

## 13 · The operator console

A separate surface for Relay's own staff, on its own login, its own session
table, and its own database pool. It is *not* a tenant with extra rights.

Approvals · routes · rates and margin · tenants (suspend, throttle, reinstate,
flag abuse) · support tickets · an append-only audit log.

Three controls that exist because they were once missing:

- **Grey routes are refused by default.** A grey route is traffic reaching
  handsets without being registered with the operator behind it. It delivers
  until the carrier notices, and then it does not — messages filtered without a
  report, sender id blocked, and in India the penalty lands on the principal
  entity, not on us. Two were found *active* on production with registered
  alternatives sitting beside them in the same corridor.
- **The console can be restricted to known networks** (`OPERATOR_IP_ALLOWLIST`).
  Off-network requests get **404, not 403** — a 403 confirms an operator console
  exists at this address.
- **Operator MFA**, with the API warning at every boot if any operator account
  still accepts the password published in the repository.

---

## 14 · How it is built

### Tenant isolation is the database's job

34 tables carry `FORCE ROW LEVEL SECURITY` with a policy of
`tenant_id = current_tenant_id()`. Every tenant query runs through a helper that
sets the tenant for the transaction, so a handler that forgets to filter returns
*nothing* rather than everything.

Two pools, deliberately separate. Tenant handlers use the app pool; operator
handlers use a pool carrying `app.operator`, which sees across tenants. The
moment those share a code path, one missing branch becomes a cross-tenant leak.

### Append-only where it counts

`wallet_ledger` and `operator_audit_log` both have triggers rejecting UPDATE and
DELETE. Money and accountability are not editable.

### Background workers

| Worker | Every | Does |
|---|---|---|
| delivery-drainer | 2 s | Applies the sandbox carrier's delivery reports. Real carriers POST instead — same settlement code, different trigger |
| reconciler | 15 min | Expires messages a carrier accepted and never reported on, releasing the money. Silence is treated as failure, and failure is free |
| campaign sweep | 15 min | Lands campaigns abandoned mid-fan-out on a terminal status |

Each runs under a supervisor that restarts it on panic and records an incident.
One tenant's bad row must never stall the sweep for everyone else — the
reconciler continues past a message it cannot settle and joins the failures,
because ClickHouse retains messages for 90 days while a tenant row can be
deleted long before that.

### The contract

`openapi/control.json` in the backend is a **symlink** to `openapi.json` in the
frontend — one file, one source of truth. The backend generates typed handlers
from it with oapi-codegen; the frontend generates TypeScript types from the same
file.

oapi-codegen renders contract enums as plain Go string aliases, so an
out-of-enum value reaches the database and 500s. Enum values are validated
explicitly where they arrive.

### Deployment

```
push to main (GitHub)
   └─▶ build · vet · test          fails here, nothing is uploaded
        └─▶ pg_dump backup         Relay's own database only — it shares the box
             └─▶ migrate           Postgres (goose) + ClickHouse
                  └─▶ swap binary  previous kept as .prev for rollback
                       └─▶ health-check ×20, roll back if unhealthy
```

The API runs on a shared VPS as a guest alongside two unrelated products, so
every step is scoped to Relay's own unit, containers and database.

**A push is not a deploy.** The upload step has failed intermittently on SSH
while tests passed — so the run's conclusion is worth confirming, and the API's
own endpoints are faster proof than the workflow's per-step status, which lags
by minutes.

The dashboard deploys separately, on Vercel.

---

## What is not built

- **RCS media, rich cards and carousels** — only the text shape registers and
  sends. Cards can be built in a carrier's portal and attached by code.
- **Inbound RCS threading** — arrives and is logged, does not reach the inbox.
- **Capability-driven fallback** — `campaigns.fallback_channel` exists and is not
  yet driven by the reachability check, even though that check now works. The
  obvious next slice.
- **Per-tenant carrier credentials** — one agent identity per deployment.
- **The campaign readiness matrix ignores carrier registration** — an IN × RCS
  corridor can read "Ready" while every send would be refused.
- **Known bug:** the support reply handler drains the sandbox report queue
  destructively, racing the background drainer. Reproduce with
  `e2e/inbox.spec.ts` — the reply-flow test passes alone and fails after its
  siblings.
