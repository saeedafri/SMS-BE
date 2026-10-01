---
title: "Trustsignal (Sigmo) integration: everything Relay needs"
subtitle: "Checklist for the Trustsignal call, 2 October 2026: RCS, SMS, WhatsApp, Email, Voice"
---

# How to use this document

Relay is our multi-tenant messaging platform (API `https://sms-api.saqibsaeed.cloud`,
server on AWS Mumbai). We want to send **RCS, SMS, WhatsApp and Email** for our
customers through Trustsignal (sold to us as Sigmo).

For every channel this document gives:

1. **Where Relay stands today**, checked against the code on 1 Oct 2026.
2. **What we need from Trustsignal**: credentials, settings, approvals. Tick them off.
3. **What our portal already collects** from customers, and the Trustsignal field it maps to.
   A gap here means Trustsignal needs something our portal doesn't ask for yet.
4. **Questions to ask on the call**, numbered so answers can be written next to them.

# 0. The ten things to leave the call with

If the call runs short, get these. Everything else can follow by email. The **Call tick list** (next page) numbers every item 1–49; §A says how to run the call, §B gives the reason for every item, §C lists what we and our customers must provide.

| # | Item | Why it blocks us |
|---|---|---|
| 1 | **RCS credits** on the Sigmo account (balance is **0** today) | Every RCS send is refused |
| 2 | **An approved RCS bot** and its `bot_id` (or who creates it, and how long approval takes) | No bot, no RCS sender |
| 3 | **RCS webhook** set in Sigmo to our URL (§2.1) | Messages stay at "sent" forever; template approvals never arrive |
| 4 | **How `sms_fallback` works**: optional? which DLT sender/template? charged how? | Our own SMS fallback can't work with Trustsignal (it needs a pre-send RCS check they don't offer), so a phone without RCS gets **nothing** unless we use theirs or build our own after-failure fallback |
| 5 | **SMPP or HTTP for SMS?** If SMPP: host, port, system_id, password, TPS, binds | SMPP = no code on our side; HTTP = 1–2 days of build |
| 6 | **Their DLT Telemarketer (TM) ID** and the DLT TLV tags they read | Every Indian SMS fails DLT scrubbing without them |
| 7 | **WhatsApp onboarding model**: one WABA per customer? Embedded signup? | Decides the design of the WhatsApp connector |
| 8 | **Email: API or SMTP**, plus the **real SPF / DKIM / return-path records** | Our portal shows placeholder DNS records today (§5.3) |
| 9 | **Price list** for every channel, and prepaid vs postpaid | We must price above their cost |
| 10 | **Webhook source IPs** and whether they sign webhooks | Locks our webhook endpoints to them |

**Where each channel stands on our side (verified in code):**

| Channel | Relay today | What going live needs |
|---|---|---|
| **RCS** | **Built, tested and deployed** for Trustsignal (commit `3128554`, 1 Oct). API key works; base URL `https://rcsapi.trustsignal.io` | Credits, approved bot, webhook in Sigmo, one approved template, test phones |
| **SMS** | **Built over SMPP**: live on VIDEOCON. Our console stores binds per operator (host, port, system_id, password, TPS, window, TLV overrides) | An **SMPP account** = no code, a bind in our console. **HTTP-only** = a new connector (~1–2 days) |
| **WhatsApp** | In the product (senders with WABA id / phone / display name, templates with text, buttons, list). **No provider connected**: sends go to the sandbox (simulated) | WhatsApp connector to Trustsignal (~3–5 days) + the details in §4 |
| **Email** | In the product (sender domain, from address/name, DNS records, subject + HTML templates). **No provider connected for campaigns**: sandbox. Our own login/reset emails go through Resend and stay there | Email connector, API or SMTP (~2–3 days) + the details in §5 |
| **Voice** | In the product (caller-ID sender with ownership check). Sandbox | Trustsignal's API collection shows no voice. Ask (§6) |
| **OTP (Otify)** | We have our own verify service | Not needed. Price comparison only |

---

<div style="page-break-before: always"></div>

# Call tick list: everything from Sigmo, numbered

Tick each item as Sigmo answers. **If the call is short, get the starred (★) ones.**
Write the answer, or who will send it and by when, in the last column.

## Part 1. Before the first RCS message (blockers)

| ✓ | # | What we need | Why | Answer |
|--|--|------------|------------|-------|
| [&nbsp;&nbsp;] | 1 | API key | Every call to Sigmo must carry it. **Have** | |
| [&nbsp;&nbsp;] | 2 | Base URL `https://rcsapi.trustsignal.io` | Where Relay sends requests. **Have** | |
| [&nbsp;&nbsp;] | 3 ★ | **Credits** | Balance is 0, so every send is refused | |
| [&nbsp;&nbsp;] | 4 ★ | **Price per RCS message** | We charge ₹0.35; their price must be lower | |
| [&nbsp;&nbsp;] | 5 ★ | **Webhook set in Sigmo** (Delivery Status, Template, User Response) | Without it: messages stay "sent", no charges, templates never unlock | |
| [&nbsp;&nbsp;] | 6 | First approved bot and its `bot_id` | Templates are created under a bot id | |
| [&nbsp;&nbsp;] | 7 | Test phones whitelisted, if required | To prove the first send arrives | |

## Part 2. Bot API (brands create bots from Relay)

