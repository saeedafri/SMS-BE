---
title: "Trustsignal (Sigmo) integration: what Relay needs"
subtitle: "Checklist for the Trustsignal call, 2 October 2026"
---

# How to use this document

Relay is our multi-tenant messaging platform (API at
`https://sms-api.saqibsaeed.cloud`). We want to send **RCS, SMS, WhatsApp
and Email** for our customers through Trustsignal (sold to us as Sigmo).

Each section lists **what we need from Trustsignal** (credentials, settings,
approvals) and **the questions to ask on the call**. Tick items off as they
arrive.

**Where each channel stands on our side today:**

| Channel | Relay today | What going live needs |
|---|---|---|
| **RCS** | **Built, tested and deployed** for Trustsignal (1 Oct). The API key works | Credits, an approved bot, the webhook set in Sigmo, one approved template |
| **SMS** | **Built** over **SMPP**: live today on another operator (VIDEOCON) | An **SMPP account** from Trustsignal means no code; we add a bind in our console. An HTTP-only account means building a small HTTP SMS connector (~1–2 days) |
| **WhatsApp** | Channel exists in the product; **no WhatsApp provider connected yet** (messages are simulated) | A WhatsApp connector to Trustsignal's API (~3–5 days) plus the WABA/number details below |
| **Email** (campaigns) | Channel exists; **no email provider connected for campaigns**. Our own account emails (login, reset) go through Resend | An email connector to Trustsignal's API or SMTP (~2–3 days) plus the domain/DNS details below |
| **Voice** | Channel exists, simulated | Trustsignal's API collection has no voice; ask if they offer it |
| **OTP (Otify)** | Relay has its own OTP/verify service | Not needed; price comparison only |

---

# 1. Account, commercial and security (all channels)

## 1.1 We need

- [ ] **Account id** and account name exactly as Trustsignal has it
- [ ] **API keys**: one per environment (**live** and **test/sandbox**) if they offer a test environment. We already have one key; confirm it is the live one
- [ ] **Sub-accounts.** Their API has `POST /register-subaccount`. We need to know whether each of **our customers** should get their own Trustsignal sub-account (separate bots, senders, WABAs and billing) or all traffic runs under one account
- [ ] **Price list per channel** (India): RCS (basic / rich / per-message vs per-conversation), SMS (transactional / promotional / service), WhatsApp (marketing / utility / authentication / service), Email (per 1,000)
- [ ] **Billing model**: prepaid credits or postpaid; how to top up; minimum top-up; invoice with GST; payment terms
- [ ] **Credit alerts**: can they warn us at a low balance? Is there a balance API? (`/v1/accounts/credits` exists for SMS; ask for RCS/WhatsApp/Email)
- [ ] **Support and escalation**: named account manager, technical contact, WhatsApp/email for incidents, response times, uptime SLA
- [ ] **Rate limits**: requests per second per API (RCS send, bulk send, template, WhatsApp, email) and what happens above them (HTTP 429?)

## 1.2 Security: we give them, and we need from them

- [ ] **Our server's fixed IP** for their allowlist: **`3.111.226.170`** (all our outbound traffic, AWS Mumbai)
- [ ] **Their webhook source IPs**, so our webhook endpoints accept only them
- [ ] **Webhook signing**: do they sign webhook requests (HMAC header)? If not, we protect each URL with a secret token in the path (done for RCS)
- [ ] **Webhook retries**: on a non-200 answer, how often and for how long they retry, and whether an event can arrive twice
- [ ] **API key rotation**: how to rotate a key without downtime; can two keys be active during a switch?
- [ ] **API key in the URL** (`?api_key=`): can it be sent as a **header** instead? A key in the URL ends up in proxy logs. Our code keeps it out of our own logs

## 1.3 Questions

1. Is there a **sandbox/test environment** with a separate base URL and key, or a test mode on the live account?
2. Which **Indian operators** does each channel reach (Jio, Airtel, Vi, BSNL)? International?
3. Is there a **status page** or incident notification?
4. Where is data stored, and for how long do they keep message content and phone numbers (DPDP Act)?

---

# 2. RCS

**Status:** our side is done. We send by approved template, submit templates
from our dashboard (text and rich card with buttons), and read delivery, read,
"not RCS", template-approval and customer-reply webhooks.

## 2.1 We need

- [ ] **Credits** for RCS on the account. The balance is **0** today, so every send would be refused
- [ ] **An RCS bot, created and approved** (Sigmo → RCS → RCS Settings → Bots), with:
  - Bot name: what the user sees, **max 40 characters**
  - Brand name
  - Short description: **max 100 characters**
  - Logo: **224 × 224 px**, JPG/PNG, **max 90 KB**
  - Banner: **1440 × 448 px**, JPG/PNG, **max 360 KB** (the logo is overlaid bottom-centre)
  - Brand colour: hex, at least **4.5 : 1 contrast** against white
  - Phone number(s), email(s), website(s), each with a label
  - Terms of service URL and privacy policy URL (public)
  - Message type: **promotional / transactional / OTP**. Every template must match the bot's type
  - Languages (e.g. English)
