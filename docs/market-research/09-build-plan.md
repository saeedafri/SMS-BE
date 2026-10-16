# 09 — Build plan

Status: 16 Oct 2026. Derived from [08-gap-analysis.md](08-gap-analysis.md). The gap IDs in brackets (A1, C-WA1, …) refer to that file.

## Planning assumptions

- **Team.** One backend stream (SMS-BE) and one frontend stream (SMS-UI, via `HANDOFF_TO_UI_*` docs and the relay bridge).
- **Effort.** Estimates are in engineer-weeks (ew) for someone who knows the code. Calendar time also depends on the external lead times in §2, which usually dominate.
- **Done means live.** Every epic follows the project rule: "tested locally against remote datastores, then live on relay-aws, then proven against the public API". An epic is not done until its acceptance criteria pass **live**.
- **Order.** Integrity first, then India-complete, then channel parity, then enterprise and reseller, then international. Channel work runs in parallel with India-complete because its external lead times are long.

---

## 1. Timeline at a glance

| Phase | Weeks (from 19 Oct 2026) | Theme | Exit criterion |
|---|---|---|---|
| **0. Stop the bleeding** | W1–W2 | Integrity and security P0s | No customer can be charged for, or shown, something that does not work; P0 security closed |
| **1. India-complete** | W2–W10 | Promotional SMS flows, money works, RCS solid | Promotional plus transactional SMS on 2 binds; UPI/card top-up; GST invoice; RCS fallback before sending |
| **2. Channel parity** | W3–W16 (parallel) | Email → WhatsApp → Voice made real | Each channel live-proven end to end with a real handset or inbox |
| **3. Sell to mid-market and resellers** | W10–W20 | Developer experience, sub-accounts, journeys v2, Verify v2, trust surface | SDKs plus API reference live; reseller tenant onboarded; status page public |
| **4. Enterprise and international** | W16–W30+ | SSO, compliance v2, certifications, UAE/KSA | SSO login live; consent provenance; UAE regime sendable; SOC 2 window started |

Total engineering effort is roughly 70–90 ew across both streams. With one backend and one frontend stream, Phases 0–3 take about 5 months. Phase 4 overlaps and runs on.

---

## 2. Start these now (external lead times are the critical path)

| # | Action | Owner | Lead time | Unblocks |
|---|---|---|---|---|
| X1 | Apply to Meta as a **WhatsApp Tech Provider** (Business verification, app review for `whatsapp_business_management` and `whatsapp_business_messaging`, Embedded Signup config) | Saeed | 2–8 weeks [04 §4] | Epic 2.2 |
| X2 | Move **SES ap-south-1 out of sandbox** (production access request) and verify a Relay sending domain | Saeed (AWS project RelaySMS) | 1–3 days | Epic 2.1 |
| X3 | Sign a **second SMS route**: a tier-1 Indian aggregator over SMPP (quotes needed) | Saeed | 2–4 weeks | Epic 1.2 |
| X4 | Confirm with VIDEOCON whether operator-side DLT/DND scrubbing applies to Relay's binds (decides the "operator scrubs" default) | Saeed | days | Epic 1.1 |
| X5 | Payment gateway merchant account: **Razorpay** (preferred) or Cashfree, with UPI AutoPay / e-mandate enabled | Saeed | 1–2 weeks of KYC | Epic 1.3 |
| X6 | GSTIN, legal entity and state code for invoices, plus a CA's sign-off on SAC code and place-of-supply rules | Saeed + CA | 1 week | Epic 1.4 |
| X7 | Voice upstream account with **Exotel** (or Plivo India): KYC, virtual numbers, 140/1600 series where needed | Saeed | 2–4 weeks | Epic 2.3 |
| X8 | Trustsignal: ask for a capability-check API or `sms_fallback` support, carousel template support, and header-based auth instead of a query-string key | Saeed | vendor-dependent | Epic 1.5, B5 |
| X9 | Jio direct RCS commercial (the only Indian iPhone RCS path) | Saeed | 4–8 weeks | Epic 3.6 |
| X10 | Lawyer: whether reselling voice under Relay's brand needs a DoT licence, plus DPDP processor terms | Saeed | 2–4 weeks | Epics 2.3, 4.2 |