| ✓ | # | What we need | Why | Answer |
|--|--|------------|------------|-------|
| [&nbsp;&nbsp;] | 8 ★ | **Full list of bot fields**, required ones and limits | Our form must ask for exactly these, or bots come back rejected | |
| [&nbsp;&nbsp;] | 9 ★ | **List of verification documents** | Brands must upload them before submitting | |
| [&nbsp;&nbsp;] | 10 ★ | **Create bot API** (endpoint, fields, example) | Relay submits bots automatically, no retyping in Sigmo | |
| [&nbsp;&nbsp;] | 11 | Logo/banner upload: file or URL? size/format | How we hand the images over | |
| [&nbsp;&nbsp;] | 12 | Document upload API | Documents go with the bot automatically | |
| [&nbsp;&nbsp;] | 13 ★ | **Bot status API**, status values, rejection reason | Brand sees pending / approved / rejected because… | |
| [&nbsp;&nbsp;] | 14 ★ | **Bot status webhook** | Approval reaches Relay without the operator typing the bot id | |
| [&nbsp;&nbsp;] | 15 | Update bot API; does a change need re-approval? | Brands change logos and contacts | |
| [&nbsp;&nbsp;] | 16 | Delete / suspend bot API | When a brand leaves or misuses it | |
| [&nbsp;&nbsp;] | 17 | Bot approval time | To tell brands how long to wait | |
| [&nbsp;&nbsp;] | 18 | One approval covers Jio, Airtel and Vi? | One approval to track, or three | |
| [&nbsp;&nbsp;] | 19 | One account for all brands, or sub-account per brand? | Which API key per brand; how billing splits | |

## Part 3. Templates

| ✓ | # | What we need | Why | Answer |
|--|--|------------|------------|-------|
| [&nbsp;&nbsp;] | 20 | Template name rules | Reject bad names in our portal, not days later | |
| [&nbsp;&nbsp;] | 21 | Variable format and max count | Relay converts `{{name}}` to Sigmo's format; a wrong guess fails every template | |
| [&nbsp;&nbsp;] | 22 | Text, card title and description limits | Catch it in our portal | |
| [&nbsp;&nbsp;] | 23 | Max buttons per message | Same | |
| [&nbsp;&nbsp;] | 24 | Card media: upload or our URL? size/format | Rich cards break otherwise | |
| [&nbsp;&nbsp;] | 25 | Carousels via API? | We can add carousels if yes | |
| [&nbsp;&nbsp;] | 26 | Template approval time | Brands need to know | |

## Part 4. Sending and delivery reports

| ✓ | # | What we need | Why | Answer |
|--|--|------------|------------|-------|
| [&nbsp;&nbsp;] | 27 ★ | **How `sms_fallback` works**: optional? which DLT sender/template? charged? reported? | Our own fallback can't work with Sigmo; today a phone without RCS gets **nothing** | |
| [&nbsp;&nbsp;] | 28 | Capability check API ("can this phone take RCS?") | Would make our own fallback work | |
| [&nbsp;&nbsp;] | 29 | How to pass our message id (`pr1`–`pr5`) | Certain matching of reports to messages | |
| [&nbsp;&nbsp;] | 30 | Does a retried send go out twice? | A timeout retry could double-send | |
| [&nbsp;&nbsp;] | 31 | Is `nonrcs` charged? | We don't charge brands for it | |
| [&nbsp;&nbsp;] | 32 ★ | **Full error code list** | Brands see real reasons, not numbers | |
| [&nbsp;&nbsp;] | 33 | What `ttl` means | When we mark a message failed | |
| [&nbsp;&nbsp;] | 34 | Max recipients per bulk call | Batch size for big campaigns | |
| [&nbsp;&nbsp;] | 35 | Rate limit and response above it | We slow down to their limit | |

## Part 5. Webhook security

| ✓ | # | What we need | Why | Answer |
|--|--|------------|------------|-------|
| [&nbsp;&nbsp;] | 36 ★ | **Webhook source IPs** | Only Sigmo can post to our webhook; fakes could trigger wrong charges | |
| [&nbsp;&nbsp;] | 37 | Do they sign webhooks (HMAC)? | Second proof it's really Sigmo | |
| [&nbsp;&nbsp;] | 38 | Retry rules if our server is down | Whether reports are lost in an outage | |
| [&nbsp;&nbsp;] | 39 | Can an event arrive twice? | Confirms our duplicate handling | |
| [&nbsp;&nbsp;] | 40 | Example payload for every event | Our code reads each one correctly | |

## Part 6. Account and commercial

| ✓ | # | What we need | Why | Answer |
|--|--|------------|------------|-------|
| [&nbsp;&nbsp;] | 41 | Prepaid/postpaid, top-up, minimum | Sending never stops for credits | |
| [&nbsp;&nbsp;] | 42 | Balance API and low-balance alert | Warned before 0 | |
| [&nbsp;&nbsp;] | 43 | GST invoice and payment terms | Accounts | |
| [&nbsp;&nbsp;] | 44 | Sandbox account | Test without paying | |
| [&nbsp;&nbsp;] | 45 | Allowlist our IP `3.111.226.170` | So Sigmo accepts our calls | |
| [&nbsp;&nbsp;] | 46 | API key in a header, not the URL | Keys in URLs leak through logs | |
| [&nbsp;&nbsp;] | 47 | Key rotation without downtime | Changing keys doesn't stop sending | |
| [&nbsp;&nbsp;] | 48 | Support contact, escalation, SLA | Who to call when sending stops | |
| [&nbsp;&nbsp;] | 49 | Data storage location and retention | India's DPDP Act | |

## Part 7. Other channels

