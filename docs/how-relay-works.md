# How Relay works — a guided tour

Every screen in the product, what it does, and exactly what the backend does
underneath it. Screenshots are real: captured from the running dashboard against
the running API with the demo tenant's data, so every number on screen is a
number the system actually produced.

Read it top to bottom and you follow the same path a customer does — sign up,
get permission to send, load an audience, send, get paid for, and see what
happened.

> **A note on trust.** Where this document and the code disagree, the code is
> right and this is stale. Every "behind it" section names the endpoint and the
> tables, so you can go and check.

---

## Contents

| | |
|---|---|
| **1** | [What Relay is](#1--what-relay-is) |
| **2** | [Signing up and logging in](#2--signing-up-and-logging-in) |
| **3** | [The overview screen](#3--the-overview-screen) |
| **4** | [Permission to send: compliance](#4--permission-to-send-compliance) |
| **5** | [Sender IDs](#5--sender-ids) |
| **6** | [Templates](#6--templates) |
| **7** | [Audience: contacts, lists, suppressions](#7--audience-contacts-lists-suppressions) |
| **8** | [Campaigns](#8--campaigns) |
| **9** | [The send path — what happens to one message](#9--the-send-path--what-happens-to-one-message) |
| **10** | [Automation (journeys)](#10--automation-journeys) |
| **11** | [Inbox](#11--inbox) |
| **12** | [Analytics and logs](#12--analytics-and-logs) |
| **13** | [Billing and the wallet](#13--billing-and-the-wallet) |
| **14** | [The developer surface](#14--the-developer-surface) |
| **15** | [Verify — one-time codes](#15--verify--one-time-codes) |
| **16** | [RCS reachability](#16--rcs-reachability) |
| **17** | [Settings, team, security](#17--settings-team-security) |
| **18** | [Support](#18--support) |
| **19** | [The operator console](#19--the-operator-console) |
| **20** | [How it is built](#20--how-it-is-built) |

---

## 1 · What Relay is

Businesses send SMS, RCS, WhatsApp, Email and Voice to their customers through
Relay. Two programs make it up:

- **`SMS-BE`** — a Go API. All the rules, all the money, all the carrier talk.
- **`SMS-UI`** — a Next.js dashboard. Every screen below.

They share one file: an OpenAPI contract with 133 endpoints. The backend
generates its typed handlers from it; the frontend generates its TypeScript types
from the same file. A route cannot exist on one side and not the other.

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
                    ┌───────────────────────▼─────────────────────┐
                    │ Carriers: sandbox · Airtel IQ · Vi RBM      │
                    └─────────────────────────────────────────────┘
```

**Why three stores?**

- **PostgreSQL** holds everything that must be exactly right and transactional —
  accounts, money, approvals. 34 of its 47 tables enforce row-level security.
- **ClickHouse** holds the message log. Hundreds of millions of rows, written
  once, read for logs and analytics. Postgres would fall over; ClickHouse is
  built for it.
- **Redis** does one job: send rate limiting. If it goes down the limiter fails
  **open** — a dead cache must never stop a paying customer from sending.

### The one idea everything else follows from

Incumbent platforms report **"sent"** and bill you for it. "Sent" means a carrier
took the message. It does not mean anybody received it.

Relay keeps those two facts apart everywhere:

- `accepted` — a carrier took it.
- `delivered` — a handset confirmed it.

Money is **held** at send and only becomes a **charge** when a handset confirms.
Everything else refunds. That is not a promise in a report — it is a state
machine, and you can read it in §9.

---

## 2 · Signing up and logging in

![Sign up](screenshots/02-signup.png)

**What you're looking at.** The public sign-up form: your name, work email,
password, organisation name and country. The country matters more than it looks —
it decides which regulatory regime your account runs under for the rest of its
life.

**Behind it — `POST /v1/auth/signup`**

Three rows are created in **one transaction**, so a half-made account is
impossible:

| Table | Row |
|---|---|
| `tenants` | the organisation, with its country |
| `users` | the person, with an argon2 password hash |
| `tenant_users` | joins them, with role `owner` |

Then, in the same request:

1. A verification email is sent. **If it fails, the signup still succeeds** — the
   account exists and the session is already valid, so refusing over an
   undelivered email would throw away a working account. The failure is logged
   and the resend endpoint is still there.
2. A session token is returned, so you are logged in immediately.

**Invite gating.** If the deployment sets `SIGNUP_INVITE_CODE`, the request must
carry a matching `x-invite-code` header or it is refused. Empty means open —
right for a development instance, wrong on the public internet. A stranger
signing up and then funding their own wallet for nothing was a real go-live
blocker on this product.

![Log in](screenshots/01-login.png)

**Behind it — `POST /v1/auth/login`**

The reply is a *discriminated union*: either a session, or an MFA challenge. The
client cannot mistake one for the other.

Two details that look like paranoia and are not:

- **Every failure returns the identical 401 body.** Unknown email, wrong
  password, no membership — all the same.
- **The password is verified even when the email is unknown**, hashed against a
  dummy value. Without this, an unknown address returns faster than a known one
  with a wrong password, and anyone can time the difference to discover which of
  your customers bank here.

### The rest of identity

| What | Endpoint | Notes |
|---|---|---|
| MFA (TOTP) | `/v1/auth/mfa/*` | Enroll → confirm with a code → recovery codes issued, hashed, single-use |
| Verify email | `/v1/auth/verify-email/*` | Token link, resendable |
| Reset password | `/v1/auth/password/*` | Emailed token, single-use |
| Sessions | `/v1/sessions` | Each shows the device it was minted on; any can be revoked |
| SSO | `/v1/sso` | Per-tenant configuration |

---

## 3 · The overview screen

![Overview](screenshots/03-overview.png)

**What you're looking at.** The first screen after login: what has been sent
recently, what it cost, what is waiting on you, and the wallet balance.

**Behind it.** Tiles read `GET /v1/analytics` (from the ClickHouse hourly
rollup, not from raw messages — see §12) and `GET /v1/wallet/balances`. Anything
"needs attention" comes from the compliance and approval state in Postgres.

**Live updates.** The dashboard holds an `EventSource` open to `GET /v1/events`,
a server-sent stream scoped to your tenant. It is mounted outside the generated
contract on purpose — oapi-codegen models request/response pairs, and this is a
response that never ends.

---

## 4 · Permission to send: compliance

![Compliance](screenshots/13-compliance.png)

**What you're looking at.** What each country requires before you may send
there, and how far along you are.

**Why it exists.** A2P messaging is regulated per country and the rules do not
resemble each other. India runs DLT: you register a legal entity, then a sender
header against it, then every message template. The US runs TCR/10DLC: a brand,
then campaigns under it.

**Behind it.** Compliance is **data behind one interface**, never branches in
shared code. A new country is a new file in `internal/domain/compliance` plus one
registry entry — no handler changes anywhere. The interface deliberately gives
handlers *no way to ask* "which country is this?", because the moment they can,
they will.

| Country | Regime | Currency | What you register |
|---|---|---|---|
| **IN** | DLT | INR | Principal entity (PE/RTM) → DLT header → content template |
| **US** | TCR / 10DLC | USD | Brand → campaign *(campaign cannot be filed before the brand exists)* |
| **GB** | MMA — *stub* | GBP | Nothing yet |
| **AE** | — *stub* | AED | Nothing yet |

> A **stub** regime is not an unsupported country. A tenant in GB is told "no
> registrations are required here yet", not "we do not operate in your country".
> Those are different facts and only one of them is true.

![India compliance](screenshots/14-compliance-india.png)

**What you're looking at.** The India regime in detail — the three tiers and the
exact fields each needs.

**Behind it — `POST /v1/registrations`**

Each registration object declares its tier, its fields, its dependency, and a
**remediation string**. That last one is why a rejection here says

> *"Headers are six alphanumeric characters and must already be registered
> against your principal entity on the DLT portal."*

rather than just "rejected". The rejection reason is stored on the row so it
survives a page reload and the customer can act on it days later.

Required-field checking returns the missing keys **in the object's own field
order**, so the UI can highlight the first one rather than a random one.

---

## 5 · Sender IDs

![Senders](screenshots/10-senders.png)

**What you're looking at.** The names that appear on your customers' handsets,
one row per header × channel × country, each with an approval status.

**Behind it — `POST /v1/sender-ids`, `GET /v1/sender-ids`**

A sender starts `pending_review`. **Nothing is approved on arrival.** An operator
approves or rejects it from the console (§19), writing the status and, on
rejection, a reason.

**Voice senders need one extra step.** A voice caller ID has to be proven to
belong to you:

1. `POST /v1/sender-ids/{id}/voice-call` — Relay calls the number and reads a code.
2. `POST /v1/sender-ids/{id}/voice-code` — you type the code back.

Only then can an operator approve it. The operator console enforces this too: a
voice sender approval is blocked until the caller ID is verified.

**Email senders** need a verified domain instead — DNS records (SPF, DKIM) that
you publish and Relay checks, stored in `sender_dns_records`.

---

## 6 · Templates

![Templates](screenshots/11-templates.png)

**What you're looking at.** Your pre-approved message shapes. Note the columns:
**Status** is Relay's own review; **Carrier** is a *second, separate* approval
that only RCS has (see below).

**Behind it — `POST /v1/templates`**

A template belongs to a sender and **inherits that sender's channel and
country** rather than declaring its own. A template claiming a different country
from the sender it goes out through would be unenforceable at send time, so the
option simply does not exist.

**Variables** are parsed out of the body as distinct `{{named}}` tokens in
first-seen order. `"Hi {{name}}, order {{order_id}} ships today. Thanks
{{name}}!"` yields `["name", "order_id"]` — the repeat collapses. **That order is
load-bearing** and you will meet it again in §9 and §16.

**Content is per-channel.** SMS and Voice carry a plain `body`. RCS, WhatsApp and
Email carry structured content in their own `jsonb` columns, and a database
constraint enforces that each belongs to exactly one channel:

```sql
CHECK ( (rcs_content   IS NULL OR channel = 'RCS')
    AND (wa_content    IS NULL OR channel = 'WHATSAPP')
    AND (email_content IS NULL OR channel = 'EMAIL') )
```

Without it a template could claim to be an SMS with a WhatsApp button payload
attached, and whichever renderer looked first would win — which shows up as a
*wrong message delivered to a customer*, not as an error.

![New template](screenshots/12-template-new.png)

**What you're looking at.** The template editor, with a live preview shaped like
the channel you picked.

**Behind it.** Validation runs against the country's regime before the row is
written. In India, a shortened CTA URL (`bit.ly`, `tinyurl.com`, `t.co`, …) is
**rejected inline at creation time**. DLT's real control is the operator's CTA
whitelist; catching the common shorteners here means you learn immediately
instead of at rejection, days later. The US regime allows them.

### RCS templates are approved twice

This is the part that surprises people, so it is worth being explicit.

| | Who reviews | Column | If it fails |
|---|---|---|---|
| **Relay's approval** | Relay compliance | `templates.status` | `template_not_approved` |
| **The carrier's approval** | Airtel, or a Vi admin | `templates.carrier_status` | `carrier_template_not_approved` |

A template can read **approved** on your screen and still fail every single send,
because the *carrier* has never seen it. Airtel refuses with "Template not found
for provided templateId" — after the money has been held.

![Carrier registration](screenshots/15-carrier-registration-dialog.png)

**What you're looking at.** The dialog for getting that second approval.

**Behind it — `POST /v1/templates/{id}/carrier-registration`**

Two modes, because the two carriers differ:

- **Empty body** → Relay submits the template to the carrier's API. Airtel
  supports this; their review takes up to 24 hours and the decision arrives later
  on a webhook. The template comes back `pending`.
- **`{ "carrierTemplateId": "…" }`** → attaches a code you obtained in the
  carrier's own portal. **This is the only route for Vi**, which has no template
  API at all. Attaching a code records it as `approved`, because your assertion
  is the only source of truth Vi offers.

Calling the first mode against Vi returns **409 with an explanation**, not an
error — the dialog shows it as an instruction to go to their portal.

Three things are refused before the carrier is ever called, because each would
waste a 24-hour review slot:

1. Not an RCS template → 422.
2. Not approved in Relay yet → 422. The carrier's review is a second opinion on
   content we already stand behind.
3. No category → 422. The carrier needs a *use case*, and a promotional template
   submitted under a transactional agent is **auto-rejected**. There is no safe
   default, so it is asked for.

---

## 7 · Audience: contacts, lists, suppressions

![Audience](screenshots/20-audience.png)

**What you're looking at.** Your contact lists, each with a count and — the
important part — a **per-channel consent breakdown**.

**Why consent is not one flag.** A contact can be opted in for SMS and unknown
for RCS. Those are different permissions and treating them as one would either
block sends you are allowed to make or permit sends you are not. The audience
screen shows the split because that is what decides who a campaign can legally
reach.

**Behind it.** `contacts.consent` is a `jsonb` map:

```json
{"SMS":"opted_in","RCS":"unknown","WHATSAPP":"opted_in","EMAIL":"opted_in"}
```

`GET /v1/contact-lists` returns `consentedCounts` per channel — and it is
**always sent, including when it is zero**. A zero here is a real and important
answer ("nobody on this list can be messaged on WhatsApp right now"), not a
missing one; omitting it would let the campaign wizard read *unknown* as *no
restriction*.

![Contact list](screenshots/23-audience-list.png)

**What you're looking at.** One list opened up: its members, and the consent
each one has given per channel. A contact can be removed from a list without
being deleted — `contact_list_members` is a join table, so membership and
existence are separate facts.

![Import](screenshots/21-audience-import.png)

**What you're looking at.** CSV import, with a preview before anything is
written.

**Behind it — `POST /v1/contacts/import`**

You declare a **default country** and a **consent basis** up front, because a
CSV cannot tell us either. The response reports created / duplicate / invalid
counts separately.

Each row can carry a `line` number threaded through from the client-side
preview, so a server-side conflict can report the **original CSV line** even
after client-side filtering compacted the rows. An absent or out-of-range value
is treated as unknown provenance — never guessed from array position.

**Numbers are normalised the same way here and on send**, so a number stored
from a CSV matches a number sent to via the API. The country rule is tried
first, then a country-agnostic E.164 fallback — an API caller may legitimately
send to a country other than the sender's home one.

![Suppressions](screenshots/22-suppressions.png)

**What you're looking at.** Everyone who must not be contacted, and why.

**Behind it — `/v1/suppressions`**

Suppressions are **global, not per-channel**. Someone who opts out in India stays
suppressed for a campaign sent from a US sender. Removing one is gated by a
reason, so it cannot be done casually.

An inbound STOP-class keyword on SMS or RCS adds one automatically; the message
that triggered it is marked with `keywordMatched` in the log.

---

## 8 · Campaigns

![Campaigns](screenshots/30-campaigns.png)

**What you're looking at.** Every campaign with its status, recipient count and
cost.

![New campaign](screenshots/31-campaign-new.png)

**What you're looking at.** The wizard: pick a channel and country, a sender, a
template, a list — and a **readiness matrix** that tells you what is blocking
before you commit.

**Behind it — `POST /v1/campaigns/estimate` then `POST /v1/campaigns`**

The estimate is a **range, not a number**, because per-contact personalisation
changes the length: `"Hi {{first_name}}"` is a different segment count for
*Jo* than for *Bartholomew*. The upper bound assumes every personalised message
tips into one more segment.

The same segment arithmetic runs here and at send, so **the quote you approved
and the charge you get cannot disagree**.

![Campaign detail](screenshots/32-campaign-detail.png)

**What you're looking at.** One campaign, its funnel, and per-recipient outcomes.

**Behind it — `GET /v1/campaigns/{id}` and `/messages`**

The counts come from ClickHouse. Notice that **sent** and **delivered** are
separate columns here and everywhere else; see §9.

### How fan-out actually runs

A campaign is paged at **500 recipients at a time**, so a million-contact list
never has to fit in memory. Everything identical across recipients — the sender,
the template, the rate, the tenant's status, the route — is resolved **once**,
not per message.

> The unbatched path cost about eight Postgres round trips and six ClickHouse
> inserts *per message*, capping out near 68 messages a second. That is 5.9M a
> day, far short of the target.

What is deliberately **not** batched is correctness. Every recipient still passes
the same gate with the same rules, and the money still moves before anything
reaches a carrier. One wallet movement covers the page, so a wallet that runs dry
mid-page refuses the rest instead of going negative.

**Personalisation.** Bodies are filled from each contact's own `fields`. An
unknown placeholder is **left visible** rather than blanked:

> Sending `"Hi {{name}}"` is an obvious bug someone reports in minutes. Sending
> `"Hi "` looks deliberate and ships to the whole list unnoticed.

**Abandoned campaigns.** If fan-out dies mid-run — a contact query errors, a
ClickHouse blip — a sweep lands the campaign on a terminal status within 15
minutes, and the status matches what actually left: some messages out is a send
that happened, none out is one that did not. Before that sweep existed, a
campaign sat "sending" forever and the customer watched a send that never ended.

---

## 9 · The send path — what happens to one message

This is the centre of the product. One code path serves **both** the public API
and campaign fan-out, deliberately, so the rules cannot drift between "a
developer sent this" and "a campaign sent this".

```
POST /v1/messages   ·   or campaign fan-out
  │
  1  resolve the sender          must belong to this tenant
  2  normalise the recipient     country rule, then E.164 fallback
  3  price it                    segments × rate  (same maths as the estimate)
  4  gather the gate's inputs    template, suppression, balance, tenant status
  5  ──── THE GATE ────          nothing charged, nothing sent yet
  6  hold the money              BEFORE the carrier sees anything
  7  pick the path               carrier + route recorded on the message
  8  record it as queued
  9  submit to the carrier
 10  apply the receipt           accepted → wait  ·  rejected → release now
      ⋮
      ⋮   later, asynchronously  (minutes to hours)
      ⋮
 11  delivery report arrives     carrier webhook, or drained from the sandbox
 12  settle                      delivered → charge stands
                                 anything else → refund
```

**The ordering is the safety-critical part.** Everything that could refuse the
send happens *before* money moves and *before* anything reaches a carrier. A
message that reached a carrier but failed to be recorded is unrecoverable: the
recipient got it and we have no idea.

### 9.1 The gate

Eight checks over a plain struct with no database or HTTP types, so the rules
stay testable in isolation. Each failure is a **distinct error** — "blocked" with
no reason is exactly the opaque behaviour this product exists to replace.

| # | Check | Code returned |
|---|---|---|
| 1 | Tenant is not suspended | `tenant_suspended` |
| 2 | Recipient is addressable **on this channel** | `invalid_recipient` |
| 3 | Sender is approved | `sender_not_approved` |
| 4 | Template is approved *(by Relay)* | `template_not_approved` |
| 5 | Template belongs to this sender | `sender_template_mismatch` |
| 6 | Carrier has approved the template *(RCS, and only when a carrier is configured)* | `carrier_template_not_approved` |
| 7 | Recipient is not suppressed | `recipient_suppressed` |
| 8 | Balance covers the cost | `insufficient_balance` |

**Why this order:**

- **Compliance before money.** Telling someone "insufficient balance" when the
  real problem is an unapproved sender sends them to fix the wrong thing.
- **Suppression before balance.** An opted-out recipient is never billed for,
  not even momentarily.
- **Our approval before the carrier's.** The carrier's review cannot even begin
  until ours has passed.
- **Check 6 only when a carrier exists.** On a deployment whose only connector is
  the sandbox there is nothing to have approved anything, and demanding it would
  make RCS unusable in test mode.

**A refused send is still recorded**, with its reason, and costs nothing. Someone
asking "why didn't this arrive?" deserves an answer.

### 9.2 The money, in two steps

```
  HOLD                                    SETTLE
  a `charge` ledger entry, written        delivered  → the charge simply stands
  BEFORE anything reaches a carrier       otherwise  → a `refund` entry returns it
```

A carrier can never receive a message we have not reserved payment for. And a
carrier **rejection at submit** releases the hold *immediately* — nothing was
delivered, so nothing is owed.

### 9.3 The state machine

```
queued ──▶ submitting ──▶ submitted ──▶ accepted ──▶ delivered      charge stands
   │            │             │             ├──────▶ undelivered    refund
   └────────────┴─────────────┴─ rejected                           refund
                              └─ expired  ◀──────────┘              refund
```

Terminal states go nowhere. A carrier replaying a receipt — and they do, they
retry — **cannot walk a message backwards out of one**. That is what makes the
ingest path idempotent without a dedupe table.

> **`accepted` means a carrier took it. `delivered` means a handset confirmed
> it.** The dashboard contract collapses these eight internal states to five, and
> `accepted` maps to **"sent"**, never **"delivered"**. That single mapping is
> where this product's central claim is kept or broken.

### 9.4 What if the carrier never answers?

Delivery reports are best-effort: carriers drop them, networks partition, our own
ingest can be down during a deploy. Without a backstop, a lost report means a
customer is charged forever for a message nobody can prove arrived.

A **reconciler** runs every 15 minutes. Anything still `accepted` after 48 hours
is moved to `expired` and the money released. **Silence is treated as failure,
and failure is free.**

### 9.5 Segment arithmetic

From GSM 03.38. Not adjustable preferences — a 161-character GSM-7 message
really is billed as two segments, and getting this wrong makes every estimate and
every charge wrong by a multiple.

| Encoding | One segment | Per segment once concatenated |
|---|---|---|
| GSM-7 | 160 | 153 — 7 septets go to the header |
| UCS-2 | 70 | 67 |

> **A single smart quote** pasted from a word processor pushes the whole message
> out of GSM-7 into UCS-2. That is the most common way a 160-character message
> silently becomes a 70-character one — and doubles in price.

---

## 10 · Automation (journeys)

![Automation](screenshots/40-automation.png)

**What you're looking at.** Multi-step flows: a trigger, then a sequence of
sends and waits.

**Behind it — `/v1/automation/journeys/*`**

Steps are stored as `jsonb`, because the set of step types belongs to the
product and will keep growing. Four states:

```
draft ──activate──▶ active ⇄ paused ──archive──▶ archived
                                                     │
                        unarchive: never straight back to active
                        never activated ──▶ draft    ◀┘
                        had run          ──▶ paused
```

**Why unarchiving splits in two.** Restoring a journey and resuming it are two
decisions and the operator makes them separately. Coming back *active* would
resume sending to a live list the instant the button is pressed, from a single
click with no confirmation.

**`activated_at` is never rewritten.** Unarchiving is not a re-activation, and
the enrollment funnel, the per-step "currently here" counts and the message
trajectory are all computed from elapsed time since the **original** activation.
Stamping it to `now()` would corrupt every one of them.

The status check and the write are a **single conditional UPDATE**, so two
concurrent unarchives — or an unarchive racing an archive — cannot both pass the
guard.

---

## 11 · Inbox

![Inbox](screenshots/50-inbox.png)

**What you're looking at.** Two-way conversations, one per contact per channel.

![Inbox thread](screenshots/51-inbox-thread.png)

**Behind it — `/v1/conversations/*`**

`conversations` is unique on `(tenant_id, contact_id, channel)` — one thread per
contact per channel, so an SMS reply and a WhatsApp reply from the same person
are separate conversations, which is what the customer expects.

**A reply is a real send.** It goes through the same gate as anything else, is
charged at the same rate, and appears in the wallet ledger as an inbox reply. It
is not a free-form message escape hatch.

Conversations carry an unread flag, and can be closed and reopened.

> **Inbound RCS is not threaded yet.** Replies and suggestion taps arrive on the
> carrier webhook and are **logged**, so the traffic is visible — but they do not
> reach this screen. That needs the conversation model the RCS send path does not
> touch, and it is listed in §21.

---

## 12 · Analytics and logs

![Analytics](screenshots/60-analytics.png)

**What you're looking at.** Delivery rates, volumes, cost, broken down by
channel, country and time.

**Behind it — `GET /v1/analytics`**

Three ClickHouse tables carry all of this:

| Table | Shape | Purpose |
|---|---|---|
| `messages` | `ReplacingMergeTree(version)` | One row per message per version. A settled message *replaces* its earlier row |
| `message_events` | append-only | Every state transition, with its reason |
| `message_rollup_hourly` | aggregate | What analytics actually reads |

**Analytics never reads raw messages.** Rollups are permanent while raw rows age
out under the retention policy — so a year-old chart stays truthful after the
messages behind it are gone.

![Logs](screenshots/82-developer-logs.png)

**What you're looking at.** Every individual message, filterable.

**Behind it.** Filters on channel, country, status, campaign and **error class**.
Error class is the useful one: it separates *the number is unreachable* from *we
blocked it ourselves* — different problems, different fixes, and a single
"failed" bucket hides which one you have.

Because `messages` is a `ReplacingMergeTree`, a message that has not been merged
yet has several rows. Reads that need the current state order by `version DESC`;
an unqualified read returns whichever row it finds first, which is often the
pre-submit one.

---

## 13 · Billing and the wallet

![Billing](screenshots/70-billing.png)

**What you're looking at.** Balance per currency, and the statement.

**Behind it — `/v1/wallet/*`**

Relay is **prepaid**. Every movement is a row in an **append-only ledger**,
enforced by a database trigger that rejects any UPDATE or DELETE. A balance
cannot be quietly edited — not by a bug, not by a person with database access
and a bad afternoon.

```sql
-- one movement, one transaction, one lock
INSERT wallet_balances (tenant, currency) ON CONFLICT DO NOTHING  -- create on first use
SELECT balance FROM wallet_balances ... FOR UPDATE                -- serialise
   credit ▸ balance += amount
   debit  ▸ balance < amount ? ErrInsufficientFunds : balance -= amount
UPDATE wallet_balances
INSERT wallet_ledger (..., balance_after_minor)                   -- running total, stored
```

- **Every amount is an integer of minor units** — paise, cents. No floats
  anywhere in the money path.
- **`balance_after_minor` is written into each row**, so the statement
  reconstructs without re-summing. A discrepancy becomes *visible* rather than
  inferred.
- The `FOR UPDATE` serialises concurrent movements on the same wallet. This is
  also why campaign fan-out takes **one** hold per 500-recipient page rather than
  one per message — a per-message lock serialised the entire campaign behind a
  single row.

![Usage](screenshots/71-billing-usage.png)
![Invoices](screenshots/72-billing-invoices.png)
![Estimator](screenshots/73-billing-estimator.png)

Usage by channel and period; invoices with line items; and a standalone
estimator that runs the same segment arithmetic as the campaign wizard and the
send path.

**Auto-recharge** (`/v1/wallet/auto-recharge`) tops the wallet up when it falls
below a threshold, against a saved payment method.

---

## 14 · The developer surface

![API keys](screenshots/80-developer-api-keys.png)

**What you're looking at.** Keys for sending from your own code.

**Behind it — `/v1/developer/api-keys`**

- Prefixed `sk_live_` / `sk_test_` so an environment mix-up is visible.
- **Shown once**, at creation. Stored hashed. Rotatable and revocable.
- Carry **scopes**: `send:sms`, `send:rcs`, and so on. The send path checks the
  scope matching the *sender's channel* — a key scoped only for SMS cannot send
  RCS even with a valid RCS sender.

> **Scopes apply to keys only.** A dashboard session is authorised by role.
> Treating a session as "a key with every scope" would silently grant send rights
> that the key model exists to limit.

**Rate limits.** Per tenant, per environment: **200/second live, 20/second
test**, counted in Redis.

The window is anchored to the tenant's **first request**, not to wall-clock
seconds. A clock-anchored window splits a burst across two counters and lets
through double the intended rate at the boundary. If Redis is unavailable the
limiter **fails open** — read `GET /v1/developer/rate-limit` to see your own
budget.

![Webhooks](screenshots/81-developer-webhooks.png)

**What you're looking at.** Where Relay POSTs your delivery events.

**Behind it — `/v1/developer/webhooks/*`**

Endpoints are per environment, each with a signing secret revealed once. **Every
attempt is recorded** — event type, payload, attempt number, outcome, HTTP status
and a response snippet — and any of them can be **resent** from the dashboard.
A test event can be fired on demand so you can wire up an integration before any
real traffic exists.

There is also an **IP allowlist** for API access (`/v1/developer/ip-allowlist`).

---

## 15 · Verify — one-time codes

![Verify](screenshots/83-developer-verify.png)

**What you're looking at.** Hosted OTP services: define one, and Relay handles
sending the code, checking it, retrying on another channel, and rate limiting.

**Behind it — `/v1/verify/*`**

A **verify service** configures: channels in fallback order, code length, TTL,
max attempts, and per-phone rate limits.

The security details are the whole product here:

- **Codes come from `crypto/rand`**, never `math/rand`. A predictable OTP is not
  an OTP.
- Each digit is drawn **independently**, so every code of the configured length
  is equally likely — **including ones with leading zeros**. Trimming those would
  shrink the keyspace and bias the result.
- Codes are stored **hashed** and compared in **constant time**.
- **Expired**, **incorrect** and **locked** are three different answers.
  *Locked* means the attempt budget is spent and the verification is dead **even
  if the next guess would have been right** — that is the entire point of an
  attempt limit.

Each service has its own analytics: funnel, success rate, fraud flags, and a
drill-down to individual attempts.

---

## 16 · RCS reachability

![RCS reach](screenshots/84-developer-rcs-reach.png)

**What you're looking at.** Ask the carrier whether a handset can receive RCS at
all — and, for a single number, which rich features it supports.

**Why it matters.** An RCS message sent to a handset that cannot display it
produces **no message, no error, and a charge**. This is the check that should
drive RCS-versus-SMS fallback.

**Behind it — `POST /v1/rcs/capabilities`**

```json
→ { "msisdns": ["+919820000001", "+91 98200 00002"] }

← { "vendor": "airtel", "checkedCount": 2, "reachableCount": 1,
    "featuresIncluded": false,
    "results": [ { "msisdn": "+919820000001", "reachable": false },
                 { "msisdn": "+919820000002", "reachable": true } ] }
```

Two rules that will bite anyone building on it:

1. **Features come back only for a single number.** Neither carrier returns them
   in bulk. Read `featuresIncluded` rather than inferring it from an empty array.
2. **`features: null` and `features: []` mean different things.** Null means this
   kind of check does not answer that question. An empty array on a single check
   means *reachable, nothing rich supported* — plain RCS text only.

**Reachability is about the brand's agent, not just the handset.** A perfectly
RCS-capable phone is unreachable until the agent has launched on that
subscriber's carrier. Copy that says "this phone doesn't support RCS" will be
wrong some of the time.

### The two carriers, and why they are hard

Airtel IQ and Vi RBM are both Indian gateways to Google's RBM. They agree on
almost nothing:

| | Airtel IQ | Vi RBM |
|---|---|---|
| Auth | Static Basic credential, never expires | OAuth token from a **separate host**, 60 mints/min per account |
| Bulk reachability | **Refuses lists under 500 numbers** | No floor |
| "Not reachable" | A **failed** envelope wrapping a Google 404 | HTTP 200 with `{}` |
| Templates | API; up to 24 h review; decision on a webhook | **No API at all** — portal only |
| Placeholders | Positional `{{1}}` | Named `[NAME]`, as a JSON *string* |
| Correlation | Their `messageRequestId` — nothing of ours | Accepts our own uuid |
| Revoke | Via TTL only | Explicit |

Consequences you can see in the code:

- **Airtel's unreachable-is-an-error** shape means a naive reading turns every
  non-RCS subscriber in India into an outage, and one of them fails a whole
  audience check.
- **Airtel's 500-number floor** means a 400-number list is 400 individual calls
  and is *slower* than a 5,000-number one.
- **Vi's token cache is not an optimisation.** Minting a token per capability
  check would spend a tenant's whole per-minute budget on one audience screen.
- **Placeholders are translated per vendor**, ordered by the template's declared
  variable list — the *same* list the send path fills values from. If those two
  ever read from different places, every message goes out with its variables
  shuffled and nothing errors.

Carrier callbacks arrive at `POST /v1/carrier-webhooks/rcs/{vendor}/{token}`.
Neither vendor signs its callbacks and neither lets us set a header, so the
shared secret travels in the path — paired with an optional IP allowlist, and the
routes are **not mounted at all** without a token.

You can run both carriers locally: see `scripts/rcs-stubs/`.

---

## 17 · Settings, team, security

![Team](screenshots/91-settings-team.png)

**What you're looking at.** Who is on the account and what they can do.

| Role | Can do | Cannot |
|---|---|---|
| `owner` | Everything, including billing and deleting the account | — |
| `admin` | Everything operational: senders, templates, campaigns, developer, billing, team | — |
| `member` | Day-to-day sending and reading | Settings, billing, compliance, developer, team |

**Behind it — `/v1/team/*`**

The role gate is enforced **twice, on purpose**:

1. The dashboard's middleware hides pages a member may not use.
2. Every gated server action calls `blockedForMember()` **before** any
   validation or fetch, and the API refuses independently with a real 403.

> Why twice: a Server Action is dispatched by its **own id**, not by the page URL
> that rendered the form. A member who captured a mutation could replay it
> against an ungated path and bypass a URL-based gate entirely.

![Security](screenshots/92-settings-security.png)

MFA enrolment (TOTP), active sessions with the device each was minted on, and
recovery codes — hashed and single-use.

![Profile](screenshots/90-settings-profile.png)
![Alerts](screenshots/93-settings-alerts.png)
![Data retention](screenshots/94-settings-data.png)

Organisation details; alert rules (volume ceilings, delivery-rate floors, wallet
thresholds); and per-tenant data retention, which is what ages raw message rows
out while the rollups stay.

---

## 18 · Support

![Support](screenshots/95-support.png)

Tickets from you to Relay's operators. The customer side is here; the operator
side is in §19. A ticket can be replied to by either side, resolved by an
operator, and reopened by the customer — and every transition is in the operator
audit log.

---

## 19 · The operator console

A separate surface for Relay's own staff. **It is not a tenant with extra
rights**: its own login, its own session table, its own database pool.

![Operator login](screenshots/A0-operator-login.png)

**Behind it — `POST /v1/operator/login`**

Operator sessions resolve from `operator_sessions`, and **only for `/v1/operator`
paths**. Two separate lookups rather than one combined one, because a tenant
token must never satisfy an operator route and an operator token must never
scope a tenant query. The moment those share a code path, one missing branch is a
cross-tenant leak.

![Tenants](screenshots/A1-operator-tenants.png)

Every customer, with volume, balance and standing. Actions: suspend, throttle,
reinstate, flag for abuse, dismiss a flag.

![Tenant detail](screenshots/A1b-operator-tenant-detail.png)
![Approvals](screenshots/A2-operator-approvals.png)

**What you're looking at.** The approval queue: senders, templates and
registrations waiting on a human.

**Behind it.** Approving writes the status; rejecting writes a status **and a
reason**, which the customer sees on their own screen. A voice sender cannot be
approved until its caller ID is verified — the console enforces it, not just the
UI.

![Routes](screenshots/A3-operator-routes.png)

**What you're looking at.** Carrier paths per country × channel: priority, cost,
and **compliance standing**.

> **Grey routes are refused by default.** A grey route reaches handsets without
> being registered with the operator behind it. It delivers until the carrier
> notices, and then it does not — messages filtered without a report, sender id
> blocked, and in India the penalty lands on the *principal entity*, not on us.
> Two were found **active** on this product's production deployment, with
> registered alternatives sitting beside them in the same corridor. Enabling one
> now requires `ALLOW_GREY_ROUTES`, which is off by default.

![Rates](screenshots/A4-operator-rates.png)

Default per-segment pricing per country × channel × category, plus per-tenant
overrides, plus a **margin view** showing sell price against route cost.

![Usage](screenshots/A5-operator-usage.png)
![Abuse](screenshots/A6-operator-abuse.png)
![Audit log](screenshots/A7-operator-audit.png)

The audit log is **append-only**, trigger-enforced, exactly like the wallet
ledger. Every operator action lands in it.

![Operator support](screenshots/A8-operator-support.png)
![Operator security](screenshots/A9-operator-security.png)

### Two controls worth knowing about

**Network restriction.** `OPERATOR_IP_ALLOWLIST` limits `/v1/operator` to known
networks. An off-network request gets **404, not 403** — a 403 confirms an
operator console exists at this address, which is exactly the fact worth
withholding from a scan.

**Operator MFA**, with hashed single-use recovery codes. The API **warns at
every boot** if any operator account still accepts the password published in the
repository, and refuses to consider itself safe if the allowlist is empty *and*
staff lack MFA.

---

## 20 · How it is built

### Tenant isolation is the database's job

34 tables carry `FORCE ROW LEVEL SECURITY` with a policy of
`tenant_id = current_tenant_id()`. Every tenant query runs inside a helper that
sets the tenant for the transaction.

> The point: a handler that **forgets to filter** returns *nothing* rather than
> *everything*. Isolation does not depend on every developer remembering.

**Two pools, deliberately separate.** Tenant handlers use the app pool. Operator
handlers use a pool carrying `app.operator`, which sees across tenants.

### Append-only where it counts

`wallet_ledger` and `operator_audit_log` both have triggers rejecting UPDATE and
DELETE. Money and accountability are not editable.

### Background workers

| Worker | Every | What it does |
|---|---|---|
| delivery-drainer | 2 s | Applies the sandbox carrier's delivery reports. Real carriers POST instead — **same settlement code**, different trigger |
| reconciler | 15 min | Expires messages accepted and never reported on, releasing the money |
| campaign sweep | 15 min | Lands campaigns abandoned mid-fan-out on a terminal status |

Each runs under a supervisor that restarts it on panic and records an incident.

**One tenant's bad row must never stall the sweep for everyone else.** The
reconciler continues past a message it cannot settle and joins the failures —
ClickHouse retains messages for 90 days while a tenant row can be deleted long
before that, so a closed account's stale messages would otherwise block every
other tenant's refunds forever.

### The contract

`openapi/control.json` in the backend is a **symlink** to `openapi.json` in the
frontend. One file, one source of truth.

> **A trap worth knowing:** oapi-codegen renders contract enums as plain Go
> string aliases, so an out-of-enum value sails through the type system, reaches
> the database, and 500s. Enum values are validated explicitly where they arrive.

### Deployment

```
push to main (GitHub)
   └─▶ build · vet · test         fails here → nothing is uploaded
        └─▶ pg_dump backup        Relay's own database only — it shares the box
             └─▶ migrate          Postgres (goose) + ClickHouse
                  └─▶ swap binary previous kept as .prev for rollback
                       └─▶ health-check ×20, roll back if unhealthy
```

The API runs on a shared VPS as a **guest** alongside two unrelated products, so
every step is scoped to Relay's own unit, containers and database. Nothing
restarts docker, touches nginx, installs a package or changes a firewall rule —
the three things that would take the neighbours down.

> **A push is not a deploy.** The upload step has failed intermittently on SSH
> while tests passed green. Confirm the run's conclusion, and note that the
> workflow's *per-step* status lags by minutes — probing the API's own endpoints
> is faster proof.

The dashboard deploys separately, on Vercel.

---

## 21 · What is not built

An honest inventory. A reference that lists only what works teaches people to
trust it where they should not.

| | |
|---|---|
| **RCS media, rich cards, carousels** | Only the text shape registers and sends. Cards can be built in a carrier's portal and attached by code |
| **Inbound RCS threading** | Arrives on the webhook and is logged; does not reach the inbox |
| **Capability-driven fallback** | `campaigns.fallback_channel` exists and is **not** yet driven by the reachability check, even though that check now works. The obvious next slice |
| **Per-tenant carrier credentials** | One agent identity per deployment |
| **Campaign readiness vs carrier registration** | An IN × RCS corridor can read "Ready" while every send would be refused for want of a carrier-approved template |
| **Known bug** | The support reply handler drains the sandbox report queue destructively, racing the background drainer. Reproduce with `e2e/inbox.spec.ts` — the reply-flow test passes alone and fails after its siblings |

---

## Appendix · Where to look in the code

| To understand | Read |
|---|---|
| The gate and its ordering | `internal/domain/messaging/gate.go` |
| States and billing effects | `internal/domain/messaging/state.go` |
| The send path end to end | `internal/sending/service.go` |
| Campaign fan-out | `internal/sending/batch.go`, `campaign.go` |
| Segment arithmetic | `internal/domain/billing/segments.go` |
| Per-country rules | `internal/domain/compliance/` |
| OTP generation and checking | `internal/domain/verify/verify.go` |
| The carriers | `internal/connector/rcs_*.go` |
| RCS design decisions in full | `docs/rcs-carrier-integration.md` |
| Running a carrier locally | `scripts/rcs-stubs/README.md` |

Most of the *reasoning* lives in comments next to the code that implements it —
that is deliberate, and it is where to go when this document is not enough.