- [ ] **The bot's `bot_id`** once it is **active**. We link it to the customer's agent in Relay
- [ ] **Webhook configured** (Sigmo → RCS → Webhooks → Add Event Webhook):
  - URL: `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/rcs/trustsignal/<token>`. The token is shared separately and is secret
  - Events: **Delivery Status, Template, User Response**; Agent Delivery Status optional
- [ ] **One or two test phones**: Android, Google Messages, RCS chats turned on

## 2.2 Questions (answers change our code or our pricing)

1. **`sms_fallback` on the send API.** Their docs mark it required. We run our own SMS fallback and **do not want theirs**, or the SMS goes out twice. Can we send **without** `sms_fallback`? If not, can we disable it?
2. **Carrying our message id.** Their webhook includes `pr1`–`pr5`, "custom parameters passed while sending". **How do we pass them on the send?** We would put our own message id in `pr1`; it makes matching reports safer than their `transaction_id` alone.
3. **Idempotency.** If we retry a send after a timeout, is it sent twice? Is there a client reference that makes a retry safe?
4. **Charging for `nonrcs`** (the phone can't take RCS): are we charged? Relay does not charge the customer for these.
5. **Capability check.** Is there an API to ask whether a number can receive RCS before sending? Their public docs have none; the direct Airtel and Vi APIs do.
6. **Template rules**: allowed characters and length for the **template name**; variable naming (`[custom_param0]` or any `[name]`?); maximum number of variables; maximum buttons; text length limit.
7. **Media for rich cards**: must images/videos be hosted by them (upload API) or can we pass our own https URL? Size and format limits? Is a thumbnail needed for video?
8. **Carousels**: supported through the API (we saw `s=3`)? We will add carousels to our product if yes.
9. **Approval times**: typical time to approve a bot, and a template.
10. **TTL**: what `ttl: "30s"` does. Is it the time RCS is attempted before giving up?
11. **Error codes**: the full list behind the "ERROR Codes" button in the portal, with meanings.
12. **Bulk send**: maximum recipients per `bulk_with_fallback` call.
13. **Per-customer bots**: can one account hold many bots (one per customer brand)? Any limit?

---

# 3. SMS

**Status:** our SMS engine speaks **SMPP** and is live today with another
operator. With an **SMPP account** from Trustsignal we add a bind in our
operator console, with no code change. An **HTTP-only** account needs a new
connector (~1–2 days).

## 3.1 We need: SMPP account (preferred)

- [ ] **Host** and **port**, live, plus a test bind if they have one
- [ ] **system_id** (username) and **password**
- [ ] **system_type**, if they require one
- [ ] **Bind type**: transceiver (preferred), or separate transmitter and receiver
- [ ] **Number of simultaneous binds (sessions)** allowed
- [ ] **TPS** (messages per second) per bind and per account
- [ ] **Window size**: submits in flight before a response
- [ ] **enquire_link** interval they expect
- [ ] **TLS or plain TCP?** Our SMPP client uses **plain TCP** today. If they require TLS or a VPN, tell us early
- [ ] **Our IP to allowlist** on their side: **`3.111.226.170`**
- [ ] **TON/NPI** for source (alphanumeric header: we send 5/0) and destination (we send 1/1, international)
- [ ] **DLT TLV tags**: which tags carry the **Principal Entity ID** and **Content Template ID** (we default to **0x1400 / 0x1401**), and whether they need the **PE–TM chain hash** on **0x1402**
- [ ] **Their Telemarketer (TM) DLT ID**, for the PE→TM chain
- [ ] **Delivery receipt format**: standard `id:… sub:… dlvrd:… submit date:… done date:… stat:… err:…`? Is the message id hex or decimal (it must match what `submit_sm_resp` returns)?
- [ ] **Long SMS**: UDH concatenation, or `sar_*` / `message_payload` TLVs?
- [ ] **Unicode**: `data_coding` 8 (UCS2) supported for Hindi and other Indian scripts?
- [ ] **Error code list** for `err:` in receipts, and for `submit_sm_resp` command statuses
- [ ] **Routes**: transactional, promotional and service (implicit/explicit). Is it one bind for all, or a bind per route?

## 3.2 If they only offer HTTP

- [ ] The same commercial and DLT details, plus: which endpoint (`/v1/sms`, `/v1/sms/multi`, `/v1/sms/params`), maximum numbers per bulk call, and the **SMS delivery webhook** payload (their docs show `POST /v1/SMSsavewebhook` to set the URL, but not the payload)

## 3.3 DLT (India): from each of our customers, with Trustsignal's help

- [ ] **Principal Entity (PE) ID** of the customer
- [ ] **Sender IDs (headers)** registered on DLT under that PE (e.g. `ACMERT`)
- [ ] **Content templates** registered on DLT, each with its **DLT template ID**, category (transactional / service / promotional) and exact approved text. Our templates store the DLT template ID and check every message against the text
- [ ] **Trustsignal added as telemarketer** on each customer's DLT account (the PE→TM chain binding), or the messages fail scrubbing
- [ ] Question: **does Trustsignal handle DLT registration** for customers who don't have it yet?

## 3.4 Questions

1. Promotional window (9 am–9 pm): enforced by them, us, or both? (We already enforce it.)
2. DND scrubbing: done by them? Do they return a DND error per message?
3. Sender ID for international traffic, if needed.

---

# 4. WhatsApp

**Status:** WhatsApp is in our product (templates, campaigns, contacts) but
**not connected to any provider**, so messages are simulated. We build a
**Trustsignal WhatsApp connector** (~3–5 days) once the details below are known.

## 4.1 We need

- [ ] **Onboarding model.** Does each customer get their **own WABA** (WhatsApp Business Account) and number under our Trustsignal account, via **embedded signup** or via Trustsignal? This decides how we design it
- [ ] Per WABA: **WABA id**, **phone number id**, the **phone number**, **display name** (Meta-approved), and **Meta Business Manager verification** status
- [ ] **Messaging tier** per number (1K / 10K / 100K unique users per day) and how it is raised
- [ ] **Webhook**: the payloads for status (sent / delivered / read / failed), inbound replies, template status, and phone quality. Their docs list these; confirm they apply to our account. Our URL will be `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/whatsapp/trustsignal/<token>` once built
- [ ] **Template API** access: create / list / get / delete templates (their docs show all four), categories **marketing / utility / authentication**, and the approval time
- [ ] **Media**: upload by URL or form-data returns a media id (their docs show both). Size limits per type?
- [ ] **Pricing** per conversation category in India, and who pays Meta's fee

## 4.2 Questions

1. Can one Trustsignal account hold **many WABAs** (one per customer)? Is there a limit?
2. Do they support **Meta's embedded signup** so a customer can connect their own number from our dashboard?
3. **24-hour service window**: can we send free-form replies (the "agent-reply" API) inside the window?
4. Opt-in: do they require proof of consent, or is that our responsibility? (We record consent per contact per channel.)
5. What happens to a WhatsApp message that fails: any automatic SMS fallback? We would turn it **off**, as with RCS.
6. **Authentication (OTP) templates** with copy-code buttons: supported through the send API?

---

# 5. Email

**Status:** campaign email is in our product but **not connected to a
provider**. We build a **Trustsignal email connector**, API or SMTP (~2–3 days).
Our own account emails (verification, password reset) stay on Resend.

## 5.1 We need

- [ ] **API** (their docs: `/v1/otp-email`, `/v1/transactional-email`, `/v1/promotional-email`) **or SMTP relay details**:
  - SMTP **host**, **port** (587 STARTTLS / 465 TLS), **username**, **password**
  - Sending limits per second and per day
- [ ] **Sending domains**: for each domain we (or a customer) send from, the **DNS records** to add:
  - **SPF** (include), **DKIM** (CNAME/TXT), **DMARC** policy, **return-path / bounce** domain
  - Verification steps and how long they take
- [ ] **From / reply-to** rules: any address on a verified domain?
- [ ] **Dedicated or shared IP**; if dedicated, the **IP warm-up** plan
- [ ] **Webhooks** for delivered, bounced (hard/soft), complaint (spam), opened, clicked, unsubscribed, with example payloads
- [ ] **Suppression list**: do they keep one, and can we read and sync it?
- [ ] **Unsubscribe**: do they add `List-Unsubscribe` headers for promotional mail, or do we?
- [ ] **Content**: raw HTML accepted, or only their templates? Attachments? Maximum size?
- [ ] **Price** per 1,000 emails; separate for transactional and promotional?

## 5.2 Questions

1. One account with many customer domains: supported? Is each domain verified separately?
2. Open/click tracking: can it be turned off per message?
3. Do bounces and complaints auto-suppress on their side?

---

# 6. What we give Trustsignal

| Item | Value |
|---|---|
| Company | Textify Digitals (account `afzal+1@textifydigitals.in`) |
| Our outbound IP (allowlist) | `3.111.226.170` |
| RCS webhook | `https://sms-api.saqibsaeed.cloud/v1/carrier-webhooks/rcs/trustsignal/<token>` (token shared privately) |
| SMS delivery receipts | over the SMPP bind itself, so no URL is needed |
| WhatsApp / Email webhooks | issued once those connectors are built |
| Technical contact | (fill in) |

---

# 7. After the call: our order of work

1. **RCS first send**: needs credits, bot, webhook and test phones (§2). About 1 hour on our side once they exist.
2. **SMS through Trustsignal**: SMPP details (§3.1) mean a same-day bind in our console. DLT per customer (§3.3).
3. **WhatsApp connector**: build once §4.1 is answered, then onboard the first customer WABA.
4. **Email connector**: build once §5.1 is answered, then verify the first domain.