| ✓ | Channel | Main things we need | Answer |
|--|----|--------------|-------|
| [&nbsp;&nbsp;] | SMS | SMPP login, speed limit, **their DLT TM ID**, DLT TLV tags (§3) | |
| [&nbsp;&nbsp;] | WhatsApp | Number per brand?, send and template API, webhook (§4) | |
| [&nbsp;&nbsp;] | Email | API or SMTP, **real SPF/DKIM records** (§5) | |
| [&nbsp;&nbsp;] | Voice | Do they offer it? (§6) | |

<div style="page-break-before: always"></div>

# A. How to run the call

**Before the call (tonight)**

1. Print this document or keep it open. In the call you need §0, the **Call tick list**, §A and §B. §C is what we and our customers must provide. From §1 on is the full technical detail, for the developer.
2. Fill in the blanks in §7 (our technical contact, billing contact, our DLT TM ID if we have one).
3. Decide the answers in §C ("Decisions only we can make"). Trustsignal will ask them.
4. Do **not** share the RCS webhook token in the call or in email. Send it separately, privately.

**During the call**

1. Work from the **Call tick list** (numbered 1–49, right after §0). Start with the ★ items.
2. §0 is the short version: the ten must-haves. If the call is cut short, those are what matter.
2. Then go channel by channel through §B: for each row, ask for the item and say the reason if they ask why.
3. Write each answer next to the question number. "We'll check" is a fine answer: write down **who** will check and **by when**.
4. Ask them to **email** everything technical (credentials, IPs, DNS records, payload examples, error-code lists). Credentials spoken aloud get mistyped.
5. Before hanging up, agree: who is our single technical contact at Trustsignal, and the date RCS goes live.

**After the call**

1. Forward their email to the developer. Credentials go into the server (encrypted), never into chat, documents or the code.
2. Follow §8 for the order of work.

---

# B. What we need from Trustsignal, and why

Every row is something only Trustsignal can give us. The reason column is what
to say if they ask "why do you need this?".

## B.1 All channels

| What we need | Why we need it |
|---|---|
| Confirmation of which channels our API key covers | We have one key and have tested it only on RCS. If it doesn't cover SMS, WhatsApp or Email, those calls are refused |
| Base URL for each channel | Our connectors need the exact address to call. We know only the RCS one |
| A test (sandbox) key, if they have one | To test without paying for real messages or texting real people |
| Sub-accounts: one per customer, or one shared account? | Decides whether we store one key or one per customer, and how bots, senders and billing are kept apart |
| Price list for every channel | We must charge our customers more than they charge us. Today we charge ₹0.35 per RCS message without knowing their cost |
| Billing model (prepaid or postpaid) and how to top up | With prepaid, a zero balance stops all sending, which is what blocks RCS today |
| An API to read our balance | So we're warned before credits run out, not after sends start failing |
| Rate limits per API | Sending faster than allowed gets messages refused; we slow down to their limit |
| The IP addresses their webhooks come from | So our webhook endpoints accept reports only from them |
| Whether they sign webhooks | Lets us confirm a delivery report really came from them before acting on it |
| Webhook retry rules | Tells us whether a report can arrive twice, and how long they retry if our server is down |
| A safe way to rotate the API key | So a key change doesn't stop sending |
| Whether the key can go in a header instead of the URL | A key in a URL ends up in proxy logs, where it can leak |
| Support contact and uptime SLA | Who to call when sending stops, and what they've committed to |

## B.2 RCS

| What we need | Why we need it |
|---|---|
| **Credits** | The balance is 0, so every send is refused |
| **An approved bot and its `bot_id`** | The bot is the brand people see on their phone. Without its id we can't link a customer to it, so we can't send |
| **The webhook added in Sigmo**, pointing at our URL | Without it, delivery and read reports never reach us, messages stay at "sent" forever, and template approvals never arrive |
| How `sms_fallback` works, and whether it's optional | Our fallback picks SMS **before** sending, using the carrier's "can this phone take RCS?" check. Trustsignal has no such check, so today a phone without RCS gets nothing. Their fallback may be the only way to reach those phones |
| How to pass our message id on a send (`pr1`) | So each delivery report matches the right message for certain |
| Does a retried send go out twice? | After a timeout we can't tell if the message went out; a retry could send it twice |
| Is a failed `nonrcs` send charged? | We don't charge our customer for these. If they charge us, we lose money on every phone without RCS |
| A way to check if a phone can receive RCS | Lets us use SMS up front for those phones instead of failing first |
| Template rules (name format, variables, length, buttons) | So our portal rejects a bad template immediately instead of Sigmo rejecting it days later |
| Media rules for rich cards | Whether card images must be uploaded to them or can stay on our URL, and the size limits |
| Bot verification documents and approval time | We have to collect the right documents from customers and tell them how long they'll wait |
| **A bot API**: create, upload logo/banner, submit with documents, read status and rejection reason, status webhook, update, delete | We will create each customer's bot from our own portal. Without the API, every new customer is a manual step in their portal and approval status never reaches the customer |
| Full list of error codes | So we can show the customer why a message failed |

## B.3 SMS