---

## 3. Phase 0 — Stop the bleeding (W1–W2, about 3 ew)

### Epic 0.1 Channel honesty [A1, A2, A3, A8, A9]

**Backend (SMS-BE)**
- Add a `channel_unavailable` refusal in `internal/domain/messaging/gate.go`. It applies to any channel with no real connector configured, so the gate refuses WHATSAPP, EMAIL and VOICE live traffic. The sandbox stays allowed for test-mode API keys only.
- Drive this from a server capability list, `GET /v1/capabilities` → `{channels: {SMS: live, RCS: live, WHATSAPP: unavailable, …}, payments: {card: false}, sso: false}`. Add it to `openapi/control.json`.
- Stop generating placeholder DNS records in `internal/store/compliance.go:226-235`. Return "pending provider" until Epic 2.1.

**Frontend (SMS-UI)**
- `src/lib/registries/channels.ts`: read `/v1/capabilities` and show "Coming soon / Sandbox" badges.
- Disable unavailable channels in the campaign wizard and template editor (draft templates still allowed).
- Fix `twoWay: true` on Email (`channels.ts:146`).
- Hide card top-up, auto-recharge and the SSO card while the capability is false.

**Data clean-up**
- Find tenants charged for sandbox WhatsApp/Email/Voice sends since launch (ClickHouse `messages` where channel ∉ {SMS, RCS} and the connector was the sandbox).
- Refund those charges through the ledger with a reason code.

**Acceptance (live)**
- A WhatsApp campaign launched by a live-mode tenant is refused `channel_unavailable` at cost 0.
- The UI shows the badge.
- A test-mode key can still use the sandbox.
- The refund report has been run.

### Epic 0.2 Security P0s [A4, B1, B2, B3, B7, B8, B9, B10]

- **B1 operator roles.** Call `requireOperatorAdmin` on wallet credit, payment record/void, credit limit, rate default/override, tenant suspend and throttle (`internal/api/postpaid.go:210,624,779`, `operator.go:1203,1297`, suspend handlers). Add an internal test per route that a plain `operator` gets 403. The UI hides those actions for that role.
- **A4 voice verification.** Remove the code from the response (`internal/api/senders.go:254`). Until Epic 2.3 lands, verification by voice call returns 501/unavailable.
- **B2 `/metrics`.** Require an operator token or a loopback/allowlisted IP (`server.go:370-373`).
- **B3 SSRF.** Check addresses at dial time with a custom `net.Dialer.Control` that refuses private, loopback and link-local ranges after DNS resolution (`internal/webhook/webhook.go`).
- **B7 / B8 / B9 production flags.** Read the production values of `ENABLE_DEV_ENDPOINTS`, `SIGNUP_INVITE_CODE` and `OPERATOR_IP_ALLOWLIST` on relay-aws (Saeed runs this with `!`, since the classifier blocks remote reads). Make the boot step **refuse to start** with `ENABLE_DEV_ENDPOINTS=1` when `APP_ENV=production`. Add an invite-code field to signup, or remove the gate deliberately.
- **B10.** Enable RLS on `rate_overrides`, or document that only the operator pool reads it.
- **B6.** Cloudflare Turnstile on signup, login and forgot-password: UI widget plus a backend verification call.
- Rotate the Trustsignal webhook token printed on 11 Oct (from the checkpoint).

**Acceptance:** a plain operator gets 403 on all money routes live; `/metrics` returns 401 from the internet; a DNS-rebinding test webhook URL is refused.

### Epic 0.3 Billing correctness quick wins [A5, A6, D5, C-RCS5 decision]

