# 08 — Gap analysis: Relay vs the market

Status: 16 Oct 2026. This file pulls together 01–07. Every row traces to one of three places:

- the code, cited as `file:line`;
- a research file, cited as `[0n §x]`;
- a command run in this research session, cited as `[run]`.

Competitor facts are only as strong as the source tags in 02/03/07 (`[V]` verified, `[U]` unverified). Do not quote a `[U]` fact externally without checking it.

Priority key:

- **P0**: stops wrong charges, misrepresentation, or a security hole. Fix before any new customer.
- **P1**: blocks selling in India.
- **P2**: needed for mid-market and enterprise deals.
- **P3**: needed for international expansion and parity.

Effort is in engineer-weeks (ew), assuming one engineer who knows the code:

| Size | Engineer-weeks |
|---|---|
| S | ≤ 1 |
| M | 1–3 |
| L | 3–6 |
| XL | > 6 |

---

## 1. Where Relay stands, in one paragraph

Relay is a credible **India-first, compliance-first SMS + RCS orchestration platform**, and its engineering is better than its commercial surface. Its foundations are strong:

- campaigns, ledger, RLS and auth all score maturity 4 in the scorecard [01 §12];
- a real DLT gate;
- live SMPP;
- live Trustsignal RCS with fallback after a failed send;
- an operator console;
- a 211-operation API.

Commercially, though, it is **not yet sellable as the five-channel product the UI presents**:

- **Three of the five channels are not real.** WhatsApp, Email and Voice sends go to a sandbox that marks them delivered **and charges for them** [01 §0.2].
- **Indian promotional SMS cannot go out.** Every one is refused (`internal/sending/dnd.go:38-43`).
- **A customer cannot pay online and cannot get a GST invoice** [01 §8].
- **Operator money actions are not role-gated** [01 §10.2.1].

Meanwhile:

- Airtel launched a telco-owned version of Relay's pitch on 5 Oct 2026 (IQ Gateway) [03 §0.1].
- Global players are pivoting to AI agents on top of messaging [02 §0.2].
- The SMB layer competes on published INR prices with UPI checkout [03 §0.4].

The window is the **neutral, multi-operator, DLT-gated orchestration layer** for Indian mid-market and enterprise senders, plus engagement platforms that need a delivery pipe. That window needs WhatsApp live, promotional SMS flowing, and proper money handling.

---

## 2. Where Relay already wins (protect these)

| # | Strength | Evidence | Who lacks it |
|---|---|---|---|
| W1 | **DLT-native India sending.** Entity, header and template registry; template match; entity-not-registered gate; DLT TLVs on SMPP. | `internal/domain/compliance/india.go`, `gate.go:178-309`, `smpp.go:23-31` [01 §7] | Twilio and Plivo make customers self-register and wait about 10 days, or route over ILDO at $0.065–0.083 (Plivo lists domestic India as "Not Supported") [02 §0.4] |
| W2 | **Two-stage RCS→SMS fallback.** One fallback leg is planned before sending; another fires after an `undelivered` report. | `legs.go:290`, `fallback_after_failure.go:29`; after-failure proven live via a simulated webhook on 8 Oct | Bird RCS is private beta; most Indian SMB tools have no RCS fallback [02 §5][03 §5] |
| W3 | **Pay-on-delivery ledger.** The charge is held at send and refunded on failure or expiry. | `state.go:84-89`, `reconciler.go:17` | MSG91 charges for failed messages; Twilio charges a failed-message fee [03][07 §5] |
| W4 | **Operator/reseller console.** Tenants, SMPP connections, routes, rate overrides, margin, abuse queue, live campaigns, audit. | [01 §9] (19 operator pages) | Gupshup partners get WhatsApp only; MSG91 reselling is SMS-only above 5 lakh SMS a month [03 §2] |
| W5 | **Per-tenant RLS on 55 tables, argon2id, TOTP, hashed keys and tokens, encrypted secrets** | [01 §10.1] | Not differentiating on its own, but it passes security questionnaires |
| W6 | **CRM-native webhooks** in WebEngage, MoEngage and CleverTap formats | `internal/webhook/integrations.go:14-16` | Those engagement platforms buy CPaaS; Relay already speaks their format [03 §6] |
| W7 | **Lightweight, clean frontend.** About 450–540 KB of JS per page, no third-party scripts, strict CSP and HSTS. | [07 §8] | Bird's login page loads 19.5 MB; MSG91 sign-in is 2.4 MB of jQuery plus three tag managers [07 §7.9] |
| W9 | **DLT as a managed registry.** Plivo, Vonage and Infobip only pass DLT IDs on each send, and no SDK examined exposes registry management (bounded search). Also level or ahead on campaigns plus automation in one API, webhook test/resend, and RCS agent lifecycle with capability check. | [06 §6] | Plivo, Vonage, Infobip |
| W10 | **Open lane for India SDKs.** MSG91's SDK is official but thin (OTP, flow, campaigns); Gupshup and Exotel have only unofficial packages; Kaleyra has no messaging SDK on npm. | [06 TL;DR] | MSG91, Gupshup, Exotel, Kaleyra |
| W8 | **Five-channel data model already in the contract.** Templates, senders, consent and caps all carry a channel. | `enums.go:33`; `SMS-UI/src/lib/registries/channels.ts` | Adding a real connector is cheaper here than for an SMS-only shop |