| What we need | Why we need it |
|---|---|
| **SMPP or HTTP?** | SMPP works with our existing engine, no code. HTTP means building a new connector (1–2 days) |
| **SMPP host, port, system_id, password** | The login details our bind form needs. Without them we can't connect |
| Bind type, number of binds, TPS, window size | Going over their limits gets the bind dropped. We configure ours to match |
| TLS or plain connection | Our client uses a plain connection. If they require TLS or a VPN, we must know before connecting |
| Allowlisting our IP `3.111.226.170` | Most operators refuse binds from unknown IPs |
| **Their DLT Telemarketer (TM) ID** | TRAI requires the full chain (customer → telemarketer → operator) on every Indian SMS. Without their ID, messages fail DLT checks |
| **Which TLV tags carry the DLT IDs** | If our tags don't match what they read, every message fails DLT checks |
| Delivery receipt format (hex or decimal id) | If it differs from what we expect, receipts don't match messages and status never updates |
| Long SMS and Unicode handling | Hindi and long messages break or get cut if we split or encode them differently |
| SMS error codes | So we can show why a message failed |
| Routes: one bind for all, or one per route? | Promotional and transactional traffic may need separate logins |
| Inbound SMS (replies, STOP) | We must honour opt-outs, so we need to know how replies reach us |
| Do they help customers register on DLT? | Customers without DLT registration can't send SMS in India at all |
| Do they filter DND numbers? | Tells us whether DND filtering is our job or theirs, and how a blocked number is reported |

## B.4 WhatsApp

| What we need | Why we need it |
|---|---|
| **Onboarding model** (one WABA per customer? embedded signup?) | Decides how the whole WhatsApp connector is designed. We can't start building without it |
| WABA id, **phone number id**, number, display name | Meta's API needs the phone number id on every send; our portal doesn't collect it yet |
| Messaging tier and quality rating | Meta caps daily sends per number and restricts low-quality numbers; we need both to avoid failures |
| Send API (templates and in-conversation replies) | Needed to build the connector |
| Template API and approval time | Customers' templates are submitted to Meta through them |
| Variable format (`{{1}}` or named) | Our templates use named variables. If only numbered ones work, we convert |
| Can list and button messages be templates? | Our portal offers "list" templates, which Meta normally allows only inside a conversation. They may need removing |
| Media upload rules | Templates with an image, video or document need them |
| Webhook payloads (status, replies, template status, quality) | Without them we can't show delivery, receive replies or learn about approvals |
| Pricing, and whether Meta's fee is included | So our price covers the full cost |
| Do they fall back to SMS automatically? | We would turn it off, so the message isn't sent twice |
| Proof-of-consent rules | Meta and Indian law require opt-in; we need to know what proof they expect |

## B.5 Email

| What we need | Why we need it |
|---|---|
| **API or SMTP, and the credentials** | Our email connector needs one or the other |
| **The real SPF, DKIM and return-path records** | Our portal shows customers placeholder records today. Without the real ones, emails fail checks and land in spam |
| An API to verify customer domains | So the portal shows each customer whether their domain is verified, without manual checks |
| Dedicated or shared IP, and the warm-up plan | A new IP sending full volume immediately gets blocked by Gmail and Outlook |
| Webhook payloads (bounce, complaint, open, click, unsubscribe) | We must stop emailing addresses that bounce or complain, or the domain gets blacklisted |
| Suppression list access | So we don't keep sending to addresses they've already blocked |
| Do they add unsubscribe headers? | Gmail and Yahoo require one-click unsubscribe on bulk mail; we need to know who adds it |
| Content rules (raw HTML, attachments, size) | Our templates are raw HTML. If they accept only their own templates, our design doesn't work |
| Price per 1,000 emails | To set our price |

## B.6 Voice

| What we need | Why we need it |
|---|---|
| Do they offer voice calls at all? | Our voice channel is still simulated. If they don't offer it, we need another provider |

---

# C. What else is needed (not from Trustsignal)

These are on **our** side or our **customers'** side. Trustsignal can't give them,
but the go-live waits on them just the same.

## C.1 Decisions only we can make

| Decision | Why it matters |
|---|---|
| Whose brand is the **first RCS bot**: Textify, or a customer (e.g. Acme Retail)? | The bot shows that brand's name and logo, and needs that brand's documents |
| The first bot's **message type**: promotional, transactional or OTP | Each bot has one type; templates of another type are rejected |
| Which **tenant** sends the first RCS message | Its registration must be approved and its wallet funded |
| **One Trustsignal account for everyone, or a sub-account per customer** | Changes how we store keys and how billing is split |
| **Our price per channel** once their prices are known | Our ₹0.35 per RCS message must stay above their cost |
| Whether **SMS moves to Trustsignal** or stays on VIDEOCON | Decides whether we need the SMPP details at all |
| **Who adds the webhook in Sigmo** (you, or the developer) | The developer was told not to change the Sigmo portal |

## C.2 From us (Textify)

