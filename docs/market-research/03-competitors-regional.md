# 03 — Regional CPaaS and engagement competitors

Research date: 15–16 Oct 2026. All external links were accessed 2026-10-15 unless marked otherwise.
Relay claims cite `file:line` in this repository. Anything this research could not confirm from a
primary or two independent secondary sources is tagged **[UNVERIFIED]** and collected in §9.

Source quality key: **P** = primary (regulator, company release, filing, vendor's own pricing page);
**S** = reputable press; **V** = vendor or competitor blog (biased, used only where nothing better exists);
**A** = aggregator or review site.

---

## 0. Executive read (what matters for Relay)

1. **India is now a telco-owned stack.** Airtel launched **IQ Gateway** on 5 Oct 2026: SMS + WhatsApp + RCS +
   voice + silent auth on one control plane, native DLT template handling, automatic failover to *other
   vendors*, on-prem or SaaS ([Airtel press release, P](https://www.airtel.in/press-release/10-2026/airtel-business-launches-iq-gateway-indias-first-business-communications-platform-with-telco-grade-network-intelligence-for-safe-enterprise-messaging/)).
   That is Relay's pitch (multi-operator routing + DLT gate + fallback), sold by an operator that owns the
   network signal. Jio owns Haptik and Interakt and is the exclusive iOS RCS carrier in India.
2. **The listed aggregators are healthy and growing.** Tanla Q1 FY27 revenue ₹1,226 cr (+17.8% YoY), PAT
   ~₹142 cr ([Tanla, P](https://www.tanla.com/newsroom/results-q1-fy27)); Route Mobile Q1 FY27 ₹1,151.5 cr
   (+9.6%), 20.9% gross margin ([exchange4media, S](https://www.exchange4media.com/digital-news/route-mobile-reports-96-revenue-growth-in-q1-fy27-156637.html)).
   Gross margins of ~21–27% on SMS are the market's reality; Relay's margin must come from software, not routes.
3. **The venture-funded "conversation" players are shrinking.** Gupshup marked down to ~$278 M by Fidelity
   (Jan 2026), >80% below its $1.4 B 2021 mark ([letsdatascience, A](https://letsdatascience.com/news/fidelity-cuts-gupshup-valuation-to-278-million-47aa4911));
   repeated layoffs ([Inc42, S](https://inc42.com/buzz/exclusive-saas-unicorn-gupshup-fires-500-employees-in-5-months/)).
   Yellow.ai cut ~30% of staff, India-entity revenue flat at ₹233.6 cr FY25 ([Inc42, S](https://inc42.com/buzz/exclusive-yellow-ai-lays-off-over-100-employees-amid-automation-push)).
   Their mid-market customers are a target.
4. **The self-serve SMB layer is price-transparent and cheap.** MSG91 ₹0.25→₹0.16/SMS (+GST), Fast2SMS
   ₹0.25→₹0.11, AiSensy WhatsApp marketing ₹1.09 vs Meta's ₹0.8631 list. Relay publishes no price list and has
   no payment gateway (context.md). It cannot win SMB self-serve today.
5. **Rules keep moving.** TCCCPR amendment (12 Feb 2025): `-P/-S/-T/-G` header suffixes live since ~6 May 2025,
   URL/APK/callback whitelisting, 1600-series for BFSI (deadlines Jan–Mar 2026), Digital Consent Acquisition
   pilot. Meta WhatsApp max-price bidding is in **open beta from Oct 2026** and mandatory in eligible
   geographies from Q2 2027 ([Meta developer docs, P](https://developers.facebook.com/documentation/business-messaging/whatsapp/marketing-messages/pricing)).
   Jio repriced iPhone RCS on 1 Sep 2026 to ₹0.25–0.30/msg. Relay's compliance engine is a real asset here,
   but its DND default refuses every Indian promotional SMS (`internal/sending/dnd.go:38-43`).

---

## 1. Indian telecom market structure

### 1.1 Volumes and market size

| Metric | Figure | Source |
|---|---|---|
| Total outgoing SMS / day | 6.63 bn | [TRAI deck to BEREC, 21 May 2026, P](https://www.berec.europa.eu/index%2ephp/system/files/2026-05/5_Sanjay_KUMAR_TRAI.pdf) |
| Commercial SMS / day | 2.30 bn (34.67% of outgoing) | same |
| Commercial SMS blocked by DLT for DND / day | 60 mn (2.61%) | same |
| SMS tagged spam / day | 159 mn (0.21% of incoming) | same |
| Commercial SMS / month (derived ×30) | ~69 bn | derived, not a TRAI figure |
| Promotional 140-series calls / day | 108 mn, of which 9 mn DND-blocked | same TRAI deck |
| Telecom subscribers | 1.3 bn | same |
| Older estimate (2024) | 1.5–1.7 bn commercial msgs/day, ~55 bn/month | [communicationstoday, S](https://www.communicationstoday.co.in/?p=308076) **[UNVERIFIED — not read in full]** |
| India A2P SMS / messaging value | Range **$2.36 B** (IMARC 2024) to **$8.7 B** (MarketsandMarkets 2024) to **$10 B** (TechSci 2024) — definitions differ wildly | [IMARC](https://www.imarcgroup.com/india-a2p-messaging-market), [MarketsandMarkets](https://www.marketsandmarkets.com/Market-Reports/geography/a2p-messaging-market/India), TechSci via republishers (A) |
| India CPaaS market | ~$1.0–1.1 B in 2025 → $2.4–5.1 B by 2031–32 (CAGR 13–24%) | [Mordor](https://www.mordorintelligence.com/industry-reports/india-communication-platform-as-a-service-market) $1.12 B → $2.36 B 2031; [BlueWeave](https://www.blueweaveconsulting.com/report/india-communications-platform-as-a-service-cpaas-market/report-sample) $1.08 B → $5.06 B 2032; TechSci $1.03 B → $3.43 B 2031 (A) |

**Sanity check (own arithmetic):** 69 bn commercial SMS/month × ~₹0.12–0.16 blended wholesale ≈ ₹8,300–11,000 cr/yr
(~$1.0–1.3 B). Tanla (~₹4,900 cr annualised) + Route Mobile India share + Gupshup + Kaleyra + MSG91 etc. fit inside that.
The $8–10 B "A2P SMS" numbers are not credible for India-terminated traffic and likely include international/OTT.
**Use ~$1.0–1.3 B for India domestic A2P SMS and ~$1.1 B for CPaaS when pitching.** [own estimate — label it]

### 1.2 DLT platforms (TCCCPR 2018 registry)

Each access provider runs a DLT portal; registering a Principal Entity (PE) on one is shared to all via the
blockchain ledger ([Infobip docs, V](https://www.infobip.com/docs/essentials/asia-registration/dlt-registration)).

| Operator | Portal | Notes |
|---|---|---|
| Reliance Jio | Jio TrueConnect (trueconnect.jio.com) | Exotel says ~₹5,900 + GST one-time fee ([Exotel docs, V](https://developer.exotel.com/docs/sms-support/dlt-registration)); Fast2SMS says Jio is free ([Fast2SMS, V](https://fast2sms.com/DLT-Registration)) — **conflict** |
| Bharti Airtel | Airtel DLT (dltconnect.airtel.in) | |
| Vodafone Idea | ViLPower | ₹5,900 per Fast2SMS |
| BSNL / MTNL | BSNL DLT / MTNL DLT | |
| Videocon (Quadrant) | SmartPing (STPL) | Relay's only live SMPP operator is VIDEOCON (context.md) |
| Tata Teleservices | Tata DLT | listed by some sources only |

Fee consensus: one-time ~₹5,900 incl. GST on the first portal; further portals free (Infobip). **[UNVERIFIED which portal is free]**

Tanla's **Trubloq** is the blockchain platform behind several operators' DLT; Tanla claimed ~63–70% of India A2P
traffic passes through it (2020–21 company releases — dated) ([Tanla via kyodonewsprwire, P](https://kyodonewsprwire.jp/release/202103112130)).
This gives Tanla structural visibility into every competitor's traffic — a moat no startup can match.

### 1.3 TCCCPR amendments 2024–2026

| Rule | Detail | Date | Source |
|---|---|---|---|
| URL / APK / OTT-link whitelisting | All CTAs in template variables must be pre-whitelisted on DLT; non-whitelisted rejected | Variable tagging for new templates from 4 Aug 2024; scrubbing enforced 1 Oct 2024 (vendor accounts differ) | [MSG91 help, V](https://msg91.com/help/dlt-registration-in-india/cta-whitelisting-faqs), [Fast2SMS, V](https://www.fast2sms.com/help/?p=14981) |
| Callback-number whitelisting | Numbers in SMS must be whitelisted; 10-digit, or toll-free 1800/1860 (8–11 digits) | same | same |
| TCCCPR amendment | 140 for promotional calls; **1600** for service/transactional calls; 10-digit numbers barred from telemarketing; complaint window 3→7 days; action on unregistered senders in 5 days (was 30) on 5 complaints/10 days; first offence = 15-day outgoing bar, repeat = 1-year disconnection across all operators + blacklist; operator penalties ₹2 L / ₹5 L / ₹10 L; mandatory opt-out in promos; 90-day cool-off before re-seeking consent | Notified 12 Feb 2025 (TRAI PR 11/2025) | [Storyboard18, S](https://www.storyboard18.com/how-it-works/trai-tightens-rules-against-spam-introduces-provision-of-stricter-penalties-56616.htm); [TRAI PR (P, unreachable 15 Oct)](https://cms.trai.gov.in/sites/default/files/2025-02/PR_No.11of2025.pdf) |
| Header suffixes | `-P` promotional, `-S` service, `-T` transactional, `-G` government; appended automatically by operators from the DLT template category; "Service Explicit" folded into -P per one vendor | Live ~6 May 2025 (MediaNama saw them 7 May) | [MediaNama, S](https://www.medianama.com/2025/05/223-suffixes-commercial-sms-headers-spam/), [Tanla, V](https://www.tanla.com/udayreddy/trai-mandates-new-message-suffixes-heres-what-you-need-to-know) |
| 1600 series for BFSI | Commercial banks by 1 Jan 2026; large NBFCs/payments/SFBs by 1 Feb; MFs/AMCs 15 Feb; others 1 Mar; stockbrokers 15 Mar 2026; insurance pending IRDAI | Direction 19 Nov 2025 | [Outlook Business, S](https://www.outlookbusiness.com/news/trai-sets-firm-deadlines-for-bfsi-entities-to-adopt-1600-series-for-service-transactional-calls) |
| Digital Consent Acquisition (DCA) | Unified consent registry checked against DLT; short code 127xxx; pilot with RBI + 11 banks, due to end Feb 2026 | Pilot 2025–26 | [MediaNama, S](https://www.medianama.com/2025/10/223-digital-consent-platform-trai-rbi-february-2026/), [tele.net, S](https://tele.net.in/trai-to-conclude-digital-consent-pilot-by-february-2026/) — **post-pilot status [UNVERIFIED]** |
| Strategic direction | TRAI: "transition from entity registration to traceability-based enforcement", AI/ML spam detection, PE–TM binding | May 2026 | TRAI BEREC deck (P) |

**Relay fit:** DLT entity/header/template registry and `entity_not_registered` gate exist
(`internal/domain/messaging/gate.go:309`), and SMPP refuses Indian SMS without entity+template IDs
(`internal/connector/smpp.go:810`). Not found in this pass: URL/callback whitelist validation, header-suffix
awareness in reporting, 1600/DCA consent integration. **[Relay gaps to verify in code — not checked here]**

### 1.4 RCS operator economics

| Item | Figure | Source |
|---|---|---|
| Jio iPhone/RBM tariff from 1 Sep 2026 | Marketing ₹0.27–0.30, transactional ₹0.25, OTP ₹0.11–0.16 per msg; replaces flat ₹0.08–0.10 | [Inc42 citing ET, S](https://inc42.com/buzz/jio-introduces-new-rcs-business-messaging-rates-for-iphone-users-in-india/) |
| Jio role | Claims to be exclusive RCS provider for iOS in India via Apple partnership (2025) | [TelecomTalk, S](https://telecomtalk.info/reliance-jio-helps-apple-bring-rcs-messaging/1011712/) |
| Airtel + Google | Deal Dec 2025: ~₹0.11/msg, **80:20 revenue share** (Airtel:Google) per unnamed execs; contingent on Airtel spam filter in Google Messages (announced Mar 2026) | [TelecomTalk, S](https://telecomtalk.info/airtel-google-to-launch-rcs-messaging-india/1002371/), [MediaNama Mar 2026, S](https://www.medianama.com/2026/03/223-airtel-spam-detection-googles-rcs-messaging-india/) — 80:20 **[UNVERIFIED]** |
| Vi | MaaP via Dotgo ("Rich Business Services"); basic RCS ~15 paise, conversational up to 35 paise; ~1 bn RCS msgs/month (undated) | [TelecomTalk, S](https://telecomtalk.info/?p=976484), [Benzinga, S](https://in.benzinga.com/content/37197842/google-unleashes-rcs-in-india-setting-stage-for-showdown-with-whatsapp) **[UNVERIFIED volume]** |
| MoEngage claim | RCS marketing from ~6–7 paise to 20–25 paise from 1 Sep 2026; CPM/CPC buying introduced | [MoEngage blog, V](https://www.moengage.com/?p=93281) |
| Google promo caps (India) | Rolling 28-day initial-message caps per user: High 8 / Medium 4 / Low 2; new agents start Low; sending 07:00–22:00 | [Telekom docs, V](https://docs.developer.telekom.com/en/messages/guides/rcs/rcs-agent-use-cases-business-rules), [Infobip, V](https://www.infobip.com/docs/rcs/guidelines-and-compliance/india-compliance-requirements) |
| Global forecast | Business RCS 125 bn msgs (2026) → 485 bn (2030); operator revenue >$7 B | [TelecomLead/Juniper, S](https://telecomlead.com/smart-phone/business-rcs-messaging-to-reach-485-billion-by-2030-as-operator-revenue-tops-7-billion-127410) |

**Read:** RCS in India is no longer "cheap SMS+" — at ₹0.25–0.30 marketing it sits between SMS (₹0.11–0.25)
and WhatsApp marketing (₹0.86 list). Relay sends RCS live only through Trustsignal/Sigmo; Jio/Airtel/Vi/RBM
connectors are coded but never sent live, and carousels are absent (context.md).

### 1.5 WhatsApp India pricing 2025–2026

| Change | Detail | Source |
|---|---|---|
| Per-message pricing | Replaced conversation pricing 1 Jul 2025; utility/auth volume tiers; service replies free | [MyOperator support, V](https://support.myoperator.com/portal/en/kb/articles/whatsapp-has-shifted-to-per-message-pricing-effective-july-1-2025-based-on-message-categories-and-your-recipient-s-country-in-india-there-are-three-paid-categories) |
| India list rates (from 1 Jan 2026, ex-GST) | Marketing **₹0.8631** (up ~10% from ₹0.7846); utility ~₹0.115; authentication ~₹0.115 | [Montymobile, V](https://montymobile.com/blogs/whatsapp-business-api-pricing-in-india-inr-rates-gst-and-the-2026-currency-migration-deadline), [ecorpit, V](https://ecorpit.com/whatsapp-business-api-cost-india-d2c-2026/) — Meta rate card not directly read **[UNVERIFIED vs Meta CSV]** |
| INR billing | Localised INR billing for India sold-to from 1 Jan 2026; migration deadline 31 Dec 2026 | [baat.ai, V](https://baat.ai/blog/whatsapp-api-pricing-india) |
| 1 Oct 2026 | Charging for service replies beyond 1,000 free per number/month and for utility templates inside an open window; India list rates unchanged | baat.ai (V); AiSensy pricing page confirms "1,000 FREE Service messages every month" from 1 Oct 2026 ([AiSensy, P](https://www.aisensy.com/pricing)) |
| Marketing max-price ("auction") | `bid_amount` per 1,000 deliveries (now `optimization_spec`; `bid_spec` dropped after 31 Jul 2026). Limited beta mid-May 2026 → **open beta Oct 2026** → GA/mandatory in eligible geographies **Q2 2027**. Billing remains per message | [Meta developer docs, P](https://developers.facebook.com/documentation/business-messaging/whatsapp/marketing-messages/pricing), [changelog, P](https://developers.facebook.com/documentation/business-messaging/whatsapp/marketing-messages/changelog) |
| Netcore guidance | Bid below clearing price → not delivered at that moment, not charged | [Netcore docs, V](https://cedocs.netcorecloud.com/docs/whatsapp-template-optimized-dynamic-price) |
| GST | 18% on Meta fee and BSP fee | Montymobile (V) |

**BSP markup observed (India marketing msg):** Meta ₹0.8631 → Interakt ₹0.949–0.970 (+10–12%) → AiSensy ₹1.09
(+26%); utility ₹0.115 → Interakt ₹0.14–0.16, AiSensy ₹0.145 (+26%). MSG91 says zero markup + subscription
([zoko.io, A](https://www.zoko.io/post/msg91-whatsapp-api-pricing-india)). Gupshup self-serve: +$0.001/msg flat
([Gupshup, P](https://www.gupshup.ai/isv-partners/whatsapp-api/pricing)).

---

## 2. India — company teardowns

Legend for channel columns: SMS / RCS / WA (WhatsApp) / EM (email) / VO (voice) / AMB (Apple Messages for
Business) / VB (Viber) / TG (Telegram) / IG (Instagram) / PU (push).

### 2.1 Gupshup
- **Channels:** SMS, RCS, WA, IG, voice (via Knowlarity), web/app chat; email on a legacy Webaroo gateway (context.md first-pass).
  AMB and Telegram **not confirmed** ([LinkedIn, P](https://linkedin.com/company/gupshup); [getmacha, V](https://www.getmacha.com/blog/gupshup-ai-complete-guide)).
- **Pricing:** WA self-serve = Meta fee at actuals + **$0.001/msg** Gupshup fee ([P](https://www.gupshup.ai/isv-partners/whatsapp-api/pricing)). SMS India price not published; enterprise = sales + minimum monthly commit ([prospeo, A](https://prospeo.io/s/gupshup-vs-wati)).
- **DLT:** runs a PE-registration help flow ([wss.gupshup.io, P](https://wss.gupshup.io/dlt/principal-entity-registration)). Customer still registers.
- **Self-serve vs sales:** both (ISV self-serve for WA; enterprise sales).
- **Journey / inbox / AI:** Conversation Cloud; "Superagent" AI agents; Juniper RCS "Established Leader" 2026 (self-reported) ([Gupshup blog, V](https://www.gupshup.ai/resources/blog/gupshup-named-established-leader-in-juniper-researchs-rcs-for-business-vendors-2026-competitor-leaderboard/)).
- **Reseller:** ISV partner programme (self-serve WA API for ISVs). Terms not published.
- **Google RBM directory:** "5Cr+ messages monthly in India" ([Google, P-listing](https://rcsforbusiness.google/intl/en_in/find-a-partner/)).
- **Financials/news:** Fidelity mark ~$278 M (31 Jan 2026) vs $1.4 B 2021; $486 M late 2024 ([TechCrunch, S](https://techcrunch.com/2024/12/30/gupshup-investor-cuts-value-of-its-stake-by-65-implying-new-valuation-of-486m)); raised $60 M equity+debt 2025; IPO "in 24 months" (Jul 2025), redomicile to India; layoffs ~300 Dec 2024, ~200 Apr 2025, 100+ mid-2025; FY24/FY25 financials undisclosed ([Inc42, S](https://inc42.com/?p=530448)). FY25 revenue ₹1,943 cr **[UNVERIFIED — single aggregator]**. Owns Knowlarity (Feb 2022, ~$100 M per Bloomberg) ([BQ, S](https://www.bloombergquint.com/business/chatbot-startup-gupshup-buys-knowlarity-in-100-million-deal)).
- **Review weaknesses:** Capterra: "one of the worst after-sales support I have experienced… queries are responded to in days and not hours"; "Billings/Accounts Team is super inefficient" ([Capterra, A](https://www.capterra.com/p/233786/Gupshup/reviews/)). G2 4.4/5 (66 reviews).

### 2.2 Route Mobile (Proximus Global)
- **Channels:** SMS, RCS, WA, VB (listed Viber partner, [Viber, P](https://www.forbusiness.viber.com/en/messaging-partners/route-mobile)), email, voice, SMS firewall (365squared, acquired 2017 — [P](https://www.365squared.com/blog/route-mobile-acquires-managed-sms-firewall-brand-365squared/)). AMB/TG not confirmed.
- **Pricing:** not published (sales-led).
- **Financials:** Q1 FY27 revenue ₹1,151.5 cr (+9.6% YoY), PAT ₹68.55 cr, gross margin 20.9%, adj. EBITDA ₹1,089 mn (−5.6% YoY); margin hit by a security incident at its Colombian subsidiary; guides 10–15% volume growth ([exchange4media, S](https://www.exchange4media.com/digital-news/route-mobile-reports-96-revenue-growth-in-q1-fy27-156637.html), [Quartr, A](https://quartr.com/events/route-mobile-limited-route-q1-26-27_F3z06VMt)). Proximus Opal 82.7% stake (2024, ~€643 M); merged with BICS + Telesign into **Proximus Global** Jan 2025 (~€3.1 B equity value) ([RCR Wireless, S](https://rcrwireless.com/20250106/carriers/proximus-bics-telesign-route-mobile)).
- **Reviews:** only aggregator data — "slow or absent support responses… inconsistent across accounts" ([rfp.wiki, A](https://www.rfp.wiki/it-security/communications-platform-as-a-service/route-mobile/clickatell)). Weak evidence.

### 2.3 Tanla Platforms (Karix, Wisely, ValueFirst, Trubloq)
- **Channels:** SMS, RCS, WA, email, voice (Karix + ValueFirst); Wisely.ai = AI anti-spam/scam platform sold to operators (Indosat, >100 M users).
- **DLT:** Trubloq powers operator DLT (§1.2) — Tanla sits on both sides of the compliance wall.
- **Financials:** Q1 FY27 revenue ₹1,226 cr (+17.8% YoY), gross profit ₹326 cr (26.6%), EBITDA ₹201 cr, PAT ~₹142 cr (+20.1%), FCF ₹126 cr; Enterprise Comms 91.6% of revenue; earnings calls paused ([Tanla, P](https://www.tanla.com/newsroom/results-q1-fy27), [Kotak Neo, S](https://www.kotakneo.com/news/stocks/tanla-platforms-q1-fy-2026-27-results-revenue-rises-17-8-percent-shares-up-8-08-percent/)).
- **ValueFirst:** bought from Twilio, closed 3 Jul 2023, $45.5 M (₹3,737 M ≈ ₹374 cr) all-cash after adjustments, vs ₹346 cr ($42 M) announced ([Tanla, P](https://tanla.com/newsroom/tanla-acquires-valuefirst-india-from-twilio), [Twilio, P](https://www.twilio.com/en-us/blog/company/news/tanla-to-buy-twilios-valuefirst-business)). This is the "Twilio–ValueFirst legacy": Twilio exited Indian domestic aggregation.
- **Pricing:** not published. **Reviews:** none substantive found.

### 2.4 MSG91 (Walkover)
- **Channels:** SMS, WA, RCS, email, voice, OTP widget, **AMB** (free AMB link/QR generator; [MSG91, P](https://msg91.com/in/free-apple-messages-for-business-link-generator); `/api/apple-messages/url` seen in bundle capture per context.md), "Hello" inbox, Segmento, Campaign. Telegram not confirmed.
- **Pricing (P):** SMS India ₹0.25 (5k) → ₹0.20 (16.5k) → ₹0.18 (30k) → ₹0.17 (60k–450k) → ₹0.16 (962.5k); "up to ₹0.13" negotiated; +18% GST ([MSG91 pricing, P](https://msg91.com/in/pricing)). WA: no markup on Meta, monthly subscription (A).
- **DLT/compliance:** extensive DLT/CTA help docs; customer self-registers.
- **Reseller:** reseller account at >500,000 SMS/month, white-label, client sub-accounts, referral track ([MSG91 help, P](https://msg91.com/help/resellers/how-can-i-get-reseller-account-on-msg91)). Commission not published.
- **Reviews:** Trustpilot 3.5/5 (6 reviews): "pushed to recharge a minimum of ₹5,000… promised double SMS credits… credited only 1,000"; "sent successfully" for undelivered messages while credits were debited ([Trustpilot, A](https://dk.trustpilot.com/review/msg91.com)).

### 2.5 Exotel
- **Channels:** voice (core: cloud telephony, contact centre), SMS, WA, RCS (glossary), AI voicebots.
- **Pricing:** no public prices; "Talk to an Expert" ([Exotel pricing, P](https://exotel.com/pricing/)).
- **Financials:** FY25 operating revenue ~₹490.5 cr (+~10%), total income ~₹507 cr, turned profitable ([Entrackr, S](https://entrackr.com/fintrackr/exotel-turns-profitable-in-fy25-total-income-crosses-rs-500-cr-11017649)); last priced round Series D Jan 2022 at $400 M ([CB Insights, A](https://www.cbinsights.com/company/exotel/financials)).
- **Reviews:** G2 4.4 (90): "occasional issues with call quality and connectivity"; Capterra "after-sales service horrible" ([G2](https://www.g2.com/products/exotel-customer-communication-platform/reviews_and_filters), [Capterra](https://www.capterra.com/p/233144/Exotel/reviews/), A). Actively poaching ex-Textlocal India customers ([Exotel, V](https://exotel.com/competitor/exotel-vs-textlocal)).

### 2.6 Netcore Cloud
- **Channels:** email (Pepipost heritage), SMS, WA, RCS, push, in-app, web push, voice, Viber, Zalo — "nine channels" ([Netcore EU LP, V](https://netcorecloud.com/lp-eu-brand), [docs, P](https://cedocs.netcorecloud.com/docs/sms)).
- **Journey/AI:** journey builder with triggers, "Co-Marketer" gen-AI (STO, subject lines, Campaign Insight Agent); WA max-price "Optimized Dynamic Price" already documented.
- **Financials:** private; ~$85 M 2022 revenue + ~$10 M Unbxd; IPO deferred 2022 ([IIFL, S](https://www.indiainfoline.com/article/news-business/netcore-cloud-defers-its-ipo-122111000335_1.html)). No FY25 figure found.
- **Reviews:** G2 reviewer: billing "awful"; "once the journey is live making edits can be difficult" ([G2, A](https://g2.com/products/netcore-email-api/reviews)).

### 2.7 Airtel IQ (incl. IQ Gateway, 5 Oct 2026)
- **Channels:** SMS, WA (claims first telco BSP worldwide), RCS (Google deal), voice, silent authentication, IQ Verify fraud scoring, anti-SMS-pumping ([Airtel, P](https://www.airtel.in/press-release/10-2026/airtel-business-launches-iq-gateway-indias-first-business-communications-platform-with-telco-grade-network-intelligence-for-safe-enterprise-messaging/)).
- **DLT:** "native DLT integration" removing manual template management. **Multi-vendor failover** to other providers; on-prem or SaaS; DPDP-compliant. Targets BFSI, government, e-commerce, logistics. Pricing not disclosed.
- **IQ Reach (2023):** self-serve marketing platform, prepaid; ₹10,000–₹1.5 L/month spend cited at launch ([Bharti, P](https://bharti.com/press-release-2023-2024-airtel-launches-airtel-iq-reach-indias-first-of-its-kind-self-serve-marketing-communications-platform.html)).
- **Weakness:** "pricing opacity", sales-led cycles slow for mid-market ([rfp.wiki, A](https://www.rfp.wiki/vendors/airtel-iq)) — weak source.
- **Threat level to Relay: highest.** Same feature list, owns network signals and DLT, and can route to rivals.

### 2.8 Jio Business Messaging / JioCX / Haptik / Interakt
- **Jio:** RCS (exclusive iOS RCS partner in India, Apple + Google), SMS via JioCX CPaaS ([JioCX, P](https://jiocx.com/solutions/communications-service-providers)). Tariff in §1.4.
- **Haptik (Jio):** WhatsApp AI agents for SMBs from ₹10,000, usage-based, ~2,000 AI conversations, 22 Indian languages; target 3–5 lakh SMBs in 24 months ([UNI, S](https://uniindia.com/~/jio-s-haptik-launches-whatsapp-ai-agents-at-starting-price-rs-10-000-for-smbs/Business%20Economy/news/3564915.html)). ARR ~$35 M **[UNVERIFIED]**.
- **Interakt (Haptik/Jio):** see 2.13. 50,000+ businesses claimed (A).

### 2.9 Vi Business
- RCS via Dotgo MaaP ("Rich Business Services"), Google RBM partnership; blog on RCS use cases ([Vi, P](https://www.myvi.in/business/blog/rcs-data-backed-use-cases)). No WhatsApp BSP offering found. Pricing in §1.4. Weak financial position of Vi (parent) not researched here.

### 2.10 Kaleyra (Tata Communications)
- Acquired by Tata Communications 2023 (~$100 M cash + debt) ([TelecomTalk, S](https://telecomtalk.info/?p=726038)); Dec 2025 Tata took 51% of Commotion Inc. to embed AI orchestration into "Kaleyra Customer Interaction Suite" (A).
- **Channels:** SMS, voice, WA, RCS, video, conversational AI (G2 listing). **Pricing:** negotiated; "3–6 weeks to launch" per a competitor ([messagecentral, V](https://messagecentral.com/en-us/blog/best-otp-sms-provider-in-india-and-how-to-select-one)).
- **Reviews:** GetApp support 3.5/5, value 3.5/5, 60% recommend ([GetApp, A](https://www.getapp.ca/software/2063430/kaleyra)).

### 2.11 Fast2SMS
- **Pricing (P):** DLT SMS by wallet top-up: ₹0.25 (₹100–3,999) → ₹0.21 → ₹0.19 → ₹0.17 → ₹0.15 → ₹0.13 → **₹0.11** (≥₹6 L), rate reverts to ₹0.25 after validity; start from ₹100 ([Fast2SMS, P](https://www.fast2sms.com/pricing)).
- **Reseller:** white-label panel ₹0.25 (10k) → ₹0.12 (50 lakh) ([Fast2SMS, V](https://www.fast2sms.com/help/?p=17108)). Also WA and RCS pages.
- Pure SMB/price play; heavy DLT help content (acts as de facto educator).

### 2.12 SMSCountry
- Bulk SMS from ₹0.24/SMS, 10,000-message starter pack, no monthly fee; credits expire 120 days and "rates change without prior notice" per a competitor ([TextBolt, A](https://textbolt.com/blog/smscountry-pricing/); [Fast2SMS, V](https://www.fast2sms.com/help/?p=16170)). Reseller terms not found.

### 2.13 WhatsApp-first SMB tools: Interakt, AiSensy, WATI
| | Interakt (Jio Haptik) | AiSensy | WATI |
|---|---|---|---|
| Plans | Instagram ₹999; Growth ₹2,799; Advanced ₹3,799; Sales CRM ₹2,499/mo; Enterprise custom | Base plans not in page text; Chatbot ₹2,500/mo; AI Agent ₹1,350/mo (1,000 AI msgs); Indian virtual number ₹2,000/yr | Growth / Pro / Business (USD; base not extracted); extra users $24–69; Shopify add-on $4.99 |
| WA marketing (India) | ₹0.949–0.970 | ₹1.09 | rate card only |
| WA utility/auth | ₹0.127–0.160 | ₹0.145 | — |
| Channels | WA, IG, RCS (Enterprise only) | WA (IG not on pricing page) | WA, IG, WA Business Calling, voice AI (Business) |
| AI | WA AI agents add-on ₹3,000; Sales CRM AI ₹0.50/msg after 100 | AI agent builder, KB, tool calling | 1–unlimited AI agents, Copilot |
| Source | [Interakt, P](https://www.interakt.shop/pricing/) | [AiSensy, P](https://www.aisensy.com/pricing) | [WATI, P](https://www.wati.io/pricing/) |

- **AiSensy reseller:** full white-label (domain, logo, own pricing), partner marks up subscription + conversation credits; affiliate 20% recurring ([AiSensy, V](https://m.aisensy.com/blog/whatsapp-api-whitelabelled-partnership/)).
- **Weaknesses:** WATI Trustpilot 3.5/5 (256): hard to cancel ("had to contact their bank to block the charges"), account bans, "can't talk to a person", breach used to send "tens of thousands of unsolicited messages" ([Trustpilot, A](https://www.trustpilot.com/review/wati.io?page=7)). AiSensy G2 cons: "Poor Customer Support (3)"; Capterra "pricing is not suitable for startup business" ([G2, A](https://www.g2.com/compare/aisensy-vs-haptik-interakt)). Interakt "email-only support" (AiSensy blog, V).

### 2.14 MyOperator, Knowlarity
- **MyOperator:** cloud telephony + WhatsApp for SMBs; acquired Ziffy (3,500 Tier-II/III clients, date not found) ([MyOperator blog, V](https://myoperator.com/blog/news?pg=2)). Flat yearly plans (competitor claim). No financials found.
- **Knowlarity:** Gupshup subsidiary since 2022; 6,000+ customers in 65 countries at acquisition ([KMWorld, S](https://www.kmworld.com/Articles/News/News/Gupshup-snaps-up-Knowlarity-Communications-to-further-invest-in-conversational-engagement-151314.aspx)); contact-centre plans from ₹1,999/agent/month **[UNVERIFIED]** ([caller.digital, V](https://caller.digital/blog/exotel-alternatives-india-2026)).

### 2.15 Haptik, Yellow.ai (conversational AI)
- **Yellow.ai:** ~30% staff cut (late 2025), plus 40–50 in Aug 2025; India entity FY25 revenue ₹233.6 cr vs ₹237.9 cr; claims ~40% consolidated growth; auditor flagged FEMA non-compliance; last raise $78.15 M Series C Aug 2021 ([Inc42, S](https://inc42.com/buzz/exclusive-yellow-ai-lays-off-over-100-employees-amid-automation-push)). Buys CPaaS from others for WA/SMS.
- **Haptik:** see 2.8.

### 2.16 CleverTap / MoEngage / WebEngage (engagement layer that buys CPaaS)
- **CleverTap:** RCS via Infobip partnership (Apr 2025); SMS/WA/RCS via Infobip integration ([CleverTap PR, P](https://clevertap.com/news/press-release/clevertap-partners-with-infobip-to-strengthen-omnichannel-offering-with-rcs-messaging/)); Gartner MQ Leader, Personalization Engines 2026 (self-reported). Last raise $105 M Series D 2022.
- **MoEngage:** native RCS for Business (date unclear); Google RCS partner; Series F-II $57 M (Dec 2025) per CB Insights; ~$100 M 2025 revenue (estimate); acquired Aampe Jun 2026 per Owler ([CB Insights, A](https://www.cbinsights.com/company/moengage/financials)) **[UNVERIFIED round sizes]**.
- **WebEngage:** no 2026 data found.
- **Relay angle:** these platforms *buy* SMS/WA/RCS delivery. Relay already emits WebEngage/MoEngage/CleverTap
  webhook formats (`internal/webhook/integrations.go`). A "custom SMS/RCS provider for CleverTap/MoEngage"
  integration is a cheap B2B2B channel — Infobip holds that slot at CleverTap today.

### 2.17 Textlocal India (exit)
- MSG91: Textlocal India stopped recharges 1 May 2025 and shut 31 Oct 2025 ([MSG91, V](https://msg91.com/textlocal-alternative)). Separately, Textlocal UK Messenger closed 31 Jul 2026, migrated to Webex Interact ([Webex, P](https://webexinteract.com/textlocal)). Its SMB base was absorbed by MSG91/Exotel/Fast2SMS.

---

## 3. APAC and Middle East

| Player | Channels | Pricing | Compliance | Financial / news | Weakness (reviews) |
|---|---|---|---|---|---|
| **Unifonic** (Saudi) | SMS, WA, voice, push; multichannel campaigns + Flow Studio ([docs, P](https://docs.unifonic.com/docs/multichannel-campaigns)) | Custom PAYG / committed; not published on G2 | Registers KSA sender IDs for customers incl. NOC letters and trademark certificate for 3-letter IDs ([Unifonic docs, P](https://docs.unifonic.com/articles/products-documentation/ksa-sender-id-registration-requirmnets)) | $21 M Series A 2018; $125 M Series B 2021 (SoftBank, Sanabil) ([Wamda, S](https://www.wamda.com/2021/09/saudis-unifonic-raises-125-million-series-b-softbank-sanabil-arabic)); no 2026 news found | G2 4.8 (13): "perceived lack of transparent, up-front pricing and billing complexity"; "rare message delivery delays… OTPs" ([G2, A](https://www.g2.com/products/unifonic)) |
| **8x8 (Wavecell)** | SMS, WA, voice, video, chat apps in SEA | Usage-based | SEA sender ID regimes | Wavecell bought 2019 for ~$125 M; **acquired Maven Lab (Singapore, Moobidesk)** Jan 2026 ([e27, S](https://e27.co/8x8-acquires-maven-lab-signals-shift-beyond-sms-in-southeast-asia-20260107/)); no divestment found | not researched |
| **Telesign** (Proximus Global) | SMS, voice, verify, phone-ID, WA, RCS | Verify ~$0.01/unit (A); US SMS $0.0085–0.07 per conflicting blogs ([PricingSaaS, A](https://pricingsaas.com/companies/telesign)) | Fraud/identity focus | Part of Proximus Global (§2.2); no IPO found | not researched |
| **Mitto** (Zug) | SMS, WA, **Viber Business** (acquired from Rakuten Viber, date not confirmed), Gartner CPaaS MQ 2023 & 2025 | Not published | — | Exclusive intl partner for Kyrgyzstan MEGA from 1 Jun 2026 ([Mitto status, P](https://status.mitto.ch/incidents/2b6864qp1ry1)) | not found |
| **D7 Networks** (Dubai) | SMS, WA (+$0.005/msg platform fee), Viber, OTP | SMS $0.082 flat local & intl per Toolradar (Feb 2026) ([A](https://toolradar.com/tools/d7-networks/pricing)) **[UNVERIFIED]** | Publishes country regulation pages (e.g. KSA) | private | not found |
| **Vonage / "Nexmo" APAC** (Ericsson) | SMS, voice, video, WA, verify | usage-based | — | Ericsson impairments $2.9 B (2023) + $1.09 B ([DCD, S](https://www.datacenterdynamics.com/en/news/ericsson-ceo-takes-blame-for-vonage-blunder/)); no APAC sale found | not researched |
| **Twilio–ValueFirst legacy** | — | Twilio India SMS ~$0.0832/msg (A) — ~₹7, 30–40× local price | Customer handles DLT on carrier portals (competitor claim; conflicting) | ValueFirst sold to Tanla 2023 (§2.3) | priced out of Indian domestic traffic |

**Gulf regs:** KSA (CST) — all alphanumeric IDs pre-registered, promotional IDs suffixed `-AD`, no promos 20:00–08:00,
DND respected, registrations can take up to 6 months ([Clickatell, V](https://www.clickatell.com/sms-country-regulations/saudi-arabia/)).
UAE (TDRA) — register separately with e& and du, promotional prefix `AD-` since 3 Nov 2020, promos 07:00–21:00
([Telerivet, V](https://www.telerivet.com/blog/uae-sms-compliance-tdra-ad-prefix)). Relay: `AED` currency exists
(`internal/api/enums.go:38`) but AE/GB regimes are stubs (context.md).

---

## 4. EMEA and LatAm

| Player | Channels | Pricing | Financial / news | Weakness |
|---|---|---|---|---|
| **Zenvia** (Brazil, Nasdaq ZENV) | SMS, WA, email, voice; "Zenvia Customer Cloud" (ex-Sirena/Conversion) | Customer Cloud tiers $0 / $130 / $390 / $845 / enterprise (USD, third-party) + channel packages ([prospeo, A](https://prospeo.io/s/zenvia-pricing-reviews-pros-and-cons)) | Q2 2025 revenue BRL 286 M, adj. GP BRL 69 M (24.1%), EBITDA BRL 11 M; CPaaS 73% of revenue, margin squeezed by SMS cost not passed on; workforce cut Jan 2025; Movidesk earn-out ~BRL 253 M renegotiated to Dec 2032 (Feb 2026 6-K, [SEC, P](https://www.sec.gov/Archives/edgar/data/1836934/000129281426000227/zenv20260203_6k.htm)); CPaaS spun into "Zenvia CPaaS" subsidiary (date unclear). **No 2026 quarterly results found.** | financial stress |
| **Wavy / Movile** | — | — | Wavy sold to **Sinch** (BRL 355 M cash + 1.53 M shares, closed Feb 2021) ([Sinch, P](https://www.sinch.com/news/sinch-and-wavy-join-forces-to-accelerate-innovation-and-drive-growth-in-latin-america/)); now Sinch LatAm | — |
| **Botmaker** (Argentina) | WA, IG, Messenger, web, WA Calling "Callbots" | not published | Meta P1 Markets award 2025; targets $100 M revenue (Apr 2026 claim, [startupresearcher, A](https://www.startupresearcher.com/news/botmaker-eyes-100-million-revenue-with-focus-on-large-enterprises)) **[UNVERIFIED]**; built Argentina's federal AI agent "MIA" with Meta | — |
| **Twilio LatAm** | full Twilio stack | Brazil SMS ~$0.0599 (A) | — | USD pricing vs local BRL players |
| **LINK Mobility** (Nordics, Oslo-listed) | SMS, RCS (Juniper RBM Leader), WA, email; METRO Chat Germany RCS | not published | Q2 2026 GP NOK 492 M, organic GP +2%, adj. EBITDA NOK 272 M (record), H2 guide mid–high single-digit; bought LabsMobile (Spain, €2.4 M, Oct 2026), Web2SMS (Romania), FireText (UK); 35+ acquisitions since 2014 ([LINK, P](https://www.linkmobility.com/news/q2-interim-report-2026)) | roll-up integration |
| **Commify** (UK) | Esendex: SMS, RCS, WA, Esendex Connect (2025); SMSFactor (France), Text Request (US, Oct 2024) | not researched | ECI Partners buyout €300 M (2023); 18 acquisitions; 45,000+ customers; 5 bn msgs/yr ([BusinessCloud, S](https://businesscloud.co.uk/news/e300m-private-equity-takeover-at-commify/)) | — |
| **GatewayAPI** (Denmark) | SMS, RCS from €0.041/msg | usage only, no subscription ([GatewayAPI, P](https://gatewayapi.com/pricing/)); DK €0.033, DE €0.06 per third party **[UNVERIFIED]** | — | — |
| **Messente** (Estonia) | SMS, WA, Viber | from €0.001 base + Light €99 / Professional €199/mo ([Messente, P](https://messente.com/pricing)) | — | — |

---

## 5. Channel coverage matrix (confirmed only; "?" = not confirmed in this research)

| Player | SMS | RCS | WA | Email | Voice | AMB | Viber | TG | IG | Push | DLT handled for customer | Self-serve | Journey | Inbox | AI agent | Reseller / WL |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Gupshup | Y | Y | Y | Y (legacy) | Y | ? | ? | ? | Y | ? | help flow | WA yes | Y | Y | Y | ISV prog |
| Route Mobile | Y | Y | Y | Y | Y | ? | Y | ? | ? | ? | ? | N | ? | ? | ? | ? |
| Tanla (Karix/VF) | Y | Y | Y | Y | Y | ? | ? | ? | ? | ? | runs DLT (Trubloq) | N | ? | ? | Wisely | ? |
| MSG91 | Y | Y | Y | Y | Y | Y | ? | ? | ? | ? | guidance | Y | Y | Y (Hello) | ? | Y (>5 L/mo) |
| Exotel | Y | Y | Y | ? | Y (core) | ? | ? | ? | ? | ? | guidance | partial | ? | Y | Y (voice) | ? |
| Netcore | Y | Y | Y | Y (core) | Y | ? | Y | ? | ? | Y | ? | N | Y | ? | Co-Marketer | ? |
| Airtel IQ / Gateway | Y | Y | Y | ? | Y | ? | ? | ? | ? | ? | **native** | IQ Reach | ? | ? | IQ Verify | ? |
| Jio (JioCX/Haptik/Interakt) | Y | Y (iOS excl.) | Y | ? | ? | ? | ? | ? | Y | ? | ? | Interakt Y | ? | Y | Y | ? |
| Fast2SMS | Y | Y | Y | ? | ? | ? | ? | ? | ? | ? | guidance | Y | ? | ? | ? | Y |
| Interakt / AiSensy / WATI | – | Interakt Ent. | Y | – | WATI | – | – | – | Y | – | n/a | Y | flows | Y | Y | AiSensy WL |
| Unifonic | Y | ? | Y | ? | Y | ? | ? | ? | ? | Y | **registers KSA IDs** | partial | Flow Studio | ? | ? | ? |
| LINK Mobility | Y | Y | Y | Y | ? | ? | ? | ? | ? | ? | n/a | N | ? | ? | ? | ? |
| Zenvia | Y | ? | Y | Y | Y | ? | ? | ? | ? | ? | n/a | Y (Starter $0) | Y | Y | ? | ? |
| **Relay** | **Y (SMPP live)** | **Y (Sigmo only)** | sandbox | sandbox | sandbox | N | N | N | N | N | registry + gate (`gate.go:309`) | no payments | linear only | SMS/RCS | keyword bots | backend WL, no sub-accounts |

Relay row per context.md and `internal/connector/smpp.go:802-803` (non-SMS → sandbox fallback).

---

## 6. Regional matrix — where Relay wins and loses

| Region | Market shape | Relay **wins** when | Relay **loses** when |
|---|---|---|---|
| **India — enterprise BFSI/gov** | Tanla, Route, Kaleyra, Airtel IQ Gateway; operator-owned DLT + fraud signals | Buyer wants a neutral, multi-operator orchestration layer with DLT pre-send gating, consent per channel, RCS→SMS fallback, and auditable routing they can see (`gate.go:309`, smpp DLT checks) — and does not want to be locked to one operator | Buyer wants silent auth, network fraud scores, 1600 voice, on-prem — Airtel IQ Gateway does all of it with a telco's balance sheet. Relay has one live operator (Videocon) and no voice. |
| **India — mid-market brands** | Gupshup, Netcore, MoEngage/CleverTap buying CPaaS, Yellow.ai | Gupshup/Yellow.ai churners want stability and transparent pricing; engagement platforms want a cheap SMS/RCS pipe with their webhook format (already built) | WhatsApp must be live — it is sandbox only. No GST invoice, no payment gateway, no SDKs. |
| **India — SMB self-serve** | MSG91, Fast2SMS, SMSCountry, AiSensy, Interakt, WATI | — (not today) | Every competitor publishes INR prices, takes UPI/cards, and handles WA. Relay has none of that and its DND default blocks all promos (`dnd.go:38-43`). |
| **India — resellers** | MSG91 (>5 L/mo), Fast2SMS panel, AiSensy WL | White-label branding exists in backend | No sub-account hierarchy, no WL UI — resellers cannot onboard their own clients. |
| **Middle East** | Unifonic, D7, Infobip, Twilio; KSA/UAE sender-ID registries | Possible later: AED currency exists | AE regime is a stub; no Arabic, no KSA ID registration service, no local operator binds. Do not sell here in 2026–27. |
| **SEA** | 8x8/Wavecell (+Maven Lab), Sinch, Infobip | — | No routes, no regimes. |
| **Europe** | LINK, Commify, GatewayAPI, Messente; price-transparent, RCS mature | — | GB regime stub; GatewayAPI is usage-only with published per-country prices; LINK has 35+ acquisitions and direct operator links. |
| **LatAm** | Sinch(Wavy), Zenvia, Botmaker, Twilio; WhatsApp-dominant | — | WhatsApp not live; no BRL; no Portuguese/Spanish. |

**Strategic conclusion:** Relay is an India-first, compliance-first orchestration product. Its credible wedge in
2026–27 is mid-market/enterprise Indian senders who want vendor-neutral routing across operators and channels,
plus engagement platforms (CleverTap/MoEngage/WebEngage) needing a delivery pipe. To survive Airtel IQ Gateway it
must (1) get WhatsApp live, (2) add at least two more live operator binds, (3) ship a DND register integration so
promotional traffic flows, (4) ship GST invoicing + payment gateway, and (5) publish INR pricing.

---

## 7. Pricing benchmark cheat-sheet (India, ex-GST unless stated)

| Item | Low | Typical | High | Sources |
|---|---|---|---|---|
| DLT SMS, self-serve | ₹0.11 (Fast2SMS ≥₹6 L) | ₹0.16–0.18 | ₹0.25 | Fast2SMS P, MSG91 P |
| SMS OTP, negotiated | ₹0.13 | ₹0.13–0.18 | Twilio ~₹0.45 (V) | MSG91 P, messagecentral V |
| RCS (Jio, from 1 Sep 2026) | OTP ₹0.11 | transactional ₹0.25 | marketing ₹0.30 | Inc42 S |
| RCS (Airtel, reported) | ₹0.11 | | | TelecomTalk S |
| WA marketing | Meta ₹0.8631 | Interakt ₹0.95 | AiSensy ₹1.09 | §1.5 |
| WA utility/auth | Meta ~₹0.115 | ₹0.13–0.15 | ₹0.16 | §1.5 |
| BSP platform fee | Gupshup $0.001/msg | — | — | Gupshup P |
| DLT PE registration | free (2nd portal) | ₹5,900 incl. GST once | | Infobip V |

---

## 8. Sources not reachable / gaps

- TRAI press release PDF (`cms.trai.gov.in`) refused connection on 15 Oct 2026; amendment details taken from Storyboard18 and vendor docs.
- Meta INR rate-card CSV not read directly; India rates from two BSP blogs that agree.
- No Q1/Q2 2026 Zenvia results, no Proximus Global H1 2026, no Netcore/WebEngage/MyOperator FY25 numbers found.
- Review-site evidence is thin for Route Mobile, Tanla, Kaleyra, 8x8, Telesign, Mitto, D7, LINK, Commify.
- Exotel, Airtel IQ, Route, Tanla, Kaleyra, Unifonic publish no prices.

---

## 9. Unverified claims (do not quote externally without checking)

1. Gupshup FY25 revenue ₹1,943 cr, −5%, PAT ₹26 cr — single aggregator; Gupshup has not disclosed FY24/FY25.
2. Airtel–Google RCS 80:20 revenue share and ₹0.11/msg — unnamed industry executives.
3. Meta India list rates (₹0.8631 marketing, ~₹0.115 utility/auth) — from BSP blogs, not Meta CSV.
4. Meta marketing "real-time auction from 1 Oct 2026" (MoEngage wording) — Meta docs say *open beta* of max-price from Oct 2026, mandatory Q2 2027; "auction" framing is the vendor's.
5. RCS marketing "6–7 paise → 20–25 paise" (MoEngage) — conflicts with Jio's reported prior ₹0.08–0.10.
6. Which DLT portal is free (Jio vs others) — vendors conflict.
7. Trubloq handles ~63–70% of India A2P traffic — 2020–21 company figures.
8. Vi ~1 bn RCS messages/month — undated.
9. Haptik ARR ~$35 M — single low-quality source.
10. MoEngage Series F-II $57 M (Dec 2025) vs $180 M Series F — sources do not reconcile; Aampe acquisition via Owler only.
11. Botmaker $100 M revenue target — single source.
12. D7 $0.082 flat SMS rate; GatewayAPI DK €0.033 / DE €0.06 — third-party.
13. Knowlarity ₹1,999/agent/month — competitor page that "could not be loaded cleanly".
14. India A2P market size ($2.4 B–$10 B) — analyst reports with incompatible scopes; own estimate ~$1.0–1.3 B is derived, not sourced.
15. DCA pilot outcome after Feb 2026 — no 2026 status found.
16. Textlocal India shutdown 31 Oct 2025 — from a competitor (MSG91); UK closure 31 Jul 2026 is from Webex.
17. Mitto's Viber Business acquisition date.
18. Relay gaps listed in §1.3 (URL whitelist validation, header-suffix awareness, 1600/DCA) — not checked in code in this pass.
