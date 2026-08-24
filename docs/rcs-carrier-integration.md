# RCS carrier integration — Airtel IQ and Vi RBM

Working notes from the two vendor specifications, written down before any code
so the next session starts from facts rather than from the PDFs again.

**Source documents.** `Airtel IQ - RCS API Documentation (1).pdf` (v1.8, ~54pp)
and `ViRBM_RCS_APIs_Ver_9.pdf` (v9.0, ~66pp). Two of the four files supplied are
byte-identical duplicates of the Airtel doc, and
`Airtel IQ - RCS API Documentation.pdf` (the unnumbered one) is **corrupt** —
`pdftotext` reports "Couldn't find trailer dictionary" and no page tree. Ask for
a clean copy before trusting anything sourced from it.

---

## What these two vendors actually are

Both are Indian carrier gateways to Google's RBM (RCS Business Messaging). They
solve the same problem and agree on almost nothing about how.

| | Airtel IQ | Vi RBM |
| --- | --- | --- |
| Base | `iqconversation.airtel.in/gateway/airtel-xchange/…` | `api.virbm.in/rcs`, token at `auth.virbm.in/auth/oauth/token` |
| Auth | **Basic** (static username:password, base64) by default; HMAC on request | **OAuth2** access token, itself rate-limited |
| Credential lifetime | Static, never expires, reset by emailing support | Token, must be refreshed |
| Payload encryption | Optional **AES-256** client-side, symmetric key from their KMS | Not offered |
| IP allowlisting | Both directions — our egress to them, their webhook to us | Not documented |
| Throughput | 40 TPS per customer id by default | Throttling groups 1 and 2 |
| Revoke a sent message | Not offered | **Yes** |
| Message expiry / TTL | TTL on template and on send | Message expiry request |

The auth difference alone decides the shape of the code: one connector holds a
static credential, the other has to manage a token lifecycle with its own rate
limit. That is not something a single "RCS connector" can hide behind one
interface without lying about one of them.

## Shared surface, roughly in send order

1. **Brand and agent onboarding** — both require a registered agent before
   anything sends. Airtel documents billing categories per use case.
2. **Templates**, created and then *approved by the carrier*, exactly like DLT.
   Airtel has seven shapes: text, text with variables, text with suggestions,
   media, rich card, rich card without image, carousel. Fetch and edit are
   separate calls.
3. **Capability check** — is this handset RCS-reachable? Both offer single and
   bulk. This is the call that decides RCS-versus-SMS fallback, so it belongs
   in the send path, not only in a screen. **Built** — see below.
4. **Media upload** before sending anything rich. Both have file endpoints and
   both constrain type and size.
5. **Send** — same seven shapes as templates, plus session messages (a reply
   inside an open conversation, which is priced differently).
6. **Typing indicator**, and on Vi also **read notification** and **revoke**.
7. **Webhooks** — delivery receipts and inbound user messages, including
   postback data from suggestion taps.

## How this lands in Relay

The existing shape is close, which is the good news:

- `internal/connector` already abstracts a carrier behind `Submit` and delivery
  reports, and the sandbox implements it. Two new implementations go beside it.
- `routes` already models a carrier path per country × channel, and since this
  week the send path selects one and records it — so `IN/RCS` routing to Airtel
  or Vi is a data decision, not a code branch.
- `sender_ids` already carries RCS agent fields, and `templates` already models
  per-channel content with an approval status and rich JSON.

What does not exist yet, and is the real work:

- **Capability check before send.** Today nothing asks whether a handset can
  receive RCS. The campaign fallback (`fallback_channel`) exists in the schema
  and is not driven by a capability answer.
- **Carrier-side template registration.** Relay approves templates internally;
  neither vendor's approval is requested or tracked. A template approved in
  Relay and unknown to Airtel will simply fail at send.