| Item | Why |
|---|---|
| Our **DLT Telemarketer ID** (if we're registered as one) | It may need to sit in the DLT chain alongside Trustsignal's |
| **Brand assets** for the first bot: logo 224×224 (≤ 50 KB), banner 1440×448 (≤ 200 KB), colour, description (≤ 100 characters), phone, email, website, terms and privacy URLs | Sigmo needs them to create the bot; our portal enforces the same sizes |
| **Company documents** for bot verification (whatever Trustsignal lists) | The networks won't approve a bot without them |
| **1–2 test phones**: Android, Google Messages, RCS chats on | To prove the first send arrives |
| **Money for credits** | Prepaid credits must be bought before anything sends |
| Technical and billing **contact names** | Filled into §7 |

## C.3 From each customer

| Item | Channel | Why |
|---|---|---|
| Brand details and documents for **their own bot** | RCS | Each customer needs a bot in their own name |
| **DLT Principal Entity ID**, registered **headers** and **content templates** with their DLT template ids | SMS | Every Indian SMS must carry them, or the operator drops it |
| Adding **Trustsignal's TM ID** to their DLT chain (in their DLT portal) | SMS | Without the chain binding, every message fails DLT checks |
| A **WhatsApp number** and Meta Business verification | WhatsApp | Meta requires a verified business per number |
| Access to their **DNS** to add the email records | Email | Without the records, emails land in spam or are rejected |
| **Opt-in consent** for their contacts | All | Required by law and by Meta/TRAI; we record it per contact |

## C.4 Build work on our side

| Work | Size | Waits on |
|---|---|---|
| RCS first send (store the account, link the bot, submit a template) | ~1 hour | Credits, bot, webhook (§2) |
| SMS via SMPP: add the bind in the console | Same day | SMPP details (§3.1) |
| SMS via HTTP (only if no SMPP) | 1–2 days | HTTP API details (§3.2) |
| WhatsApp connector, plus phone number id, header/footer/language in templates | 3–5 days | Onboarding model and API (§4.1) |
| Email connector, plus real DNS records in the portal | 2–3 days | API/SMTP and DNS records (§5.1) |
| Portal fixes: 100-character limit on bot description; bot languages; labelled contacts | < 1 day | Nothing |
| Bot creation from our portal | Depends | A Trustsignal bot API (§2.3 Q16) |

---

# 1. Account, commercial and security (all channels)

## 1.1 We need

- [ ] **Account id** and account name exactly as Trustsignal holds them (ours: Textify Digitals, login `afzal+1@textifydigitals.in`)
- [ ] **API keys**: one per environment (**live** and **test/sandbox**) if they have a test environment. We hold one key; confirm it is the **live** key and which channels it covers (RCS only, or SMS/WhatsApp/Email too?)
- [ ] **Base URLs per channel** (RCS is `https://rcsapi.trustsignal.io`; are SMS, WhatsApp and Email on the same host?)
- [ ] **Sub-accounts.** Their API has `POST /register-subaccount`. Should each of **our customers** get their own sub-account (separate bots, senders, WABAs, billing), or does all traffic run under our one account? Our platform is multi-tenant either way; this decides whether we store one key or one key per customer
- [ ] **Price list per channel** (India):
  - RCS: basic / rich / A2P conversation; per message or per conversation; is a `nonrcs` failure charged?
  - SMS: transactional / service / promotional, per SMS part (160 GSM-7 / 70 Unicode characters)
  - WhatsApp: marketing / utility / authentication / service conversations; is Meta's fee inside their price or extra?
  - Email: per 1,000; transactional vs promotional; dedicated IP cost
- [ ] **Billing model**: prepaid credits or postpaid; one wallet for all channels or one per channel; how to top up; minimum top-up; GST invoice; payment terms
- [ ] **Balance API and low-balance alert**: `/v1/accounts/credits` exists for SMS; ask for RCS, WhatsApp, Email
- [ ] **Support**: named account manager, technical contact, incident channel (WhatsApp group / email), response times, **uptime SLA**
- [ ] **Rate limits** per API (RCS send, bulk send, template create, WhatsApp, Email, SMS HTTP): requests per second, and the response above the limit (HTTP 429? `Retry-After`?)

## 1.2 Security: we give them, they give us

- [ ] **Our fixed outbound IP** for their allowlist: **`3.111.226.170`** (all our traffic, AWS ap-south-1)
- [ ] **Their webhook source IPs**, so our endpoints accept only them
- [ ] **Webhook signing**: an HMAC header, and with which secret? If none, we protect each URL with a secret token in the path (already done for RCS)
- [ ] **Webhook retries**: retry schedule on non-200, for how long, and can the same event arrive twice (we de-duplicate, but need to know)
- [ ] **Key rotation** without downtime: can two keys be active at once?
- [ ] **Key in the URL** (`?api_key=`): can it go in a **header** instead? A key in a URL lands in proxy logs. (Our code keeps it out of our own logs; their side is the question.)
- [ ] **Data retention and location** (DPDP Act): where message content and phone numbers are stored, and for how long

## 1.3 Questions

1. Is there a **sandbox** (separate base URL and key) or a test mode on the live account?
2. Which **Indian operators** does each channel reach (Jio, Airtel, Vi, BSNL)? Any international reach?
3. **Status page** or incident notifications?
4. Is there **one webhook per channel**, or one URL for everything with an event type field?

---

# 2. RCS

**Status:** done on our side and deployed. We send by approved template; our
dashboard submits templates to Sigmo (text and rich card with buttons); we read
delivery, read, `nonrcs`, template-approval and customer-reply webhooks. The
Sigmo account itself is empty: **0 bots, 0 templates, 0 credits.**

## 2.1 We need

- [ ] **Credits** for RCS. Balance is **0**, so every send is refused today (our error: `carrier_account_unfunded`)
- [ ] **RCS price per message**. Relay charges tenants **₹0.35 per RCS message** today; that must sit above Sigmo's cost
- [ ] **An approved RCS bot** (Sigmo → RCS → RCS Settings → Bots), with the fields in §2.2
- [ ] **The bot's `bot_id`** once its status is **active**. We link it to the customer's agent: `operator-admin rcs-launch <agent> TRUSTSIGNAL <bot_id>`
- [ ] **Webhook** (Sigmo → RCS → Webhooks → Add Event Webhook):
  - URL: `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/rcs/trustsignal/<token>`. The token is secret and shared separately, not in this document
  - Events: **Delivery Status, Template, User Response**. Agent Delivery Status optional
- [ ] **One or two test phones**: Android, Google Messages, RCS chats turned on
- [ ] **Decision on our side:** whose brand is the first bot (Textify or a customer), and its message type

## 2.2 Bot fields: what Sigmo asks for vs what our portal collects

Our portal has an **RCS agent** form per customer. Most bot fields come from it.

| Sigmo bot field | Sigmo rule | Our portal field | Gap |
|---|---|---|---|
| Bot name | max **40** characters | `displayName` | None: we enforce 40 characters |
| Brand name | company behind the bot | tenant's company name | — |
| Short description | max **100** characters | `description` | **We don't limit it.** Add a 100-character limit |
| Logo | **224 × 224 px**, JPG/PNG, max **90 KB** | `logo` (uploaded asset) | None: we enforce exactly 224 × 224 and **≤ 50 KB** (Airtel's rule, stricter than Sigmo's) |
| Banner | **1440 × 448 px**, JPG/PNG, max **360 KB** | `heroImage` | None: we enforce exactly 1440 × 448 and **≤ 200 KB** |
| Brand colour | hex, contrast ≥ **4.5 : 1** on white | `primaryColor` | — |
| Phone(s), email(s), website(s), with labels | at least one each | `phoneNumber`, `email`, `website` (one each) | We hold **one** of each and **no labels** |
| Terms URL, privacy URL | public pages | `termsOfServiceUrl`, `privacyPolicyUrl` | — |
| Message type | promotional / transactional / OTP | `useCase`: OTP / TRANSACTIONAL / PROMOTIONAL | None: maps 1:1 |
| Languages | e.g. English | — | **Not collected** |
| Verification documents | ? | `verification`: contact name/email/phone + one document | Ask what Sigmo/the carriers need (§2.3 Q14) |

## 2.3 Questions (answers change our code or our pricing)

1. **`sms_fallback`**: their docs mark it **required**; we currently send without it. Our own fallback can't work with Trustsignal: it decides SMS vs RCS *before* sending, from a capability check Trustsignal doesn't have. So: (a) is a send without it accepted? (b) when we do use it, which **DLT sender and template** does the SMS carry, and is it ours or theirs? (c) is the SMS **charged separately**? (d) does the webhook tell us the SMS was sent, and its delivery? Their answer decides whether we use their fallback or build an after-failure fallback in Relay (~1–2 days)
2. **Our message id on the send**: their webhook carries `pr1`–`pr5` ("custom parameters passed while sending"). **How do we pass them?** We would put our message id in `pr1`; today we match only on their `transaction_id`
3. **Idempotency**: a retry after a timeout, is it sent twice? Is there a client reference that makes retries safe?
4. **`nonrcs` charging**: are we charged when the phone can't take RCS? (We don't charge the customer.)
5. **Capability check**: any API to ask "can this number receive RCS?" before sending? Their public docs have none; Airtel and Vi direct APIs do
6. **Template rules**: allowed characters and length of the **template name**; variable syntax (`[name]` or only `[custom_param0]`?); max variables; max buttons/suggestions; text length; card title/description length
7. **Media for rich cards**: must images/videos be uploaded to them, or can we pass our own https URL? Size/format limits? Video thumbnail required?
8. **Carousels** via API? (Our template model has text and single card today; we would add carousels.)
9. **Approval times**: bot, and template
10. **TTL** (`ttl: "30s"`): the time RCS is attempted before giving up?
11. **Error codes**: the full list behind the portal's "ERROR Codes" button, with meanings
12. **Bulk send**: max recipients per `bulk_with_fallback` call
13. **Bots per account**: one per customer brand, any limit?
14. **Bot verification**: which documents the carriers need (GST, incorporation, letter of authorisation?) and who submits them
15. **Inbound replies**: free-text replies and suggestion taps both arrive on the User Response webhook? Can we reply in-session without a template?
16. **Bot creation by API.** We will create bots from our own portal, so we need their bot API. Ask for the documentation of each of these:
    - **Create bot**: endpoint, every field, which are required, and the limits (name, description, colours, contacts, URLs, message type, languages)
    - **Upload logo and banner**: by file upload or by our https URL? Format and size rules
    - **Submit for approval** (if separate from create), with the **verification documents** the networks need, and how they are uploaded
    - **Get bot / list bots**: status values (draft, submitted, active, rejected…) and the **rejection reason**
    - **Bot status webhook** (or must we poll?), with an example payload
    - **Update bot**: which fields can change after approval, and does a change send it back for review?
    - **Delete / suspend bot**
    - **Launch scope**: is one approval live on Jio, Airtel and Vi together, or one launch per network?
    - **Test devices**: can a bot send to whitelisted test phones before it is approved?
    - **Sub-accounts**: is the bot created under our account or a customer sub-account?
    - Rate limits and error codes for these endpoints

