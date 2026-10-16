# 04 — What a complete, competitive implementation of each channel requires

Relay market research, part 4. Written 15 Oct 2026.

Scope: SMS, RCS, WhatsApp, Email and Voice, plus a short survey of candidate extra channels. For
each one this file covers:

1. integration options and vendors (cost, India availability, time to live, regulation)
2. a feature checklist that names the official documentation for each item
3. Relay's status on each item (built / partial / absent), with file:line proof, and the effort to close it
4. the vendor Relay should use, and why

## How to read this document

**Status values**

| Status | Meaning |
|---|---|
| **built** | Works end to end in code against a real provider (or, for SMS, the real SMPP operator). |
| **partial** | Some of it exists: a contract shape, a UI, a stored field, a sandbox-only path, or a real path missing important parts. |
| **absent** | No code found. Proof is the grep that came back empty. |
| **n/a** | Does not apply to Relay's model. |

**Effort scale.** Estimates assume one engineer who knows the codebase. They exclude waiting on
external approvals; the text names those waits separately.

| Effort | Meaning |
|---|---|
| S | ≤ 3 engineer-days |
| M | 1–2 weeks |
| L | 3–6 weeks |
| XL | > 6 weeks, or blocked by a contract, licence or vendor onboarding outside engineering's control |

**Evidence tags for external facts.** Every external fact carries a URL. Access date is 15 Oct 2026
unless stated otherwise.

| Tag | Meaning |
|---|---|
| **[V]** | The content was fetched or searched in this session, and the fact was read from the result. |
| **[D]** | The URL is the provider's documentation page for the feature. The specific detail was **not** re-fetched this session and comes from prior knowledge. Treat [D] details as likely, not proven. |
| **[U]** | Unverified. The only support is a third-party blog or reseller, or sources conflict. Confirm before anyone relies on it. |

**Code paths.** Relay code paths are relative to the repo they live in:

- `SMS-BE/…` is `/Users/mohdsaeedafri/All-Code-Base/SMS-BE`
- `SMS-UI/…` is `/Users/mohdsaeedafri/All-Code-Base/SMS-UI`, fast-forwarded from `upstream/master` on 15 Oct 2026, already up to date

The SMS-UI line numbers come from that checkout.

---

## 0. One-page verdict

| Channel | Real provider wired | Completeness vs a competitive India CPaaS | Biggest gaps | Recommended vendor path | Effort to "competitive" |
|---|---|---|---|---|---|
| SMS | Yes. Direct SMPP (VIDEOCON) | ~60% | Promo traffic blocked by default (`NoDNDRegister`); no MNP/HLR; routing per country, not per operator; no LCR; no failover aggregator; fraud flags never computed; no URL/callback whitelisting check | Keep direct SMPP and add a second aggregator bind for failover; treat DLT scrubbing as the operator's job | L |
| RCS | Yes. Trustsignal (live 8–11 Oct). Jio/Airtel/Vi/Google coded, never sent live | ~45% | No carousel; text or single card only; suggestions limited to reply/url/dial; no capability lookup on the live vendor; agent launch is only a DB state | Stay on Trustsignal now; add Vi (Dotgo MaaP) and Jio direct once volume justifies it; keep Google RBM for non-India | L |
| WhatsApp | **No.** Sandbox only | ~10% (contract shapes and UI only) | No Meta integration at all; 24h window computed from consent time (wrong); tier enum stale; no template sync, webhooks, media headers, carousel, flows or payments | Meta Cloud API direct as a **Tech Provider**, with embedded signup | XL (L engineering + Meta onboarding) |
| Email | **No** (Resend used only for platform mail). Sandbox only | ~10% | DNS records are fake placeholders; no sending, bounces, complaints, one-click unsubscribe, tracking or inbound | **Amazon SES ap-south-1** with tenant management and VDM | L |
| Voice | **No.** Sandbox only; caller-ID "verification" returns the code in the HTTP response | ~5% | No telephony provider, TTS, IVR, number provisioning or 140/1600 handling; regulation makes self-built PSTN impractical | **Exotel** (or Plivo India region) as licensed upstream; no own PSTN interconnect | L (XL including KYC/number series) |

Cross-cutting finding. The UI channel registry advertises WhatsApp, Email and Voice as first-class
live channels:

- `SMS-UI/src/lib/registries/channels.ts:114` (WhatsApp)
- `SMS-UI/src/lib/registries/channels.ts:133` (Email)
- `SMS-UI/src/lib/registries/channels.ts:152` (Voice)

It even claims Email is two-way (`twoWay: true` at `channels.ts:146`). The backend, meanwhile, routes
every non-SMS submission that has no dedicated gateway to the sandbox:

- `SMS-BE/internal/connector/smpp.go:802`: `if s.Channel != "SMS" || !configured { grouped[r.Fallback] … }`
- `SMS-BE/internal/sending/service.go:93`: only RCS gets a dedicated gateway

Until a channel is real, the UI should mark it "sandbox" (effort S).

---

## 1. India regulatory baseline (applies across channels)

| Rule | What it requires | Source | Relay status |
|---|---|---|---|
| TCCCPR 2018 / DLT | PE (principal entity) registration, header registration, content templates, consent templates. Operators scrub against preferences on DLT. | TRAI TCCCPR 2018 https://www.trai.gov.in/sites/default/files/RegulationUcc19072018.pdf [D] | **built** for registry and gate: entity gate (commit 4417d70); `DLT_IDS_MISSING` refusal at `SMS-BE/internal/connector/smpp.go:810-813` |
| URL / APK / OTT-link whitelisting | Every URL, APK or OTT link in an SMS must be whitelisted on DLT. Operators enforce from 1 Oct 2024. | TRAI PR 80/2024 https://trai.gov.in/sites/default/files/PR_No.80of2024.pdf [V] | **absent**. No check that a template's URLs, or Relay's own short-link domain, are whitelisted. A grep for `whitelist` in `internal/` matched only the API-key IP allowlist. Effort M. |
| Callback-number whitelisting | Callback numbers in content must be whitelisted. Format rules: mobile starts 6–9; toll-free 1800/1860; 140-series 10 digits. | MSG91 help https://msg91.com/help/dlt-registration-in-india/how-to-whitelist-urls-and-callback-numbers-on-dlt [U] (vendor help page) | **absent**. Effort S–M. |
| PE–TM chain binding | Telemarketer chain declared on DLT. Hash carried in SMPP TLV 0x1402. | TRAI direction 28 Oct 2024, summarised by TRAI PR 80/2024 above [V] | **built**: `SMS-BE/internal/connector/smpp.go:27-31,92-96`; config `SMS-BE/internal/platform/config/config.go:159` |
| Promotional window | Promotional SMS only 09:00–21:00 IST. | TCCCPR 2018 [D] | **built**: checked again at submit, `SMS-BE/internal/connector/smpp.go:446-456` |
| 140-series for promotional voice | Promotional calls only from 140xx numbers, any sector. Customers can block them via DND. | TRAI PR 91/2026 (10 Jul 2026) https://trai.gov.in/sites/default/files/2026-07/PR_No91of2026.pdf [V] | **absent** (no voice). Effort: part of the Voice build. |
| 1600-series for BFSI service calls | BFSI entities regulated by RBI/SEBI/IRDAI/PFRDA must call existing customers from 1600xx numbers. These calls cannot be tagged, blocked or filtered. | TRAI PR 91/2026 [V]. NSE circular https://nsearchives.nseindia.com/content/circulars/COMP74037.pdf [V] (lists RBI/SEBI/PFRDA only, so scope differs between the two) | **absent** |
| Voice: domestic routes need an Indian entity, media anchoring | Indian numbers only for India-registered businesses after KYC. Both call legs must originate and terminate in India. Cold calling prohibited. | Plivo India calling https://www.plivo.com/docs/voice/concepts/india-calling [V] | **absent** |
| OSP / VoIP–PSTN | 2021 OSP guidelines removed the domestic vs international OSP split and relaxed interconnect. VoIP–PSTN interconnect remains a licensed-operator activity (toll-bypass concern). | AZB summary https://www.azbpartners.com/bank/dot-issues-new-guidelines-for-osps/ [V]. Khaitan https://khaitanco.com/thought-leaderships/DoT-Re-vision-to-the-OSP-framework [V]. A 2025 DoT IP–PSTN order was **not found** [U] | n/a. Implication: Relay should not run its own PSTN gateway. See §5. |
| DPDP Act 2023 | Notice and consent for personal data, including call recordings and message content. Erasure on withdrawal. | MeitY https://www.meity.gov.in/data-protection-framework [D] | **partial**. Per-channel consent and retention exist (`SMS-BE/internal/api/retention.go`). No consent-notice artefact per recording. |
| GST on CPaaS invoices | 18% GST on Meta/BSP charges. Relay's own invoices have no GSTIN. | myOperator blog https://myoperator.com/blog/whatsapp-business-api-pricing-india-2026 [U]. Relay invoices: shared context file | **absent** (GST invoice). Effort M. Covered in another part of this research. |

---

## 2. SMS

### 2.1 Integration options

| Option | What it is | Cost (indicative) | India availability | Time to live | Regulatory load | Fit for Relay |
|---|---|---|---|---|---|---|
| Direct operator SMPP (Jio, Airtel, Vi, BSNL) | Bind to each operator's SMSC as a registered telemarketer (TM). | Lowest per-SMS. Operator wholesale is commercial-in-confidence [U] | Yes. Needs TM registration on each operator's DLT and a commercial agreement per operator | 4–12 weeks per operator (agreement, IP whitelisting, test binds) [U] | Full TM obligations: DLT scrubbing participation, PE–TM chain, complaint handling | Already doing it (VIDEOCON bind). Good margin, but a single bind is a single point of failure. |
| Tier-1 aggregator over SMPP or HTTP (Route Mobile, Tanla/Karix, Kaleyra, ValueFirst, Sinch India, Infobip India) | Aggregator holds the operator binds. Relay binds once. | ~₹0.12–0.20 per SMS wholesale. MSG91 retail ₹0.25→0.16 + GST (shared context, first-pass notes) [U] | Yes | 1–2 weeks | Aggregator is the TM. Relay is a reseller/PE agent. | **Best as failover and as coverage for operators Relay has no direct bind with.** |
| Airtel IQ Gateway | Operator-run CPaaS: native DLT, SMS/WA/RCS/voice, multi-vendor failover | Not public [U] | Yes (launched 5 Oct 2026 per shared context) [U] | Weeks | Operator-side | A competitor more than a supplier; a possible secondary route. |
| International: Twilio, Vonage, Sinch, Telnyx global | API-first global SMS | Per-country | India via their local partners | Days | 10DLC/TFV for US (Relay already has a US regime) | Only for non-India corridors. |
| HLR / MNP lookup providers (TRAI MNP database via MNPSP; aggregator HLR; Twilio Lookup v2) | Which operator currently serves a number | ~₹0.01–0.05 per lookup in India [U]. Twilio Lookup https://www.twilio.com/docs/lookup/v2-api [D] | Yes | Days | Data handling under DPDP | Needed for per-operator routing (see 2.2). |

### 2.2 Feature checklist (SMS)