- **Media upload.** Rich cards and carousels reference vendor-hosted media ids.
- **Suggestion postbacks.** Inbound webhook handling exists for messages, not
  for suggestion taps carrying `postbackData`.
- **Per-vendor credential storage**, including Airtel's optional AES-256 key and
  Vi's token cache with its own rate limit.

## The order I would build it

Each step is independently shippable and testable, and each one is useless
before the one above it.

1. **Credentials and a health check.** Store per-tenant carrier credentials,
   prove we can authenticate against both sandboxes. No sending.
2. ~~**Capability check.**~~ **Done, 2026-08-23.** `POST /v1/rcs/capabilities`,
   both carriers, plus Developer → RCS reach. Details in the section below.
3. ~~**Template registration and approval tracking.**~~ **Done, 2026-08-24.**
   `POST /v1/templates/{id}/carrier-registration`, plus a Carrier column and a
   registration dialog on the templates screen.
4. ~~**Send, text and text-with-variables only.**~~ **Done, 2026-08-24.** RCS
   sends now go to a real gateway, and delivery reports settle them.
5. **Media upload, then rich card, then carousel.**
6. **Webhooks**: delivery receipts ~~first~~ **done**, then inbound, then
   postbacks. Inbound arrives and is logged; threading it into the inbox is not
   built.
7. **Vi-only extras**: read notification, revoke, message expiry.

## Things to decide before coding

- **Which vendor first.** Airtel's static Basic auth is a smaller first step
  than Vi's token lifecycle; Vi offers revoke and read receipts that Airtel does
  not. This is a commercial question as much as a technical one.
- **Do we hold one agent per tenant, or one per tenant per vendor?** Both
  vendors register agents themselves, so a tenant sending through both has two
  agent identities for one brand.
- **Whether template approval is ours, theirs, or both.** Today Relay approves
  templates. If the carrier also approves, a template has two statuses and the
  screen has to say which one is blocking.
- **Airtel's AES-256 payload encryption** — optional, and it changes every
  request body. Opt in now or never; retrofitting it means touching every call.

---

## Capability check — what was built, and what the specs actually say

Shipped 2026-08-23: `POST /v1/rcs/capabilities` with clients for both carriers,
and a Developer → RCS reach screen. No send path change yet; nothing routes on
this answer so far, which is the next step.

### The finding that shaped it: Vi publishes capability discovery twice

The working note above said Vi offers `/rcs/v1/phones/…`. It offers that **and**
a second, different capability API, and the two do not return the same thing:

| | Vi §2.3 | Vi §3.5 | Airtel |
| --- | --- | --- | --- |
| Path | `/bot/v1/{botId}/contactCapabilities?userContact=` | `/rcs/v1/phones/{msisdn}/capabilities?botId=` | `/rcs-content-manager/v1/rcs/capabilities` |
| Vocabulary | GSMA MaaP — `chat`, `fileTransfer`, `videoCall`, `geolocationPush`, `callComposer`, `chatBotCommunication` | Google RBM — `RICHCARD_STANDALONE`, `ACTION_DIAL`, … | Google RBM, the same names |
| "Not RCS" | HTTP 404 | HTTP 200 with `{}` | failed envelope wrapping a Google 404 |

**We use §3.5.** It returns character-for-character the vocabulary Airtel
returns, so one Relay answer means one thing regardless of which carrier served
it. Choosing §2.3 would have meant inventing a mapping from `fileTransfer` to
"can this handset show a carousel" — a guess dressed up as an answer.

Because both speak Google's vocabulary, feature names pass through **unmapped**.
There is deliberately no translation table: a name Relay does not recognise is
more useful reaching the caller intact than being dropped by a filter written
before the carrier added it. `PDF_IN_RICH_CARDS` and `ACTION_OPEN_URL_IN_WEBVIEW`
are Airtel-only; `REVOCATION` is Vi-only.

### The three quirks that cost real work