---

# 3. SMS

**Status:** our SMS engine speaks **SMPP** and is live on VIDEOCON. Each bind is
a row in our operator console. An Indian SMS without a DLT entity id and DLT
template id is refused by us before it reaches the operator (`DLT_IDS_MISSING`).

## 3.1 We need: SMPP account (preferred). These are the exact fields of our console's bind form

| Our console field | What to ask Trustsignal | Our default |
|---|---|---|
| `host`, `port` | Live host and port; a **test** bind too if they have one | — |
| `system_id`, `password` | Username and password (stored encrypted on our side) | — |
| `system_type` | Required? Which value? | empty |
| `bind_type` | Transceiver preferred; or separate transmitter + receiver | transceiver |
| number of binds | How many simultaneous sessions we may open | — |
| `max_tps` | Messages per second **per bind** and per account | — |
| `window_size` | Submits in flight before a response | 10 |
| `enquire_link_seconds` | Keep-alive interval they expect | 30 |
| `reconnect_backoff_seconds` | Any rule on how fast we may rebind | 5 |
| TLS | **Our client is plain TCP.** If they require TLS or a VPN, we need to know now | plain TCP |
| Source TON/NPI | For alphanumeric headers | 5 / 0 |
| Destination TON/NPI | International E.164 numbers (`91…`) | 1 / 1 |
| `registered_delivery` | Receipt for every message | 1 |
| DLT entity TLV | Tag carrying the **Principal Entity ID** | `0x1400` |
| DLT template TLV | Tag carrying the **Content Template ID** | `0x1401` |
| DLT chain TLV | Tag for the **PE→TM chain hash** (TRAI, since 11 Dec 2024) | `0x1402` |
| chain | **Their Telemarketer (TM) ID** to end the PE→TM chain; and whether ours goes in it too | — |
| allowlist | Our IP **`3.111.226.170`** | — |