---

## 3. Gap register

### A. Integrity: what the product claims vs what it does (all P0)

| ID | Gap | Evidence | Impact | Effort |
|---|---|---|---|---|
| A1 | Sandbox sends for WhatsApp, Email and Voice are charged as delivered | `smpp.go:802`, `sandbox.go:18-27`, `state.go:84-89`; no channel gate in `gate.go:168-258` [01 §0.2] | Customers pay for messages nobody received. This is a billing-integrity and legal exposure. | S |
| A2 | The UI presents WhatsApp, Email and Voice as live, and Email as two-way | `SMS-UI/src/lib/registries/channels.ts:114-167`, `:146` [04 §0] | Misrepresentation; support load | S |
| A3 | The email SPF/DKIM records shown to customers are placeholders: `mail.relay-platform.example` and a truncated key | `internal/store/compliance.go:233-235` [run] | Customers publish useless DNS records | S (hide) / L (real, see C-EMAIL) |
| A4 | Voice caller-ID "verification" returns the code in the HTTP response | `internal/api/senders.go:254` [01 §10.2.2] | Proves nothing about ownership | S |
| A5 | The WhatsApp 24-hour window is computed from consent time, not from the customer's last inbound message | `internal/store/audience.go:64` [run] | Wrong numbers in the UI; would break Meta policy once WhatsApp is live | S |
| A6 | The WhatsApp tier enum is stale. It has 1K and is missing 2K and unlimited; limits have been per business portfolio since Oct 2025. | `openapi/control.json:14176-14180` [04 §4] | Wrong guidance shown to users | S |
| A7 | Verify fraud analytics never fill, because the send paths hard-code `FraudFlag: "none"` | `internal/sending/batch.go:257`, `:334` [run] | The fraud panel is decorative; a marketing claim with no substance | M (real engine, see E5) |
| A8 | Card top-up and auto-recharge are offered in the UI. Card top-up always returns 422; auto-recharge never runs. | `internal/api/wallet.go:139-143`, `:319-360` [01 §0.6] | A broken promise at the first payment | S (hide) / M (real, see D1) |
| A9 | SSO is configurable in settings, but login never reads it | [01 §10.2.11] | Found out at RFP stage | S (hide) / L (real) |
| A10 | Nine backend features have no UI: chatbots, links, frequency caps, drip, test send, S3 export, branding, CRM formats, error/latency analytics | [01 §0.7] | Paid-for work customers cannot use | M (spread across epics) |