**Airtel reports an unreachable handset as a FAILURE.** Not an empty feature
list — a `success:false` envelope whose message quotes a Google 404 ("the RBM
agent has not launched on the user's carrier"). Read naively, every non-RCS
subscriber in India looks like an outage, and one of them would fail a whole
audience check. `isAirtelUnreachable` recognises it by message text, which is
not something to be proud of: Airtel returns the same `code: 400` and
"Validation Error" for a malformed number and an unreachable one alike, and the
wrapped Google error is the only thing separating them. Revisit if they ever
give it a distinct code.

**Airtel's bulk endpoint has a floor of 500.** Not a performance hint — a list
of 499 is refused outright. Under the floor the client fans out to single checks
across eight workers, which is also the interactive case (someone pasting a
handful of numbers). Vi has a ceiling of 10,000 and no floor, so it serves small
lists directly.

**Vi's token endpoint allows 60 requests a minute per client.** Minting a token
per capability check would spend a tenant's whole minute on one audience screen,
so the token is cached and the refresh holds the lock across the network call —
releasing it would let a cold cache mint one token per concurrent check.

### Two more, found by driving the whole path

**The placeholder rewrite could corrupt itself.** Rendering Relay's `{{named}}`
tokens into Airtel's positional form was a `ReplaceAll` per variable, which
rewrites its own output: with variables `["x", "1"]`, substituting `{{x}}`
produces `{{1}}`, and the pass for the variable literally named `1` then turns
that into `{{2}}`. Both slots read `{{2}}`, every message goes out with the
wrong values, and nothing errors. A template body containing `{{1}}` is all it
takes, and the variable parser accepts one. Now a single regex pass.

**An RCS campaign would have sent the carrier blank variables.** Campaigns
personalise the body per contact from `contact.Fields`, but the first cut of the
carrier submission passed no variables at all. On RCS the carrier holds the
template and fills it from what we pass, not from the body — so the log would
read "Hi Priya" while the handset showed "Hi ". The submission now carries the
same contact fields that personalised the body.

### And two in the test harnesses, which had been hiding real state

Tenant cleanup in both test fixtures had been failing silently for as long as
they have existed: `wallet_ledger` is append-only, its trigger fires on the
CASCADE from `tenants`, and the delete error was discarded. Tenants and their
warehouse rows accumulated across every run — enough that a reconciler test
asserting on a global count started failing after a day of use, and long enough
that a fixed carrier-template id collided with its own previous run. Both
fixtures now disable the trigger for that one statement, as the demo reseed
does, and the send harness deletes the tenant's ClickHouse rows too. Fixing the
first exposed the second: the warehouse has no foreign keys, so messages
outlived the tenants that sent them and the reconciler failed trying to refund
wallets that no longer existed.

### Deliberately not done yet

- **Nothing routes on the answer.** `campaigns.fallback_channel` still is not
  driven by a capability check. That is the point of the next step and the whole
  reason this one came first.
- **No cache.** A reachability answer is worth caching in Redis with a short TTL
  once the send path calls this per recipient. Pointless before then.
- **Credentials are deployment-level, not per-tenant.** One agent identity for
  the whole deployment. A tenant with its own registered agent needs the
  credentials to move into the database, which is step 1 of the list above and
  was skipped to get a usable answer sooner.
- **Airtel's `totalRandomSampleUserCount` / `reachableRandomSampleUserCount`**
  come back on bulk responses and are dropped. They appear to be a sampled
  estimate; the doc does not explain them well enough to surface a number a
  customer might act on.


---

## The send path — what was built, 2026-08-24

RCS is now a real channel: a template can be registered with a carrier, a
message can be sent through one, and a delivery report settles it.

```
  templates.carrier_*  ──▶  POST /v1/templates/{id}/carrier-registration
                                │
                                ├── Airtel: submitted over their API, PENDING
                                └── Vi:     409 → create it in their portal,
                                            paste the code back
                                │
  webhook TEMPLATE_APPROVED ────┘
                                ▼
  POST /v1/messages  ──▶  gate  ──▶  connector.Registry["RCS"]  ──▶  carrier
                          │                                            │
                          └── refuses if the carrier has not approved   │
                                                                        ▼
  webhook DELIVERED  ◀──────────────────────────────────────────  settlement
```

### The decisions worth knowing

**A template has two approvals and they are separate columns.** Relay approves
content against its own compliance rules; the carrier reviews it again. They can
disagree, and a screen has to be able to say which one is blocking a send —
collapsing them sends the customer arguing with the wrong team. The gate has its
own error for the carrier's, `carrier_template_not_approved`, distinct from
`template_not_approved`.

**The carrier's approval is required only when a carrier will receive the
message.** On a sandbox-only deployment there is nothing to have approved
anything, and requiring it would make RCS unusable in test mode and on every
deployment without a commercial agreement.

**Relay's own review comes first.** The carrier's queue runs up to 24 hours;
spending it on content our compliance team would reject wastes a day.

**Placeholders are translated per vendor.** Relay's templates use `{{named}}`.
Airtel wants positional `{{1}}`, ordered by the template's declared variable
list — the SAME list the send path fills values from. If those two ever read
from different places, every message goes out with its variables shuffled and
nothing errors. Vi wants named `[NAME]` values as a JSON *string* in
`customParams`.

**Correlation differs by vendor, and Airtel's is the awkward one.** Vi accepts a
caller-supplied `messageId`, so Relay sends its own uuid and gets it back.
Airtel's callbacks contain nothing we control: they quote the
`messageRequestId` they issued at submit, so the carrier reference stored beside
the message is the only key back to it — and to the tenant whose wallet is
holding money against it.

**Webhook authentication is a secret in the path.** Neither vendor signs its
callbacks and neither lets us attach a header. It lands in access logs, so it is
paired with an optional IP allowlist and rotated by changing one environment
variable. The routes are not mounted at all without a token.

### Two bugs this work surfaced, both pre-existing

**Settling a message erased its carrier attribution.** A settled row REPLACES
the previous version, and the reload did not read `carrier`, `carrier_ref`,
`route_id`, `template_id` or `sent_at` — so all five were wiped on the first
delivery report. The deliverability-by-carrier report therefore only ever
counted messages that had *not* been delivered. Worse for RCS: `carrier_ref` is
the only key an Airtel callback can be matched on, so the second webhook for a
message (Airtel sends SENT, DELIVERED, then sometimes READ) could no longer find
it at all.

**The recorded carrier was the routes table's pick, not the gateway that sent
it.** Route selection takes the highest-priority active route in a corridor, but
an RCS send goes to whichever of Airtel or Vi the deployment holds credentials
for. A message went to Airtel and the log said Jio. `resolvePath` now records
the gateway that actually carried it and looks up that carrier's own route row.

### Deliberately not done

- **Media, rich cards and carousels.** Only the text shape is registered and
  sent. A card carries structure the carrier template spec here does not
  describe, and submitting one as text would have the carrier approve something
  that is not the template Relay holds. Card templates can still be created in
  the carrier's portal and attached by code.
- **Inbound.** Replies and suggestion taps arrive on the webhook and are logged,
  not threaded into the inbox. That needs the conversation model, which this
  send path does not touch.
- **Capability-driven fallback.** `campaigns.fallback_channel` still is not
  driven by a capability check, even though the check now exists.
- **Per-tenant credentials.** One agent identity for the whole deployment.
- **Media upload, typing indicators, read receipts, explicit revoke.**
- **The campaign readiness matrix does not account for carrier registration.**
  An IN x RCS corridor can read "Ready" while every send would be refused for
  want of a carrier-approved template.
- **Airtel's HMAC auth and AES-256 payload encryption**, and their
  `totalRandomSampleUserCount` / `reachableRandomSampleUserCount` bulk fields.