Also ask:

- [ ] **Delivery receipt format**: standard `id:… sub:… dlvrd:… submit date:… done date:… stat:… err:…`? Is the message id **hex or decimal**, and does it match `submit_sm_resp` exactly?
- [ ] **Long SMS**: UDH concatenation, or `sar_*` / `message_payload` TLVs?
- [ ] **Unicode**: `data_coding` 8 (UCS2) for Hindi and other scripts?
- [ ] **Error codes**: the list for `err:` in receipts and for `submit_sm_resp` statuses
- [ ] **Routes**: transactional / service / promotional on **one bind** or a bind per route? Separate system_ids?
- [ ] **Inbound SMS (MO)**: do replies / keywords (e.g. STOP) arrive as `deliver_sm` on the bind? On which numbers?

## 3.2 If they only offer HTTP SMS

- [ ] Endpoint to use (`/v1/sms`, `/v1/sms/multi`, `/v1/sms/params`), and max numbers per bulk call
- [ ] How DLT entity id and template id are passed in the HTTP request
- [ ] **Delivery webhook payload** (their docs show `POST /v1/SMSsavewebhook` to set the URL, not the payload)
- [ ] Their message id in the response, to match receipts

## 3.3 DLT (India): what each customer gives us, and what our portal collects

| DLT item | Who has it | Our portal field | Notes |
|---|---|---|---|
| **Principal Entity (PE) ID** | the customer, from their DLT portal (Jio/Airtel/Vi/BSNL) | the customer's **registration** (`registrationId`) | Sent on every SMS in TLV `0x1400` |
| **Header (sender ID)**, e.g. `ACMERT` | customer, registered on DLT under the PE | SMS sender `header` + its DLT header id (`registrationId`) | 6 letters for promo/service/transactional headers |
| **Content template** text | customer, approved on DLT | template `body` with `{{variables}}` | We check every message against the approved text |
| **DLT template ID** | customer | template `registrationId` | Sent in TLV `0x1401` |
| **DLT category** | customer | `dltCategory`: PROMOTIONAL / SERVICE_IMPLICIT / SERVICE_EXPLICIT / TRANSACTIONAL | TRANSACTIONAL is banking/OTP only |
| **PE→TM chain binding** | customer adds **Trustsignal's TM ID** (and ours, if required) in their DLT portal | — | Without it every message fails scrubbing |

Ask:

- [ ] Does Trustsignal **register customers on DLT** if they have no PE yet, or help with header/template approval?
- [ ] Do they **scrub DND** themselves and return a per-message DND error?
- [ ] **Promotional window** (9 am–9 pm): enforced by them, us, or both? (We already enforce it.)
- [ ] Sender ID for **international** traffic, if ever needed

---

# 4. WhatsApp

**Status:** WhatsApp is in our product (senders, templates, campaigns,
contacts, consent per channel) but **no provider is connected**: messages go to
the sandbox and are simulated. We build a Trustsignal WhatsApp connector
(~3–5 days) once §4.1 is answered.

## 4.1 We need

- [ ] **Onboarding model**: does each customer get **their own WABA** and number under our Trustsignal account? Through **Meta embedded signup** (from our dashboard) or through Trustsignal's team?
- [ ] Per number: **WABA id**, **phone number id**, **phone number**, Meta-approved **display name**, **Business Manager verification** status
- [ ] **Messaging tier** per number (1K / 10K / 100K / unlimited unique users per 24 h) and how it is raised
- [ ] **Quality rating** per number (green / yellow / red): delivered by webhook or API?
- [ ] **Send API**: template send (with variables, media header, button parameters) and free-form session send
- [ ] **Template API**: create / list / get / delete; categories **marketing / utility / authentication**; approval time; language codes
- [ ] **Media**: upload by URL or form-data returns a media id; size limits per type (image, video, document, audio)
- [ ] **Webhooks**, with example payloads: message status (sent / delivered / read / failed, with error codes), **inbound messages**, template status changes, phone quality / tier changes. Our URL will be `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/whatsapp/trustsignal/<token>` once built
- [ ] **Pricing** per conversation category in India, and whether Meta's fee is included

## 4.2 What our portal collects for WhatsApp, and what's missing

**Sender** (one per WhatsApp number):