### B. Security and operations (P0/P1)

| ID | Gap | Evidence | Priority | Effort |
|---|---|---|---|---|
| B1 | Operator `admin` role is never enforced. Any operator can credit wallets, record payments, set credit limits and change rates. | `operator.go:76-85` has no callers; `postpaid.go:210/624/779`, `operator.go:1203/1297` | P0 | S |
| B2 | `/metrics` is public | `server.go:370-373` | P0 | S |
| B3 | Webhook SSRF via DNS: only literal private IPs are refused | `webhook/webhook.go:129-152` | P0 | S |
| B4 | Rate and login limits fail open when Redis is down | `send_ratelimit.go:41-49`, `login_limit.go:11-13` | P1 | S |
| B5 | Trustsignal API key sent in the query string; carrier webhook secret in the URL path | `rcs_trustsignal.go:131`, `carrier_webhooks.go:1-14` | P1 | S (vendor-bound) |
| B6 | No bot protection (captcha/Turnstile) on signup or login | [07 §8] | P1 | S |
| B7 | UI proxies 18 `/api/dev/*` routes with no environment guard; safety rests on the backend `ENABLE_DEV_ENDPOINTS` flag (default off). The live value is unverified. | `SMS-UI/src/app/api/dev/*/route.ts`, `internal/api/dev.go:25` [run] | P0 to confirm | S |
| B8 | `SIGNUP_INVITE_CODE` gate exists, but the UI has no field for it. Live signup is either open or unusable; unverified. | `config.go:229`, `auth.go:111-114` [07] | P0 to confirm | S |
| B9 | `OPERATOR_IP_ALLOWLIST` is empty in the template, so the console is open to the internet unless it is set (live value unverified) | `main.go:203-207` | P1 | S |
| B10 | `rate_overrides` has no RLS | `store/wallet.go:580-596` | P1 | S |
| B11 | SMPP binds run over plain TCP | `smpp.go:303-306` | P2 | M (TLS or VPN per operator) |
| B12 | The CI deploy gate runs `go test` with no datastores; at least 369 of 905 tests skip | `.github/workflows/deploy.yml:44-52` [01 §0.10] | P1 | M |
| B13 | Single 2-vCPU box, single process, workers assume one instance, no HA; throughput never measured on AWS | [01 §1, §12] | P2 | L |
| B14 | No public status page; no CERT-In 6-hour incident-reporting runbook; 180-day in-India log retention not shown | [05 §1, §19][07 §7.7] | P1 | M |
| B15 | 2 vitest failures in mock audit-log paging; all 86 Playwright specs run against mocks only | [run] `src/mocks/handlers.test.ts` | P2 | S + M |

### C. Channels

