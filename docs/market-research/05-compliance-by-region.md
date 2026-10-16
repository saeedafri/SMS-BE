# 05 — Messaging compliance by region: what Relay must build to go international

Researched 15–16 Oct 2026. Every external fact carries a URL and the date it was accessed. Most of the
accessed URLs are vendor or law-firm secondary sources, because regulator PDFs were often unreachable
(TRAI's CMS refused the connection, and its Sept 2026 press release is a scanned image with no text layer).
Facts that rest on a single vendor page, or that sources disagree on, are marked **[unverified]**.
Claims about Relay cite `file:line` in SMS-BE (`internal/…`) or SMS-UI (`src/…`), read on 15 Oct 2026.

---

## 0. Where Relay stands (code baseline)

The compliance engine is a per-country `Regime` interface. The design is sound, but the coverage is thin.

| Capability | Where | State |
|---|---|---|
| Regime registry | `internal/domain/compliance/regime.go:118-123` | IN, US real. GB and AE are stubs. `Countries()` is hard-coded at `regime.go:134`. |
| Registration objects (entity → sender → template, `DependsOn`) | `regime.go:45-56`, `india.go:35-100`, `us.go:25-61` | Forms only. Self-attested, then an operator approves. No regulator or registry API (no TCR or DLT-portal integration; a grep for `campaignregistry` finds nothing outside the seed). |
| Sender-header shape check | `india.go:124-131` (6 alphanumerics) | IN only. US and stubs accept anything (`us.go:18`, `stubs.go:23`). |
| Registered-template-required gate | `india.go:29`, `internal/sending/service.go:962-965` | IN only. |
| CTA URL rule (no shorteners) | `india.go:15,106-118`, enforced on the send path at `service.go:262-275` | IN: a 5-host blocklist, with "extend before production" noted at `india.go:14`. It is a blocklist, not a check against the PE's DLT whitelist. |
| Promotional time window | `india.go:146-170` | **A country branch** (`if country != "IN"`, `india.go:152`), which breaks the package's own "no country branches" rule (`regime.go:1-6`). The error text hard-codes "10:00 and 21:00 IST" (`internal/api/server.go:262-269`). No window for any other country. |
| DND / preference scrub | `internal/sending/dnd.go:13-45, 96-115` | IN only (`+91` prefix check at `dnd.go:99`). The default `NoDNDRegister` refuses every IN promotional message (`dnd.go:39-45`). Any blocked category counts as a full block (`dnd.go:25-27`), because templates carry no category. |
| Entity-registration gate | `internal/domain/messaging/gate.go:308-309` | Built (commit 4417d70). Its code is still outside the contract enum (`gate.go:298`). |
| Opt-out keywords | `internal/store/support.go:501-511` | English only: STOP/UNSUBSCRIBE/CANCEL/END/QUIT/OPTOUT, exact match. **No HELP or START handling**, and no local-language keywords (PARE/SAIR, ARRET, etc.). |
| Consent record | `internal/store/audience.go:265,297-298` | A per-channel **timestamp only**. No source, method, wording, IP, proof artefact or expiry. |
| Message-log retention | `internal/store/retention.go:15-18` | Per tenant, default 90 days. |
| Billing time zones | `internal/domain/billing/postpaid.go:90-93` | IN, AE, US only. |
| UI regime registry | SMS-UI `src/lib/registries/regimes.ts:8, 406-424` | Same 4 ids. **Drift from the backend:** the UI requires an EIN for the TCR brand (`regimes.ts:350-355`) but the backend brand has no EIN field (`us.go:33-37`). UI campaign use cases are `marketing/otp/notifications` (`regimes.ts:384-386`), while the backend has `2fa/account_notification/customer_care/delivery_notification/marketing` (`us.go:50-56`). The GB label says "MMA code" (`regimes.ts:409`), but the MMA is a US short-code body and does not govern the UK. |
| Hosting | ap-south-1 single EC2 (shared context) | India-resident. Nothing in the EU, US or GCC. |
| GST invoice | (absent: grep for GSTIN/IRN finds nothing) | Generic tax only. No GSTIN and no e-invoice IRN. |

**Verdict:** Relay is a credible India engine with a US form-shell. Every other country is either a stub or
unknown. Even the US is not sendable in practice: there is no TCR submission, no toll-free verification, no
HELP keyword and no quiet hours.

---

## 1. India (home market): deep dive

### 1.1 TRAI TCCCPR 2018 and its amendments
- **Second Amendment, notified 12 Feb 2025.** Most of it was in force on notification. The complaint-mechanism parts started 13 Apr 2025. Access providers that fail face financial disincentives of ₹2 lakh for the first instance, ₹5 lakh for the second and ₹10 lakh per instance after that. Complaints no longer require a registered preference. The time to act against unregistered senders dropped from 30 days to 5. Explicit consent for one transaction is valid for 7 days.
  Sources: https://www.storyboard18.com/how-it-works/trai-tightens-rules-against-spam-introduces-provision-of-stricter-penalties-56616.htm (15 Oct 2026), https://www.barandbench.com/amp/story/law-firms/view-point/trais-crackdown-on-spam-calls-and-ai-driven-telemarketing (15 Oct 2026). TRAI PR 11/2025 https://cms.trai.gov.in/sites/default/files/2025-02/PR_No.11of2025.pdf was **unreachable (ECONNREFUSED)**.
- **Header suffixes -P / -S / -T / -G** (promotional, service, transactional, government). These are appended automatically once a template is validated on DLT, and were visible on handsets from about 6–7 May 2025.
  Sources: https://www.medianama.com/2025/05/223-suffixes-commercial-sms-headers-spam/, https://www.tanla.com/udayreddy/trai-mandates-new-message-suffixes-heres-what-you-need-to-know (15 Oct 2026).
- **Number series:** 140 for promotional voice, 1600 for transactional and service voice. The draft Third Amendment dropped the 1600 mandate for all BFSI entities. https://www.fonearena.com/blog/446114/trai-tcccpr-2025-tougher-rules-spam-calls-sms.html, https://www.medianama.com/2026/03/223-trai-two-tier-spam-enforcement-banks-exemption-2026/ (15 Oct 2026).
- **Third Amendment 2026** (gazette dated about 18 Sep 2026; TRAI PR 119/2026 is a scanned image, so these details come from secondary coverage):
  - AI-flagged action: once 5 or more of a sender's CLIs are flagged within 10 days, the first offence bars outgoing services for 15 days and a repeat brings 1-year disconnection plus blacklisting.
  - **A2P voice senders must declare their CLI ranges in advance, recorded on DLT.** Undeclared A2P calls count as UCC. These norms take effect after 60 days.
  - Login, reset and 2FA OTPs are classed as *service* messages.
  - "BLOCK PROMO" to 1909 replaces FULLY BLOCK within 15 days.

  Sources: https://upstox.com/news/business-news/latest-updates/trai-tightens-spam-rules-when-can-telcos-block-callers-and-disconnect-numbers/article-201041/, https://www.trai.gov.in/sites/default/files/2026-09/PR_No119of2026.pdf (16 Oct 2026). **[partly unverified: there is no text layer, and the effective dates are inferred as notification + 30/60/90 days]**
- **Preference categories:** the 2018 framework blocks by commercial category. The 2026 draft proposed sender classification by sector, with no published list. **[unverified which categories survive in the final text]**

### 1.2 URL / APK / OTT / call-back whitelisting
- Since 1 Oct 2024, SMS carrying URLs, APKs or OTT links that are not whitelisted on DLT is blocked. **Call-back number whitelisting was deferred** and no date has been fixed. Variables carrying non-whitelisted URLs fail scrubbing. https://trai.gov.in/sites/default/files/PR_No.60of2024.pdf, https://status.telesign.com/incidents/gp2thnxhyx43, https://www.fast2sms.com/help/?p=14981 (15 Oct 2026).
- **Relay gap:** `india.go:15` blocks 5 shortener hosts, but nothing models a per-PE whitelist. A non-shortener URL that is not whitelisted is accepted and silently dropped at the operator.

### 1.3 Digital Consent Acquisition (DCA)
- This is a TRAI–RBI pilot with 9 TSPs and 11 banks, using a 127xxx short code and recording consent on the DLT ledger. SMS notifications started in Dec 2025, and completion was targeted for Feb 2026. **No nationwide launch was found as of Oct 2026 [unverified].**
  https://www.medianama.com/2025/10/223-digital-consent-platform-trai-rbi-february-2026/, https://tele.net.in/trai-to-conclude-digital-consent-pilot-by-february-2026/ (15 Oct 2026).
- **Platform implication:** when DCA goes live, a promotional send must be scrubbed against ledger consent, not tenant-held consent. Relay needs a `ConsentRegister` adapter beside `DNDRegister` (`dnd.go:31-33`).

### 1.4 DPDP Act 2023 + DPDP Rules 2025
- Notified on 13 or 14 Nov 2025 (sources differ).
  - Board provisions apply immediately.
  - **Consent Manager registration applies at 12 months (about Nov 2026).**
  - Notice, verifiable consent, security safeguards, 72-hour breach reporting and erasure apply at 18 months (**about 13–14 May 2027**).

  https://www.jsalaw.com/?p=55573, https://compliancehub.wiki/india-dpdp-consent-manager-november-2026-phase-two-deadline-compliance/ (15 Oct 2026). The breach-reporting window is from memory of the Rules text **[unverified here]**.
- Relay is a *data processor* for tenants and a *data fiduciary* for its own users. It needs: a DPA template, erasure on request (only list delete exists, `internal/api/audience.go:200`), and per-purpose consent with a notice reference. Today only a timestamp is stored (`store/audience.go:265`).

### 1.5 RBI: OTP and authentication
- The Authentication Mechanisms for Digital Payment Transactions Directions 2025 were issued 25 Sep 2025, with compliance due **1 Apr 2026**. 2FA is required and at least one factor must be dynamic. **SMS OTP is not discontinued.** Issuers bear the loss if they do not comply.
  https://website.rbi.org.in/web/rbi/-/press-releases/rbi-issues-directions-on-framework-on-authentication-mechanisms-for-digital-payment-transactions (15 Oct 2026).
- What this means for Relay's sales and engineering: banks will ask for OTP delivery SLAs, fallback channels and fraud signals. Relay's Verify API with fraud analytics is the right base. A "silent/SIM-binding" or RCS/WhatsApp OTP fallback becomes a differentiator.

### 1.6 CERT-In Directions (28 Apr 2022)
- Report listed incidents **within 6 hours**. Keep ICT logs **for a rolling 180 days within Indian jurisdiction**. Sync to NIC/NPL NTP. These were verified from the PDF text via pdftotext: https://www.cert-in.org.in/PDF/CERT-In_Directions_70B_28.04.2022.pdf (16 Oct 2026).
- **Relay gap:** there is no documented incident-response runbook or CERT-In point of contact, and no evidence of 180-day log retention for system logs. Message-log retention defaults to 90 days (`retention.go:15-18`), which is a different log.

### 1.7 Data localisation
- The new authorisation rules under the Telecommunications Act 2023 (notified about 24 Jun 2026) require *authorised entities* to keep telecom data and logs in India. https://www.drishtiias.com/daily-updates/daily-news-analysis/dot-notifies-new-telecom-authorisation-norms (15 Oct 2026). Whether a CPaaS needs authorisation is **unresolved [unverified]**.
- Relay is hosted in ap-south-1, so it already complies in India. Going abroad, this becomes a **constraint**: Indian traffic data must stay in India, so international regions need separate data planes, not one global database.

### 1.8 GST e-invoicing
- B2B e-invoice (IRN) is mandatory above ₹5 crore AATO (since 1 Aug 2023). Taxpayers at or above ₹10 crore must report to the IRP within 30 days (since 1 Apr 2025).
  https://vyaparapp.in/blog/e-invoicing-applicability-turnover-limit-india/, https://taxguru.in/?p=1089104 (15 Oct 2026). A claim of a new 2026 threshold change (sansalegal) is **[unverified, conflicting]**.
- **Relay gap:** invoices carry no GSTIN, HSN/SAC, place of supply or IRN/QR. Enterprise buyers cannot claim ITC on a non-compliant invoice, which is a sales blocker even before going international.

### 1.9 OSP / voice
- OSP registration was abolished (Nov 2020 / Jun 2021) and non-voice entities are no longer DoT-regulated. https://fr-help.mypurecloud.com/wp-content/uploads/2024/01/Genesys-Cloud-CX-India-deck-v1.5.7.pdf (15 Oct 2026).
- Voice is affected by the Third Amendment's A2P CLI declaration on DLT. Relay's voice is sandbox-only, so this is future work: the CLI-range registration object goes into `india.go`.

### 1.10 What the platform must build for India (gaps, prioritised)
1. A **DND/NCPR register integration** (an operator scrub API or licensed provider). Without it, 100% of IN promotional traffic is refused (`dnd.go:39-45`).
2. A **template commercial category** (and a preference-category match), replacing "any block = blocked" (`dnd.go:25-27`).
3. A **per-PE CTA whitelist** registry object, with a send-time match (URL, APK, OTT, and later call-back numbers).
4. **Suffix awareness:** store the header type and show the -P/-S/-T/-G preview. Operators append it, but tenants should see it.
5. A **DLT CLI-range object** for A2P voice (Third Amendment).
6. A **GST e-invoice** (GSTIN, SAC 998413/998414 **[verify SAC]**, IRN via a GSP).
7. A **DPDP pack**: a DPA, erasure, consent provenance, a breach runbook, and a 6-hour CERT-In process.
8. A **DCA adapter** when the rollout is announced.

---

## 2. United States

| Topic | Rule | Source (accessed 15 Oct 2026) |
|---|---|---|
| 10DLC | Brand registration about $4–4.50. Auth+ verification $12.50 (effective 7 Aug 2025). Campaign vetting about $15 each, charged again on resubmit. Monthly fee by use case: about $1.50 low-volume, $10 standard, $30 agents/franchise, with a 3-month minimum. Unregistered traffic is surcharged or blocked. | https://support.bcmone.com/docs/10dlc-registration-fees-and-non-compliance-fines, https://api.support.vonage.com/hc/en-us/articles/360058158511 |
| Carrier per-message fees | About $0.003–0.005 per segment **[unverified: agency blog]** | https://www.ghlscaleup.com/blog/a2p-10dlc-fees-explained |
| Toll-free | Verification is mandatory, and unverified numbers are blocked to US and Canada. **A BRN (EIN/CBN) is required on new submissions from 17 Feb 2026** (SignalWire says 1 Jan 2026; the sources conflict). | https://www.bandwidth.com/support/en/articles/13145815-updated-toll-free-verification-brn-field-requirements-on-february-17-2026, https://twilio.com/docs/api/errors/30445 |
| Short codes | A separate carrier-approval programme. The CTIA rules apply. | (general; see the CTIA row) |
| TCPA consent | The FCC's one-to-one consent rule was **vacated** by the 11th Circuit (Jan 2025). Revocation by "any reasonable means" (STOP, QUIT, END, REVOKE, OPT OUT, CANCEL, UNSUBSCRIBE) took effect 11 Apr 2025, and must be honoured within **10 business days**. The "revoke across all message types" part was delayed to 11 Apr 2026, and an FNPRM (Oct 2025) may revise it. | https://www.venable.com/insights/publications/2025/01/eleventh-circuit-overrules-fccs-one-to-one, https://www.bclplaw.com/en-US/events-insights-news/tcpa-update-the-fcc-delays-portions-of-revocation-of-consent-rules-by-one-year.html |
| Quiet hours | Federal 8am–9pm recipient local time. Florida FTSA and Oklahoma: **8am–8pm**, with a cap of 3 contacts per 24h (FL). The Maryland status is **[unverified]**. | https://www.compliancepoint.com/marketing-compliance/floridas-mini-tcpa-amended/, https://justcall.io/blog/state-mini-tcpa-laws.html |
| CTIA | Opt-in confirmation (brand, frequency, "msg & data rates", HELP/STOP). **HELP must be answered.** One STOP confirmation, then silence. | https://support.telnyx.com/en/articles/8173789-short-code-best-practices, https://bird.com/explained/compliance/what-are-the-ctia-messaging-principles |
| STIR/SHAKEN | Third-party signing is allowed, but the obligated provider makes attestation decisions, signs with its **own SPC token** and keeps records. Effective 18 Sep 2025. | https://www.federalregister.gov/documents/2025/08/19/2025-15809/call-authentication-trust-anchor, https://transnexus.com/blog/2025/third-party-shaken-rules-effective-date/ |
| CAN-SPAM | Max civil penalty $53,088 per violation (FTC 2025 adjustment). | https://search.ftc.gov/news-events/news/press-releases/2025/02/ftc-publishes-inflation-adjusted-civil-penalty-amounts-2025 |

**What Relay has:** a TCR brand and campaign form (`us.go:25-61`), and English STOP (`support.go:505-507`).

**What it must build:**
1. **CSP integration with TCR**, or a reseller CSP. This means real brand and campaign submission, status webhooks, and vetting-score storage. Today registration is self-attested.
2. An EIN field in the backend, plus a fix for the UI/backend use-case drift (`regimes.ts:350-355, 384-386` vs `us.go:33-56`).
3. **HELP/START/INFO** handling with tenant-configured replies, and the opt-in confirmation template.
4. **Recipient-time-zone quiet hours** (area-code or state lookup), with FL/OK overrides. Today `PromotionalAllowedAt` ignores the US (`india.go:152`).
5. A cross-message-type revocation scope (in force from Apr 2026).
6. A toll-free verification workflow with a BRN.
7. Number provisioning (absent).
8. For voice: STIR/SHAKEN via an upstream carrier with an SPC token, and Robocall Mitigation Database filing **[RMD from memory, unverified here]**.
9. Pass-through of carrier fees on rate cards.

---

## 3. Canada (CASL)
- Express or implied consent is required. Every CEM must identify the sender and include an unsubscribe that works for 60 days. Unsubscribes must be processed within **10 business days**. Penalties reach up to CAD 10M per violation for businesses **[unverified: vendor blog]**. Enforcement examples include Compu-Finder (CAD 1.1M) and Porter (CAD 150k).
  https://blog.blg.com/-/media/legacy-news-and-publications/documents/publication_4357.pdf, https://www.bennettjones.com/Insights/Blogs/2015/06/Porter-Airlines-Pays-$150000-under-Canadas-AntiSpam-Law (15 Oct 2026).
- Toll-free verification also covers Canada (see the US section). 10DLC does not apply in Canada; carriers use short codes or toll-free.
- **Relay must build:** consent *proof* (implied-consent expiry: 2 years from purchase, 6 months from inquiry **[from memory, unverified]**), so consent needs `basis` + `expiresAt` + `evidence`. There is no regime file for CA today.

## 4. United Kingdom
- **PECR:** the Data (Use and Access) Act 2025 raised PECR fines to UK GDPR levels (£17.5M or 4% of turnover). Royal Assent came Jun 2025, with commencement phased through 2026 **[exact PECR commencement date unverified]**. The soft opt-in still applies and is construed narrowly.
  https://www.mfmac.com/insights/data-protection/data-use-and-access-act-2025-increased-maximum-fines-for-cookies-and-direct-marketing-practices/, https://www.cliffordchance.com/insights/resources/blogs/talking-tech/en/articles/2026/02/key-aspects-of-the-data--use-and-access--act-take-effect.html (15 Oct 2026).
- **SMS SenderID Protection Registry** (MEF): voluntary but industry-wide. Protected brand IDs can only be used by authorised aggregators, and look-alikes go on a denied list. Since Aug 2025 more than 2M fraudulent messages to participating banks' customers have been blocked.
  https://mobileecosystemforum.com/?p=224141, https://www.mobileeurope.co.uk/uk-s-mef-registry-launches-in-ireland-and-singapore-as-sms-fraud-fight-goes-global/ (15 Oct 2026).
- Ofcom: CLI rules for voice **[not researched]**.
- **Relay must build:** a GB regime (replacing the stub at `regime.go:121`) with a Sender-ID-authorisation object (LoA from the brand owner), a consent-basis enum including soft opt-in, and an ICO-grade DPA. Become a participating aggregator, or route via one. Fix the "MMA code" label (`regimes.ts:409`).

## 5. European Union
- **GDPR** Art. 83 fines run up to €20M or 4% of worldwide turnover. **ePrivacy** Art. 13 requires prior consent for electronic marketing, with a soft opt-in for existing customers.
  https://www.legislation.gov.uk/eur/2016/679/article/83/adopted, https://www.i-digital-m.com/whitepapers/IDM_Whitepaper_GDPR-ePrivacy-Business-Messaging_2026.pdf (15 Oct 2026).
- **France:** alphanumeric sender IDs must be registered (via AF2M), with an LoA. Only A–Z and 0–9 are allowed since 1 Jan 2025. The AF2M charter (11 chars) has applied since 10 Mar 2025.
  https://support.twilio.com/hc/en-us/articles/36973882933787-How-to-Register-an-Alphanumeric-Sender-ID-in-France, https://www.efidem.com/en/blog/2025/05/15/marketing-sms-sender-id-security/ (15 Oct 2026). A STOP-number mention ("STOP 36xxx") and marketing curfews (no Sundays or holidays, no 22:00–08:00) are **[from memory, unverified here]**.
- **Belgium:** no formal registration was found. Networks overwrite sender IDs with a short code (e.g. 8850). One vendor says only 4-digit short codes are accepted **[conflicting, unverified]**. https://knowledgecenter.cm.com/knowledge-center/communications-platform/sms/is-it-possible-to-use-a-custom-sender-name-sender-, https://support.ringring.be/hc/en-us/articles/7762033358749 (15 Oct 2026).
- Spain has a CNMC alias registry (seen in search results; **[unverified details]**).
- **Data residency:** EU personal data on an India-hosted platform needs SCCs plus a transfer impact assessment. No India adequacy decision was found **[unverified]**.
- **Relay must build:** an EU data plane (eu-central-1 or eu-west-1), a DPA with SCCs, a sub-processor list, DSAR export and erasure, a Records of Processing, and per-country sender-ID registration objects (FR first). That is a lot of work for low SMS margins, so lead with WhatsApp/RCS in the EU.

## 6. UAE
- TDRA rules: promotional sender IDs need the **"AD-" prefix** (since 3 Nov 2020). Promotional hours are **7am–9pm**, with transactional allowed 24/7. Prior consent and an opt-out in the body are required, and political, religious and gambling content is banned. IDs must be registered separately with e& and du (10–25 business days). Fines run up to AED 400k **[vendor, unverified]**.
  https://www.telerivet.com/blog/uae-sms-compliance-tdra-ad-prefix, https://www.messagecentral.com/blog/how-to-send-sms-to-the-uae (15 Oct 2026).
- **Relay state:** a stub (`regime.go:122`). The header validator accepts anything (`stubs.go:23`). The billing time zone already exists (`postpaid.go:91`).
- **Relay must build:** an AE regime with a per-operator sender object (e&, du), a header rule that requires "AD-" when the type is promotional, a promotional window in Asia/Dubai, and a content-category blocklist. This is the cheapest real regime to add because it is structurally like India's.

## 7. Saudi Arabia (CST)
- Sender names must be registered on all networks. Documents: delegation letter, CR, VAT/Zakat certificates, LoA. Timelines run from 10–20 business days up to 6 months.
- Promotional IDs end in "-AD", and Mobily reportedly blocks -AD. The DND list applies. Night ban: windows of 8pm–8am, 10pm–7am or 9:00–21:45 are cited, with Ramadan variants **[conflicting]**.
- URLs must be pre-registered, shorteners are banned, an opt-out is required, and purely numeric names are not allowed.
  https://support.cpaas.8x8.com/hc/en-us/articles/900003333866-SAUDI-ARABIA-SMS-Products-and-Compliance, https://docs.unifonic.com/articles/products-documentation/ksa-sender-id-registration-requirmnets, https://clickatell.com/sms-country-regulations/saudi-arabia (15 Oct 2026).
- **Relay must build:** an SA regime reusing the India pieces (URL whitelist, shortener ban, promotional window), plus a suffix rule and a Ramadan-aware calendar. Data residency is likely expected by Saudi enterprises (PDPL) **[unverified]**.

## 8. Singapore
- **SSIR** (SMS Sender ID Registry) has been mandatory since **31 Jan 2023**. Unregistered alphanumeric IDs show as "Likely-SCAM" or are blocked. Aggregators must be SSIR-participating.
  https://www.allenandgledhill.com/sg/publication/articles/23255/imda-implements-mandatory-registration-with-singapore-sms-sender-id-registry-from-31-january-2023 (15 Oct 2026).
- PDPA DNC Registry checks apply to marketing **[from memory, unverified here]**.
- **Relay must build:** SSIR participation as an aggregator, or routing via one; a sender object with a UEN; and a DNC-register adapter (same interface as `DNDRegister`).

## 9. Malaysia
- Every SMS must start with "**RM0** [brand]". Since Sep 2024 URLs, phone numbers and personal-information requests are blocked unless whitelisted. Marketing is allowed 8am–8pm with opt-in.
- The MCMC consulted on a UCEM framework on 13 Aug 2025, and its final report is out. **The enforcement date is unknown.**
  https://help.whispir.com/hc/en-us/articles/33692483925145-SMS-and-SenderID-Regulation-in-Malaysia, https://www.bakermckenzie.com/en/insight/publications/alerts/2025/08/malaysia-proposed-new-regulatory-framework-against-unsolicited-commercial, https://www.malaysianwireless.com/2025/10/mcmc-enforce-rules-stop-spam-messages/ (15 Oct 2026).
- **Relay must build:** a body-prefix rule (a new regime hook, `ValidateBody`), the URL whitelist (shared with IN and SA), and a promotional window.

## 10. Indonesia
- Sender IDs are registered per operator (Telkomsel, Indosat Ooredoo Hutchison, XLSmart) through an operator-approved local aggregator. Grey routes have been closing since 2023. The UU PDP law has been fully enforceable since Oct 2024. All of this is **[vendor-only, unverified]**.
  https://www.telerivet.com/blog/indonesia-sms-compliance-kominfo-sender-id-and-uu-pdp-guide (15 Oct 2026).
- **Relay must build:** a local aggregator partnership (direct is unrealistic) and a per-operator sender object. The market is large, but entry is partner-gated.

## 11. Philippines
- Unregistered alphanumeric IDs and international long codes were intermittently blocked from 24 Feb 2025 and permanently blocked from **7 Apr 2025**. Registration needs a GIS and an LoA. Promotional content on sender IDs is disallowed per AWS. Shorteners are blocked. OTPs must name the company. **BSP banks were to move off SMS OTP from 1 Jul 2026 (AFASA)**, which shrinks the OTP market.
  https://docs.aws.eu/sms-voice/latest/userguide/registrations-philippines.html, https://www.infobip.com/docs/essentials/asia-registration/philippines-letter-of-authorization-loa-guidelines, https://api.support.vonage.com/hc/en-us/articles/204017413-Philippines-SMS-Features-and-Restrictions (15 Oct 2026).

## 12. Brazil
- Short codes are the main A2P route. Alphanumeric IDs need pre-registration (TIM, Claro, Vivo; about 10 weeks). PARE, SAIR and AJUDA keywords are required in Portuguese.
- Quiet hours are cited as 9pm–9am plus all of Sunday, or 8:00–21:00 **[conflicting]**. Whether a "Não Me Perturbe" register must be checked is **[conflicting]**. LGPD fines go up to 2% of revenue, capped at R$50M per infraction.
  https://documentation.onesignal.com/docs/en/sms-registration-requirements, https://www.mailpro.com/faq/sms-regulations-brazil, https://sent.dm/en/resources/sms-compliance/brazil-sms-guide (15 Oct 2026).
- **Relay must build:** localised keyword sets per regime (today they are hard-coded English, `support.go:505-507`), short-code provisioning, and LGPD DPA and DPO contact. LatAm is RCS- and WhatsApp-led.

## 13. Mexico
- **REPEP** (the PROFECO opt-out registry) must be checked before campaigns, re-checked at least every 30 days, with records kept. LFPDPPP consent applies. Brand sender IDs generally do not display. A new 2025 data-protection law replaced the LFPDPPP and dissolved INAI **[unverified]**.
  https://help.klaviyo.com/hc/en-us/articles/52955996319131, https://www.telerivet.com/blog/mexico-sms-compliance-ift-repep (15 Oct 2026).
- **Relay must build:** a REPEP adapter (same `DNDRegister` shape) with a 30-day re-scrub and an audit trail of each check.

## 14. Nigeria
- The NCC requires sender IDs to be registered per network (MTN, Airtel, Glo, 9mobile; 1 day to 3 weeks). Unregistered IDs are rejected or rewritten. The DND short code is **2442** (not 2622). Promotional and transactional traffic must be separated. The NDPA 2023 applies **[not researched in depth]**.
  https://www.telerivet.com/blog/nigeria-sms-sender-id-dnd-compliance, https://docs.bird.com/connectivity-platform/country-restrictions-and-regulations/nigeria (15 Oct 2026).

## 15. Kenya
- The Communications Authority oversees sender IDs. One vendor says alphanumeric IDs are transactional-only; another says registration is needed only for international senders **[conflicting]**. The DPA 2019 applies, with ODPC enforcement. Explicit consent is required, and an opt-out within 24h is recommended. Hours are reportedly 8am–8pm and political SMS is banned **[vendor-only]**.
  https://www.telerivet.com/blog/kenya-sms-sender-id-compliance-safaricom, https://sent.dm/en/resources/sms-compliance/kenya-sms-guide (15 Oct 2026).

## 16. South Africa
- **POPIA s69:** opt-in is required for non-customers. Only one approach may be made to request consent, and an opt-out request does not count as consent. The sender's identity and a contact route must be shown. The Regulator's Dec 2024 Guidance Note and the Apr 2025 Regulations amendments simplified objections. The WASPA code reportedly bans marketing on Sundays and public holidays and requires opt-out within 24h **[vendor, unverified]**.
  https://insightplus.bakermckenzie.com/bm/data-technology/south-africa-direct-marketing-under-scrutiny-key-regulatory-developments-in-2024, https://www.telerivet.com/blog/business-sms-in-south-africa-popia-waspa-and-the-one-approach-rule (15 Oct 2026).
- **Relay must build:** a "one approach" consent-request counter per contact and a holiday calendar per regime.

## 17. Australia
- **SMS Sender ID Register (ACMA):** applications opened 30 Nov 2025 and enforcement began **1 Jul 2026**. Unregistered IDs show as "Unverified" in a single thread, and messages from non-participating carriers or providers are blocked.
  https://www.acma.gov.au/sites/default/files/2025-11/International%20telecommunications%20providers%20-%20SMS%20Sender%20ID%20Register%20fact%20sheet_0.pdf, https://sinch.com/engage/resources/sms-compliance/acma-sms-sender-id-register/ (15 Oct 2026).
- **Spam Act** enforcement is heavy: Tabcorp A$4.0M (2025), Latitude A$3.96M, CBA A$7.5M.
  https://igamingbusiness.com/legal-compliance/tabcorp-fined-2-7-million-telemarketing-and-spam-law-breaches/, https://www.businessnewsaustralia.com/articles/latitude-finance-hit-with-4-million-penalty-for-breaching-spam-laws.html (15 Oct 2026).
- **Relay must build:** register as a participating message provider (an international provider fact sheet exists) or route via one, a sender object with ABN, and unsubscribe within 5 business days **[from memory, unverified]**.

---

## 18. Channel-platform policies (they apply in every country)

| Platform | Rule that binds the PLATFORM | Source (15 Oct 2026) | Relay state |
|---|---|---|---|
| WhatsApp Business | **Per-message pricing since 1 Jul 2025** (marketing, utility, authentication). Non-template messages are free inside the 24h window, utility templates are free inside the window, and free-entry-point windows last 72h. **General-purpose AI assistants are banned from 15 Jan 2026** (AI that is incidental to support is allowed). | https://developers.facebook.com/docs/whatsapp/pricing, https://respond.io/blog/whatsapp-general-purpose-chatbots-ban | Sandbox only (shared context: `smpp.go:802`). The 24h window logic exists (`store/audience.go:63-64`). It needs a Tech Provider/BSP onboarding, template-category billing and an opt-in record. |
| Google RBM | Brand verification, then launch approval by Google and/or the carrier (carrier-managed launches need a contract). Opt-out must be implemented. A preview video is required. AUP bans tobacco and political content, and carriers add more (e.g. T-Mobile). | https://developers.google.com/business-communications/rcs-business-messaging/guides/launch, https://cdn.vibes.com/rcs-services/Vibes+Connect+RBM+Q%26A+V2.pdf | The Google RBM connector is coded but never sent live (shared context). |
| Gmail (5k+/day) | SPF and DKIM (DKIM ≥1024-bit), DMARC (p=none is fine) aligned with From, PTR, TLS, **spam rate <0.30%**, one-click unsubscribe (RFC 8058 headers). In force since 1 Feb 2024. | https://support.google.com/a/answer/81126 | Email is sandbox only. Relay needs per-tenant domain auth, a List-Unsubscribe-Post endpoint and complaint-rate monitoring. |
| Yahoo | Same 2024 bulk-sender rules as Gmail **[not separately fetched]** | (see Gmail) | — |
| Microsoft Outlook | 5k+/day: SPF, DKIM and DMARC (≥p=none, aligned) from **5 May 2025**. Non-compliant mail goes to junk and may later be rejected. | https://www.proofpoint.com/es/blog/email-and-cloud-threats/microsoft-new-email-authentication-requirements, https://powerdmarc.com/dmarc-outlook-email-authentication/ | — |
| Apple Messages for Business | Only Apple-approved **MSPs** can connect brands, and Relay would have to apply to become one. Brands register via Apple Business Register. Apple Pay is required on the bot/API path, and a live agent must be available during business hours. | https://bird.com/explained/apple-messages/what-does-apple-messages-for-business-cost-and-who-is-eligible **[vendor-sourced]** | Absent. MSG91 offers it (shared context). |

---

## 19. Security and certifications to sell to enterprises internationally

| Item | Why | Relay today |
|---|---|---|
| **SOC 2 Type II** | The default ask from US buyers. The observation window is at least 3–12 months, so start the clock early. | None. A single 2-vCPU EC2 with no documented controls. (Shared context notes that relay-aws is effectively a test environment.) |
| **ISO/IEC 27001:2022** | Asked for by EU, India and GCC enterprises and banks. The 2013 certificates expired **31 Oct 2025**, so new certification must be to 2022. https://www.lrqa.com/en/insights/articles/preparing-for-iso-270012022-transition-by-october-2025/ (15 Oct 2026) | None |
| **GDPR DPA + SCCs + sub-processor list** | Needed for any EU/UK customer, and Relay is a processor. | None |
| **DPDP processor terms** | Indian enterprises will add these to MSAs before May 2027. | None |
| **PCI DSS** | Only if Relay handles card data (a payment gateway is absent today). Use a hosted checkout (SAQ A) to stay out of scope. | Not applicable yet |
| **CERT-In compliance** | Mandatory in India (6h reporting, 180d logs in India). | Not evidenced |
| **Pen-test report, BCP/DR, MFA, RLS** | Standard in security questionnaires. | MFA and RLS on 55 tables exist (shared context). DR is a single box. |
| **HIPAA BAA** (US healthcare) | Optional vertical. | None |

---

## 20. Cross-cutting platform work (needed by almost every new country)

Order by reuse:
1. **Remove the country branch in time windows.** Move `PromotionalAllowedAt` into `Regime` as `PromotionalWindow() (loc, open, close, days)` plus a holiday calendar (`india.go:146-170`, `server.go:262-269`). Unblocks: US (state), AE, SA, MY, BR, ZA, FR, KE.
2. **A pluggable suppression register per regime.** Generalise `DNDRegister` (`dnd.go:31`) to `SuppressionRegister` keyed by regime: NCPR (IN), REPEP (MX), DNC (SG), DND (NG, SA), later DCA. Include an audit row per check (Mexico requires one).
3. **Consent provenance:** `basis` (express, implied, soft-opt-in, contract), `source`, `capturedAt`, `expiresAt`, `evidence`, `noticeVersion`. Replaces the timestamp map (`store/audience.go:265`). Needed by CASL, PECR, GDPR, POPIA, DPDP and TCPA.
4. **Per-regime keyword sets.** STOP, HELP and START per language (PARE/SAIR/AJUDA, ARRET), with auto-reply (`support.go:505`).
5. **A regime body hook, `ValidateBody`.** Covers RM0 prefix (MY), opt-out text (AE, SA, FR), brand-name-in-OTP (PH), suffix and prefix rules (AE "AD-", SA "-AD").
6. **A per-entity URL whitelist** object and send-time match (IN, SA, MY).
7. **A registry adapter interface** (submit, poll, webhook) behind `RegistrationObject`, so TCR, toll-free verification, SSIR, ACMA and AF2M become *integrations* instead of self-attested forms.
8. **Data-plane per region.** Indian data stays in India (CERT-In, Telecom Act), and EU data needs SCCs or an EU region. This is architectural, so decide before the first non-India customer.
9. **Compliance audit log** for every gate decision (refusal code, regime version), exportable. A user-activity and audit CSV already exists (`internal/api/exports.go:51`), but gate decisions are not in it.
10. **Close the UI/backend drift** (`regimes.ts` vs `us.go`). Better still, have the UI read the backend registry (`GET /registrations` objects) instead of keeping a second copy.

---

## 21. Market-entry order (recommendation)

Scoring: market size (A2P/CPaaS demand; global figures are only loosely comparable, e.g. India A2P was about USD 2.36B in 2024 per https://www.imarcgroup.com/india-a2p-messaging-market, and North America about 38% of global per https://www.sphericalinsights.com/blogs/world-s-top-20-companies-in-application-to-person-a2p-messaging-market-2025-industry-intelligence-report-by-spherical-insights-2026-2035 **[analyst estimates, conflicting]**) × regulatory effort × Relay readiness. The per-country sizes are qualitative **[unverified]**.

| # | Market | Why now | Effort | Readiness |
|---|---|---|---|---|
| 0 | **India: finish it** | Home market. Relay cannot send IN promotional traffic today (no DND register), and has no GST e-invoice or DPDP pack. Fixing this is cheaper than any new country and is a prerequisite for credibility. | Medium | High (DLT engine exists) |
| 1 | **UAE** (then **Saudi**) | Large Indian-diaspora brands and Indian BFSI/retail already operate there. The regime structure mirrors India (sender registration, AD- prefix, hours, whitelist). A stub and the billing time zone already exist. SA reuses about 80% of it. | Low–Medium | Medium (stub at `regime.go:122`) |
| 2 | **Singapore + Malaysia** | Hubs for SEA HQs. SSIR and the RM0 prefix are crisp, codifiable rules. English-speaking enterprise buyers. | Medium (needs SSIR participation and an MY partner) | Low |
| 3 | **UK** | High ARPU. The SenderID registry is voluntary. PECR is now GDPR-level, so consent provenance is required, which is a prerequisite anyway. | Medium | Low (stub) |
| 4 | **US** | The largest market, but 10DLC, TCR CSP, toll-free, STIR/SHAKEN, state quiet hours and SOC 2 make it the highest effort. Relay competes against Twilio-class incumbents. Enter via a CSP/reseller partner, not direct. | High | Low–Medium (forms only) |
| 5 | **Indonesia / Philippines** | Big volumes but partner-gated. PH OTP demand is shrinking as BSP moves banks off SMS OTP. | Medium (partner) | Low |
| 6 | **Brazil / Mexico** | Large and WhatsApp-first. Needs localisation (PT/ES keywords, REPEP) and real WhatsApp, which is sandbox today. | High | Low |
| 7 | **Australia** | Sender ID Register live Jul 2026, high fines, small volume. | Medium | Low |
| 8 | **Nigeria / Kenya / South Africa** | Fragmented rules, mostly vendor-sourced. Defer unless a channel partner brings demand. | Medium | Low |
| — | **EU (France first)** | Only after an EU data plane, DPA, SCCs and ISO 27001 are in place. SMS margins are thin, so lead with WhatsApp/RCS. | High | Low |

**Rationale in one line:** finish India, then go to GCC (same regulatory shape and Indian customers following
their brands), then SEA, then UK. The US and EU wait for SOC 2/ISO 27001, a regional data plane and real
registry integrations, which cost more than the regimes themselves.