| # | Feature | Official reference | Relay status | Proof (file:line) | Effort |
|---|---|---|---|---|---|
| S1 | SMPP 3.4 bind (TRX), enquire_link, reconnect | SMPP v3.4 spec https://smpp.org/SMPP_v3_4_Issue1_2.pdf [D] | **built** | `SMS-BE/internal/connector/smpp.go:51,181-215` (gosmpp, EnquireLink, ReadTimeout = 3×EnquireLink+10s) | — |
| S2 | Multiple binds per operator, round-robin | SMPP spec [D] | **built** | `SMS-BE/internal/connector/smpp.go:814-817` | — |
| S3 | Window / TPS per bind | SMPP spec [D] | **built** | `SMS-BE/internal/connector/smpp.go:122-129,417,535-553` | — |
| S4 | Priority lane for OTP | Industry practice | **built**. One window slot reserved for priority. | `SMS-BE/internal/connector/smpp.go:123-129`; `SMS-BE/internal/connector/connector.go:65` | — |
| S5 | GSM-7 vs UCS-2 detection, concatenation, segment count | 3GPP TS 23.038 https://www.3gpp.org/DynaReport/23038.htm [D] | **built** | `SMS-BE/internal/connector/smpp.go:589-598`; `SMS-BE/internal/domain/billing/segments.go:75`; UI meter `SMS-UI/src/app/(dashboard)/templates/new/segment-meter.tsx` | — |
| S6 | DLT TLVs: PE id (0x1400), template id (0x1401), chain hash (0x1402); tags configurable per operator | TRAI PR 80/2024 [V]; operator SMPP specs [U] | **built** | `SMS-BE/internal/connector/smpp.go:24-31,80,630-642`; contract field `dltChainTlv` at `SMS-BE/internal/gen/api/control.gen.go:4596` | — |
| S7 | Refuse India traffic without DLT ids | TCCCPR [D] | **built** | `SMS-BE/internal/connector/smpp.go:810-813` | — |
| S8 | Entity / header / template registry and approval gate | TCCCPR [D] | **built** | Commit 4417d70 (entity gate); header shape `SMS-BE/internal/domain/compliance/india.go:77`; `SMS-BE/internal/api/senders.go:316-321` | — |
| S9 | Sender ID types: alphanumeric 6-char header (IN), numeric long code, short code, toll-free (US) | TRAI [D]; US: The Campaign Registry https://www.campaignregistry.com [D] | **partial**. IN alphanumeric validated. Source TON fixed to 5 (alphanumeric) on the bind, so a numeric sender would be mis-flagged. | `SMS-BE/internal/connector/smpp.go:77` (`SourceTon: 0x05`); `SMS-BE/internal/domain/compliance/us.go:17` | S (derive TON/NPI from header shape) |
| S10 | Delivery receipts (deliver_sm parse, stat/err mapping) | SMPP spec Appendix B [D] | **built** | `SMS-BE/internal/connector/smpp.go:676-680` (parse), `:366` (dispatch) | — |
| S11 | Late submit_resp / no-DLR reconciliation (validity window) | SMPP spec [D] | **built** (48 h) | `SMS-BE/internal/sending/reconciler.go:14-29`; `SMS-BE/internal/connector/smpp.go:162-166` | — |
| S12 | Validity period / `schedule_delivery_time` on submit_sm | SMPP spec [D] | **absent**. A grep for `validity_period`/`ValidityPeriod` in `internal/connector` finds none; Relay only reconciles. | — | S |
| S13 | Flash SMS (class 0) | 3GPP TS 23.038 [D] | **absent**. A grep for `flash`/`esm_class`/`message_class` finds only demo-seed copy (`SMS-BE/internal/demoseed/seed.go:672`). | — | S. Low demand in India; operators often block it [U]. |
| S14 | Scheduling (campaign at time T) | — | **built** | `SMS-BE/internal/api/campaign_scheduler.go:19`; `SMS-BE/internal/store/campaigns.go:71` | — |
| S15 | Promotional time window | TCCCPR [D] | **built** | `SMS-BE/internal/connector/smpp.go:446-456` | — |
| S16 | DND / preference scrubbing | TCCCPR: scrubbing is done by the access provider on DLT [D] | **partial, and actively harmful.** The default register refuses **every** Indian promotional SMS with `dnd_check_unavailable`. | `SMS-BE/internal/sending/dnd.go:36-43` | S. Add an "operator scrubs on DLT" mode as the India default. Keep the lookup interface for operators that require pre-scrub. |
| S17 | Inbound MO (two-way) | SMPP deliver_sm [D] | **built** | `SMS-BE/internal/connector/smpp.go:366`; `SMS-BE/internal/api/inbound_sms.go:18,48` | — |
| S18 | STOP / opt-out keywords → suppression | US CTIA handbook https://www.ctia.org/the-wireless-industry/industry-commitments/messaging-interoperability-sms-mms [D]; TRAI [D] | **built** (whole-body match: STOP, UNSUBSCRIBE, CANCEL, END, QUIT, OPTOUT) | `SMS-BE/internal/store/support.go:501-531` | — |
| S19 | Keyword auto-replies / chatbot | — | **built** | `SMS-BE/internal/api/chatbots.go:88,116` | — |
| S20 | Hindi / regional opt-out words (e.g. "रोकें") and HELP / START | TRAI complaint process [D] | **absent** (English list only) | `SMS-BE/internal/store/support.go:505-507` | S |
| S21 | Per-destination operator routing (MNP-aware) | TRAI MNP regs https://www.trai.gov.in [D] | **absent**. The route is chosen per country+channel by priority, not per MSISDN. The carrier comes from the route row. | `SMS-BE/internal/sending/service.go:913-932` (`resolvePath` → `store.SelectRoute(country, channel)`); `SMS-BE/internal/store/operator.go:499` (`ORDER BY priority`) | M (needs an MNP/HLR source) |
| S22 | HLR / number lookup API (valid, ported, roaming, line type) | Twilio Lookup v2 [D] | **absent**. Only a seeded "Number lookup" price row exists. | `SMS-BE/internal/demoseed/seed.go:831` | M (buy, don't build) |
| S23 | Least-cost routing (LCR) with quality weighting | Industry practice | **absent**. Priority only. A grep for `least.cost`/`lcr` finds none. | `SMS-BE/internal/store/operator.go:499` | M |
| S24 | Automatic failover to a second route on bind-down or error spike | Industry practice | **absent** for SMS. No bind → `NO_OPERATOR_BIND` refusal. (Channel fallback RCS→SMS exists, but not route failover.) | `SMS-BE/internal/connector/smpp.go:808-809` | M |
| S25 | SMS pumping / AIT fraud protection (country allow-list, velocity, conversion-rate monitoring) | Twilio Verify Fraud Guard https://www.twilio.com/docs/verify/preventing-toll-fraud/sms-fraud-guard [V]; Geo permissions https://www.twilio.com/docs/verify/preventing-toll-fraud/verify-geo-permissions [V] | **partial, mostly cosmetic.** `fraud_flag` columns exist and the analytics count them, but no code writes a value other than the default `none` (only the demo seed does). The API abuse guard is IP rate limiting only. | Column `SMS-BE/db/clickhouse/001_messages.sql:36`, `SMS-BE/db/migrations/00015_verify.sql:46`; readers `SMS-BE/internal/store/analytics.go:179-227`, `SMS-BE/internal/api/verify.go:493-504`; writers only `SMS-BE/internal/demoseed/seed.go:1515,1644`; IP guard `SMS-BE/internal/api/abuse_guard.go:44` | M (per-tenant country allow-list + per-prefix velocity + Verify conversion-rate alarm) |
| S26 | URL whitelisting check, and whitelisting of Relay's short-link domain | TRAI PR 80/2024 [V] | **absent** | Short links `SMS-BE/internal/api/links.go:21-30` (no DLT-whitelist awareness) | M |
| S27 | Link shortening + click tracking | — | **partial** (backend only, no UI, per shared context) | `SMS-BE/internal/api/links.go:26-61` | S (UI) |
| S28 | Unicode-aware template variable rendering | — | **built** | `SMS-BE/internal/sending/render.go` | — |
| S29 | Frequency caps | — | **built** | `SMS-BE/internal/api/frequency_caps.go:82,110` | — |
| S30 | Webhooks for DLR and MO (HMAC, retries, CDP formats) | — | **built** | `SMS-BE/internal/api/webhook_emit.go:25,85,189-192`; `SMS-BE/internal/webhook/integrations.go:65` | — |
| S31 | OTP / Verify API with channel fallback | Twilio Verify https://www.twilio.com/docs/verify/api [D] | **built** for SMS. The other channels go to the sandbox. | `SMS-BE/internal/api/verify.go:20-67,167-208` | — |
| S32 | International regimes (US 10DLC live; UK/UAE stubs) | TCR [D]; Ofcom/TDRA [D] | **partial** | `SMS-BE/internal/domain/compliance/us.go`; `regime.go:81` | L per country |
| S33 | Second-operator commercial redundancy (Jio + Airtel + Vi direct, or aggregator) | — | **absent** (one live operator) | Shared context ("Live operator: VIDEOCON SMPP") | XL (commercial) |
| S34 | SMPP server for resellers (Relay exposes SMPP to its own customers) | SMPP spec [D] | **absent**. HTTP API only. | — | L. Many Indian resellers expect it [U]. |

### 2.3 Recommendation (SMS)

1. **Unblock promo traffic now (S).** In India, operators do preference scrubbing on DLT, so a
   missing DND register should not refuse traffic. Make "operator scrubs" the India default. Keep
   `DNDRegister` for pre-scrub, which avoids paying for messages the operator will reject.
   Proof of today's behaviour: `SMS-BE/internal/sending/dnd.go:38-43`.
2. **Add one aggregator SMPP bind as failover and coverage (M, plus about 2 weeks commercial).**
   Pick one tier-1 Indian aggregator with a published SLA. The bind code already supports multiple
   binds per carrier; what is missing is cross-route failover (S24).
3. **MNP-aware routing + HLR (M).** This lets direct binds be used per operator instead of per
   country. Buy lookups (aggregator HLR or MNP dip) and cache them.
4. **Fraud (M).** Put real writers behind the existing `fraud_flag` columns: per-tenant destination
   allow-list (default IN only), per-prefix velocity, and Verify conversion-rate alarm. Twilio's
   Fraud Guard is the model [V].
5. **DLT URL/callback whitelisting check at template approval (M).** Also whitelist Relay's short
   domain on each DLT platform.

Expected cost per SMS for Relay: the operator or aggregator wholesale rate. The ₹0.12–0.20 range is
[U] and needs quotes. Retail benchmark: MSG91 ₹0.25→0.16 + GST (shared context).

---

## 3. RCS

### 3.1 Integration options

| Option | What it is | Cost | India availability | Time to live | Notes |
|---|---|---|---|---|---|
| Google RBM (Jibe) direct | Google's RBM API. Agents verified and launched per carrier via the Business Communications API. | Google bills per its RBM billing categories (below). Carrier sets the price. | Vi runs RBM on Google RCS via Dotgo MaaP. Airtel announced a Google partnership (spam filtering, Mar 2026), but no confirmed commercial launch date. | Agent verification + carrier launch: weeks [U] | voicendata https://www.voicendata.com/mobile-services/rcs-gains-momentum-in-india-with-airtelgoogle-partnership-10893020 [V]; storyboard18 https://storyboard18.com/digital/airtel-google-join-forces-to-tackle-rcs-spam-in-india-with-network-level-filtering-91052.htm [V] |
| Jio RCS (own MaaP) | Jio's business messaging platform. Also the first Indian path to iPhone RCS. | iPhone RCS from 1 Sep 2026: marketing ₹0.27–0.30, transactional ₹0.25, OTP ₹0.11–0.16 (was flat ₹0.08–0.10) | Yes | Agreement with Jio or a Jio-connected aggregator | inc42 https://inc42.com/buzz/jio-introduces-new-rcs-business-messaging-rates-for-iphone-users-in-india/ [V]; Angel One https://www.angelone.in/news/market-updates/reliance-jio-apple-start-new-rcs-pricing-for-business-messages-in-india [V] |
| Airtel RCS | Airtel's own API. Reported ~₹0.11 per message, 80:20 revenue share with Google. | ₹0.11 [U] | Partnership announced; launch unconfirmed | Unknown | telecomtalk https://telecomtalk.info/airtel-google-to-launch-rcs-messaging-india/1002371/ [V] (reported, not official) |
| Vi via Dotgo MaaP | Vi RBM on Google RCS | Not public | Yes | Weeks | voicendata [V] |
| Aggregators (Trustsignal/Sigmo, Gupshup, Route Mobile, Tanla, Infobip, Sinch, Dotgo) | Hold agent/launch relationships | Markup on carrier rate [U] | Yes | Days–weeks | **Relay is live on Trustsignal** (shared context; `SMS-BE/internal/connector/rcs_trustsignal.go`) |

Google RBM billing model (applies on Google-hosted carriers):

- A **basic message** is text-only, ≤160 UTF-8 bytes.
- A **single message** is rich, or text over 160 bytes.
- **A2P conversation:** a basic message from a conversational agent becomes part of an a2p conversation if the user replies within 24 h.
- On 20 Nov 2025 Google merged the legacy Basic and Single categories into one **Non-conversational** category.

Source: RBM billing FAQ
https://developers.google.com/business-communications/rcs-business-messaging/guides/learn/rbm-billing-faq
[V, via search excerpt]. The byte limit comes from Vonage's mirror
https://developer.vonage.com/en/messages/concepts/rcs-billing [V].

Relay implication: Hindi text hits the 160-byte limit at about 53 characters, so the cost estimate
must count UTF-8 bytes, not characters.

### 3.2 Feature checklist (RCS)

| # | Feature | Official reference | Relay status | Proof | Effort |
|---|---|---|---|---|---|
| R1 | Agent (brand) creation: name, logo, banner, colour, description, contact | RBM agent creation https://developers.google.com/business-communications/rcs-business-messaging/guides/get-started/create-agent [D] | **built** (Relay-side record + brand-rule checks) | `SMS-BE/internal/api/rcs_agents.go:224,520-528` (colour check) | — |
| R2 | Agent verification (brand verification documents) | RBM verify agent https://developers.google.com/business-communications/rcs-business-messaging/guides/launch/verify [D] | **partial**. Document upload and operator review exist. No submission to Google/carrier verification APIs. | `SMS-BE/internal/api/rcs_agents.go:409-425`; operator approve via `store.ReviewRcsAgent` (`rcs_agents.go` ~596) | M per carrier API |
| R3 | Agent launch per carrier | RBM launch https://developers.google.com/business-communications/rcs-business-messaging/guides/launch/launch [D] | **partial**. Launch is a state transition in Postgres; no carrier API is called. | `SMS-BE/internal/api/rcs_agents.go:536-570` (`store.LaunchRcsAgentOnCarrier`) | M per carrier |
| R4 | Capability check (is this handset RCS-capable, which features) | RBM capabilities https://developers.google.com/business-communications/rcs-business-messaging/guides/build/capabilities [D] | **partial**. Coded for Google/Airtel/Vi. The live vendor (Trustsignal) returns "no capability lookup". | `SMS-BE/internal/connector/rcs_google.go:86-110`; `rcs_airtel.go:78,108`; `rcs_vi.go:62,107`; `rcs_trustsignal.go:62-68`; API `SMS-BE/internal/api/rcs_capabilities.go:25` | S–M (vendor-dependent) |
| R5 | Bulk capability / reachability pre-check before campaign | Same | **partial** (same carriers) | `SMS-BE/internal/connector/rcs_airtel.go:108`; `rcs_vi.go:107` | S |
| R6 | Plain text message | RBM send https://developers.google.com/business-communications/rcs-business-messaging/guides/build/send [D] | **built** | Contract `SMS-BE/openapi/control.json:14682-14720` (RcsTextContent) | — |
| R7 | Standalone rich card (media + title + description) | RBM rich cards https://developers.google.com/business-communications/rcs-business-messaging/guides/build/messages/rich-cards [D] | **built** | `SMS-BE/openapi/control.json:14738-14753` (RcsCard); UI card media upload `SMS-UI/src/app/(dashboard)/templates/new/template-editor.tsx:611-613` | — |
| R8 | Rich card **carousel** (2–10 cards) | Same page, carousels section [D] | **absent** in contract and UI. Only the capability enum knows the word. | `SMS-BE/internal/connector/rcs_capability.go:48`; `SMS-BE/internal/gen/api/control.gen.go:1430`; RcsContent oneOf is text\|card only (`control.json:14682-14700`) | M |
| R9 | Card orientation, media height, thumbnail, video | RBM rich cards [D] | **absent** (mediaUrl only) | `SMS-BE/openapi/control.json:14738-14753` | S |
| R10 | Suggested replies | RBM suggestions https://developers.google.com/business-communications/rcs-business-messaging/guides/build/suggestions [D] | **built** | `SMS-BE/internal/connector/rcs_template.go:84-91`; `control.json:14754` | — |
| R11 | Suggested actions: open URL, dial | Same [D] | **built** | Same | — |
| R12 | Suggested actions: view location, share location, create calendar event, open URL in webview, compose | Same [D] | **absent** (Type is reply\|open_url\|dial only) | `SMS-BE/internal/connector/rcs_template.go:84-85` | S |
| R13 | Suggestions preserved through carrier template registration | Carrier template APIs [U] | **partial**. Shared context says suggestions are dropped at carrier template registration. | `SMS-BE/internal/api/rcs_template_registration.go:69-106` | S |
| R14 | Carrier template registration (Airtel / Jio / Trustsignal) | Carrier docs [U] | **partial**. Airtel validation coded; Jio "templates not reviewed". | `SMS-BE/internal/connector/rcs_template.go:155-265`; `SMS-BE/internal/connector/rcs_jio.go:61`; `SMS-BE/internal/api/rcs_template_registration.go:106,267` | M |
| R15 | Delivery + read receipts | RBM events https://developers.google.com/business-communications/rcs-business-messaging/guides/build/events [D] | **built** | `SMS-BE/internal/connector/rcs_webhook.go:62-67,171,308,418`; `SMS-BE/internal/api/carrier_webhooks.go:42,121,226` | — |
| R16 | Typing indicators (send and receive) | Same [D] | **absent**. Incoming typing is ignored; no outbound typing. | `SMS-BE/internal/connector/rcs_webhook.go:40-41` | S |
| R17 | Inbound user messages and suggestion responses | RBM receive https://developers.google.com/business-communications/rcs-business-messaging/guides/build/receive [D] | **built** (two-way inbox; Jio suggestionResponse parsed) | `SMS-BE/internal/connector/rcs_jio.go:424-477` | — |
| R18 | Inbound media / location from user | Same [D] | **absent** (not found in webhook parsing) | `SMS-BE/internal/connector/rcs_webhook.go` (no `userFile`/`location` handling found by grep) | S |
| R19 | Message revocation (revoke undelivered) | RBM revoke https://developers.google.com/business-communications/rcs-business-messaging/reference/rest/v1/phones.agentMessages/delete [D] | **absent** | — | S |
| R20 | TTL / expiry on message | RBM `ttl`/`expireTime` [D] | **absent** | — | S |
| R21 | Fallback to SMS: pre-send (not capable) and post-failure | RBM fallback guidance [D] | **built** | `SMS-BE/internal/sending/legs.go:17-26`; `SMS-BE/internal/sending/fallback_after_failure.go:29,48`; `SMS-BE/internal/sending/service.go:704-706` | — |
| R22 | Multi-carrier agent selection (route to the operator where the agent is launched) | — | **built** | `SMS-BE/internal/sending/service.go:83-100` (`rcsPath`) | — |
| R23 | Media hosting with signed URLs that carriers can fetch | — | **built** | `SMS-BE/internal/api/media.go:56,189-219` | — |
| R24 | iOS 18+ reach (Apple RCS business messaging) | Apple + Jio, 1 Sep 2026 | **n/a in code** (carrier-side). Relay should show iPhone pricing as a separate line. | inc42 [V] | S (rate card) |
| R25 | Billing by RBM category (basic/single/conversation; bytes, not characters) | RBM billing FAQ [V] | **absent**. RCS is priced per message per corridor. | `SMS-BE/internal/api/operator.go:708` (comment: SMS and RCS price per message) | M |
| R26 | Verified sender badge / brand info surfaced in the preview | — | **partial**. Preview exists; there is no agent page. | `SMS-UI/src/app/(dashboard)/templates/new/rcs-preview.tsx` | S |
| R27 | Live proof per carrier connector | — | **partial**. Trustsignal live; Jio/Airtel/Vi/Google never sent live (shared context). | Connectors `SMS-BE/internal/connector/rcs_{jio,airtel,vi,google}.go` | M each (credentials + test) |
| R28 | Throttle handling | — | **built** | `SMS-BE/internal/connector/rcs_google.go:110,193` | — |
| R29 | Conversational agent + chatbot | — | **built** (keyword chatbots on RCS) | `SMS-BE/internal/api/chatbots.go:116` | — |

### 3.3 Recommendation (RCS)

- **Now:** stay on Trustsignal, because it is the only live-proven path. Close R8, R12, R13 and R16
  (one M + three S) so the product matches what aggregators sell. Carousel is the most visible gap.
- **Next quarter:** go direct on **Vi (Dotgo MaaP)** and **Jio** when monthly RCS volume justifies a
  contract. Jio is the only India route to iPhone RCS today [V]. The Jio connector is already coded
  (`SMS-BE/internal/connector/rcs_jio.go`), so the work is credentials and a live test (M).
- **Google RBM direct:** keep it for non-India corridors and for Vi if Dotgo exposes standard RBM. Airtel is unconfirmed [V].
- **Pricing:** carry Jio's iPhone price list separately (₹0.27–0.30 marketing). Count Hindi bytes, not characters, for RBM categories.

---

## 4. WhatsApp

### 4.1 Integration options

| Option | What it is | Cost to Relay | India availability | Time to live | Regulatory / policy load | Fit |
|---|---|---|---|---|---|---|
| **Meta Cloud API direct as Tech Provider** | Relay's Meta app onboards each tenant's own WABA via Embedded Signup. Tenants pay Meta directly with their own payment method (or through a Solution Partner credit line via a multi-partner solution). | No Meta markup. Meta per-message rates only. Relay charges a platform fee. | Yes. INR billing from 1 Jan 2026; WABAs must migrate to INR by 31 Dec 2026 or non-INR messages stop on 1 Jan 2027. | Meta business verification + app review for `whatsapp_business_messaging` and `whatsapp_business_management` (Advanced Access): typically weeks [U] | Relay must follow WhatsApp Business Messaging Policy, handle data under Meta's terms, and subscribe to `account_updates`. | **Recommended.** Highest margin, no BSP lock-in, Meta hosts the Cloud API (no on-prem). |
| Tech Provider + Solution Partner (multi-partner solution) | As above, but the tenant shares a Solution Partner's credit line, so Relay can invoice WhatsApp usage itself. | SP margin on top of Meta | Yes | Plus SP agreement | Credit line set by SP | Use once tenants want one consolidated Relay invoice. |
| Gupshup | Largest Indian BSP | Meta rate + ~₹0.08 per marketing message [U]; +6% on marketing fee outside MM-Lite routing from 1 Jan 2026 | Yes | Days | BSP owns compliance | Fast, but a direct competitor, and its valuation was cut >80% (shared context) |
| Trustsignal | Existing Relay RCS vendor; also sells WhatsApp [U] | Not public | Yes | Days | — | Convenient (one vendor contract) but adds markup |
| 360dialog | BSP with no markup on Meta fees; monthly licence per number | €49/month per number (Regular) direct [U]; partner $15–49 per number [U]; Meta fees passed through | Yes | Days | — | Good stop-gap if Meta onboarding stalls |
| Twilio / Infobip / Sinch | Global BSPs | Meta rate + per-message fee [U] | Yes | Days | — | Expensive for India volume |

Sources:

- Meta pricing https://developers.facebook.com/docs/whatsapp/pricing [V]
- Multi-partner solutions https://developers.facebook.com/documentation/business-messaging/whatsapp/solution-providers/multi-partner-solutions/ [V]
- Gupshup fee change https://support.gupshup.io/hc/en-us/articles/53700207494169-Gupshup-fee-changes-to-Cloud-API-marketing-w-e-f-1st-Jan-2026 [V, title only]
- 360dialog pricing https://docs.360dialog.com/docs/360dialog/prices-plans-and-payment-options [V, search excerpt]

**Meta pricing facts [V], from https://developers.facebook.com/docs/whatsapp/pricing:**

- Per-message pricing since **1 Jul 2025**. Only delivered template messages are charged. The category is the one applied at send time.
- Categories: Marketing, Utility, Authentication (plus authentication-international).
- **Customer service window (CSW):** 24 h from the user's last message. Non-template messages are free inside it. Utility templates sent inside an open CSW are free.
- **Free entry point (FEP):** a reply within 24 h to a Click-to-WhatsApp ad or Page CTA opens a 72 h window where everything is free.
- Volume tiers lower utility and authentication rates per market and category per month. The `VOLUME_BASED_PRICING_TIER_UPDATE` webhook exists since 1 Oct 2025.
- India: marketing rate increased 1 Jan 2026; authentication-international increased 1 Apr 2026.

India rates from third-party trackers [U]: marketing ₹0.8631 (was ₹0.7846), utility and authentication about ₹0.115. Sources: whautomate
https://whautomate.com/whatsapp-business-api-pricing-india and instantdm
https://instantdm.com/blog/whatsapp-business-api-pricing-2026-india-october-update. Pull the official
INR rate card from Meta before quoting.

**Messaging limits [V]**, from
https://developers.facebook.com/documentation/business-messaging/whatsapp/upcoming-messaging-limits-changes/:

- Since **7 Oct 2025** limits are **per business portfolio**, not per number.
- Tiers: 250 → 2,000 → 10,000 → 100,000 → unlimited.
- Upgrade 6 h after meeting the criteria: high quality, and ≥50% of the limit used in the last 7 days.
- New webhook field `max_daily_conversations_per_business`. The older `max_daily_conversation_per_phone` and `current_limit` were removed Feb 2026.

### 4.2 Feature checklist (WhatsApp)

Status note: every item below that touches Meta is **absent at the provider level**. WhatsApp sends
go to the sandbox (`SMS-BE/internal/connector/smpp.go:802`). A grep for
`graph.facebook|wamid|waba` across `internal/connector` and `internal/sending` finds nothing. "Partial"
means Relay has the data shape or UI only.

| # | Feature | Official reference | Relay status | Proof | Effort |
|---|---|---|---|---|---|
| W1 | Tech Provider registration + Meta app + business verification | Get started for Tech Providers https://developers.facebook.com/docs/whatsapp/solution-providers/get-started-for-tech-providers [D] | **absent** | — | XL (external: Meta review) |
| W2 | Embedded Signup (tenant connects or creates WABA + number in a popup; returns code → token, WABA id, phone id) | https://developers.facebook.com/docs/whatsapp/embedded-signup [D] | **absent**. Sender records accept a typed-in `wabaId`. | `SMS-BE/internal/api/senders.go:28`; `SMS-BE/internal/store/compliance.go:209` | M |
| W3 | Phone number registration, 2-step PIN, migration from another BSP | Phone numbers https://developers.facebook.com/docs/whatsapp/cloud-api/phone-numbers [D] | **absent** | — | M |
| W4 | Display name submission + approval status | Same page, display names [D] | **partial**. Stored field, WhatsApp-only guard. | `SMS-BE/internal/api/senders.go:29,438-441` | S (after W2) |
| W5 | Official Business Account (blue tick) request | https://developers.facebook.com/docs/whatsapp/overview/business-accounts [D] | **absent** | — | S |
| W6 | Business profile (about, address, website, photo) | Business profiles https://developers.facebook.com/docs/whatsapp/cloud-api/reference/business-profiles [D] | **absent** | — | S |
| W7 | Template CRUD via Graph API + status webhook (`message_template_status_update`) + category change webhook | Templates https://developers.facebook.com/docs/whatsapp/business-management-api/message-templates [D] | **absent**. Templates are approved by the Relay operator, not Meta. A comment notes that "WhatsApp has its own approval flow with Meta". | `SMS-BE/internal/api/rcs_template_registration.go:20`; operator approval endpoints in `SMS-BE/openapi/control.json` (`/v1/operator/templates/{id}/approve`) | M |
| W8 | Template categories: Marketing / Utility / Authentication; category validation and re-categorisation by Meta | Template categorization https://developers.facebook.com/docs/whatsapp/updates-to-pricing/new-template-guidelines [D]; pricing [V] | **partial**. Enum exists (it includes `TRANSACTIONAL`, which is not a Meta category). | `SMS-BE/openapi/control.json:14475-14478`; `SMS-BE/internal/api/enums.go:65-66` | S |
| W9 | Template language variants (same name, many languages) | Templates [D] | **absent**. No language field on Template. | `SMS-BE/openapi/control.json` Template properties: id, name, senderId, channel, country, body, rcsContent, category, registrationId, dltCategory, waContent, emailContent, … (no language) | S–M |
| W10 | Header: text / image / video / document / location | Template components https://developers.facebook.com/docs/whatsapp/business-management-api/message-templates/components [D] | **absent**. WaContent is text \| buttons \| list. | `SMS-BE/openapi/control.json:14489-14524` | M |
| W11 | Footer | Same [D] | **absent** | Same | S |
| W12 | Buttons: quick reply, URL (static/dynamic), call, copy code, OTP (one-tap / zero-tap / copy) | Same; Authentication templates https://developers.facebook.com/docs/whatsapp/business-management-api/authentication-templates [D] | **partial**. Quick reply, URL and call exist; copy-code, OTP autofill and dynamic URL suffix are absent. | `SMS-BE/openapi/control.json:14524` (WaButtonContent; WaQuickReplyButton, WaCtaUrlButton, WaCtaCallButton); UI `SMS-UI/src/app/(dashboard)/templates/new/template-editor.tsx:381-411` | M |
| W13 | Media card carousel templates (up to 10 cards) | https://developers.facebook.com/docs/whatsapp/business-management-api/message-templates/media-card-carousel-templates [D] | **absent** | — | M |
| W14 | Limited-time offer (LTO) templates | https://developers.facebook.com/docs/whatsapp/business-management-api/message-templates/limited-time-offer-templates [D] | **absent** | — | S |
| W15 | Catalog / multi-product / single-product messages | Sell products https://developers.facebook.com/docs/whatsapp/cloud-api/guides/sell-products-and-services [D] | **absent** | — | M |
| W16 | Interactive list messages (session) | Interactive list https://developers.facebook.com/docs/whatsapp/cloud-api/messages/interactive-list-messages [D] | **partial** (shape + UI builder; sandbox only) | `SMS-BE/openapi/control.json:14610` (WaListContent); `SMS-UI/src/app/(dashboard)/templates/new/template-editor.tsx:415-418` | S (after transport) |
| W17 | Interactive reply buttons, CTA URL (session) | https://developers.facebook.com/docs/whatsapp/cloud-api/messages/interactive-reply-buttons-messages [D] | **partial** | Same as W12 | S |
| W18 | Free-form session messages: text, media, location, contacts, reactions, stickers | Messages https://developers.facebook.com/docs/whatsapp/cloud-api/reference/messages [D] | **partial**. Text only. | `SMS-BE/openapi/control.json` WaTextContent | M |
| W19 | 24-hour customer service window enforcement (template-only outside CSW) | Pricing, CSW [V] | **partial and wrong.** The "window" is computed as *consent granted in the last 24 h*. The CSW actually opens on the **user's last inbound message**. Nothing enforces template-only at send. | `SMS-BE/internal/store/audience.go:26-30,64` (`consented_at ->> 'WHATSAPP' > now() - interval '24 hours'`); `SMS-BE/internal/api/audience.go:270-276` | M (track last inbound per contact per WABA; gate at send) |
| W20 | Free entry point (72 h) window from CTWA ads | Pricing [V] | **absent** | — | S (after W24) |
| W21 | Opt-in capture and proof | Getting opt-in https://developers.facebook.com/docs/whatsapp/overview/getting-opt-in [D] | **built** (per-channel consent with timestamps) | `SMS-BE/internal/api/audience.go:270`; consent gate in `SMS-BE/internal/sending/campaign.go:130` | — |
| W22 | Quality rating (GREEN/YELLOW/RED) sync via `phone_number_quality_update` | Quality rating https://developers.facebook.com/docs/whatsapp/messaging-limits [D] | **partial**. Field and enum exist; nothing populates them. | `SMS-BE/internal/api/senders.go:60-70`; `SMS-BE/openapi/control.json:14172-14175` | S (after W24) |
| W23 | Messaging limit tiers (portfolio-level, 250/2K/10K/100K/unlimited) | Limits [V] | **partial and stale.** Enum is `[250, 1000, 10000, 100000]`: no 2K, no unlimited, and per-number semantics. | `SMS-BE/openapi/control.json:14176-14180`; `SMS-BE/internal/api/senders.go:73-75` | S (contract change, UI handoff) |
| W24 | Webhooks: messages (inbound), statuses (sent/delivered/read/failed with error codes), template status, account updates, quality, tier, payment | Webhooks https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks [D] | **absent**. No Meta webhook route; carrier webhooks are RCS only. | `SMS-BE/internal/api/carrier_webhooks.go:38-42` | M |
| W25 | Webhook signature verification (X-Hub-Signature-256) and verify token | Same [D] | **absent** | — | S |
| W26 | Media upload / retrieve (Cloud API media endpoint) | Media https://developers.facebook.com/docs/whatsapp/cloud-api/reference/media [D] | **partial**. Relay media store exists, with no Meta upload. | `SMS-BE/internal/api/media.go:56` | S |
| W27 | Read receipts + typing indicator outbound | https://developers.facebook.com/docs/whatsapp/cloud-api/typing-indicators [D] | **absent** | — | S |
| W28 | WhatsApp Flows (forms, appointment booking; JSON + optional data endpoint with encryption) | Flows https://developers.facebook.com/docs/whatsapp/flows [D] | **absent** | — | L |
| W29 | Payments India (order_details, UPI intent / Razorpay / PayU / Zaakpay / Billdesk; payment-lookup API) | Payments India https://developers.facebook.com/docs/whatsapp/cloud-api/payments-api/payments-in [D]; 360dialog mirror https://docs.360dialog.com/partner/messaging/payments-india-only/receive-whatsapp-payments-via-payments-gateway [V] | **absent** | — | L |
| W30 | Calling API (user-initiated free; business-initiated billed per duration; call-permission request template) | Calling pricing https://developers.facebook.com/documentation/business-messaging/whatsapp/calling/pricing/ [V]; India availability per Chakra and Infobip [U] | **absent** | — | L (WebRTC/SIP media) |
| W31 | Marketing Messages API (formerly MM Lite): GA, optimised delivery, optional per-message max price, Intent API onboarding | Onboarding https://developers.facebook.com/documentation/business-messaging/whatsapp/marketing-messages/onboarding/ [V]; pricing (max price) [V] | **absent** | — | M |
| W32 | Block users API + user block/report signals in quality | Block users https://developers.facebook.com/docs/whatsapp/cloud-api/block-users [D] | **absent** | — | S |
| W33 | Conversational components (ice breakers, commands, welcome message) | https://developers.facebook.com/docs/whatsapp/cloud-api/phone-numbers/conversational-components [D] | **absent** | — | S |
| W34 | Click-to-WhatsApp ad attribution (referral object in inbound webhook) | Webhooks [D] | **absent** | — | S |
| W35 | Pricing per category, CSW-free utility, volume-tier aware billing | Pricing [V] | **partial**. Operator rates per category exist; no CSW-free or tier logic. | `SMS-BE/internal/api/operator.go:708`; `SMS-BE/internal/api/campaigns.go:410` | M |
| W36 | INR billing migration (WABA currency) | Pricing [V] | **absent**. Must be in onboarding before 31 Dec 2026. | — | S |
| W37 | Two-way inbox for WhatsApp | — | **partial**. UI registry says twoWay. The inbox is SMS/RCS only (shared context); WhatsApp inbound needs W24. | `SMS-UI/src/lib/registries/channels.ts:127`; `SMS-UI/src/app/(dashboard)/inbox/inbox-list-view.tsx:35` | M |
| W38 | Chatbots on WhatsApp | — | **partial**. Validation allows WHATSAPP; there is no transport. | `SMS-BE/internal/api/chatbots.go:116` | S (after W24) |
| W39 | Authentication templates for Verify (OTP via WhatsApp with copy-code / one-tap) | Auth templates [D] | **absent** (Verify channels resolve to sandbox for WA) | `SMS-BE/internal/api/verify.go:58-59` (fallback order only) | M |
| W40 | Per-tenant WABA isolation (tokens encrypted per tenant, RLS) | Multi-partner solutions [V] | **absent** (no token storage). RLS pattern exists to reuse. | — | S |

### 4.3 Recommendation (WhatsApp)

1. **Register Relay as a Meta Tech Provider now.** The calendar time is Meta's review, not
   engineering. Required: a live Meta app, business verification, and Advanced Access to
   `whatsapp_business_messaging` + `whatsapp_business_management` [V via MM API onboarding page].
2. **Build in this order** (L, about 5–6 weeks for one engineer):
   - Embedded Signup → token store (W2, W40)
   - send template + session text (W18)
   - webhooks: statuses, inbound, template status, quality and limits (W24, W25, W22, W23)
   - template sync to Meta (W7–W12)
   - real CSW gating (W19)
   - inbox and chatbots (W37, W38)
   - after that: carousel, Flows, payments, Marketing Messages API
3. **Billing model:**
   - Tenants pay Meta directly (Tech Provider default). Relay charges a platform fee per message or per seat.
   - This keeps Relay out of Meta credit risk and lets INR billing flow through Meta (mandatory by 31 Dec 2026 [V]).
   - Add a Solution Partner credit line later if consolidated invoicing matters.
4. **Interim:** if a paying customer needs WhatsApp before Meta approval, use **360dialog** (no markup
   on Meta fees [V, search excerpt]) through the same connector interface. Gupshup is a competitor;
   avoid it as a supplier.
5. **Fix now (S):** the stale tier enum (W23) and the consent-based "window" (W19). Both show wrong
   numbers in the UI today.

Unit cost to tenant: Meta's rate (marketing ~₹0.86, utility/auth ~₹0.115 [U]) + Relay fee + 18% GST.

---

## 5. Email

### 5.1 Integration options

| Option | Cost | India / ap-south-1 | Time to live | Notes |
|---|---|---|---|---|
| **Amazon SES (ap-south-1)** | À la carte **$0.10 / 1,000** outbound; attachments $0.12/GB; inbound $0.10/1,000. Plans: Essentials $0.16/1,000; Pro $0.22/1,000 + $105/mo per account-region; Enterprise $0.23/1,000 + $500/mo. Dedicated IP standard $24.95/mo per IP; managed $15/mo per account + $0.08/1,000. VDM $0.07/1,000. | SES runs in ap-south-1 (Mumbai) [D: https://docs.aws.amazon.com/general/latest/gr/ses.html]. Same region as Relay. | Production access request (sandbox exit): typically ~1 day [U]. Warm-up for dedicated IPs: weeks. | Pricing https://aws.amazon.com/ses/pricing/ [V]. **Tenant management** (Aug 2025): isolated tenants with their own identities, config sets and templates; per-tenant bounce/complaint metrics; auto-pause on reputation; up to 10,000 tenants per account. https://aws.amazon.com/about-aws/whats-new/2025/08/amazon-ses-tenant-isolation-automated-reputation-policies [V]; docs https://docs.aws.amazon.com/ses/latest/dg/tenants.html [V] |
| SendGrid (Twilio) | Plan-based; Pro with dedicated IP ~$90/mo+ [U] | Global, US-hosted | Days | Mature marketing features. Expensive at CPaaS resale volume. |
| Postmark | ~$15/mo per 10k, transactional focus [U] | US/EU | Days | Excellent transactional deliverability. Restricts bulk marketing [U]. |
| Resend | Plan-based [U] | US/EU (Relay already uses it for platform mail: `SMS-BE/internal/mailer/mailer.go:1-20`) | Hours | Good developer experience. Not built for multi-tenant CPaaS resale. Keep it for platform mail only. |
| Netcore / Pepipost (India) | Not public [U] | India | Weeks | Indian ESP; competitor. |
| Own MTA (Postfix, KumoMTA, PowerMTA) | EC2 + IPs + ops time | Yes | Months (IP reputation from zero; AWS port 25 restrictions) | Not for a small team. |

### 5.2 Mailbox-provider rules that define "complete"

- **Gmail** (since 1 Feb 2024) [V], https://support.google.com/a/answer/81126:
  - **All senders:** SPF or DKIM, valid forward and reverse DNS (PTR), TLS, RFC 5322 formatting, spam rate under 0.3% in Postmaster Tools, no From: impersonation of Gmail.
  - **Bulk senders (5,000+/day to Gmail):** SPF **and** DKIM; DMARC (p=none allowed); From: domain aligned with SPF or DKIM; one-click unsubscribe (`List-Unsubscribe-Post: List-Unsubscribe=One-Click` + `List-Unsubscribe: <https://…>`, RFC 8058) plus a visible unsubscribe link in the body; target spam rate below 0.1%. Gmail says it will move to DMARC quarantine enforcement but gives no date.
- **Microsoft Outlook.com / Hotmail / Live** (from 5 May 2025, for >5,000/day): SPF, DKIM, DMARC (at least p=none, aligned). Non-compliant mail goes to Junk first and may later be rejected. Microsoft post https://techcommunity.microsoft.com/blog/microsoftdefenderforoffice365blog/strengthening-email-ecosystem-outlook%E2%80%99s-new-requirements-for-high%E2%80%90volume-senders/4399730 [V via search excerpts; rejection status U].
- **Yahoo:** equivalent bulk-sender rules, https://senders.yahooinc.com/best-practices/ [D].
- **Standards:**
  - RFC 8058 https://www.rfc-editor.org/rfc/rfc8058 [D]
  - DKIM RFC 6376 https://www.rfc-editor.org/rfc/rfc6376 [D]
  - SPF RFC 7208 https://www.rfc-editor.org/rfc/rfc7208 [D]
  - DMARC RFC 7489 https://www.rfc-editor.org/rfc/rfc7489 [D]
  - BIMI https://bimigroup.org/ [D]. Requires DMARC at quarantine or reject, plus a VMC or CMC certificate.

### 5.3 Feature checklist (Email)

Status note: campaign email goes to the sandbox (`SMS-BE/internal/connector/smpp.go:802`). The only
real email path is Resend for platform mail: verification, reset and invites
(`SMS-BE/internal/mailer/mailer.go:1-7`). It deliberately carries no campaign traffic.

| # | Feature | Official reference | Relay status | Proof | Effort |
|---|---|---|---|---|---|
| E1 | Sending domain registration per tenant | SES identities https://docs.aws.amazon.com/ses/latest/dg/creating-identities.html [D] | **partial**. Sender stores `emailDomain`, `fromAddress`, `fromName`. | `SMS-BE/internal/store/compliance.go:209-210` | — |
| E2 | DKIM records (Easy DKIM 2048-bit CNAMEs or BYODKIM) | SES Easy DKIM https://docs.aws.amazon.com/ses/latest/dg/send-email-authentication-dkim-easy.html [D] | **partial, placeholder.** The DKIM value written is a truncated dummy key, `"v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCB"`. It is not from any provider. | `SMS-BE/internal/store/compliance.go:226-235` | S (replace with SES `CreateEmailIdentity` tokens) |
| E3 | SPF / custom MAIL FROM domain | SES custom MAIL FROM https://docs.aws.amazon.com/ses/latest/dg/mail-from.html [D] | **partial, placeholder.** `v=spf1 include:mail.relay-platform.example ~all` (example domain). | `SMS-BE/internal/store/compliance.go:233` | S |
| E4 | DMARC record guidance + alignment check | RFC 7489 [D]; Gmail [V] | **partial**. Suggests `p=quarantine; rua=mailto:dmarc@<domain>`, which sends aggregate reports to the customer's own mailbox with no parsing. | `SMS-BE/internal/store/compliance.go:234` | S |
| E5 | DNS verification polling (record status pending → verified) | SES identity status [D] | **partial, fake.** Only a dev endpoint advances the status; no DNS lookup. | `SMS-BE/internal/api/dev.go:38,256-270`; UI `SMS-UI/src/app/api/dev/advance-email-dns`; contract `SMS-BE/openapi/control.json:14181-14200` | S |
| E6 | BIMI (logo, VMC/CMC) | bimigroup.org [D] | **absent** | — | S (guidance only) |
| E7 | Send via provider API with per-tenant isolation | SES tenants https://docs.aws.amazon.com/ses/latest/dg/tenants.html [V] | **absent** | — | M |
| E8 | One-click unsubscribe (RFC 8058 headers + POST endpoint) + visible body link | Gmail [V]; RFC 8058 [D] | **absent**. A grep for `List-Unsubscribe` finds nothing. | — | S |
| E9 | Unsubscribe processed within 2 days → suppression | Gmail guidance (2-day processing) [U via 9to5google] | **partial**. Suppression model exists for SMS STOP. No email unsubscribe endpoint. | `SMS-BE/internal/store/support.go:501-531` | S |
| E10 | Bounce handling (hard → suppress, soft → retry/backoff) | SES notifications https://docs.aws.amazon.com/ses/latest/dg/monitor-sending-activity-using-notifications.html [D] | **absent**. A grep for `bounce` finds nothing. | — | M |
| E11 | Complaint (FBL) handling → suppress + tenant metric | Same [D] | **absent** | — | S (with E10) |
| E12 | Account- and tenant-level suppression list | SES suppression https://docs.aws.amazon.com/ses/latest/dg/sending-email-suppression-list.html [D] | **partial** (generic suppression exists, not fed by email events) | Consent and suppression in `SMS-BE/internal/sending/campaign.go:130` | S |
| E13 | Event publishing (send, delivery, open, click, bounce, complaint, reject, rendering failure, delivery delay) → ClickHouse | SES event publishing https://docs.aws.amazon.com/ses/latest/dg/monitor-using-event-publishing.html [D] | **absent** | — | M (SNS/EventBridge → SQS → consumer) |
| E14 | Open tracking (pixel) and click tracking with a branded tracking domain | SES custom tracking domain https://docs.aws.amazon.com/ses/latest/dg/configure-custom-open-click-domains.html [D] | **partial**. Relay has its own link shortener and click log, not wired to email. | `SMS-BE/internal/api/links.go:21-61` | S |
| E15 | Apple Mail Privacy Protection awareness (machine opens) | Industry practice | **absent** | — | S |
| E16 | Template authoring: subject, HTML, preheader, variables | — | **built** (shape + UI preview) | `SMS-BE/openapi/control.json:14663-14680`; `SMS-UI/src/app/(dashboard)/templates/new/email-preview.tsx`; preheader `template-editor.tsx:445-450,1080` | — |
| E17 | Plain-text alternative part (multipart/alternative) | RFC 2046 [D] | **absent** (bodyHtml only) | `SMS-BE/openapi/control.json:14663-14680` | S |
| E18 | Drag-and-drop / MJML builder + responsive preview | MJML https://mjml.io [D] | **absent** (raw HTML) | — | L (or embed an OSS editor, e.g. Unlayer or GrapesJS [U]) |
| E19 | Attachments (size limits, MIME) | SES quotas https://docs.aws.amazon.com/ses/latest/dg/quotas.html [D] | **absent** | — | S |
| E20 | Dedicated IP (standard or managed) + automated warm-up | SES managed dedicated https://docs.aws.amazon.com/ses/latest/dg/managed-dedicated-sending.html [D]; pricing [V] | **absent** | — | S (config) + operator UI M |
| E21 | Shared-pool default with reputation tiers | SES tenants reputation policy [V] | **absent** | — | S |
| E22 | Deliverability analytics (inbox placement, per-ISP rates, Postmaster) | SES VDM https://docs.aws.amazon.com/ses/latest/dg/vdm.html [D]; Google Postmaster https://postmaster.google.com [D] | **absent** | — | M |
| E23 | Inbound email / parse (replies into inbox) | SES receiving https://docs.aws.amazon.com/ses/latest/dg/receiving-email.html [D]; pricing $0.10/1,000 [V] | **absent**, though the UI registry claims `twoWay: true` | `SMS-UI/src/lib/registries/channels.ts:146` | M |
| E24 | List hygiene: syntax/MX validation at import, role-address warning | — | **absent** | — | S |
| E25 | Rate limiting per tenant / per ISP (throttle) | SES sending quotas [D] | **absent** for email. Frequency caps explicitly exclude EMAIL. | `SMS-BE/internal/api/frequency_caps.go:82` | S |
| E26 | Bulk-sender compliance gate (block send if DKIM/DMARC not verified) | Gmail [V] | **partial**. Comment says an email sender is not usable until its domain is authenticated, but verification is fake (E5). | `SMS-BE/internal/store/compliance.go:220-224` | S |
| E27 | Per-email pricing in wallet ledger | — | **partial** (operator pricing per category) | `SMS-BE/internal/api/operator.go:708` | S |
| E28 | AMP for Email / annotations | Gmail AMP https://developers.google.com/gmail/ampemail [D] | **absent** | — | Skip (low ROI) |

### 5.4 Recommendation (Email)

- **Amazon SES in ap-south-1, à la carte ($0.10/1,000 [V]),** with **SES tenants** mapped 1:1 to
  Relay tenants. This gives per-tenant reputation isolation and auto-pause, which is the
  multi-tenant problem a CPaaS must solve [V].
- Add VDM ($0.07/1,000 [V]) once volume exists. Add managed dedicated IPs only for tenants above
  roughly 100k/month (estimate [U]).
- **Build order** (L, about 4 weeks):
  1. real identities and DKIM/SPF/MAIL FROM (E2–E5)
  2. send path in the connector registry (E7)
  3. RFC 8058 unsubscribe (E8–E9)
  4. event pipeline into ClickHouse (E10–E13)
  5. click tracking via the existing shortener (E14)
  6. plain-text part (E17)
  7. then inbound (E23) and the builder (E18)
- **Keep Resend** for Relay's own platform mail. It is already isolated
  (`SMS-BE/internal/mailer/mailer.go:1-7`).
- **Cost to Relay at 1M emails/month:** ~$100 à la carte + ~$70 VDM ≈ **$170/month** (≈ ₹14k, FX
  estimate). Competitive retail in India is often ₹0.02–0.10 per email [U], so the margin is large.

---

## 6. Voice

### 6.1 Regulatory reality (why "build our own" is the wrong answer)

- Domestic Indian voice needs Indian numbers. Those are only for India-registered businesses after
  KYC. Media must be anchored in India, so both call legs originate and terminate in India.
  Promotional calls must come from the 140 series. BFSI service calls must come from 1600 series.
  Landline series are for service and transactional calls only. Using the wrong series is itself a
  violation. Sources: Plivo India calling [V]; TRAI PR 91/2026 [V].
- PSTN interconnect is a licensed activity. The 2021 OSP liberalisation helps call centres, not
  unlicensed platforms selling PSTN access. Sources: AZB [V], Khaitan [V].
- So Relay should **resell a licensed Indian CPaaS or TSP**: Exotel, Plivo (India data region), Tata
  Tele Business, or Airtel IQ. It should not run Asterisk or FreeSWITCH as a PSTN gateway.
  Asterisk/FreeSWITCH remain fine for in-app logic (IVR trees, recording) behind a licensed SIP trunk
  [U, needs legal confirmation].

### 6.2 Integration options

| Option | What it is | Cost | India | Time to live | Notes |
|---|---|---|---|---|---|
| **Exotel** | Indian cloud telephony (ExoPhones, IVR "Flows", click-to-call, masking, voice OTP, AMD [U]) | Prepaid bundles (₹9,999/5 months Dabbler, etc.); per-minute ~₹0.50–1.50; pulse-based billing [U] | Yes, India-native | Days to 2 weeks (KYC + number allocation) | Number types FAQ https://developer.exotel.com/docs/faqs/number-types [V]. API https://developer.exotel.com/api [D] |
| **Plivo (India data region)** | Global CPaaS with India domestic routes after KYC | Per minute [U] | Yes (India-registered businesses) | 1–2 weeks | 140-series currently unavailable at Plivo; 160 only for BFSI [V] |
| Twilio | Global | — | Outbound to India reportedly only from non-Indian numbers [U] | — | Not suitable for domestic India |
| Telnyx / Vonage | Global SIP + API | — | Limited India domestic [U] | — | International only |
| Airtel IQ / Tata Tele Business SIP trunk | TSP-provided SIP trunk / CPaaS | Contract | Yes | Weeks–months | Needs a commercial relationship. Best long-term margin. |
| Asterisk / FreeSWITCH self-hosted | Open-source PBX/media | EC2 + trunk | Only behind a licensed trunk | Months | Ops burden on a 2-vCPU box; regulatory risk |
| WhatsApp Calling API | In-WhatsApp voice | User-initiated free; business-initiated per duration [V] | India availability reported [U] | After the WhatsApp build | See W30 |

### 6.3 Feature checklist (Voice)

Status note: voice sends go to the sandbox (`SMS-BE/internal/connector/smpp.go:802`). The only
voice-specific backend code is caller-ID "verification", which returns the code in the HTTP
response. No call is placed:

- `SMS-BE/internal/api/senders.go:232-254`: `return gen.RequestVoiceCall200JSONResponse(gen.VoiceCallResult{Code: code})`

| # | Feature | Official reference | Relay status | Proof | Effort |
|---|---|---|---|---|---|
| V1 | Provider integration (outbound call API + status callbacks) | Exotel API https://developer.exotel.com/api [D] | **absent** | `SMS-BE/internal/connector/` has no voice connector (file list: smpp, rcs_*, sandbox) | M |
| V2 | Number provisioning: virtual numbers (DID/landline series), toll-free 1800, 140-series (promo), 1600-series (BFSI) | Exotel number types [V]; TRAI PR 91/2026 [V]; Plivo [V] | **absent** | — | M + XL external (KYC, series allocation) |
| V3 | Number-series compliance gate (promo → 140 only; BFSI service → 1600) | TRAI PR 91/2026 [V] | **absent** | — | S |
| V4 | Caller-ID verification for BYO numbers (real call or SMS) | — | **partial, insecure.** Code returned in the API response, so the "verification" proves nothing. | `SMS-BE/internal/api/senders.go:232-254`; contract `SMS-BE/openapi/control.json:14152-14171`; UI `SMS-UI/src/app/(dashboard)/senders/voice-verify-row.tsx` | S (place a real TTS call via V1; never return the code) |
| V5 | TTS with Indic languages (Hindi, Tamil, Telugu, Kannada, Marathi, Bengali, Gujarati, Malayalam, Punjabi, Odia) | Amazon Polly languages https://docs.aws.amazon.com/polly/latest/dg/supported-languages.html [D] (Hindi + Indian English only [U]); Google Cloud TTS https://cloud.google.com/text-to-speech/docs/voices [D]; Sarvam bulbul https://docs.sarvam.ai [D] | **absent**. Template has a "Call script" preview only. | `SMS-UI/src/app/(dashboard)/templates/new/voice-preview.tsx:1-25`; `template-editor.tsx:1105-1124` | M |
| V6 | Pre-recorded audio upload (WAV/MP3) | Provider docs [D] | **absent** | — | S (reuse media store `SMS-BE/internal/api/media.go:56`) |
| V7 | IVR / DTMF menus, gather input, branching | Exotel Flows / Plivo GetDigits https://www.plivo.com/docs/voice/xml/getdigits [D] | **absent** | — | L |
| V8 | Voice OTP (TTS digit read-out, repeat) inside Verify | Twilio Verify voice channel https://www.twilio.com/docs/verify/api [D] | **partial**. Verify supports channel fallback order, but VOICE resolves to the sandbox. | `SMS-BE/internal/api/verify.go:58-59,208` | S (after V1, V5) |
| V9 | Click-to-call (bridge agent + customer) | Exotel connect two numbers [D] | **absent** | — | S |
| V10 | Number masking (virtual number between two parties) | Exotel ExoBridge / masking [D] | **absent** | — | M |
| V11 | Answering machine detection | Twilio AMD https://www.twilio.com/docs/voice/answering-machine-detection [D]; Exotel AMD [U] | **absent** | — | S (provider flag) |
| V12 | Call status webhooks (initiated, ringing, answered, completed, busy, no-answer, failed) + duration | Provider docs [D] | **absent** | — | S |
| V13 | Per-pulse billing (15 s / 30 s / 60 s pulses) and per-minute rate cards | Exotel pulse billing [U] | **absent**. Rates are per message/category. | `SMS-BE/internal/api/operator.go:708` | M |
| V14 | Call recording + storage + retention + consent announcement | DPDP Act [D] | **absent** | — | M |
| V15 | Inbound calls: IVR, missed-call campaigns (very common in India) | Exotel [D] | **absent**. UI registry marks Voice `twoWay: false`. | `SMS-UI/src/lib/registries/channels.ts:167` | M |
| V16 | Bulk voice broadcast (OBD) with concurrency limits, retry on no-answer, calling-hours window | TCCCPR [D] | **absent** | — | M |
| V17 | DND / preference check for promotional calls (140-series traffic is DLT-scrubbed) | TRAI PR 80/2024 (140-series on DLT from 1 Oct 2024) [V] | **absent** | — | S (same "operator scrubs" model as S16) |
| V18 | SIP trunk / BYOC for enterprises | Plivo SIP trunking India https://www.plivo.com/docs/voice-agents/sip-trunking/deploy/calling-in-india [V via search] | **absent** | — | L |
| V19 | AI voice agents (streaming audio to an LLM/STT/TTS) | Plivo/Exotel streaming [D] | **absent** | — | L (market pull is high [U]) |
| V20 | Caller-ID name branding (truecaller-style CNAP) | TRAI CNAP rollout [U] | **absent** | — | n/a (operator-side) |

### 6.4 Recommendation (Voice)

- **Exotel first.** It is India-native, owns ExoPhones and number series, supports pulse billing, and
  its IVR, voice OTP and masking all match Indian demand. **Plivo India region** is the second
  source: better API ergonomics, but 140-series is unavailable [V].
- **Start with the smallest sellable voice product (M, about 3 weeks):**
  - outbound TTS/voice OTP (V1, V5, V8, V12)
  - a real caller-ID verification call (V4)
  - per-pulse rating (V13)
- **Then:**
  - click-to-call and masking (V9, V10)
  - IVR (V7)
  - OBD with 140-series compliance (V2, V3, V16)
- TTS: Polly covers Hindi and Indian English [U]. For wider Indic coverage use Google Cloud TTS or
  Sarvam (the team already uses Sarvam bulbul for audio updates).
- Do **not** self-host Asterisk/FreeSWITCH against the PSTN.
- **Cost:** about ₹0.50–1.50 per minute plus number rental [U]. Get Exotel and Plivo quotes.

---

## 7. Candidate extra channels

| Channel | Status Oct 2026 | Integration path | India relevance | Cost | Effort for Relay | Recommendation |
|---|---|---|---|---|---|---|
| **Apple Messages for Business** | Live globally. Needs Apple Business Register + an approved **Messaging Service Provider (MSP)**; no direct access; Apple doesn't charge [V via Bird and Microsoft guides] | Become an MSP, or partner with one. https://register.apple.com/resources/messages/messaging-documentation [D] | Low–medium (iPhone share in India is small but high-value). MSG91 already offers it (shared context). | Free from Apple; MSP engineering | L (MSP approval XL) | Later. Watch Apple's **RCS** route via Jio, which reaches iPhones without an MSP role. |
| **Viber Business Messages** | Live. Billed per delivered transactional/promotional message; conversational sessions 24 h / 60 messages; €9.99 setup per ID via resellers; domestic vs international rates since Oct 2024 [V] https://forbusiness.viber.com/en/business-messages | Via aggregator (Infobip, 8x8) | Very low in India (no India data found [V]) | Per message | M | Skip for India. Only if a corridor customer (e.g. Eastern Europe, Philippines) asks. |
| **Telegram Gateway (OTP)** | Live. **$0.01 per delivered code**; undelivered refunded per the Gateway page, but the ToS says no refunds for non-delivery (conflict, [U]); E.164; user must be on Telegram [V] https://core.telegram.org/gateway ; ToS https://telegram.org/tos/gateway | Simple HTTPS API (`sendVerificationMessage`, `checkSendAbility`, which itself is charged) | Medium (large Indian Telegram base [U]) | $0.01/code (≈₹0.84) | S | **Add as a Verify channel.** In India it costs more than SMS OTP (~₹0.84 vs ₹0.12–0.20), so put it after SMS for domestic numbers and before SMS for international numbers, where SMS can cost up to 50× more [V]. |
| **Telegram Bot API (conversational)** | Free | Bot API https://core.telegram.org/bots/api [D] | Medium | Free | M | Optional inbox channel. |
| **Instagram / Messenger** | Live. Instagram Messaging API and Messenger Platform; 24 h standard window; human-agent tag for 7 days [D] https://developers.facebook.com/docs/messenger-platform ; https://developers.facebook.com/docs/instagram-platform/instagram-api-with-instagram-login/messaging-api [D] | Same Meta app as WhatsApp (shared Tech Provider work) | Medium for D2C brands | Free (no per-message fee [D]) | M each after WhatsApp | **Second wave after WhatsApp.** It reuses the Meta app, webhooks and token storage. |
| **Web push / app push (FCM, APNs, Web Push)** | Live, free transport | FCM https://firebase.google.com/docs/cloud-messaging [D]; APNs https://developer.apple.com/documentation/usernotifications [D]; Web Push RFC 8030 https://www.rfc-editor.org/rfc/rfc8030 [D] | High for apps (CleverTap, MoEngage and WebEngage lead here, and Relay already integrates their webhook formats) | Free transport; SDK work | L (needs SDKs, which Relay lacks per shared context) | Defer. Relay's position is as the SMS/RCS/WA pipe *for* those CDPs, not competing on push. |
| **Google Business Messages** | **Shut down 31 Jul 2024** (no new conversations after 15 Jul 2024) [V] https://developers.google.com/business-communications/business-messages/resources/release-notes/update-on-gbm | — | None | — | — | Do not build. Remove from any roadmap or marketing. |
| **WhatsApp Calling** | See W30 | — | — | — | — | Bundle with the Voice roadmap after WhatsApp is live. |

---

## 8. Consolidated gap list, ordered by value ÷ effort

| Order | Item | Channel | Effort | Why now |
|---|---|---|---|---|
| 1 | Mark WhatsApp/Email/Voice as "sandbox" in the UI until real | All | S | The UI currently implies they are live (`SMS-UI/src/lib/registries/channels.ts:114-167`). |
| 2 | "Operator scrubs on DLT" mode as India default for promotional traffic | SMS | S | Every Indian promo SMS is refused today (`SMS-BE/internal/sending/dnd.go:38-43`). |
| 3 | Remove code-in-response from voice caller-ID verification | Voice | S | Security / integrity (`SMS-BE/internal/api/senders.go:254`). |
| 4 | Fix WhatsApp tier enum (2K, unlimited, per portfolio) and CSW-from-inbound | WhatsApp | S + M | The UI shows wrong numbers (`SMS-BE/openapi/control.json:14176-14180`; `SMS-BE/internal/store/audience.go:64`). |
| 5 | Start Meta Tech Provider registration (calendar-bound) | WhatsApp | XL external | Long external wait; start immediately. |
| 6 | SES identities + real DKIM/SPF + send + RFC 8058 + bounce/complaint pipeline | Email | L | Cheapest channel to make real; same AWS region. |
| 7 | Telegram Gateway as a Verify channel | Extra | S | $0.01/code [V]. Cheap for international OTP, not for India (see Appendix E). |
| 8 | RCS carousel + full suggestion set + typing | RCS | M | Matches aggregator feature lists. |
| 9 | Aggregator SMPP failover + MNP routing + HLR | SMS | M + M | Resilience; single-operator risk. |
| 10 | Fraud writers behind `fraud_flag` (country allow-list, velocity, OTP conversion) | SMS/Verify | M | AIT is the top CPaaS loss vector [V Twilio]. |
| 11 | WhatsApp Cloud API core (signup, send, webhooks, templates) | WhatsApp | L | Biggest revenue channel in India. |
| 12 | Exotel minimum voice product (TTS/voice OTP, status, pulse billing) | Voice | M | Completes Verify fallback chain. |
| 13 | DLT URL/callback whitelisting check + short-domain whitelisting | SMS | M | Operator rejections otherwise (TRAI PR 80/2024 [V]). |
| 14 | Jio + Vi direct RCS live | RCS | M each | iPhone reach via Jio [V]. |
| 15 | WhatsApp carousel, Flows, payments, MM API; Instagram/Messenger | WhatsApp+ | L each | Second-wave parity. |
| 16 | Email builder (MJML), inbound parse, VDM dashboards | Email | L | Parity with ESPs. |
| 17 | IVR, masking, OBD with 140/1600 compliance | Voice | L | Indian voice market staples. |

---

## 9. Recommended vendor per channel (summary)

| Channel | Pick | Second source | Reasoning | Indicative unit cost to Relay |
|---|---|---|---|---|
| SMS | Direct operator SMPP (existing) | One tier-1 Indian aggregator over SMPP | Margin plus redundancy; code already supports multi-bind | ₹0.12–0.20/SMS [U]; quote needed |
| RCS | Trustsignal (live) | Jio direct (iPhone reach), Vi via Dotgo | Proven path now; direct later for margin and iPhone | Jio iPhone: ₹0.25–0.30 promo/transactional, ₹0.11–0.16 OTP [V]; Airtel reported ₹0.11 [U] |
| WhatsApp | Meta Cloud API as Tech Provider (Embedded Signup) | 360dialog for interim / overflow | No markup; tenant pays Meta; INR billing via Meta | Meta rate: marketing ~₹0.86, utility/auth ~₹0.115 [U]; service free [V] |
| Email | Amazon SES ap-south-1 with tenants (+VDM later) | Postmark for premium transactional tenants [U] | Same region and account; per-tenant reputation isolation built in | $0.10/1,000 (+$0.07 VDM) [V] |
| Voice | Exotel | Plivo India region | India-native licensing and number series; Relay must not interconnect PSTN itself | ~₹0.50–1.50/min + rentals [U] |
| OTP extra | Telegram Gateway | — | International OTP and pumping resistance; dearer than Indian SMS | $0.01/code [V] |

---

## 10. Sources

Access date for all sources: 15 Oct 2026.

**Regulation (India)**
- TRAI PR 80/2024 (URL/APK whitelisting; 140-series to DLT): https://trai.gov.in/sites/default/files/PR_No.80of2024.pdf [V]
- TRAI PR 91/2026 (1600 / 140 series clarification): https://trai.gov.in/sites/default/files/2026-07/PR_No91of2026.pdf [V]
- NSE circular on 1600 series: https://nsearchives.nseindia.com/content/circulars/COMP74037.pdf [V]
- TCCCPR 2018: https://www.trai.gov.in/sites/default/files/RegulationUcc19072018.pdf [D]
- OSP 2021 summaries: https://www.azbpartners.com/bank/dot-issues-new-guidelines-for-osps/ [V]; https://khaitanco.com/thought-leaderships/DoT-Re-vision-to-the-OSP-framework [V]
- TRAI interconnection consultation responses, Dec 2025: https://www.trai.gov.in/sites/default/files/2025-12/RJIL_16122025.pdf [V]
- Plivo India calling rules: https://www.plivo.com/docs/voice/concepts/india-calling [V]
- DLT CTA whitelisting how-to (vendor): https://msg91.com/help/dlt-registration-in-india/how-to-whitelist-urls-and-callback-numbers-on-dlt [U]

**WhatsApp / Meta**
- Pricing: https://developers.facebook.com/docs/whatsapp/pricing [V]
- Messaging limits (portfolio): https://developers.facebook.com/documentation/business-messaging/whatsapp/upcoming-messaging-limits-changes/ [V]
- Multi-partner solutions: https://developers.facebook.com/documentation/business-messaging/whatsapp/solution-providers/multi-partner-solutions/ [V]
- Marketing Messages API onboarding: https://developers.facebook.com/documentation/business-messaging/whatsapp/marketing-messages/onboarding/ [V]
- Calling pricing: https://developers.facebook.com/documentation/business-messaging/whatsapp/calling/pricing/ [V]
- Payments India (partner mirror): https://docs.360dialog.com/partner/messaging/payments-india-only/receive-whatsapp-payments-via-payments-gateway [V]
- Feature docs listed in §4.2 marked [D]
- India rate trackers: https://whautomate.com/whatsapp-business-api-pricing-india [U]; https://instantdm.com/blog/whatsapp-business-api-pricing-2026-india-october-update [U]
- Gupshup fee change: https://support.gupshup.io/hc/en-us/articles/53700207494169-Gupshup-fee-changes-to-Cloud-API-marketing-w-e-f-1st-Jan-2026 [V title / U detail]
- 360dialog pricing: https://docs.360dialog.com/docs/360dialog/prices-plans-and-payment-options [U]

**RCS**
- RBM billing FAQ: https://developers.google.com/business-communications/rcs-business-messaging/guides/learn/rbm-billing-faq [V via excerpt]
- Vonage RCS billing (bytes rule): https://developer.vonage.com/en/messages/concepts/rcs-billing [V]
- Jio iPhone RCS pricing: https://inc42.com/buzz/jio-introduces-new-rcs-business-messaging-rates-for-iphone-users-in-india/ [V]; https://www.angelone.in/news/market-updates/reliance-jio-apple-start-new-rcs-pricing-for-business-messages-in-india [V]
- Airtel–Google: https://storyboard18.com/digital/airtel-google-join-forces-to-tackle-rcs-spam-in-india-with-network-level-filtering-91052.htm [V]; https://telecomtalk.info/airtel-google-to-launch-rcs-messaging-india/1002371/ [V]
- Vi on Google RCS via Dotgo: https://www.voicendata.com/mobile-services/rcs-gains-momentum-in-india-with-airtelgoogle-partnership-10893020 [V]
- Feature docs listed in §3.2 marked [D]

**Email**
- Gmail sender guidelines: https://support.google.com/a/answer/81126 [V]
- Microsoft high-volume sender rules: https://techcommunity.microsoft.com/blog/microsoftdefenderforoffice365blog/strengthening-email-ecosystem-outlook%E2%80%99s-new-requirements-for-high%E2%80%90volume-senders/4399730 [V via excerpts]
- SES pricing: https://aws.amazon.com/ses/pricing/ [V]
- SES tenant management: https://aws.amazon.com/about-aws/whats-new/2025/08/amazon-ses-tenant-isolation-automated-reputation-policies [V]; https://docs.aws.amazon.com/ses/latest/dg/tenants.html [V]
- RFCs 8058 / 6376 / 7208 / 7489 and BIMI: [D] as linked in §5.2

**Voice**
- Exotel number types: https://developer.exotel.com/docs/faqs/number-types [V]
- Exotel pricing (third-party): https://caller.digital/blog/exotel-alternatives-india-2026 [U]; https://www.cloudtalk.io/blog/exotel-pricing/ [U]

**Fraud**
- Twilio Verify Fraud Guard: https://www.twilio.com/docs/verify/preventing-toll-fraud/sms-fraud-guard [V]
- Twilio Geo permissions: https://www.twilio.com/docs/verify/preventing-toll-fraud/verify-geo-permissions [V]

**Extra channels**
- Telegram Gateway: https://core.telegram.org/gateway [V]; ToS https://telegram.org/tos/gateway [V]
- Viber Business Messages: https://forbusiness.viber.com/en/business-messages [V]
- Apple Messages for Business (third-party guides): https://bird.com/explained/apple-messages/how-does-a-business-get-set-up-on-apple-messages-for-business [V]; https://learn.microsoft.com/en-us/dynamics365/customer-service/configure-apple-messages-for-business-channel [V]
- Google Business Messages shutdown: https://developers.google.com/business-communications/business-messages/resources/release-notes/update-on-gbm [V]

## Appendix A — Greps run for "absent" claims (SMS-BE, `internal/` and `cmd/`, excluding `_test.go`)

| Pattern | Result |
|---|---|
| `graph.facebook\|wamid` | No match in connector or sending |
| `List-Unsubscribe` | No match |
| `bounce` / `complaint` | No match outside unrelated comments |
| `validity_period` / `ValidityPeriod` in connector | No match |
| `flash\|esm_class\|message_class` | Demo-seed copy only (`internal/demoseed/seed.go:672`) |
| `hlr\|mnp\|portab` | Demo-seed price row only (`internal/demoseed/seed.go:831`) |
| `least.cost\|lcr` | No functional match |
| Writers of `fraud_flag` other than default | Demo seed only (`internal/demoseed/seed.go:1515,1644`) |
| `carousel` | Capability constant and generated enum only (`internal/connector/rcs_capability.go:48`, `internal/gen/api/control.gen.go:1430`) |
| Voice connector | None. `internal/connector/` holds smpp, rcs_*, sandbox, registry, connector |
| `dtmf\|ivr\|tts\|recording` in api/sending/connector | No functional match |

## Appendix B — Assumptions to confirm before acting

1. **Operator DND scrubbing.** That access providers scrub promotional SMS preferences on DLT, so a TM-side DND register is optional, is from TCCCPR 2018 [D]. Confirm with the VIDEOCON operator contact before switching the default.
2. **Meta India rates.** These are from third-party trackers [U]. Pull the official INR rate card.
3. **Aggregator, voice and email vendor prices.** All [U]. Get written quotes.
4. **Licensing for reselling voice.** Reselling Exotel/Plivo voice to tenants under Relay's brand may need a reseller agreement and possibly a DoT authorisation under the Telecommunications Act 2023. This is [U] and needs legal review.
5. **Airtel commercial RCS launch.** Unconfirmed [V that only a partnership was announced].

---

## Appendix C — Implementation blueprints (how each missing channel plugs into Relay)

Everything below is a **proposal**, grounded in the seams that already exist. Nothing here is built.

### C.0 The seam every channel uses

- **The connector contract is three methods.**
  - `SMS-BE/internal/connector/connector.go:106-109`: `Name() string`, `Submit(ctx, []Submission) ([]Receipt, error)` and `Health(ctx) Health`.
  - `Submission` (`connector.go:14-76`) already carries channel, msisdn, sender, body, template id/variables, agent id, carrier, DLT ids, `Priority`, `Promotional` and `TTLSeconds`.
- **The registry picks a connector per channel.**
  - `SMS-BE/internal/connector/registry.go:31-36` and `:57`: `Registry.ByChannel[CHANNEL]` is the dedicated gateway, and anything without one falls back to `Default`, which is the sandbox.
  - A new channel goes live by registering a connector under `ByChannel["WHATSAPP" | "EMAIL" | "VOICE"]`.
  - The send pipeline, wallet hold/refund, campaigns, journeys and webhooks stay untouched. `service.go:185` (`carrierFor`) already resolves through it.
- **Delivery and inbound flow back through `DeliveryReport` and `InboundSMS`** (`connector.go:115,148`).
  - Email needs an `email` field next to `Msisdn`. Messages already store `email` (`SMS-BE/internal/store/messages.go:96`).
  - WhatsApp needs a wamid as `CarrierRef`.
- **The SMPP router keeps its job.** `smpp.go:802` sends non-SMS to `Fallback`, and stays correct once the registry gives each channel its own connector, because `For` checks `Dedicated` first (`registry.go:32`).

Lazy path: three new connector files, three webhook receivers, two migrations. No new abstraction layer.

### C.1 WhatsApp connector (Meta Cloud API)

| Piece | Proposal | Reuses |
|---|---|---|
| Credentials | `wa_accounts` table: tenant_id, waba_id, phone_number_id, display_phone, business_portfolio_id, access_token (encrypted, the same way SMPP passwords are stored), token_expires_at, currency, messaging_limit, quality_rating, status. RLS like the other 55 tables. | RLS pattern in `db/migrations/*` |
| Signup | `POST /v1/whatsapp/embedded-signup/complete` receives `code`, `waba_id` and `phone_number_id` from the FB JS SDK popup. The server exchanges `code` for a business token, subscribes the app to the WABA (`POST /{waba-id}/subscribed_apps`), then registers the number (`POST /{phone-number-id}/register` with PIN). | Sender record fields `wabaId`, `displayName` (`SMS-BE/internal/api/senders.go:28-29`) |
| Send | `internal/connector/whatsapp.go`: `POST https://graph.facebook.com/vXX.X/{phone-number-id}/messages`, body type `template` (name, language, components) or `text` / `interactive` / `image` / … The returned `messages[0].id` (wamid) becomes the Receipt carrier ref. | `Submission.TemplateVariables`, `CarrierTemplateID` |
| Template sync | Create via `POST /{waba-id}/message_templates`; poll and listen for `message_template_status_update`, `template_category_update` and `message_template_quality_update`. Map to Relay statuses pending/approved/rejected/paused/disabled. | Status mapping style in `SMS-BE/internal/connector/rcs_template.go:94-100,258-265` |
| Webhook | `GET /webhooks/whatsapp` (hub.verify_token challenge) and `POST /webhooks/whatsapp` (verify `X-Hub-Signature-256` HMAC with the app secret). Fields: `messages` (inbound + statuses), `message_template_status_update`, `phone_number_quality_update`, `business_capability_update`, `account_update`. | Mount next to RCS webhooks, `SMS-BE/internal/api/carrier_webhooks.go:38` |
| Status mapping | sent → submitted; delivered → delivered; read → delivered + read (as RCS does, `rcs_webhook.go:62-67`); failed + error code → failed with class. Meta error 131047 = re-engagement required (outside CSW) [D]; 131026 = undeliverable [D]; 131049 = marketing frequency cap ("ecosystem engagement") [D]. | Error-class columns in messages |
| CSW | `contact_channel_state(tenant_id, contact_id, channel, last_inbound_at)`, written by the inbound webhook. The send gate refuses non-template WhatsApp when `now - last_inbound_at > 24h`. The audience "session active" count reads this instead of `consented_at` (`SMS-BE/internal/store/audience.go:64`). | Inbound path `SMS-BE/internal/api/inbound_sms.go:48` (`FileReply`) |
| Billing | Rate per category per country, already priced per category (`SMS-BE/internal/api/operator.go:708`). Add zero-rate rules for (a) utility inside CSW and (b) any message inside FEP. Charge on `delivered`, not `sent`, to match Meta [V]. | Wallet hold/refund in sending |
| Limits | Store `max_daily_conversations_per_business` from webhooks. Campaign launch warns when audience > remaining limit. | Send-cap machinery `SMS-BE/internal/api/send_cap.go` |
| Opt-out | Inbound "STOP" works through the same suppression (`SMS-BE/internal/store/support.go:501-531`). Also map Meta's user-block signal to suppression. | Same |

Contract changes (UI handoff needed):
- `WaMessagingTier` → `[250, 2000, 10000, 100000, "unlimited"]`, and move it to tenant level (`SMS-BE/openapi/control.json:14176-14180`).
- `WaContent` gets `template` with header/footer/buttons/carousel (`control.json:14489`).
- `Template` gets `language`.
- Drop `TRANSACTIONAL` from the WhatsApp category choices (`control.json:14475-14478`).

Test plan:
- Meta provides a test WABA with a test number and up to 5 recipient numbers for free sends [D].
- Live verification uses Saeed's own handset as recipient.
- Webhook signature tests use a fixed app secret.

### C.2 Email connector (Amazon SES v2, ap-south-1)

| Piece | Proposal | Reuses |
|---|---|---|
| Account setup | One SES account in ap-south-1 (same AWS account as relay-prod). Request production access. Create a configuration set per tenant or use SES **tenants** (`CreateTenant`, `CreateTenantResourceAssociation`) [V docs]. Reputation policy "standard". | — |
| Domain identity | On email sender create, call `CreateEmailIdentity(domain, DkimSigningAttributes{NextSigningKeyLength: RSA_2048_BIT})`. SES returns 3 DKIM CNAME tokens; store them as the DKIM rows. Also call `PutEmailIdentityMailFromAttributes(mail.<domain>)`, giving SPF `v=spf1 include:amazonses.com ~all` on the MAIL FROM subdomain plus an MX record. | Replace placeholders at `SMS-BE/internal/store/compliance.go:226-235`; table `db/migrations/00017_sender_dns_records.sql` |
| Verification | A periodic job calls `GetEmailIdentity` → `DkimAttributes.Status` and `MailFromAttributes.MailFromDomainStatus` → records verified/failed. Delete the dev advance endpoint for production (`SMS-BE/internal/api/dev.go:38,256`). | Scheduler pattern of `LaunchDueCampaigns` (`SMS-BE/internal/api/campaign_scheduler.go:19`) |
| DMARC | Advise `p=none` first with `rua` to a Relay-owned address; parse aggregate reports later. Never default `p=quarantine` on a domain whose other mail streams Relay cannot see (current default at `compliance.go:234` risks a customer's corporate mail). | — |
| Send | `internal/connector/email.go`: SES v2 `SendEmail` with `Content.Simple` (subject, html, text), `ConfigurationSetName`, `TenantName`, `EmailTags` (message_id, tenant_id, campaign_id), and headers `List-Unsubscribe: <https://…/u/{token}>, <mailto:…>` plus `List-Unsubscribe-Post: List-Unsubscribe=One-Click` [V Gmail]. | `SMS-BE/internal/mailer/mailer.go` stays for platform mail only |
| Unsubscribe | `POST /u/{token}` (RFC 8058 one-click: body `List-Unsubscribe=One-Click`, no auth, idempotent) → suppression; `GET /u/{token}` → a confirm page. Token is an HMAC of tenant + contact + list, like the signed media URLs (`SMS-BE/internal/api/media.go:189-219`). | Suppression store |
| Events | Configuration set → EventBridge (or SNS) → SQS → a consumer in relay-api. Map Send→submitted, Delivery→delivered, Bounce(Permanent)→failed + suppress, Bounce(Transient)→retry/ignore, Complaint→suppress + tenant metric, Reject, RenderingFailure, DeliveryDelay, Open, Click → ClickHouse events. | `DeliveryReport` path; ClickHouse `db/clickhouse/001_messages.sql` |
| Tracking | SES open/click tracking with a custom HTTPS tracking domain per tenant (needs a CloudFront/ACM cert) [D], **or** rewrite links through Relay's shortener (`SMS-BE/internal/api/links.go`). The shortener is cheaper and already logs clicks. | links.go |
| Guardrails | Per-tenant hourly cap. Block send when DKIM is not verified. Auto-pause the tenant when SES tenant status goes paused (EventBridge). Bounce rate > 5% or complaints > 0.1% → operator abuse queue. | Abuse queue in the operator console |
| Inbound (later) | SES receiving rule set on `reply.<relay-domain>` → S3 → SQS → inbox thread match via the `Reply-To` token. | Two-way inbox |

Regional notes:
- SES receiving is not offered in every region. **Confirm ap-south-1 supports inbound receiving before promising Email two-way** [U].
- Mail Manager is an alternative [V pricing page lists Mail Manager processing].

### C.3 Voice connector (Exotel first)

| Piece | Proposal | Reuses |
|---|---|---|
| Account | One Exotel account (Relay's KYC). Each tenant's caller ID is either an ExoPhone allocated to Relay and assigned to the tenant, or a tenant-owned number verified by a real call. | Sender `callerIdNumber` (`SMS-BE/internal/store/compliance.go:210`) |
| Send (TTS / OTP) | `internal/connector/voice.go`: Exotel "connect a number to a call flow" (or Plivo `POST /Call/` with an answer URL returning XML `<Speak>`/`<Play>`). The answer URL is a Relay endpoint that renders the template text in the chosen language and voice. | Template body + variables (`SMS-UI/.../voice-preview.tsx` already frames it as a script) |
| TTS | Pre-render to audio with a TTS engine (Polly for hi-IN/en-IN [U], Sarvam or Google for other Indic) and cache in S3 by hash. OTP digits spoken one by one, repeated twice. | Media store |
| Status | Status callback → `DeliveryReport`. completed + duration > 0 → delivered; busy/no-answer/failed → failed with class. Duration is stored for pulse billing. | Webhook emit `SMS-BE/internal/api/webhook_emit.go:189` |
| Billing | New rate unit "pulse": rate per pulse + pulse seconds per route; cost = ceil(duration / pulse) × rate. The hold at submit = one pulse; settle at the status callback. | Wallet hold/refund |
| Caller-ID verification | Place a real call that speaks the code. **Never** return the code in the API response (fix `SMS-BE/internal/api/senders.go:254`). | — |
| Compliance | Sender has a `number_series` (landline/1600/140/tollfree). Campaign gate: `Promotional` → 140 only; a BFSI tenant's service calls → 1600 [V TRAI]. Calling-hours window as for promo SMS. | `Submission.Promotional` (`connector.go:71`); promo window logic `smpp.go:446-456` |
| Later | IVR (`<GetDigits>` → journey branch), masking (Exotel bridge), AMD flag, recording with announcement + retention policy. | Journeys |

### C.4 Telegram Gateway (Verify channel)

| Piece | Proposal |
|---|---|
| Send | `POST https://gatewayapi.telegram.org/sendVerificationMessage` with `phone_number`, `code` (Relay-generated) or `code_length`, `ttl`, `callback_url` [V gateway page; method names D] |
| Pre-check | Skip `checkSendAbility`. It is charged as one message when the user is reachable [V search], so just send and fall back on failure. |
| Status | Delivery report callback → Verify attempt state; on not-delivered within ttl, fall back to SMS (Verify fallback order already exists, `SMS-BE/internal/api/verify.go:58-59`). |
| Billing | $0.01 per code [V]; price in INR with an FX buffer. |
| Effort | S (one connector file + one channel enum value + UI label) |

---

## Appendix D — Competitive feature matrix (what Indian buyers compare)

This is a feature presence check of Relay against what Indian CPaaS buyers expect. The competitor
columns come from public product pages and are **[U]** unless they appear elsewhere in this research
series. Use it for prioritisation, not as a claim about competitors.

| Capability | Expected by Indian buyers | Relay today | Gap size |
|---|---|---|---|
| DLT-native SMS (entity/header/template, chain TLV) | Yes | built | — |
| Promotional SMS actually deliverable | Yes | blocked by default (`dnd.go:38-43`) | Critical, S |
| SMS failover across operators/aggregators | Yes | absent | M |
| HLR / number lookup | Often | absent | M |
| OTP API with multi-channel fallback | Yes | SMS real; others sandbox | M–L |
| WhatsApp templates + campaigns | Yes (table stakes) | sandbox | L |
| WhatsApp inbox + chatbot | Yes | sandbox | M after core |
| WhatsApp Flows / payments | Growing | absent | L |
| RCS with carousel + iPhone (Jio) | Growing fast | single card; Trustsignal only | M |
| Email campaigns with deliverability tooling | Common (bundled) | sandbox, fake DNS | L |
| Voice OTP / IVR / masking / click-to-call | Common | absent | L |
| Missed-call service | Common in India [U] | absent | M |
| Telegram OTP | Rare (differentiator) | absent | S |
| Apple Messages for Business | Rare (MSG91 has it, shared context) | absent | L/XL |
| GST invoices | Mandatory for B2B | absent | M |
| Payment gateway top-up | Expected | absent (manual) | M |
| Sub-accounts / reseller white-label | Common for resellers | backend branding only | L |
| SMPP server for resellers | Common for wholesale | absent | L |

## Appendix E — Cost worksheet (per 1,000 messages, Relay's cost side)

All figures are indicative. [V] marks provider-published numbers, [U] marks third-party or estimated
ones. FX: ₹84/USD [U, the rate moves].

| Channel / unit | Cost basis | Per 1,000 (₹) | Source tag |
|---|---|---|---|
| SMS India (aggregator wholesale) | ₹0.12–0.20 per SMS | 120–200 | [U] |
| RCS India, Jio iPhone marketing | ₹0.27–0.30 | 270–300 | [V] |
| RCS India, Jio iPhone OTP | ₹0.11–0.16 | 110–160 | [V] |
| RCS India, Airtel (reported) | ₹0.11 | 110 | [U] |
| WhatsApp marketing (Meta) | ~₹0.8631 | ~863 | [U] (official INR card not fetched) |
| WhatsApp utility/auth (Meta) | ~₹0.115 | ~115 | [U] |
| WhatsApp utility inside CSW / service | Free | 0 | [V] |
| Email (SES à la carte) | $0.10/1,000 | ~8.4 | [V] |
| Email + VDM | $0.17/1,000 | ~14.3 | [V] |
| Telegram OTP | $0.01/code | ~840 | [V] |
| Voice India (per 1,000 one-minute calls) | ₹0.50–1.50/min | 500–1,500 | [U] |

Observations:

- **Email is cheapest by two orders of magnitude.** Making it real is the highest-margin engineering available (L).
- **Telegram OTP is not cheaper than Indian SMS OTP** at ~₹0.84 versus ₹0.12–0.20 SMS. Its value is international OTP, where SMS can cost "up to 50×" more [V], and pumping-resistant delivery, not domestic price. For India-only tenants, rank it below SMS in the fallback order.
- **Jio iPhone RCS OTP (₹0.11–0.16) is now comparable to SMS** [V], with branding and read receipts.