| ID | Channel | Gap | Evidence | Priority | Effort |
|---|---|---|---|---|---|
| C-SMS1 | SMS | **Every Indian promotional SMS is refused** (no DND register; `NoDNDRegister` wired) | `main.go:347`, `dnd.go:38-43` | **P1, top** | S ("operator scrubs" mode) → M (real NCPR/preference) |
| C-SMS2 | SMS | Templates carry no commercial category, so any partial DND block counts as a full block | `dnd.go:25-27` | P1 | M |
| C-SMS3 | SMS | One live operator (VIDEOCON); no failover aggregator; routing picks a route per country, not per operator; no MNP/HLR; no least-cost routing (LCR) | `service.go:913-932` [04 §2] | P1 | M + M |
| C-SMS4 | SMS | No check of DLT URL, APK or callback-number whitelists (enforced by operators since Oct 2024); only a 5-host shortener blocklist | `india.go:15` [05 §1] | P1 | M |
| C-SMS5 | SMS | No awareness of header suffixes (-P/-S/-T/-G), 1600-series or the Digital Consent Acquisition (DCA) | [03 §1] unchecked | P2 | S–M |
| C-SMS6 | SMS | No Unicode/Indic segment tests beyond the base path; no flash SMS | [04 §2] | P3 | S |
| C-RCS1 | RCS | No carousel; text or single card only; suggestions limited to reply, URL and dial (no location, calendar or share) | `rcs_template.go:37-39`, `control.json:14682` | P2 | M |
| C-RCS2 | RCS | Live vendor has no capability check, so pre-send fallback never fires on Trustsignal | `rcs_trustsignal.go:62-68`, `legs.go:211-215` | P1 | M (capability source or vendor `sms_fallback`) |
| C-RCS3 | RCS | Jio, Airtel, Vi and Google connectors coded but never sent live; Jio is the only Indian iPhone RCS path (from 1 Sep 2026) | [01 §6][03 §1] | P2 | M each |
| C-RCS4 | RCS | "Launching" an agent only changes a DB row; RCS vendor connections are managed in the CLI only | `rcs_agents.go:536` [01 §9] | P2 | M |
| C-RCS5 | RCS | Billing: RCS text over 160 characters bills 2 segments (70 vs 35 list); every channel is priced per SMS segment | `store/wallet.go:612-631` [01 §8] | P1 | M |
| C-WA1 | WhatsApp | **No Meta integration at all**: no embedded signup, WABA/number management, sending, webhooks, template sync or media | [04 §4] | **P1** | XL (L engineering + Meta onboarding lead time) |
| C-WA2 | WhatsApp | Second wave: carousel, Flows, payments (India), calling, Marketing Messages Lite API, catalog | [04 §4] | P2 | L each |
| C-WA3 | WhatsApp | Pricing model: per-message since Jul 2025; service messages charged from 1 Oct 2026 per BSPs (Meta page not read); INR billing by 31 Dec 2026; max-price bidding in open beta | [02 §0.5][03 §1] | P1 | M (rate model) |
| C-EM1 | Email | **No customer email sending**: no SES/ESP, real DKIM/SPF/DMARC, bounce or complaint handling, RFC 8058 one-click unsubscribe, suppression or tracking | [04 §5] | P1 | L |
| C-EM2 | Email | Builder (MJML), inbound parse, dedicated IP and warm-up, deliverability dashboards | [04 §5] | P2 | L |
| C-VO1 | Voice | **No telephony provider**: no TTS/voice OTP, call status, pulse billing or number provisioning; 140/1600 series and AI-caller declaration rules (TCCCPR 3rd amendment, Sep 2026) | [04 §6][05 §1] | P2 | L |
| C-VO2 | Voice | IVR, number masking, outbound dialing (OBD), click-to-call, recording consent | [04 §6] | P3 | L |
| C-X1 | Extra | Telegram Gateway for international OTP; Instagram/Messenger after WhatsApp; Apple Messages for Business (MSG91 sells it, seen in the REA capture) | [04 §7] | P3 | S / L |

### D. Money (P1)

| ID | Gap | Evidence | Effort |
|---|---|---|---|
| D1 | No payment gateway: card top-up returns 422, auto-recharge never runs. India needs Razorpay/Cashfree (UPI, cards, netbanking, mandates for auto-recharge). | `wallet.go:139-143` | M |
| D2 | No GST invoice: no GSTIN, legal name, place of supply, HSN/SAC, CGST/SGST/IGST split or e-invoice IRN (mandatory for B2B above ₹5 cr turnover) | grep finds nothing; flat 18% at `store/wallet.go:686` | M |
| D3 | No published INR price list or slab calculator; every SMB rival publishes one | [03 §0.4][07 §7.1] | S |
| D4 | Pay-on-delivery vs operators charging on submit is a margin risk; needs a per-tenant policy (refund on DLR failure, or charge on submit with transparency) | [01 §0.5] | S (policy) + S |
| D5 | Single API sends pass no category, so they are billed at the cheapest category | `store/wallet.go:612-631` | S |
| D6 | Margin: Indian aggregator gross margins run about 21–27%; Relay's margin must come from software (platform fee, per-seat, journeys), not routes | [03 §0.2] | Product decision |