- WhatsApp window: compute the 24-hour window from the last inbound WhatsApp message (`store/audience.go:64`). Fix the tier enum (1K, 2K, 10K, 100K, unlimited, per portfolio) in `openapi/control.json` and `api-types.ts`.
- Single API sends take a template category (or a `category` field) so they price correctly (`store/wallet.go:612-631`).
- Decision for Saeed: RCS pricing basis. Price RCS per message by type (basic / rich / conversation), not per 160-character segment.

---

## 4. Phase 1 — India-complete (W2–W10, about 20 ew)

### Epic 1.1 Promotional SMS flows [C-SMS1, C-SMS2] — highest commercial priority

- Add `DNDRegister` modes:
  - `operator` (default for IN): Relay trusts operator DLT scrubbing, does not refuse, and maps operator DND rejections to `dnd_blocked`.
  - `ncpr`: a real lookup through a vendor or preference API, if one is contracted later.
  - `deny`: today's behaviour.
- Config `DND_MODE`, wired in `cmd/control-api/main.go:347`.
- Templates get a TRAI commercial category (preference categories 1–7 and 0). The gate compares the category against the subscriber's preference when the register mode can answer. Migration plus contract field plus a template-editor select in the UI.
- Map operator error codes for DND, header and template mismatch to clear refusal codes in the DLR parser (`smpp.go:680`).
- **Acceptance (live):** one promotional SMS to a test handset inside 10:00–21:00 IST is delivered on the TEXTFI/test1111 DLT registration. A DND-registered number comes back with `dnd_blocked`, refunded.

### Epic 1.2 Resilient routing [C-SMS3, C-SMS4, C-SMS5]

- Second bind (X3) configured in the operator console. Routes gain **per-operator selection** (prefix series, later MNP) and **ordered failover**: on bind down or `NO_OPERATOR_BIND`, try the next route. Code in `service.go:913-932` and `smpp.go:806-817`.
- **LCR:** a route cost per carrier (operator console field). The router chooses by priority, then cost.
- HLR/MNP lookup behind an interface (vendor TBD), cached per number for 30 days. Optional per tenant.
- **DLT whitelist checks:** a per-entity URL, APK and callback-number whitelist object (registry plus UI). The gate refuses SMS whose links or numbers are not whitelisted (`india.go:15,106-118`). Replace the 5-host blocklist with whitelist matching.
- Header suffix (-P/-S/-T/-G) shown in previews and analytics; a 1600-series flag on voice senders later.
- **Acceptance:** kill bind A live and traffic continues on bind B within the reconnect window. A link that is not whitelisted is refused `url_not_whitelisted` before any carrier cost.

### Epic 1.3 Payments [D1, A8]

- Implement the Razorpay `billing.Gateway` (`internal/domain/billing/gateway.go`): orders, a checkout session (hosted, SAQ-A), webhook `payment.captured` → ledger credit, idempotency by payment id, refunds.
- Auto-recharge: a worker reads `auto_recharge_configs` when the balance crosses the threshold. It charges through a UPI AutoPay / card e-mandate token, then credits the wallet. Ledger entry plus email receipt.
- UI: top-up dialog using Razorpay Checkout; payment methods list from tokens; failure states.
- **Acceptance (live, small amount):** a ₹10 UPI top-up credits the wallet within seconds of the webhook. An auto-recharge fires once below the threshold and never double-charges (replay the webhook twice).

### Epic 1.4 GST invoicing [D2, D3]

