# Relay market research, gap analysis and build plan

16 Oct 2026. These files were built from three sources:

- a code read of SMS-BE and SMS-UI;
- both test suites run on 15 Oct;
- web research across about 50 competitors, plus REA (reverse-engineering) captures of competitor SDKs, public apps and Relay's own live UI.

## Read in this order

| File | What it answers |
|---|---|
| [08-gap-analysis.md](08-gap-analysis.md) | **Start here.** Where Relay wins, every gap with its evidence and priority, the threats, and what "done" means |
| [09-build-plan.md](09-build-plan.md) | **What to build.** Phases 0–4 with epics, files, acceptance criteria, effort, external lead times to start now, KPIs, and the decisions needed |
| [01-relay-as-built.md](01-relay-as-built.md) | What Relay actually does, from the code (file:line on every claim). Covers architecture, data model, 211-operation API, message lifecycle, channels, connectors, compliance, billing, consoles, security, quality, and a 32-area scorecard |
| [02-competitors-global.md](02-competitors-global.md) | Teardowns of 14 global CPaaS companies (Twilio, Vonage, Sinch, Infobip, Bird, Plivo, Telnyx, Bandwidth, CM.com, 8x8, Clickatell, Esendex, Brevo, Kaleyra), a 67-row feature matrix and a pricing table |
| [03-competitors-regional.md](03-competitors-regional.md) | Indian telecom market structure (TRAI volumes, DLT, TCCCPR amendments, RCS and WhatsApp pricing), about 20 Indian players, APAC/Middle East, EMEA/LatAm, and where Relay wins or loses by region |
| [04-channel-requirements.md](04-channel-requirements.md) | A complete feature checklist per channel (SMS, RCS, WhatsApp, Email, Voice, plus extra channels) mapped to Relay's code, with effort and a vendor pick per channel |
| [05-compliance-by-region.md](05-compliance-by-region.md) | Rules in 17 regions, platform policies, certifications, cross-cutting platform work, and the market-entry order |
| [06-rea-api-teardown.md](06-rea-api-teardown.md) | REA reverse engineering of competitor SDK packages and OpenAPI specs, with an endpoint-capability matrix against Relay's contract |
| [07-rea-web-teardown.md](07-rea-web-teardown.md) | REA captures of competitor pricing, signup and console pages, plus Relay's live UI: stack, pricing pulled out of JS, signup friction, UX to copy |

Counts from 06 (public OpenAPI specs, fetched 16 Oct): Twilio 1,570 operations, Telnyx 1,422, Resend 139, Vonage 88 (a partial sample), Bandwidth 55. Relay has 211, of which about 141 are customer-facing. Plivo, Sinch, Infobip, MessageBird and MSG91 publish no spec I could find, so their counts are unknown. REA's analyser here records modules and exports but not string literals, so endpoint lists in 06 were read directly from package files (marked [src]) and are lower bounds.

## Executive summary

**Position.** Relay is a well-engineered, India-first, compliance-first **SMS + RCS** orchestration platform. Its strongest parts:

- DLT gate and live SMPP;
- live Trustsignal RCS with fallback;
- pay-on-delivery ledger;
- operator console;
- RLS on 55 tables;
- a 211-operation API.

It is **not yet** the five-channel product its UI presents.

**Five facts that change priorities (each proven in code):**

1. WhatsApp, Email and Voice go to a sandbox that marks most messages delivered **and charges for them** (`internal/connector/smpp.go:802`, `sandbox.go:18-27`, `state.go:84-89`).
2. Every Indian promotional SMS is refused, because no DND register is wired (`internal/sending/dnd.go:38-43`).
3. There is no payment gateway (card top-up returns 422, auto-recharge never runs) and no GST invoice.
4. The operator `admin` role is never enforced on money actions (`internal/api/operator.go:76` has no callers).
5. The email DNS records shown to customers are placeholders (`internal/store/compliance.go:233-235`).

**Market.**

- **Global players are weak in India.** Twilio and Bird route Indian traffic internationally at $0.065–0.083; Plivo does not support domestic India at all.
- **Reviews are poor across the category.** Trustpilot scores run 1.1–2.1, so trust and support are a cheap way to stand out.
- **The main threat is Airtel IQ Gateway** (5 Oct 2026): a telco-owned version of Relay's pitch.
- **Indian SMB tools compete on published INR prices** (₹0.11–0.25 per SMS) and UPI checkout.
- **WhatsApp India has moved to per-message pricing,** with INR billing due by 31 Dec 2026.
- **RCS to iPhones on Jio was repriced** to ₹0.25–0.30 on 1 Sep 2026.

**Plan.**

| Phase | When | Work |
|---|---|---|
| 0 | 2 weeks | Channel honesty, refunds, security P0s |
| 1 | W2–W10 | India-complete: promotional SMS, second route, Razorpay, GST, RCS carousel and pre-send fallback |
| 2 | W3–W16, in parallel | Email on SES ap-south-1 → WhatsApp as a Meta Tech Provider → Voice on Exotel |
| 3 | W10–W20 | SDKs and docs, sub-accounts and white-label resellers, journeys v2, Verify fraud engine, status page |
| 4 | W16+ | SSO, consent provenance, HA, SOC 2 / ISO 27001, then UAE and KSA |

Start the external lead-time items now (09 §2). The first is the Meta Tech Provider application.

## Method and limits

- **Code claims:** cite `file:line` and were spot-checked in-session. The biggest ones were re-verified by hand: sandbox routing, DND refusal, fraud flag, DNS placeholders, WhatsApp window, operator role, dev-hook gate.
- **External claims:** carry a URL and access date. Each fact is tagged verified, single-source or unverified; each file ends with its list of unverified claims.
- **Live testing:** the authenticated read-only sweep of the live API and route probing were **blocked by the permission classifier**. Live per-channel behaviour is taken from earlier live-proven work in the project checkpoint, not re-run here.
- **Tests run on 15 Oct:**
  - backend `make test`: all 20 packages green;
  - frontend vitest: 5,986 of 5,988 pass, with the 2 failures in mock audit-log paging (`src/mocks/handlers.test.ts`);
  - the 86 Playwright specs run against mocks only.
- **REA limits:** competitor product consoles are behind login, so REA saw only public bundles and SDK packages. Vonage and Zenvia (Cloudflare) and Netcore (403) were blocked. REA evidence IDs from the 15 Oct session may not be retrievable later.