### E. Platform and product parity (P2)

| ID | Gap | Who has it | Effort |
|---|---|---|---|
| E1 | **Sub-accounts / reseller hierarchy** (parent wallet, child tenants, white-label UI on top of the existing branding API) | Twilio subaccounts, Infobip, MSG91 reseller [02 §15] | L |
| E2 | Journeys: branching, conditions, event/API triggers, wait-for-reply, A/B | Netcore, MoEngage, Infobip Moments, Twilio Studio [02][03] | L |
| E3 | Audience segments (dynamic filters); template versioning; campaign PATCH | PRD:103/106/153 [UI audit] | M |
| E4 | SSO (SAML/OIDC) login and SCIM | Every enterprise CPaaS [02 §15] | L |
| E5 | Verify product: real fraud engine (SMS pumping/AIT), multi-channel Verify (WhatsApp, voice, email, Telegram, silent network auth) | Twilio Verify, Bird (free Verify), Airtel IQ (silent auth) [02][03] | L |
| E6 | Number intelligence: HLR, MNP, line type. All 7 global SMS vendors have it; Twilio Lookups v2 adds SIM-swap, SMS-pumping risk score and reassigned-number checks; Vonage NI v2 has fraud_score and sim_swap [06 §3] | Twilio, Telnyx, Vonage, Sinch | M (vendor) |
| E11 | Rich-content API: Twilio Content API has 11 types (carousel, card, list picker, catalog, flows); Relay has one rich card [06 §3] | Twilio | M–L |
| E12 | Verify channel breadth: Twilio sms/call/email/whatsapp/silent-network-auth; Vonage adds rcs and whatsapp_interactive; Sinch adds flashcall. Relay has the channel enum but only SMS is live [06 §3] | Twilio, Vonage, Sinch | L |
| E13 | Spend limits and usage triggers (alerts when spend crosses a threshold) [06 §5] | Twilio, Vonage | S–M |
| E14 | Webhook-signature helper libraries inside SDKs (Relay signs with HMAC but ships no verifier) [06 §5] | All global vendors | S (part of 3.1) |
| E7 | AI layer: AI chatbot on inbox/WhatsApp, AI template writing, agent handoff | Twilio, Infobip AgentOS, Sinch, CM.com HALO [02 §0.2] | L |
| E8 | Developer experience: SDKs (Node, Python, Go, PHP, Java) generated from the contract, public API reference, Postman collection, `llms.txt`, markdown docs, MCP server, CLI. Also move the 39 off-contract routes into `openapi.json`. | Bird, Twilio, Telnyx [02 §17.3] | M |
| E9 | Live-backend e2e suite; analytics: cost per conversion, journey funnel | [UI audit] | M |
| E10 | Inbox: assignment, SLA, canned replies, notes, WhatsApp/email threads | Gupshup, Interakt, WATI, Infobip Conversations | L |

### F. Compliance and international (P3, except where it is India)

| ID | Gap | Evidence | Priority | Effort |
|---|---|---|---|---|
| F1 | Consent provenance (basis, source, evidence, expiry, notice version); today it is only a timestamp. Required by DPDP (main duties by May 2027), CASL, PECR, GDPR, TCPA and POPIA. | `store/audience.go:265` [05 §20.3] | P1 (DPDP) | M |
| F2 | Quiet hours are an `if country != "IN"` branch; move to per-regime windows plus a holiday calendar | `india.go:152`, `server.go:262-269` | P2 | S–M |
| F3 | Suppression register per regime (NCPR, REPEP, DNC, DND), with an audit row per check | [05 §20.2] | P2 | M |
| F4 | STOP keywords are English only; no HELP or START (US carriers require them) | `store/support.go:505-507` | P2 | S |
| F5 | Registry adapter (submit, poll, webhook) so DLT, TCR, toll-free verification, SSIR and ACMA become integrations, not self-attested forms | [05 §20.7] | P3 | L |
| F6 | UI/backend regime drift: EIN field and US use cases differ; GB labelled "MMA code" | `regimes.ts:350-386,409` vs `us.go:33-56` | P2 | S |
| F7 | DPDP processor pack, GDPR DPA and SCCs, sub-processor list; SOC 2 Type II and ISO 27001:2022 (none held) | [05 §19] | P2 | XL (process, not code) |
| F8 | Regional data planes (India stays in India; EU needs SCCs or an EU region); decide before the first non-India tenant | [05 §20.8] | P3 | XL |
| F9 | UAE, then KSA regimes (sender registration, AD- prefix, hours, Arabic); GB/AE are stubs today | `stubs.go:23` [05 §21] | P3 | M each |