- Tenant billing profile: legal name, GSTIN (validated checksum plus state code), billing address, place of supply.
- Invoice lines carry the SAC code. Tax split is CGST + SGST for intra-state and IGST for inter-state. Invoice numbering per financial year. PDF matches the Rule 46 fields.
- E-invoice (IRN plus QR) through a GSP API once Relay's turnover crosses ₹5 cr. Behind a feature flag, with the interface built now.
- Public **INR price page:** read-only `GET /v1/public/pricing` from the default rate card, plus a UI slab calculator (copy MSG91's pattern [07 §7.1]).
- **Acceptance:** the CA reviews one invoice each for an intra-state and an inter-state tenant. The price page renders live rates.

### Epic 1.5 RCS solid [C-RCS1, C-RCS2, C-RCS4, C-RCS5]

- Fallback before sending on Trustsignal. Either the vendor's `sms_fallback` (X8), or a capability cache built from DLR history: a number that returned `nonrcs` is marked non-RCS for 30 days, and the next send goes straight to SMS. `legs.go:211-215` then treats "unknown + known-nonrcs" correctly.
- **Carousel** (2–10 cards) plus full suggestions: reply, URL, dial, location, calendar, share. Covers the contract, `rcs_template.go` and the Trustsignal mapping; the UI carousel builder plus preview.
- RCS vendor connections and carrier launch admission move into the operator console (today they are CLI-only).
- RCS pricing per message type (from 0.3).
- **Acceptance:** a carousel template is approved on Trustsignal and delivered to a test phone. A known non-RCS number receives SMS directly, with no RCS attempt billed.

### Epic 1.6 Surface what's already built [A10]

UI screens for the nine backend features that have none:

| Feature | Screen |
|---|---|
| Chatbots | Keyword rules |
| Short links | Link analytics |
| Frequency caps | Settings |
| Drip | Campaign option |
| Test send | Button in the template editor |
| S3 report export | Settings → data |
| White-label branding | Settings → branding |
| CRM webhook formats | Webhook form |
| Error/latency analytics | Analytics tabs |

Move the 39 off-contract routes into `openapi/control.json` first, so the UI gets typed clients. Regenerate `internal/gen` (204 of 211 operations are generated today).

---

## 5. Phase 2 — Channel parity (W3–W16, parallel, about 25 ew)

### Epic 2.1 Email on Amazon SES ap-south-1 [C-EM1] — cheapest channel to make real

- `internal/connector/email_ses.go`, a `Connector` registered under `ByChannel["EMAIL"]`:
  - SES v2 `SendEmail` with configuration sets;
  - **SES tenants** for per-customer reputation isolation;
  - tags `tenant_id` and `message_id`.
- **Domain onboarding:** `CreateEmailIdentity` → real DKIM CNAMEs (Easy DKIM), custom MAIL FROM with an SPF record, DMARC guidance. A poller sets the sender to verified. This replaces `compliance.go:226-235`.
- **Events:** SES → SNS → `POST /v1/carrier-webhooks/email/ses/{token}` (SNS signature verification). Delivery, bounce, complaint, open and click map onto message states. Hard bounces and complaints go into suppressions automatically.
- **Bulk-sender rules (Gmail/Yahoo/Outlook):**
  - RFC 8058 `List-Unsubscribe` + `List-Unsubscribe-Post` headers;
  - a one-click unsubscribe endpoint;
  - spam-rate monitor with a tenant auto-pause above 0.3%.
- **Billing:** per email (not per segment), rate-card category `EMAIL`.
- **UI:** real DNS records with a copy button and a verification status; email preview; the "Clicked (Email)" analytics tile starts filling.
- **Acceptance (live):** a verified test domain sends a campaign to 3 inboxes (Gmail, Outlook, Yahoo); DKIM/SPF/DMARC show pass in the headers; one-click unsubscribe works from Gmail; a forced bounce (`bounce@simulator.amazonses.com`) suppresses the address.
- **Later [C-EM2]:** MJML builder, inbound parse (check SES inbound availability in ap-south-1 first), dedicated IPs plus warm-up.

### Epic 2.2 WhatsApp on Meta Cloud API as Tech Provider [C-WA1, C-WA3] — biggest revenue channel

- **Embedded Signup:** UI launches Meta's popup → callback code → backend exchanges it for a business token. The backend stores WABA id, phone number id and token (sealed with `secrets.Box`), subscribes the app to the WABA webhooks, and registers the number. The customer pays Meta directly, so there is no Relay markup on Meta fees and no credit-line risk.
- **`internal/connector/whatsapp_meta.go`:**
  - Graph API `/{phone-number-id}/messages`: text, template, media, interactive buttons and lists.
  - Media upload.
  - Idempotency.
- **Webhooks:**
  - `POST /v1/carrier-webhooks/whatsapp/meta`, with an `X-Hub-Signature-256` check and a GET challenge;
  - statuses (sent, delivered, read, failed with error codes) and inbound messages go to the inbox;
  - template status updates, quality rating and messaging-limit changes go to the sender.
- **Templates:** create, submit and sync with Meta, covering categories (marketing, utility, authentication), components (header media, body variables, buttons, OTP copy-code) and rejection reasons. Map them to Relay template versions.
- **Customer service window:** derived from the last inbound message (the A5 fix); free-form only inside the window, otherwise a template is required.
- **Pricing:**
  - per-message rates by category and country, loaded from Meta's rate card;
  - Relay platform fee per message (a pricing decision);
  - INR billing (Meta deadline 31 Dec 2026);
  - service-message charging from 1 Oct 2026 (verify on Meta's own page).
- **Inbox, chatbots, journeys and fallback:** WhatsApp as a leg (WA→SMS fallback on `failed`), chatbot keyword replies, journeys step.
- **Interim:** if a customer needs WhatsApp before Meta approves (X1), use a 360dialog connector behind the same interface. Do not use Gupshup, which competes with Relay [04 §9].
- **Acceptance (live):**
  - a test business completes Embedded Signup;
  - an approved utility template is delivered and read on a handset (`read` status in Relay);
  - an inbound reply opens a 24-hour window and a free-form reply goes out;
  - a template rejection reason appears in the UI.
- **Wave 2 [C-WA2]:** carousel, Flows, India payments, Marketing Messages Lite API, calling. Then Instagram and Messenger on the same Meta app.

### Epic 2.3 Voice on Exotel (or Plivo India) [C-VO1, A4]

Relay must not interconnect with the PSTN itself [04 §6].

- `internal/connector/voice_exotel.go`:
  - outbound call with TTS (Indic voices) for **voice OTP** and notifications;
  - call status webhooks;
  - per-pulse billing (15 s / 30 s / 60 s, configurable);
  - DTMF capture for confirmations.
- **Verify:** add voice as a channel, and as the fallback after SMS failure.
- **Caller-ID verification for real:** place the call and read the code out (replaces A4).
- **Compliance:** 140-series numbers for promotional calls; 1600 for BFSI service calls; AI-caller declaration per the TCCCPR 3rd amendment (Sep 2026); recording consent prompt.
- **Acceptance (live):** a voice OTP call to a test handset reads the code. Status and duration are billed by pulse. A Verify check with `channel=voice` succeeds.
- **Later [C-VO2]:** IVR builder, number masking, outbound dialing (OBD) campaigns, click-to-call.

### Epic 2.4 Extras [C-X1]
- Telegram Gateway as a Verify channel for international OTP. Size: S.
- Apple Messages for Business: only on customer demand.

---

## 6. Phase 3 — Sell to mid-market and resellers (W10–W20, about 20 ew)

| Epic | Scope | Acceptance |
|---|---|---|
| **3.1 Developer experience** [E8] | Contract-complete OpenAPI. SDKs (Node, Python, Go, PHP, Java) generated from it, published to npm, PyPI and pkg.go.dev. Public API reference (Redoc/Scalar) plus quickstarts per channel. Postman collection. `llms.txt` plus `.md` docs. A hosted **MCP server** exposing send, status and templates (copy Bird [02 §17.3]). | `npm i @relay/sdk` sends an SMS in 5 lines against live |
| **3.2 Sub-accounts and white-label reseller** [E1, W4] | `parent_tenant_id`; parent wallet funds children (an atomic ledger transfer across tenants under RLS through the operator pool); reseller margin on child rates; white-label UI (branding API already built: custom domain via DNS-TXT, logo, colours, email "from"); reseller console = operator console scoped to the reseller's tenants. | A reseller on its own domain creates a child tenant, funds it, and the child sends an SMS billed at the reseller's price |
| **3.3 Journeys v2** [E2, E3] | Branching on delivery, read, click or reply; conditions on contact fields; event and API triggers (`POST /v1/journeys/{id}/events`); wait-for-reply; A/B split; channel steps for all live channels; dynamic segments; template versioning; campaign PATCH. | A journey "WhatsApp → if not read in 2h → SMS → if clicked → tag" runs live |
| **3.4 Verify v2 and fraud** [A7, E5, E6] | A real fraud engine: country allow-list per service, velocity per prefix, OTP-conversion tracking, SMS-pumping (AIT) detection (write the `fraud_flag` columns), automatic blocking. Multi-channel Verify (SMS → WhatsApp → voice → email → Telegram). HLR/line-type lookup. Price Verify per *successful* verification (Twilio model) or free with channel billing (Bird model). | A simulated pumping burst to premium prefixes is blocked and flagged; conversion is reported per channel |
| **3.5 Trust surface** [G1, G2, B14] | Public status page (hosted Statuspage-style, fed by health checks and incidents); published support SLAs; written suspension reasons in the UI and email; trial credits plus mobile OTP at signup; CERT-In incident runbook and 180-day log retention in India; identifier-first login. | Status page public; a suspension shows its reason; runbook in `docs/` |
| **3.6 RCS direct** [C-RCS3] | Jio direct (iPhone RCS) and Vi via Dotgo, live-proven; RCS routing per operator. | A delivery to an iPhone on Jio through Relay is proven |
| **3.7 Inbox v2** [E10] | Assignment, notes, canned replies, SLA timers, WhatsApp and email threads, chatbot handoff. | Two agents work one shared inbox |
| **3.8 Migration kit** [G3] | Import templates, DLT IDs, contacts and suppressions from MSG91, Gupshup, Fast2SMS and Textlocal exports. | An MSG91 template CSV imports with DLT IDs mapped |

---

## 7. Phase 4 — Enterprise and international (W16–W30+)

| Epic | Scope |
|---|---|
| **4.1 SSO** [E4, A9] | SAML 2.0 and OIDC login (crewjam/saml, coreos/go-oidc), JIT provisioning, SCIM, enforced-SSO setting. The existing settings form becomes real. |
| **4.2 Compliance engine v2** [F1–F6] | Consent provenance model (DPDP main duties by May 2027); per-regime promotional windows plus holiday calendar (remove the `india.go:152` branch); a suppression register per regime; STOP/HELP/START per regime in local languages; `ValidateBody` hook; compliance audit log for every gate decision; UI reads the backend regime registry (fixes the drift). |
| **4.3 Infrastructure and HA** [B11, B12, B13] | CI runs the datastore suite (ephemeral Postgres, ClickHouse and Redis services in GitHub Actions, or remote-test on every PR). Worker leader election so more than one API instance can run. Load test on AWS with a published throughput number. Second AZ or warm standby; point-in-time recovery for Postgres. SMPP over TLS or VPN per operator. Tracing (OpenTelemetry). |
| **4.4 AI layer** [E7] | AI reply suggestions in the inbox, AI template writer that is aware of DLT and Meta template rules, an LLM chatbot on WhatsApp/RCS with human handoff. Note that WhatsApp banned general-purpose AI chatbots from 15 Jan 2026, so keep it business-scoped [05 §18]. |
| **4.5 Certifications** [F7] | Start SOC 2 Type II (Vanta/Drata-style evidence collection; 3–12 month window). ISO 27001:2022. GDPR DPA, SCCs and sub-processor list. DPDP processor terms. Pen test. |
| **4.6 UAE → KSA** [F9] | Regime: sender registration, AD- prefix, promotional hours, Arabic templates, AED billing (the timezone already exists). One local route. Then KSA (CST sender ID registration). Market-entry order from [05 §21]: UAE/KSA → SG/MY → UK → US through a 10DLC partner → ID/PH → BR/MX. |
| **4.7 Data plane per region** [F8] | Decide the architecture before the first non-India tenant: per-region deploy versus a single control plane with regional data stores. |

---

## 8. Dependencies

```
Phase 0 (honesty + P0 security) ──► everything
X4 VIDEOCON answer ──► 1.1 Promotional SMS
X3 second bind ──► 1.2 Routing failover
X5 Razorpay KYC ──► 1.3 Payments ──► 3.2 Reseller wallets
X6 GSTIN/CA ──► 1.4 GST invoices
X2 SES production ──► 2.1 Email
X1 Meta Tech Provider ──► 2.2 WhatsApp ──► 3.3 Journeys v2 (WA steps), 3.7 Inbox v2, 4.4 AI
X7 Exotel KYC ──► 2.3 Voice ──► 3.4 Verify v2 (voice fallback)
1.6 Contract complete ──► 3.1 SDKs
4.3 CI with datastores ── should start in Phase 1; it protects every later epic
```

---

## 9. KPIs to track (the definition of "winning")

| KPI | Today | Target by end of Phase 3 |
|---|---|---|
| Live channels (delivering to real handsets or inboxes) | 2 (SMS, RCS) | 5 |
| Live SMS binds | 1 | ≥ 3, including 1 aggregator |
| Indian promotional SMS deliverable | 0% (all refused) | ≥ 95% of DLT-valid submissions |
| Time from signup to first real message | not measured | < 15 minutes for transactional SMS with existing DLT IDs |
| Charges for undelivered messages | sandbox charges exist (A1) | 0 |
| Payment methods | manual bank credit | UPI, card, netbanking, auto-recharge |
| GST-compliant invoices | 0% | 100% |
| Backend tests running in the CI gate | ~60% (369 of 905 skip) | 100% |
| Published throughput on AWS | none | Measured and published |
| Public status page uptime history | none | Live |

---

## 10. Decisions Saeed needs to make

1. **Billing basis:** keep pay-on-delivery for every tenant, or charge on submit for some routes/tenants to match operator billing (D4)?
2. **RCS pricing basis:** per message type (basic/rich/conversation) instead of per 160-character segment (C-RCS5)?
3. **WhatsApp commercial model:** customer pays Meta directly with a Relay platform fee (recommended), or Relay resells Meta fees with a markup?
4. **Verify pricing:** per successful verification (Twilio) or free with channel billing (Bird)?
5. **Refund policy** for sandbox-charged WhatsApp/Email/Voice messages found in Epic 0.1.
6. **Second SMS route vendor** (X3) and **voice upstream** (Exotel vs Plivo India).
7. **First international market:** UAE (recommended) or another.
8. **Whether to keep the invite-code signup gate** (B8).

---

## 11. Risks

| Risk | Mitigation |
|---|---|
| Meta Tech Provider review slips | Start X1 now; 360dialog interim connector behind the same interface |
| Airtel IQ Gateway undercuts on price | Compete on neutrality, transparency and reseller depth, not on route price |
| Regulatory change (TCCCPR amendments, DPDP Rules, WhatsApp pricing) | The regime engine is the asset. Keep `05-compliance-by-region.md` as a living doc and review it quarterly. |
| Single box outage | Phase 4.3 HA. Until then, document RTO/RPO honestly on the status page. |
| Margin squeeze on routes | Platform fees, seats, journeys, Verify and AI: charge for software |
| UI and backend drift | Every new route goes into `openapi/control.json` first; the UI reads `/v1/capabilities` and the regime registry instead of keeping copies |