| Our field | Trustsignal / Meta equivalent | Gap |
|---|---|---|
| `wabaId` | WABA id | — |
| `phoneNumber` | the number | — |
| — | **phone number id** (Meta's id, used on every send) | **Not collected.** Add, or look it up from the WABA |
| `displayName` | Meta-approved display name | — |
| `qualityRating`, `messagingTier` | from Meta via Trustsignal | Shown in our UI, but nothing fills them yet. Needs their webhook/API |

**Template content** our portal supports today:

| Our template kind | Contents | Matches Meta? |
|---|---|---|
| `text` | body with `{{named}}` variables | Meta uses `{{1}}` or named params. **Ask which Trustsignal accepts** |
| `buttons` | body + **max 3** buttons: quick reply, URL, call | Meta allows up to 10 (quick reply + CTA mixes). Fine as a subset |
| `list` | body + button label + sections of rows | **Lists are session (non-template) messages in Meta.** A list can't be approved as a template. **Ask** |
| categories | MARKETING / UTILITY / AUTHENTICATION | Matches Meta |
| — | **header** (text / image / video / document), **footer**, **language** | **Not in our template model.** Needed for most real templates |
| — | **authentication template** with copy-code / one-tap button | **Not in our model.** Ask if their API supports it |

## 4.3 Questions

1. Can one account hold **many WABAs** (one per customer)? Limit?
2. **Embedded signup** supported, so a customer connects their own number from our dashboard?
3. **24-hour service window**: free-form replies (the "agent-reply" API) allowed inside it?
4. **Opt-in**: do they need proof of consent? (We record consent per contact per channel.)
5. **Automatic SMS fallback** on WhatsApp failure? We would keep it **off**, as with RCS
6. **Variables**: positional `{{1}}` only, or named? Any limit on variable count?
7. **Interactive list** and **reply-button** messages: allowed only in-session, or as templates?
8. **Migration**: can a customer move an existing number/WABA from another BSP to Trustsignal?
9. **Error codes**: Meta's codes passed through as-is, or their own?

---

# 5. Email

**Status:** campaign email is in our product (sender domain, from address and
name, DNS records, templates with subject + HTML + preheader) but **no provider
is connected for campaigns**: sandbox. We build a Trustsignal email connector,
API or SMTP (~2–3 days). Our own account emails (verification, password reset)
stay on Resend.

## 5.1 We need

- [ ] **Which interface**: their API (`/v1/otp-email`, `/v1/transactional-email`, `/v1/promotional-email`) **or SMTP relay**. If SMTP:
  - **host**, **port** (587 STARTTLS or 465 TLS), **username**, **password**
  - limits per second, per hour, per day
- [ ] For each sending domain, the **exact DNS records** to add:
  - **SPF** (`include:` value)
  - **DKIM** (selector, CNAME or TXT, value)
  - **return-path / bounce** domain (CNAME)
  - **DMARC** recommendation
  - tracking domain (CNAME), if open/click tracking is used
  - how verification is triggered and how long it takes
- [ ] **From / reply-to** rules: any address on a verified domain?
- [ ] **Dedicated or shared IP**; if dedicated, the **warm-up** plan
- [ ] **Webhooks** with example payloads: delivered, **bounced (hard / soft)**, **complaint (spam)**, opened, clicked, unsubscribed
- [ ] **Suppression list**: do they keep one, can we read and sync it?
- [ ] **Unsubscribe**: do they add `List-Unsubscribe` / one-click headers for promotional mail, or do we?
- [ ] **Content**: raw HTML accepted, or their templates only? Attachments? Max message size?
- [ ] **Price** per 1,000; transactional vs promotional

## 5.2 What our portal collects for email

| Our field | Used for |
|---|---|
| `emailDomain` | the domain the customer sends from, e.g. `notifications.acme.in` |
| `fromAddress`, `fromName` | the From header |
| `dnsRecords` (type, host, value, status) | records the customer adds to their DNS, with a verified/pending status |
| template `subject`, `bodyHtml`, `preheader` | the message |

## 5.3 Gap: our DNS records are placeholders

Today the portal generates **placeholder** records when a customer adds an
email domain: SPF `include:mail.relay-platform.example` and a fixed dummy DKIM
key. These are not real. **We need Trustsignal's real records** (§5.1), and an
API to create a domain and read its verification status, so the portal shows
the customer the records Trustsignal will actually check.

## 5.4 Questions

1. One account with **many customer domains**: supported, each verified separately? Via API?
2. **Open/click tracking**: can it be turned off per message?
3. Do bounces and complaints **auto-suppress** on their side?
4. Do they need a **reply-to** inbox, or handle replies?

---

# 6. Voice

Our product has Voice senders (a caller-ID number whose ownership we verify by
call or code) but sends are simulated. Their API collection shows no voice.

1. Do they offer **voice / OBD** (pre-recorded or text-to-speech calls) in India?
2. If yes: API, caller-ID rules, DTMF capture, price per pulse (15 s / 30 s / 60 s)

---

# 7. What we give Trustsignal

| Item | Value |
|---|---|
| Company | Textify Digitals (account `afzal+1@textifydigitals.in`) |
| Our outbound IP (allowlist) | `3.111.226.170` |
| RCS webhook | `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/rcs/trustsignal/<token>` (token shared privately) |
| SMS delivery receipts | over the SMPP bind itself, so no URL needed (HTTP SMS would need one) |
| WhatsApp / Email webhooks | issued once those connectors are built |
| Our DLT TM ID (if they need it in the chain) | (fill in) |
| Technical contact | (fill in) |
| Billing contact | (fill in) |

---

# 8. After the call: order of work

1. **RCS first send** (§2): credits, bot, webhook, test phones. About 1 hour on our side once they exist.
2. **SMS through Trustsignal** (§3): SMPP details = same-day bind in our console. DLT chain per customer.
3. **WhatsApp connector** (§4): build once §4.1 is answered; add phone number id, header/footer/language to our template model; onboard the first customer WABA.
4. **Email connector** (§5): build once §5.1 is answered; replace placeholder DNS records with Trustsignal's; verify the first domain.