### G. Go-to-market and trust (P1/P2)

| ID | Gap | Evidence |
|---|---|---|
| G1 | No public status page, published support SLAs, or written suspension reasons. The category's weakest point: Trustpilot 1.1–2.1 across Twilio, Plivo, Sinch, Bird, Infobip and Vonage. | [02 §0.7] |
| G2 | No trial-credit or no-card messaging; no mobile+OTP signup for India; no identifier-first login | [07 §7] |
| G3 | No migration kit to import templates and DLT IDs from MSG91, Textlocal (shut down 2025), Gupshup or Fast2SMS | [03 §0.3] |
| G4 | No positioning against Airtel IQ Gateway: neutral multi-operator routing, transparency, no lock-in to one telco | [03 §6] |

---

## 4. Threat summary

| Threat | Why it matters | Relay's answer |
|---|---|---|
| **Airtel IQ Gateway** (5 Oct 2026) | Telco-owned SMS + WhatsApp + RCS + voice, native DLT, failover to *other* vendors, silent auth, fraud scoring, on-prem option | Neutrality (Airtel will not route Jio-first), multi-operator binds, transparent per-message cost, faster self-serve, reseller depth. Needs C-SMS3 and C-WA1 first. |
| **Jio ecosystem** (Haptik, Interakt, exclusive iPhone RCS) | Controls the iPhone RCS path and a WhatsApp SMB tool | Jio direct RCS connector (C-RCS3), so Relay *uses* Jio rather than competing with it |
| **Price compression** | US SMS at $0.0035–0.0083; India SMS at ₹0.11–0.25 | Charge for software (platform fee, seats, journeys, Verify), not for routes |
| **AI-agent pivot** | Twilio, Infobip, Sinch, CM.com, Plivo, Telnyx all shipped AI agent layers in 2026 | E7, after channels are real. Do not lead with AI while channels are sandbox. |
| **Rules keep changing** | TCCCPR amendments, DPDP Rules, WhatsApp pricing changes, Gmail/Yahoo/Outlook bulk-sender rules | Relay's regime engine is the asset. Keep it current (F1–F5). |

---

## 5. What "done" looks like for the positioning claim

Relay can honestly say **"India's neutral, compliance-first omnichannel CPaaS"** when all of the following hold:

1. SMS promotional and transactional traffic flows across at least two live operator or aggregator binds, with failover.
2. RCS is live on Trustsignal plus at least one direct operator, with a carousel, and fallback that works before sending.
3. WhatsApp is live through the Meta Cloud API as a Tech Provider.
4. Email is live on SES ap-south-1 with real DNS authentication, bounce handling and one-click unsubscribe.
5. Voice OTP and TTS are live through Exotel or Plivo.
6. Customers can pay by UPI or card, auto-recharge works, and every invoice is GST-compliant.
7. There is a public price page, a status page, SDKs and API reference, and a sub-account / white-label reseller tier.
8. The operator role is enforced, the P0 security items are closed, and the e2e suite runs against a real backend.

The build plan in [09-build-plan.md](09-build-plan.md) sequences exactly this.
