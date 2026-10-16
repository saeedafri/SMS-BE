# 02 — Global CPaaS competitors: teardown

Research date: **15 Oct 2026**. Every external URL below was accessed 15 Oct 2026 unless a different date is given.

## How to read this

| Mark | Meaning |
|---|---|
| ✓ | Verified this session from the cited page |
| ✓? | Known from earlier public material (prior knowledge), **not** re-checked this session. Treat it as unverified |
| ✗ | Confirmed absent, or the vendor says it is not offered |
| ? | Unknown or not found this session |
| (3p) | From a third-party aggregator or review site, not the vendor |

Relay facts come from the shared context file (first-pass audit, 15 Oct 2026) and from the code lines cited inline.

Prices are list prices in USD, per SMS segment, outbound, before carrier pass-through fees, unless stated otherwise.

---

## 0. Headline findings

1. **US SMS prices have converged around $0.004–$0.008 base plus carrier pass-through. Margin has moved to fees and platform.**
   - Base rates: Twilio $0.0083, Sinch $0.0078, Plivo $0.0077, Telnyx $0.004, Bird $0.0035.
   - Twilio's Q2 2026 non-GAAP gross margin fell 160 bp. Zacks attributes this to $71M of US carrier pass-through fees ([Twilio Q2 26 8-K](https://www.sec.gov/Archives/edgar/data/0001447669/000144766926000088/twloq226ex991.htm), [Zacks](https://www.zacks.com/stock/news/2971055/twilios-q2-earnings-surpass-expectations-revenues-rise-yy)).
   - Bandwidth's GAAP gross margin fell from 40% to 36% for the same reason ([Bandwidth Q2 26](https://www.bandwidth.com/newsroom/bandwidth-announces-second-quarter-2026-financial-results/)).
2. **Every major player pivoted in 2026 to "AI agents + conversation layer":**
   - Twilio Conversation Memory / Orchestrator / Agent Connect, GA at SIGNAL, 6 May 2026 ([Twilio IR](https://investors.twilio.com/news-releases/news-release-details/twilios-next-generation-platform-infrastructure-layer-every)).
   - Infobip AgentOS, available 1 Apr 2026 ([IT Brief](https://itbrief.co.uk/story/infobip-unveils-agentos-to-power-ai-first-customer-service)).
   - Sinch "agentic conversations", 26 Feb 2026 ([eCommerceNews](https://ecommercenews.com.au/story/sinch-unveils-ai-agent-tools-for-customer-engagement)).
   - CM.com HALO, with ARR up 187% ([CM.com Q2 26](https://www.cm.com/cdn/web/en/file/investor-relations/cmcom-q2-2026-press-release.pdf)).
   - Plivo Voice AI, June 2026 ([JoinNextDev](https://www.joinnextdev.com/a/plivo/plivo-voice-ai-the-cpaas-moat-just-got-smarter)).
   - Telnyx WhatsApp Business Calling, 30 Apr 2026 ([AAP/GlobeNewswire](https://www.aap.com.au/aapreleases/globenewswire9710384/)).
3. **Bird is the price-and-DX disruptor to watch.**
   - Prices: US SMS $0.0035, UK $0.050, India $0.065, Brazil $0.0325 ([bird.com/pricing/sms.md](https://bird.com/pricing/sms.md)). Bird also includes Verify for free.
   - Agent-first surfaces: every page is available as Markdown, plus a hosted MCP server and a CLI ([bird.com/llms.txt](https://bird.com/llms.txt)).
   - Bird bid €5.16/share for CM.com in Nov 2025. CM.com's boards rejected it ([Bird blog](https://bird.com/blog/bird-announces-intention-to-acquire-cm-com-through-an-all-cash-public-offer), [DutchNews](https://www.dutchnews.nl/2025/11/bird-makes-surprise-e166-million-bid-for-rival-cm-com)).
4. **Meta started charging for WhatsApp service messages on 1 Oct 2026.** Several BSPs say each number gets 1,000 free per month ([Sinch support](https://support.app.sinch.com/hc/en-us/articles/17433223449103-How-WhatsApp-pricing-works), [Bird WhatsApp pricing](https://bird.com/pricing/whatsapp.md), [Zoho](https://help.zoho.com/portal/en/community/topic/whatsapp-business-platform-message-pricing-guide)). This changes every inbox and chatbot cost model, including Relay's.
5. **Published WhatsApp markups on top of Meta range from $0.00016 to $0.005 per message.**
   - Vonage: from €0.0001 ([Vonage](https://www.vonage.com/communications-apis/messages/features/whatsapp/pricing/)).
   - Plivo: $0.001 plus $10 per WABA per month ([Plivo](https://www.plivo.com/whatsapp/pricing/)).
   - Telnyx: $0.004 ([Telnyx](https://telnyx.com/pricing/whatsapp)).
   - Twilio: $0.005, charged both inbound and outbound ([Twilio](https://www.twilio.com/en-us/whatsapp/pricing)).
   - Infobip, Sinch and CM.com: contract or quote only.
6. **India is not a strength for the global players.**
   - Twilio India is $0.0832 per segment and does not mention DLT ([Twilio IN](https://www.twilio.com/en-us/sms/pricing/in)).
   - Plivo India is $0.0800, over an international route only ("Domestic: Not Supported") ([Plivo IN](https://www.plivo.com/sms/pricing/in/)).
   - MSG91's domestic rate is about ₹0.16–0.25, roughly $0.002–0.003. That makes global international routing 25–40× more expensive. Relay's native DLT plus SMPP binds are a real moat here, but only for domestic Indian traffic.
7. **Review sentiment is terrible across the category.** The same themes repeat: unexplained account suspensions, 10DLC limbo, support SLAs, and fraud bills.
   - Trustpilot: Twilio 1.1/5 (978 reviews), Plivo 1.2, Clickatell 1.3, Sinch 1.5, Bird 1.6, Infobip 1.6, Vonage 2.1. Telnyx sits mid-table at 3.3. Brevo (3.9), Mailgun (4.1) and CM.com (4.0) score highest, though CM.com's own breakdown shows 47% 1-star reviews.
   - Trust and support is Relay's cheapest differentiator.

---

## 1. Twilio (Programmable Messaging, Voice, SendGrid, Segment, Verify, Studio, Flex)

### Snapshot

- **HQ and ownership:** San Francisco (✓?). Public, NYSE: TWLO.
- **Scale:** Q2 2026 revenue $1.50B, up 22% reported and 17% organic. Dollar-based net expansion 116%. Non-GAAP operating income $285M, free cash flow $353M ([Twilio IR](https://investors.twilio.com/news-releases/news-release-details/twilio-announces-second-quarter-2026-results), [8-K](https://www.sec.gov/Archives/edgar/data/0001447669/000144766926000088/twloq226ex991.htm)).
- **Guidance:** Full-year 2026 reported growth raised to 18–18.5% ([Zacks](https://www.zacks.com/stock/news/2971055/twilios-q2-earnings-surpass-expectations-revenues-rise-yy)).

**2025–26 news**
- **Stytch acquisition:** announced 30 Oct 2025 and closed 14 Nov 2025. Stytch is identity for AI agents ([Security Boulevard](https://securityboulevard.com/2025/10/the-twilio-stytch-acquisition-a-watershed-moment-for-developer-first-ciam/), [Stytch changelog](https://stytch.com/docs/resources/changelog)).
- **SIGNAL, 6 May 2026:** Conversation Memory, Conversation Orchestrator, Conversation Intelligence and Agent Connect went GA. Agent Connect is an open-source Python/TS SDK ([Twilio IR](https://investors.twilio.com/news-releases/news-release-details/twilios-next-generation-platform-infrastructure-layer-every), [BlogGeek](https://bloggeek.me/twilio-signal-2026/)).
- **SendGrid site merged into twilio.com** ([twilio.com/sendgrid](https://www.twilio.com/en-us/sendgrid)).
- **RCS:**
  - New guided RCS compliance onboarding, 24 Feb 2026 ([changelog](https://www.twilio.com/en-us/changelog/new-guided-compliance-registration-experience-for-rcs-business-m)).
  - Netherlands RCS with KPN, Mar 2026 ([CXM](https://cxm.world/customer-experience/rcs-is-coming-to-the-netherlands-powered-by-twilio/)).
- **10DLC:**
  - Granular rejection codes from 23 Mar 2026 ([changelog](https://www.twilio.com/en-us/changelog/more-actionable-error-codes-for-a2p-10dlc-campaign-registrations)).
  - T&C URL mandatory for API-created campaigns from 30 Jun 2026 ([error 30934](https://www.twilio.com/docs/api/errors/30934)).
- **Outages (3p):**
  - 24 Sep 2026: nine incidents, including a Brazil SMS delay of about 21 hours.
  - 30 Sep 2026: 13 SMS incidents.
  - Earlier in 2026: Israel (about 20 hours) and Guatemala (6.5 hours) delays.
  - Sources: [UptimeRobot 24 Sep](https://uptimerobot.com/is-it-down/twilio/2026-09-24/), [30 Sep](https://uptimerobot.com/is-it-down/twilio/2026-09-30/).

### Channel depth

**SMS**
- Long code (10DLC), toll-free and short code, all at $0.0083.
- Alphanumeric sender IDs abroad; free in the UK ([Twilio GB](https://www.twilio.com/en-us/sms/pricing/gb)).
- MMS at $0.022.
- Lookup (line type, SIM swap) ✓?
- Unicode and concatenation billed per segment ✓.
- Scheduling via Messaging Services ✓?
- Failed-message fee of $0.001 ✓ ([Twilio US](https://www.twilio.com/en-us/sms/pricing/us)).

**RCS**
- Rich cards and carousels ✓ ([skills.sh](https://www.skills.sh/twilio/ai/twilio-rcs-messaging)).
- Automatic SMS fallback ✓.
- Console registration covers only Google and US carriers so far, with more countries planned ([changelog](https://www.twilio.com/en-us/changelog/new-guided-compliance-registration-experience-for-rcs-business-m)).
- US RCS onboarding fees apply ([Twilio US](https://www.twilio.com/en-us/sms/pricing/us)).

**WhatsApp**
- Twilio is a BSP / Tech Provider (✓?) with embedded signup (✓?).
- Fee is $0.005 per message, inbound and outbound ([Twilio WA](https://www.twilio.com/en-us/whatsapp/pricing)).
- Flows, catalog and calling: ?

**Email (SendGrid)**
- Claims more than 200B emails per month ([twilio.com/sendgrid](https://www.twilio.com/en-us/sendgrid)).
- Dedicated IPs on Pro (3p). Inbound Parse ✓?. Dynamic templates ✓?. Email Validation ✓?.
- Free tier replaced by a 60-day trial (3p, [automationatlas](https://automationatlas.io/answers/sendgrid-pricing-explained-2026/)).

**Voice** ([Twilio Voice US](https://www.twilio.com/en-us/voice/pricing/us))

| Item | Price |
|---|---|
| Local and toll-free numbers | $1.15 / $2.15 per month |
| Outbound | $0.014/min |
| Inbound local / toll-free | $0.0085 / $0.022 per min |
| SIP interface / BYOC | $0.004/min |
| Premium TTS | $0.0008–0.013 per 100 characters |
| STT | $0.02 per Gather |
| AMD | $0.0075 per call |
| Conferencing | from $0.0018 per participant-minute |
| ConversationRelay (AI voice) | $0.07/min |
| Media Streams | $0.0044/min |
| Branded calling | $0.12 per call |

### Platform

- **Studio** (flow builder) ✓?
- **Flex** (contact centre; Agent Connect integrates into Flex) ✓ ([cuspera](https://www.cuspera.com/vendors/twilio/news))
- **Segment CDP** ✓?
- **Verify** ([Verify pricing](https://www.twilio.com/en-us/verify/pricing)):
  - $0.05 per successful verification plus channel fee.
  - Fraud Guard SMS-pumping protection included.
  - Silent Network Auth available.
- **Lookup** (number intelligence) ✓?
- **SDKs:** official helper libraries in Node, Python, C#, Java, Go, PHP, Ruby ✓?
- **OpenAPI:** public specs on GitHub (twilio-oai) ✓?
- **Test credentials and magic numbers** ✓?
- **Status page:** status.twilio.com ✓ ([status](https://status.twilio.com/incidents/6x8s6vgrpw51))
- **Subaccounts** ✓?
- **ISV programme:** ISV/partner registration flows ✓?
- **SSO and SOC 2 / ISO 27001 / HIPAA-eligible** ✓?
- **Data residency:** Ireland/Australia regions ✓?

### Pricing (accessed 15 Oct 2026; the US page says "current as of October 2026")

**SMS by destination**

| Destination | SMS out | Source |
|---|---|---|
| US | $0.0083, plus carrier fees $0.0035–0.005 | [US](https://www.twilio.com/en-us/sms/pricing/us) |
| UK | $0.056 | [GB](https://www.twilio.com/en-us/sms/pricing/gb) |
| India | $0.0832 | [IN](https://www.twilio.com/en-us/sms/pricing/in) |
| UAE | $0.1176 | [AE](https://www.twilio.com/en-us/sms/pricing/ae) |
| Brazil | $0.0599 | [BR](https://www.twilio.com/en-us/sms/pricing/br) |
| Indonesia | $0.4414 | [ID](https://www.twilio.com/en-us/sms/pricing/id) |

**Other fees**
- **10DLC:** brand $4.50 one-time. Standard vetting $41.50. Campaign vetting $15. Monthly campaign fee $1.50–$10 ([Twilio brand docs](https://www.twilio.com/docs/trust-hub/registrations/a2p-10dlc-brand), [vetting FAQ](https://support.twilio.com/hc/en-us/articles/11587910480155-A2P-10DLC-Campaign-Vetting-FAQ)).
- **Short code (UK):** $1,667 per month ([GB](https://www.twilio.com/en-us/sms/pricing/gb)).
- **Email:** Essentials $19.95 and Pro $89.95 per month (3p, [usecarly](https://www.usecarly.com/blog/sendgrid-pricing/)).

### Onboarding

- $15 trial credit, no card required ([Twilio signup credits](https://twilio.com/en-us/signup-credits)).
- Trial limits: verified numbers only (up to 5), one number, trial prefix on calls, template bodies only ([trial docs](https://www.twilio.com/docs/usage/trials)).
- For US sending, 10DLC brand and campaign registration takes days to weeks.

### Weaknesses (reviews)

**Trustpilot: 1.1/5 from 978 reviews** ([Trustpilot](https://www.trustpilot.com/review/twilio.com)). Representative quotes:
- "SendGrid (Twilio) blocked/closed my account and refuses to say why." (16 Sep 2026)
- "Charged US$484.41 for fraudulent traffic, no response after 7 days" (3 Oct 2026)
- "My submission sat in "pending" status for three months." (10DLC, 11 Oct 2026)
- "customer support manages to be even worse than the platform itself." (15 Oct 2026)

**Other evidence**
- A forum user gave up after a CTA rejection and could not reach a human ([Inductive Automation forum](https://forum.inductiveautomation.com/t/twilio-a2p-10dlc-campaign-approval-requirements/98586)).
- A July 2026 Console bug made rejected campaigns un-editable for about 8 hours ([isdown](https://isdown.app/status/twilio/incidents/618205-inability-to-edit-and-resubmit-rejected-a2p-campaigns-via-console-ui)).

### What Relay should copy and where Relay can beat Twilio

**Copy**
- Per-country pricing pages with a CSV download.
- A failed-message fee, which deters spray-and-pray sending.
- Verify bundled with pumping protection.
- Messaging Services: a sender pool, sticky sender, and automatic fallback.
- Granular registration rejection codes.
- Magic test numbers.

**Beat**
- **India domestic price.** Twilio charges $0.0832 on an international route. Relay uses domestic SMPP with DLT (`internal/connector/smpp.go:810` refuses Indian sends that lack a DLT entity or template).
- **A human registration desk** with an SLA, instead of 3-month pending queues.
- **Transparent account-suspension reasons.**

---

## 2. Vonage (Ericsson) — Communications APIs

### Snapshot

- **HQ:** Holmdel, NJ (✓?).
- **Ownership:** Ericsson subsidiary since July 2022, bought for $6.2B ([Wikipedia](https://en.wikipedia.org/wiki/Vonage)).
- **Impairments:** about $4B written down in total. Cumulative operating loss of about $450M from 2022 to 2025 ([Light Reading](https://www.lightreading.com/5g/ericsson-kicks-off-vonage-revival-plan-after-years-of-losses), [CX Today](https://cxtoday.com/ericssons-vonage-losses-exceed-4bn-what-went-wrong)).
- **New CEO:** Christophe Van de Weyer from 15 Aug 2026. He ran the API business unit and was formerly CEO of Telesign ([CX Today](https://www.cxtoday.com/service-management-connectivity/ericsson-appoints-new-vonage-ceo/), [Fierce](https://www.fierce-network.com/wireless/ericsson-names-christophe-van-de-weyer-head-global-communications-platform-biz-and-ceo)).
- **Strategy:** network APIs (Aduna JV). Adoption is slower than planned and mostly fraud and SIM checks ([Light Reading](https://www.lightreading.com/5g/ericsson-kicks-off-vonage-revival-plan-after-years-of-losses)).

### Channel depth

- **SMS:** around $0.008 per US message base (3p). Carrier pass-through: AT&T $0.0035 from 1 Apr 2026, T-Mobile $0.0045 from 19 Jan 2026 ([Vonage support](https://api.support.vonage.com/hc/en-us/articles/5672262029084-SMS-MMS-Pass-through-Fees)).
- **Messages API:** WhatsApp, Messenger, Viber and RCS through one API ✓? ([costbench](https://www.costbench.com/software/sms-api/vonage/), 3p).
- **WhatsApp:** platform fee from €0.0001 (about $0.00016) per message, plus Meta ([Vonage WA](https://www.vonage.com/communications-apis/messages/features/whatsapp/pricing/)).
- **Email:** ✗ (no native email API ✓?).
- **Voice:** Voice API, SIP trunking, AI Studio low-code agents (GA per Apr 2026 article) ([Vonage AI Studio](https://www.vonage.com/resources/articles/vonage-no-code-ai-builder/)).
- **Verify v2:** silent auth via network APIs ✓?
- **Also:** Video API (ex-TokBox) ✓?

### Platform

- AI Studio (flow builder), Verify, Number Insight ✓?
- SDKs: Node, Python, Java, .NET, PHP, Ruby, Go?, Kotlin/Swift ✓?
- OpenAPI specs published ✓?
- Subaccounts API ✓?

### Pricing

- vonage.com pricing pages returned **HTTP 403** to our fetcher. US SMS is about $0.0076–0.0086 (3p, [textbee](https://textbee.dev/sms-pricing/vonage/us), [casrai](https://www.casrai.org/guides/vonage-pricing)).
- UK, India, UAE, Brazil and Indonesia: ? (not retrievable).

### Onboarding

- Free trial credit (around €2 ✓?), then KYC for some countries ✓?

### Weaknesses

- **Trustpilot 2.1/5, 1,554 reviews**, mostly UCaaS ([Trustpilot](https://www.trustpilot.com/review/www.vonage.com)).
  - On the API: "We've been using their API for a while and this API is very unreliable thesedays." (27 Jun 2026)
  - On support hours: "Weekend support is unavailable…" (11 Jun 2026)
- **Strategic uncertainty under Ericsson,** with repeated write-downs.

### Copy / beat

- **Copy:** a single Messages API with a channel field, and failover rules. Relay already has `POST /v1/messages` (`internal/gen/api/control.gen.go:9281`).
- **Beat:** support responsiveness and pricing pages that load. Vonage's own pricing pages refuse bots, which also hurts AI-agent buyers.

---

## 3. Sinch (incl. Mailgun, Mailjet, ACL Mobile, Inteliquent)

### Snapshot

- **HQ and listing:** Stockholm, listed (✓?).
- **Q2 2026:** net sales SEK 6,866M, organic growth 6%. Adjusted EBITDA SEK 858M, a 12.5% margin ([Sinch Q2 26](https://www.group.sinch.com/media/press-releases-and-news/2026/sinch-reports-second-quarter-2026-results--accelerated-revenue-growth-led-by-americas/)).
- **Leadership:** CEO Laurinda Pang stepped down. CFO Jonas Dahlberg is acting CEO (same source).
- **2027 targets:** 7–9% organic growth.
- **Product:** "Agentic conversations" launched 26 Feb 2026 ([eCommerceNews](https://ecommercenews.com.au/story/sinch-unveils-ai-agent-tools-for-customer-engagement)).
- **Mailjet price rise:** Essential went from $17 to $19, 9 Sep 2026 (3p, [pricingsaas](https://pricingsaas.com/companies/mailjet)).

### Channel depth

**SMS (US)** ([Sinch SMS](https://sinch.com/pricing/sms/))

| Item | Price |
|---|---|
| 10DLC / toll-free SMS | $0.0078 |
| Short code SMS | $0.009 |
| MMS | $0.018–0.02 |
| 10DLC number | $1/mo plus $1 setup |
| Toll-free number | $2/mo |

- India: ACL Mobile acquisition gives domestic DLT routes ✓?

**RCS**
- Live demo at MWC 2026 with Google ✓ ([eCommerceNews](https://ecommercenews.com.au/story/sinch-unveils-ai-agent-tools-for-customer-engagement)).
- Strong RCS Tier-1 aggregator in many markets ✓?

**WhatsApp**
- Two-part fee: Meta plus a Sinch fee set by contract ([Sinch support](https://support.app.sinch.com/hc/en-us/articles/17433223449103-How-WhatsApp-pricing-works)).
- The Sinch fee applies even to free-form replies where Meta waives its fee ([Sinch community](https://community.sinch.com/t5/WhatsApp/Sinch-WhatsApp-Business-API-Pricing-Frequently-Asked-Questions/m-p/6867)).

**Email** ([Mailgun](https://www.mailgun.com/pricing/), [Mailjet](https://www.mailjet.com/pricing/))

| Plan | Price | Volume | Notes |
|---|---|---|---|
| Mailgun Free | $0 | 100/day | |
| Mailgun Basic | $15/mo | 10k | |
| Mailgun Foundation | $35/mo | 50k | |
| Mailgun Scale | $90/mo | 100k | Overage $1.10 per 1k; dedicated IP $59/mo; inbound routes; validation |
| Mailjet Free | $0 | 6k/mo (200/day) | |
| Mailjet Essential | $19/mo | 15k | |
| Mailjet Premium | $29/mo | 15k | Includes a dedicated IP at 100k+ |

**Voice**
- Voice API, Elastic SIP Trunking (Inteliquent US), number porting ✓?

### Platform

- Sinch Engage (inbox) ✓?. Conversation API ✓?. Verification API with flash call ✓?. Number lookup ✓?.
- SDKs: Java, .NET, Node, Python ✓?.

### Pricing beyond the US

- Not published ([Sinch SMS](https://sinch.com/pricing/sms/) shows only the US and Canada). UK, India, UAE, Brazil, Indonesia: ?

### Onboarding

- Self-serve for the US. Most other countries go through sales.

### Weaknesses

- **Trustpilot 1.5/5, 32 reviews** ([Trustpilot](https://www.trustpilot.com/review/sinch.com)). Quote: "Their response times are abysmal, often taking 4-5 months to address any issues." (1 Jun 2024)
- **Mailgun Trustpilot 4.1/5, 1,538 reviews,** but recent 1-star posts include "They play bait-and-switch games." (9 Oct 2026) ([Trustpilot](https://www.trustpilot.com/review/mailgun.com))
- **CEO turnover and low organic growth.**

### Copy / beat

- **Copy:** Mailgun-style inbound routes, plus log retention as a pricing lever (1/5/30 days by tier).
- **Beat:** a transparent WhatsApp fee. Sinch's is contract-only and also charges on free-form replies.

---

## 4. Infobip

### Snapshot

- **HQ and ownership:** Vodnjan, Croatia (✓?). Private; One Equity Partners invested (✓?).
- **2025 results (3p):** revenue $2.34B, adjusted EBITDA $200.6M (up 42%), first-ever net profit ([ICT Business](https://www.ictbusiness.biz/business/infobip-shifts-from-growth-to-profits), [Lider](https://en.lider.media/?p=218466)).
- **IPO:** now a "longer-term option".
- **Leadership:** COO Roberto Kutić left in 2025. Management reshuffled in early 2026 (same sources).
- **AgentOS:** announced Feb 2026, available 1 Apr 2026. It combines a CDP, journey orchestration and MCP connectors across 15+ channels ([IT Brief](https://itbrief.co.uk/story/infobip-unveils-agentos-to-power-ai-first-customer-service)). Release notes tag it "early access" ([Infobip notes](https://www.infobip.com/docs/release-notes/driftwood)).

### Channel depth

- **SMS:** global direct-connect footprint ✓?. Prices appear only in a calculator, as averages; per-network rates are in the Portal ([Infobip SMS pricing](https://www.infobip.com/sms/pricing)).
- **RCS:** RCS billing transparency and sender-level analytics, Q1 2026 ([Infobip Q1 26](https://www.infobip.com/product-updates/product-updates-q1-2026)). Carousels ✓?
- **WhatsApp:** BSP. WhatsApp Business App and Cloud API coexistence. AI Video Editor ([Q1 26](https://www.infobip.com/product-updates/product-updates-q1-2026)). Calling ✓?
- **Markup (3p):** 30–55% above Meta ([YCloud](https://www.ycloud.com/vs-infobip)). India example ₹1.18 against Meta's ₹0.7846 ([WANotifier](https://wanotifier.com/best-infobip-alternatives/)).
- **Email:** yes ✓?, with dedicated IPs ✓?
- **Voice:** Voice API, SIP trunking, IVR builder, voice OTP ✓?

### Platform

- **Moments** (journeys), **Conversations** (inbox), **People** (CDP), **Answers** (chatbot), AgentOS ✓?
- **2FA API** ✓?
- **Signals / anti-fraud** ✓?
- **Number Lookup** ✓?
- **SDKs:** Java, C#, Python, PHP, Node, Go ✓?
- **OpenAPI:** public specs ✓?
- **Conversational modules:** from €39/month (3p, [WANotifier](https://wanotifier.com/best-infobip-alternatives/)). AgentOS from $530/month (3p, [YCloud](https://www.ycloud.com/vs-infobip)).

### Onboarding

- 60-day trial, shared sender, 100 free WhatsApp conversations (3p, [YCloud](https://www.ycloud.com/vs-infobip)).

### Weaknesses

- **Trustpilot 1.6/5, 35 reviews** ([Trustpilot](https://www.trustpilot.com/review/infobip.com)):
  - "Their SMS rarely reach the destination, their support is poor." (28 Sep 2026)
  - "They haven't been able to solve a simple integration problem with Hubspot for four weeks."
- **G2 4.3/5, about 58 reviews.** Main complaints: slow support and pricing complexity ([G2](https://g2.com/products/infobip/reviews?page=5)).

### Copy / beat

- **Copy:** AgentOS's MCP connector model, and sender-level analytics by business unit.
- **Beat:** published per-network prices (Infobip hides them in the Portal) and a WhatsApp markup that is not 30–55%.

---

## 5. Bird (formerly MessageBird)

### Snapshot

- **HQ and leadership:** Amsterdam. CEO Robert Vis.
- **Size:** about $244–250M net revenue in 2025, profitable (3p, [iamsterdam](https://startupmap.iamsterdam.com/news/note/bird-cuts-20-of-staff-and-shifts-away-from-europe-as-us-customers-take-over)).
- **Layoffs:**
  - 2026: about 20% of staff, mostly in Europe. 75% of revenue now comes from US-headquartered firms (same source).
  - Earlier: 90 jobs in 2024 and 120 jobs in early 2025.
- **Pricing history:** cut SMS prices by 90% at the Feb 2024 rebrand ([TechCrunch](https://techcrunch.com/2024/02/01/messagebird-rebrands-as-bird-and-slashes-prices-by-90-on-sms-to-take-on-twilio)).
- **Hostile bid for CM.com:** €5.16/share announced 5 Nov 2025. CM.com's boards rejected it on 7 Nov. Bird held about 5% afterwards ([Bird](https://bird.com/blog/bird-announces-intention-to-acquire-cm-com-through-an-all-cash-public-offer), [Silicon Canals](https://siliconcanals.com/bird-becomes-cm-coms-third-largest-shareholder/)). The AFM register was updated in March 2026 ([AFM](https://www.afm.nl/en/sector/registers/meldingenregisters/openbare-biedingen/details?id=4861)). The outcome is unclear (?). CM.com still published standalone H1 2026 results.

### Channel depth

All prices below are from [bird.com/pricing](https://bird.com/pricing.md) and its sub-pages.

**SMS**
- US long code and toll-free $0.0035, short code $0.0070.
- 10DLC: brand $4.50, vetting $15, monthly campaign $1.50–$30.
- Lookup $0.005 per answered lookup.

**RCS**
- Private beta, by quote. SMS fallback billed at the SMS rate ([RCS](https://bird.com/pricing/rcs.md)).

**WhatsApp**
- US prices **include** Meta: marketing $0.030, utility/authentication/service $0.0084.
- Meta charges for service messages from 1 Oct 2026, with 1,000 free per number ([WhatsApp](https://bird.com/pricing/whatsapp.md)).

**Email** ([Email](https://bird.com/pricing/email.md))

| Plan | Price | Volume |
|---|---|---|
| Free | $0 | 1k/mo |
| Startup | $15 | 50k |
| Growth | $80 | 100k |
| Growth | $1,039 | 2.5M ($0.4156 per 1k) |

- Overage $0.90 per 1k. Dedicated IP $24.95/mo. Also sells PowerMTA licences.

**Voice** ([Voice](https://bird.com/pricing/voice.md))
- US outbound $0.0049/min plus a $0.004/min handling fee.
- Inbound local $0.009/min, toll-free $0.018/min.
- Numbers $0.50/mo.

**Also:** Apple Messages for Business, eSIM, Realtime, agent mailboxes ([llms.txt](https://bird.com/llms.txt)).

### Platform

- Free shared Inbox ✓.
- Automations billed per workflow invocation ✓.
- Verify included free; you pay only for channel messages ✓.
- SDKs: TS/JS, Python, Go, PHP; Swift/Kotlin realtime clients ✓.
- CLI (Homebrew), hosted MCP server, OpenAPI ✓.
- Any page as Markdown via `.md` or `Accept: text/markdown` ✓.
- Workspaces, scoped API keys and audit logs ✓ ([llms.txt](https://bird.com/llms.txt)).

### Pricing by destination ([sms/{cc}.md](https://bird.com/pricing/sms/gb.md))

| Destination | Price |
|---|---|
| UK | $0.050 |
| India | $0.065 |
| UAE | $0.105 |
| Brazil | $0.0325 |
| Indonesia | $0.30 |

### Weaknesses

- **Trustpilot 1.6/5, 22 reviews** ([Trustpilot](https://www.trustpilot.com/review/bird.com)):
  - "Most aspects are bad, price is bad, uptime is bad" (18 May 2026)
  - "no service delivered, no timeline, and silence on refund." (Oct 2025)
- **Three layoff rounds in about 2.5 years.** RCS is still in beta.

### Copy / beat

- **Copy (highest priority):**
  - `.md` versions of pricing and docs pages, plus `llms.txt`.
  - A hosted MCP server and a CLI.
  - All-inclusive WhatsApp per-message prices.
  - Free Verify, monetised through channel usage.
  - A free inbox.
  - A published 10DLC fee table.
- **Beat:** India (Bird is $0.065 on an international route), production-grade RCS (Bird's is beta; Relay's Trustsignal RCS is live), and stability.

---

## 6. Plivo

### Snapshot

- **HQ:** Austin, TX / Bengaluru (✓?).
- **Size:** about $86M revenue, profitable since 2016 (3p, [CB Insights](https://www.cbinsights.com/investor/plivo)).
- **Product:** Plivo Voice AI launched June 2026. Claims under 500 ms latency and 99.99% uptime ([JoinNextDev](https://www.joinnextdev.com/a/plivo/plivo-voice-ai-the-cpaas-moat-just-got-smarter)).
- **Price changes:** US SMS rose about 40%, from about $0.0055 to $0.0077 (3p, [apicostcalc](https://apicostcalc.com/blog/plivo-sms-price-hike-closes-twilio-gap.html)). Plivo's own page confirms $0.0077 now.

### Channel depth

**SMS (US)** ([Plivo US](https://www.plivo.com/sms/pricing/us/))
- Long code and short code $0.0077, toll-free $0.0079.
- Inbound is charged.
- Number rental: long code $0.50/mo, toll-free $1/mo.
- Short code: $1,500 setup plus $500 or $1,000 per month.

**RCS (US)**
- Rich text $0.0077 per segment, rich media $0.018 (same page).

**MMS**
- $0.018–0.02.

**WhatsApp** ([Plivo WA](https://www.plivo.com/whatsapp/pricing/))
- $0.001 per message plus $10 per WABA per month.

**Email**
- ✗ (✓?).

**Voice**
- Voice API, SIP trunking (Zentrunk), Voice AI agents ✓?

### Platform

- PHLO visual flow builder ✓?
- Verify API with fraud shield ✓?
- Number Lookup ✓?
- SDKs: Python, Node, Ruby, PHP, Java, .NET, Go ✓?
- Subaccounts ✓?
- Twilio-like API shape, which makes migration easy (3p, [suprsend](https://www.suprsend.com/post/twilio-alternatives-for-sms)).

### Pricing by destination

| Destination | Price | Source |
|---|---|---|
| India | $0.0800, international route only; domestic "Not Supported" | [IN](https://www.plivo.com/sms/pricing/in/) |
| UK | from $0.0372; mobile networks $0.0486–0.0527 | [GB](https://www.plivo.com/sms/pricing/gb/) |
| UAE | $0.0988 (du) – $0.115 (Etisalat) | [AE](https://www.plivo.com/sms/pricing/ae/) |
| Brazil | $0.0484–0.0705 | [BR](https://www.plivo.com/sms/pricing/br/) |
| Indonesia | $0.3333–0.4419 | [ID](https://www.plivo.com/sms/pricing/id/) |

- Plivo's calculator claims 100k US SMS costs $528 on Plivo against $790 on Twilio ([Plivo US](https://www.plivo.com/sms/pricing/us/)).

### Weaknesses

- **Trustpilot 1.2/5, 87 reviews** ([Trustpilot](https://www.trustpilot.com/review/plivo.com)):
  - "Plivo is too expensive, too restrictive and customer support is locked behind a paywall." (5 Jul 2026)
  - "They simply flat out deny allowing me to create an account" (Dec 2024)

### Copy / beat

- **Copy:** per-carrier rate tables, a "vs Twilio" cost calculator, and Twilio-compatible API ergonomics.
- **Beat:** domestic India, which Plivo does not support. Do not paywall support.

---

## 7. Telnyx

### Snapshot

- **HQ and ownership:** Chicago, private (✓?).
- **Revenue:** $100–500M range (3p, [Dealroom](https://dealroom.co/companies/telnyx/)).
- **2026 launches:**
  - ClawdTalk demo (Feb).
  - LiveKit on Telnyx (Apr).
  - WhatsApp on Telnyx numbers across 180+ countries, 2 Apr 2026 ([release notes](https://telnyx.com/release-notes/whatsapp-business-messaging-telnyx-numbers)).
  - WhatsApp Business Calling, 30 Apr 2026 ([GlobeNewswire via AAP](https://www.aap.com.au/aapreleases/globenewswire9710384/)).
- **Pricing change:** voice-agent rate moved from $0.056 to $0.06/min in Sep 2026 (3p, [pricingsaas](https://pricingsaas.com/companies/telnyx.conversationalai/diffs/2026W39)).
- **Network:** owns its private IP network and is a licensed carrier in several countries ✓?

### Channel depth

**SMS (US)** ([Telnyx messaging](https://telnyx.com/pricing/messaging))
- Local $0.004, toll-free $0.0055, short code $0.007.
- Carrier fees passed through at cost.
- Automatic volume tiers: local drops to $0.002 above 100M per month.

**RCS**
- GA since 2 Jul 2025, with carousels, suggested replies and SMS fallback ([Telnyx RCS](https://telnyx.com/resources/rcs-business-messaging-is-live)).
- US price: rich text $0.0065, rich media $0.016.

**WhatsApp**
- $0.004 per message plus Meta, $0 platform fee ([Telnyx WA](https://telnyx.com/pricing/whatsapp)).
- Tech Provider embedded signup flow for ISVs ([docs](https://developers.telnyx.com/docs/messaging/whatsapp/embedded-signup/tech-provider.md)).
- WhatsApp calling ✓.

**Email**
- ✗ (✓?).

**Voice**
- SIP trunking, Call Control, AI voice agents (voice engine $0.05/min, telephony $0.0032/min; 3p).
- TTS/STT, conferencing, porting ✓?

### Platform

- AI Assistants ✓. Mission Control portal ✓?.
- Number Lookup ✓?. Verify API ✓?.
- SDKs: Node, Python, Ruby, Java, .NET, PHP, Go ✓?.
- llms.txt for docs ✓ ([developers.telnyx.com llms](https://developers.telnyx.com/development/llms-txt-full)).
- Machine-readable pricing endpoint mentioned on the pricing page. The `/v2/public/pricing` call returned 404 to us (?).

### Pricing beyond the US

- UK, India, UAE, Brazil, Indonesia: ? Country pages exist (`/pricing/messaging/gb`) but render client-side; not extracted.

### Onboarding

- Level 1 after email confirmation.
- eKYC needed to buy numbers.
- Level 2 (payment method plus business details, up to 48 hours) unlocks international calling and raises the message rate from 600 to 3,000 ([Telnyx support](https://support.telnyx.com/en/articles/1130595-account-verification)).
- Free testing credits; amount ?

### Weaknesses

- **Trustpilot 3.3/5, 649 reviews** ([Trustpilot](https://www.trustpilot.com/review/telnyx.com)):
  - "they are extremely slow to respond to support queries, if they bother to respond at all" (5 Oct 2026)
  - "Then I couldn't Port." (14 Oct 2026)
- Steep learning curve and lengthy verification (3p, [authgear](https://www.authgear.com/post/twilio-alternatives/)).

### Copy / beat

- **Copy:**
  - Automatic volume tiers shown on the pricing page.
  - Carrier fees "at cost", stated plainly.
  - A Tech Provider embedded-signup API for ISVs. Relay needs this for WhatsApp resale.
- **Beat:** onboarding friction on verification levels; India coverage.

---

## 8. Bandwidth

### Snapshot

- **HQ and ownership:** Raleigh, NC; NASDAQ: BAND. Owns a US carrier network (✓?).
- **Q2 2026:** revenue about $220M, up 22%. Programmable Messaging $31M, up 22%. Messaging surcharge revenue $68M ([Bandwidth Q2 26](https://www.bandwidth.com/newsroom/bandwidth-announces-second-quarter-2026-financial-results/), [MarketBeat](https://www.marketbeat.com/instant-alerts/bandwidth-q2-earnings-call-highlights-2026-07-29/)).
- **Guidance:** full-year 2026 $900–910M.
- **Stock:** fell about 26% after the print (3p, [Barchart](https://www.barchart.com/story/news/3526551/bandwidths-nasdaqband-q2-cy2026-sales-beat-estimates-but-stock-drops-26)).
- **Customer win:** one text platform serving about 2,500 brands moved more than 95% of its traffic to Bandwidth.

### Channel depth

- **SMS/MMS:** US/Canada carrier-grade, 10DLC, toll-free, short code ✓?
- **RCS:** ✓? (US)
- **WhatsApp:** ? ✗?
- **Email:** ✗
- **Voice:** core strength. 911 / E911, SIP, Maestro AI voice orchestration ✓?
- **International messaging:** limited ✓?

### Pricing

- [bandwidth.com/pricing](https://www.bandwidth.com/pricing/) returned **HTTP 403**. All prices: ? Historically quote-led for enterprise ✓?

### Weaknesses

- Enterprise and US-centric. Not self-serve for small businesses (✓?).
- Gross margin squeezed by surcharges.

### Copy / beat

- **Copy:** carrier-surcharge revenue reported as a separate line item, which makes the cost structure honest.
- **Beat:** Bandwidth has no India or MENA presence.

---

## 9. CM.com

### Snapshot

- **HQ and listing:** Breda, NL (✓?); Euronext Amsterdam (CMCOM).
- **H1 2026:** revenue €129.9M (up 9%), EBITDA €11.9M (up 52%), net loss €2.3M.
- **Q2 2026:** revenue €67.0M (up 14%). Messaging volume a record 2.8B (up 30%). HALO agentic AI ARR €3.3M (up 187%) ([CM.com Q2 26](https://www.cm.com/cdn/web/en/file/investor-relations/cmcom-q2-2026-press-release.pdf)).
- **Bird bid:** see §5.

### Channel depth

- **SMS:** "one fixed price per country, all operators, no monthly fee". Rates are on regional pages we could not extract (?) ([CM SMS](https://www.cm.com/pricing/sms/)).
- **RCS:** ✓?
- **WhatsApp:** BSP, with a one-time onboarding fee and three bundle models ([CM pricing](https://www.cm.com/pricing/)).
- **Apple Messages for Business:** ✓?
- **Email campaigns:** ✓?
- **Voice:** Voice API and SIP trunking ([CM pricing](https://www.cm.com/pricing/)).
- **Payments:** CM.com Payments, a licensed PSP ✓?

### Platform

- Mobile Marketing Cloud (journeys) ✓?
- Conversational AI Cloud / HALO ✓
- Customer Context Platform, June 2026 ✓
- Subscription tiers: Activate / Support / Connect × Core / Plus / Max ([CM pricing](https://www.cm.com/pricing/))

### Weaknesses

- Trustpilot (www.cm.com) 4.0/5 from 243 reviews, but 47% of ratings are 1-star ([Trustpilot](https://www.trustpilot.com/review/www.cm.com)). The visible review text concerned insurance, which may be a different "CM" entity, so the profile match is ?
- ARR grew only 3% in Q2 after a contract was lost at re-tender.

### Copy / beat

- **Copy:** one price per country covering all operators, which removes per-network confusion. Payments inside conversations.
- **Beat:** CM.com is EU-centric with no India domestic route (✓?).

---

## 10. 8x8 (CPaaS, ex-Wavecell)

### Snapshot

- **HQ and listing:** Campbell, CA; NASDAQ: EGHT (✓?).
- **FY2026 (to 31 Mar 2026):** first full-year GAAP profit since 2015. Usage-based revenue up more than 50%. More than 5B interactions over its CPaaS APIs ([8x8 FY26](https://investors.8x8.com/news-releases/news-release-details/8x8-inc-reports-fourth-quarter-and-fiscal-year-2026-financial)).
- **Acquisition:** Maven Lab (Singapore), Jan 2026, for APAC messaging (3p, [privsource](https://www.privsource.com/acquisitions/deal/Q6pSr4)).

### Channel depth

- **SMS:** strong APAC direct routes via Wavecell ✓?
- **WhatsApp:** ✓?
- **RCS and WhatsApp depth:** lags Twilio and Infobip (3p, [rfp.wiki](https://www.rfp.wiki/vendors/8x8/twilio)).
- **Voice:** UCaaS/CCaaS heritage. Voice API, AI Studio agents, OpenAI realtime model support ✓ ([8x8 FY26](https://investors.8x8.com/news-releases/news-release-details/8x8-inc-reports-fourth-quarter-and-fiscal-year-2026-financial)).
- **Email:** ✗ (✓?).

### Pricing

- Quote-driven (3p). No public per-country rates found: ?

### Weaknesses

- Support is the most-cited weakness. Developer experience trails Twilio. Admin UI is dated (3p, [rfp.wiki](https://www.rfp.wiki/it-security/communications-platform-as-a-service/twilio/8x8)).

### Copy / beat

- **Copy:** CPaaS sold into an installed UCaaS base.
- **Beat:** self-serve onboarding and published prices.

---

## 11. Clickatell

### Snapshot

- **HQ:** US / South Africa (✓?). Private.
- **Funding:** last round was a $91M Series C in Feb 2022 ([TelecomTV](https://www.telecomtv.com/content/digital-platforms-services/clickatell-a-global-leader-in-chat-commerce-secures-91-million-in-oversubscribed-series-c-funding-for-us-expansion-and-product-acceleration-43756)).
- **Positioning:** 25th anniversary in Oct 2025 with an "agentic commerce" message. Customers include Fiserv, Absa, MTN, Toyota ([BusinessWire](https://www.businesswire.com/news/home/20251014108061/en)).
- **2026 news:** none found (?).

### Channel depth

- SMS (global) ✓?. WhatsApp chat commerce with payments ✓?. RCS ?. Email ✗ ✓?. Voice ✗ ✓?

### Pricing

- Calculator only; rates load dynamically and were not extractable: ? ([Clickatell pricing](https://www.clickatell.com/pricing-and-coverage/message-pricing/))

### Weaknesses

- **Trustpilot 1.3/5, 93 reviews** ([Trustpilot](https://www.trustpilot.com/review/clickatell.com)):
  - "Took £192 set up fee, couldn't help with setting up integration with Zoho." (18 Mar 2026)
  - "Support keeps talking about looking in to the situation but after 2 days no progress." (1 May 2026)

### Copy / beat

- **Copy:** in-chat payments ("chat commerce").
- **Beat:** no setup fees, and published rates.

---

## 12. Esendex / Commify

### Snapshot

- **Ownership:** Commify (UK), owned by ECI Partners since Sep 2023, a deal of about £257M / €300M ([BusinessCloud](https://businesscloud.co.uk/news/e300m-private-equity-takeover-at-commify/)).
- **Acquisitions:** SMSFactor (FR) and Text Request (US, Oct 2024).
- **Scale:** more than 46k customers.
- **Product:** Esendex Connect (SMS, WhatsApp, email, RCS through one API) launched 2025 ([Esendex about](https://ops.esendex.co.uk/about-us/)).

### Pricing (UK bundles)

- From [Esendex pricing](https://www.esendex.co.uk/pricing/):

| Bundle | Price | Effective per SMS |
|---|---|---|
| Starter: 500 SMS | £54/mo | ~£0.11 |
| Growth: 2,500 SMS | £143/mo | ~£0.06 |
| Enterprise: 10k SMS | £468/mo | ~£0.05 |

- Bundles include a virtual mobile number, sender ID and API.
- WhatsApp and RCS: contact sales.

### Weaknesses

- **Trustpilot 3.3/5, 223 reviews** ([Trustpilot](https://www.trustpilot.com/review/www.esendex.co.uk)): "Very slow website that keeps crashing and poor customer services." (4 Sep 2026)

### Copy / beat

- **Copy:** SMB bundles with an inclusive virtual mobile number.
- **Beat:** API depth; pay-as-you-go instead of monthly minimums.

---

## 13. Brevo (formerly Sendinblue)

### Snapshot

- **HQ:** Paris.
- **Funding:** €500M (about $583M) round on 4 Dec 2025, making it a unicorn. General Atlantic and Oakley joined; Partech exited ([TechCrunch](https://techcrunch.com/2025/12/03/new-unicorn-brevo-raises-583m-to-challenge-crm-giants), [Partech](https://partechpartners.com/news/brevo-europes-leading-customer-engagement-platform-becomes-a-unicorn-following-a-new-500-million-funding-round)).
- **Scale:** more than €200M ARR in 2025, double-digit EBITDA margin. Target €1B revenue by 2030.
- **M&A:** 11 acquisitions to date.

### Channel depth

- **Email:** core product. Marketing and transactional, dedicated IP ✓?
- **SMS:** pay-as-you-go credits; US about $0.0109 (3p, [emailtooltester](https://www.emailtooltester.com/en/reviews/brevo/pricing/)).
- **WhatsApp campaigns:** ✓?
- **RCS:** ?
- **Voice:** ✗? (Aircall-type integrations only)

### Platform

- Automation builder, CRM, inbox (Conversations), segments ✓?

### Pricing (3p; the vendor page did not render)

- Starter $9/mo, Standard $18/mo at 5k emails; Professional $499/mo at 150k ([checkthat](https://checkthat.ai/brands/brevo/pricing)).

### Weaknesses

- **Trustpilot 3.9/5, 7,243 reviews** ([Trustpilot](https://www.trustpilot.com/review/brevo.com)):
  - "Brevo suspends my account because the opening rate isn't high enough." (5 Oct 2026)
  - "My account was suspended even though I had already paid for 3 months of service." (8 Oct 2026)

### Copy / beat

- **Copy:** an SMB marketing UX with email at the centre, and pricing based on volume rather than contacts.
- **Beat:** Relay's carrier-grade SMS/RCS for India. Brevo is an email company that also offers SMS.

---

## 14. Kaleyra / Tata Communications (global side)

### Snapshot

- **Acquisition:** Tata Communications closed its Kaleyra purchase in Oct 2023, about $100M plus debt ([w.media](https://w.media/?p=124303)).
- **Commotion stake:** Tata bought 51% of Commotion (AI omnichannel CX) in Dec 2025, to be folded into the Kaleyra Customer Interaction Suite ([privsource](https://www.privsource.com/acquisitions/deal/Q6pSr4)).
- **Earlier acquisition:** Kaleyra bought mGage (US) for $215M in 2021 ([privsource](https://www.privsource.com/acquisitions/deal/q5SxNB)).

### Channel depth

- SMS (India DLT-native plus US mGage short codes) ✓?
- WhatsApp ✓?, RCS ✓?, email ✓?, voice / IVR / click-to-call ✓?

### Pricing

- Quote only: ?

### Weaknesses

- G2 4.5 from 14 reviews: WhatsApp template approval is slow, and reviewers want 24/7 chat support ([G2](https://www.g2.com/products/kaleyra/reviews)).
- Gartner reviewer: "inflexible approach and inability to tailor to the business requirements" ([Gartner](https://www.gartner.com/reviews/market/communications-platform-as-a-service/vendor/tata-communications)).

### Copy / beat

- **Copy:** bundling CPaaS with enterprise connectivity (Tata's network).
- **Beat:** self-serve and time-to-first-message. Kaleyra is sales-led.

---

## 15. Master feature matrix

**Column abbreviations**

| Code | Vendor | Code | Vendor | Code | Vendor |
|---|---|---|---|---|---|
| TW | Twilio | PL | Plivo | 8x8 | 8x8 |
| VG | Vonage | TX | Telnyx | CK | Clickatell |
| SN | Sinch | BW | Bandwidth | ES | Esendex |
| IB | Infobip | CM | CM.com | BR | Brevo |
| BD | Bird | | | KL | Kaleyra |

**Relay column legend**

| Mark | Meaning |
|---|---|
| ✓ | Built |
| ◐ | Partial, or sandbox only |
| ✗ | Absent |

Relay facts are per the shared context, 15 Oct 2026.

| # | Feature | TW | VG | SN | IB | BD | PL | TX | BW | CM | 8x8 | CK | ES | BR | KL | **Relay** |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | SMS API | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓? | ✓? | ✓ | ✓? | ✓? | ✓ (SMPP) |
| 2 | US 10DLC | ✓ | ✓? | ✓ | ✓? | ✓ | ✓? | ✓ | ✓? | ? | ? | ? | ✓? | ? | ✓? | ◐ (regime coded, `internal/domain/compliance/us.go:6`; no TCR API) |
| 3 | Toll-free SMS | ✓ | ✓? | ✓ | ✓? | ✓ | ✓ | ✓ | ✓? | ? | ? | ? | ? | ? | ? | ✗ |
| 4 | Short code | ✓ | ✓? | ✓ | ✓? | ✓ | ✓ | ✓ | ✓? | ✓? | ? | ✓? | ? | ? | ✓? | ✗ |
| 5 | Alphanumeric sender | ✓ | ✓? | ✓? | ✓? | ✓ | ✓? | ✓? | ✗? | ✓? | ✓? | ✓? | ✓ | ✓? | ✓? | ✓ (DLT headers) |
| 6 | India DLT native (domestic route) | ✗ (intl $0.0832) | ? | ✓? (ACL) | ✓? | ✗ (intl $0.065) | ✗ (domestic not supported) | ? | ✗ | ? | ? | ? | ✗ | ? | ✓? | ✓ |
| 7 | MMS | ✓ | ✓? | ✓ | ✓? | ? | ✓ | ✓ | ✓? | ? | ? | ? | ? | ✗? | ? | ✗ |
| 8 | Unicode / concatenation billing per segment | ✓ | ✓? | ✓? | ✓? | ✓ | ✓? | ✓ | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓ |
| 9 | Scheduled sends | ✓? | ? | ✓? | ✓? | ? | ? | ? | ? | ✓? | ? | ? | ✓? | ✓? | ✓? | ✓ (campaigns) |
| 10 | Inbound MO / two-way | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓? | ✓? | ✓? | ✓ | ✓? | ✓? | ✓ |
| 11 | STOP / opt-out handling | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ? | ? | ✓? | ✓? | ? | ✓ |
| 12 | Number lookup / HLR | ✓? | ✓? | ✓? | ✓? | ✓ ($0.005) | ✓? | ✓? | ? | ✓? | ? | ? | ? | ✗ | ? | ✗ |
| 13 | RCS API | ✓ | ✓? | ✓ | ✓ | ◐ beta | ✓ | ✓ | ✓? | ✓? | ? | ? | ✓ | ? | ✓? | ✓ (Trustsignal live) |
| 14 | RCS carousel | ✓ | ? | ✓? | ✓? | ? | ? | ✓ | ? | ? | ? | ? | ? | ? | ? | ✗ (single card) |
| 15 | RCS suggested replies/actions | ✓? | ? | ✓? | ✓? | ? | ? | ✓ | ? | ? | ? | ? | ? | ? | ? | ◐ (dropped at template registration) |
| 16 | RCS → SMS fallback | ✓ | ? | ✓? | ✓? | ✓ | ? | ✓ | ? | ? | ? | ? | ? | ? | ? | ✓ |
| 17 | RCS agent registration UI | ✓ | ? | ? | ✓? | ? | ? | ✓? | ? | ? | ? | ? | ? | ? | ? | ? |
| 18 | WhatsApp API | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ? | ✓ | ✓? | ✓? | ✓ | ✓? | ✓? | ◐ sandbox |
| 19 | WhatsApp embedded signup / Tech Provider | ✓? | ? | ✓? | ✓? | ✓? | ? | ✓ | ? | ? | ? | ? | ? | ? | ? | ✗ |
| 20 | WhatsApp Flows | ✓? | ? | ? | ✓? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ✗ |
| 21 | WhatsApp catalog / commerce | ? | ? | ? | ✓? | ? | ? | ? | ? | ✓? | ? | ✓? | ? | ? | ? | ✗ |
| 22 | WhatsApp calling | ? | ? | ? | ✓? | ? | ? | ✓ | ? | ? | ? | ? | ? | ? | ? | ✗ |
| 23 | Apple Messages for Business | ? | ? | ✓? | ✓? | ✓ | ✗? | ✗? | ✗ | ✓? | ? | ? | ✗ | ✗ | ? | ✗ |
| 24 | Email API | ✓ (SendGrid) | ✗? | ✓ (Mailgun) | ✓? | ✓ | ✗? | ✗? | ✗ | ✓? | ✗? | ✗? | ✓ | ✓ | ✓? | ◐ sandbox |
| 25 | Dedicated IPs | ✓? | ✗ | ✓ ($59) | ✓? | ✓ ($24.95) | ✗ | ✗ | ✗ | ? | ✗ | ✗ | ? | ✓? | ? | ✗ |
| 26 | Inbound email parse | ✓? | ✗ | ✓ (routes) | ? | ? | ✗ | ✗ | ✗ | ? | ✗ | ✗ | ? | ✓? | ? | ✗ |
| 27 | Email validation | ✓? | ✗ | ✓ | ? | ✓ | ✗ | ✗ | ✗ | ? | ✗ | ✗ | ? | ? | ? | ✗ |
| 28 | Voice API | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✗? | ? | ✗? | ✓? | ◐ sandbox |
| 29 | SIP trunking / BYOC | ✓ | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓ | ✓? | ✗ | ✗ | ✗ | ? | ✗ |
| 30 | TTS / STT | ✓ | ✓? | ✓? | ✓? | ? | ✓? | ✓? | ✓? | ? | ✓? | ✗ | ✗ | ✗ | ? | ? |
| 31 | IVR builder | ✓ (Studio) | ✓ (AI Studio) | ? | ✓? | ? | ✓? (PHLO) | ✓? | ? | ✓? | ✓? | ✗ | ✗ | ✗ | ✓? | ✗ |
| 32 | Conferencing | ✓ | ✓? | ? | ? | ? | ✓? | ✓? | ✓? | ? | ✓? | ✗ | ✗ | ✗ | ? | ✗ |
| 33 | Voice OTP | ✓ | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ? | ✓? | ? | ✗ | ? | ✗ | ✓? | ◐ |
| 34 | AI voice agents | ✓ (ConvRelay $0.07/min) | ✓ (AI Studio) | ✓ (2026) | ✓ (AgentOS) | ? | ✓ (Voice AI) | ✓ | ✓? | ✓ (HALO) | ✓ | ✗ | ✗ | ✗ | ✓? | ✗ |
| 35 | Visual journey / flow builder | ✓ | ✓ | ✓? | ✓ | ✓ | ✓? | ? | ✗? | ✓? | ? | ? | ? | ✓ | ✓? | ◐ (linear only) |
| 36 | Branching / event triggers | ✓ | ✓? | ✓? | ✓ | ✓? | ✓? | ? | ✗ | ✓? | ? | ? | ? | ✓ | ? | ✗ |
| 37 | Conversation inbox | ✓ (Flex) | ✓? | ✓? (Engage) | ✓ | ✓ (free) | ✗? | ✗? | ✗ | ✓? | ✓ | ✓? | ? | ✓? | ✓? | ✓ (SMS/RCS) |
| 38 | CDP / segments | ✓ (Segment) | ✗? | ? | ✓ (People) | ✓? | ✗ | ✗ | ✗ | ✓ (CCP) | ? | ? | ? | ✓? | ? | ✗ |
| 39 | Chatbot / AI agent builder | ✓ | ✓ | ✓ | ✓ | ✓? | ✓ | ✓ | ? | ✓ | ✓ | ✓? | ? | ✓? | ✓? | ◐ (keyword bots) |
| 40 | MCP server / agent tooling | ✓? (Agent Connect) | ? | ? | ✓ (AgentOS MCP) | ✓ (hosted MCP) | ? | ✓? | ? | ? | ? | ? | ? | ? | ? | ✗ |
| 41 | Verify / OTP API | ✓ ($0.05) | ✓? | ✓? | ✓? | ✓ (free) | ✓? | ✓? | ? | ✓? | ? | ? | ? | ✗ | ✓? | ✓ |
| 42 | SMS pumping protection | ✓ (Fraud Guard) | ✓? | ? | ✓? | ? | ✓? | ? | ? | ? | ? | ? | ? | ✗ | ? | ◐ (per-phone rate limit `internal/api/verify.go:181`; fraud counts `verify.go:493`; send-path `fraud_flag` hardcoded "none" `internal/sending/batch.go:257`) |
| 43 | Silent network auth | ✓ | ✓? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ✗ | ✗ | ? | ✗ |
| 44 | Link shortener + click tracking | ✓? | ? | ? | ✓? | ? | ? | ? | ✗ | ✓? | ? | ? | ✓? | ✓? | ✓? | ◐ (no UI; `internal/api/links.go:165`) |
| 45 | Official SDKs (≥5 languages) | ✓? | ✓? | ✓? | ✓? | ✓ (4 + mobile) | ✓? | ✓? | ✓? | ? | ? | ? | ? | ✓? | ? | ✗ |
| 46 | Public OpenAPI spec | ✓? | ✓? | ? | ✓? | ✓ | ? | ✓? | ? | ? | ? | ? | ? | ✓? | ? | ◐ (internal `openapi/control.json`, 211 ops; not published) |
| 47 | llms.txt / Markdown docs | ? | ? | ? | ✓ (md exports) | ✓ | ? | ✓ | ? | ? | ? | ? | ? | ? | ? | ✗ |
| 48 | CLI | ✓? | ✓? | ? | ? | ✓ | ? | ✓? | ? | ? | ? | ? | ? | ? | ? | ✗ |
| 49 | Sandbox / test mode | ✓ | ✓? | ? | ✓ (trial) | ? | ✓? | ✓ | ? | ? | ? | ? | ✓ | ✓? | ? | ✓ (live/test env, `internal/api/enums.go:39`) |
| 50 | Webhooks with signing + retries | ✓ | ✓ | ✓? | ✓? | ✓ | ✓? | ✓ | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓ (HMAC) |
| 51 | Martech webhook formats (MoEngage/CleverTap/WebEngage) | ? | ? | ? | ✓? | ? | ? | ? | ✗ | ? | ? | ? | ? | ? | ✓? | ✓ (`internal/webhook/integrations.go:31`) |
| 52 | Public status page | ✓ | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ? | ✗ |
| 53 | Published SLA | ✓? | ✓? | ✓? | ✓? | ? | ✓ (99.99% claim, Voice AI) | ✓? | ✓? | ? | ✓? | ? | ? | ? | ? | ✗ |
| 54 | Sub-accounts | ✓? | ✓? | ✓? | ✓? | ✓ (workspaces) | ✓? | ✓? | ✓? | ? | ? | ✓? | ? | ✓? | ? | ✗ |
| 55 | Reseller / white-label / ISV | ✓? (ISV) | ✓? | ✓? | ✓? | ? | ✓? | ✓ (WA Tech Provider) | ✓? | ✓? | ? | ✓? | ✓? | ✓? | ✓? | ◐ (branding backend only, `internal/api/branding.go:23`) |
| 56 | SSO (SAML) | ✓? | ✓? | ✓? | ✓? | ? | ✓? | ✓? | ✓? | ✓? | ✓? | ? | ? | ✓? | ? | ✗ (settings only) |
| 57 | MFA | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓ |
| 58 | Role-based access | ✓? | ✓? | ✓? | ✓? | ✓ (scoped keys) | ✓? | ✓? | ✓? | ✓? | ✓? | ? | ? | ✓? | ? | ✓ (4 roles) |
| 59 | Audit log | ✓? | ? | ? | ✓? | ✓ | ? | ? | ? | ? | ? | ? | ? | ? | ? | ✓ (operator) |
| 60 | Data residency choice | ✓? (EU/AU) | ? | ✓? (EU) | ✓? | ? | ? | ✓? | ✗ | ✓? (EU) | ✓? | ? | ✓? (UK/EU) | ✓? (EU) | ✓? (IN) | ◐ (single AWS ap-south-1) |
| 61 | SOC 2 / ISO 27001 | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✓? | ✗ |
| 62 | HIPAA | ✓? | ✓? | ? | ? | ? | ✓? | ✓? | ✓? | ? | ✓? | ? | ? | ✗? | ? | ✗ |
| 63 | Prepaid wallet | ✓ | ✓ | ✓? | ✓? | ✓ ("add money") | ✓ | ✓ | ✗? | ✓? | ? | ✓? | ✗ (bundles) | ✓ (credits) | ? | ✓ |
| 64 | Card / online payment gateway | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✗? | ✓ | ? | ✓? | ✓? | ✓ | ? | ✗ (manual only) |
| 65 | Local tax invoices (GST etc.) | ✓? (GST for IN) | ? | ? | ✓? | ? | ✓? | ? | ✗ | ? | ? | ? | ✓ (VAT) | ✓ (VAT) | ✓? | ✗ (no GSTIN) |
| 66 | Frequency caps | ? | ? | ? | ✓? | ? | ? | ? | ✗ | ✓? | ? | ? | ? | ✓? | ? | ✓ |
| 67 | Per-channel consent ledger | ✓? | ? | ? | ✓? | ? | ? | ? | ? | ? | ? | ? | ? | ✓? | ? | ✓ |

## 16. Pricing table (list prices, USD, accessed 15 Oct 2026)

**SMS outbound per segment, before carrier pass-through fees**

| Vendor | US | UK | India | UAE | Brazil | Indonesia | Source |
|---|---|---|---|---|---|---|---|
| Twilio | 0.0083 | 0.056 | 0.0832 | 0.1176 | 0.0599 | 0.4414 | [twilio.com/…/sms/pricing/{cc}](https://www.twilio.com/en-us/sms/pricing/us) |
| Plivo | 0.0077 (TF 0.0079) | 0.0372–0.0527 | 0.0800 (intl only) | 0.0988–0.115 | 0.0484–0.0705 | 0.3333–0.4419 | [plivo.com/sms/pricing/{cc}](https://www.plivo.com/sms/pricing/us/) |
| Telnyx | 0.004 (TF 0.0055) | ? | ? | ? | ? | ? | [telnyx.com/pricing/messaging](https://telnyx.com/pricing/messaging) |
| Sinch | 0.0078 (SC 0.009) | ? | ? | ? | ? | ? | [sinch.com/pricing/sms](https://sinch.com/pricing/sms/) |
| Bird | 0.0035 (SC 0.007) | 0.050 | 0.065 | 0.105 | 0.0325 | 0.30 | [bird.com/pricing/sms/{cc}.md](https://bird.com/pricing/sms.md) |
| Vonage | ~0.0076–0.0086 (3p) | ? | ? | ? | ? | ? | [textbee](https://textbee.dev/sms-pricing/vonage/us) (official page 403) |
| Esendex | — | £0.05–0.11 effective (bundles) | — | — | — | — | [esendex.co.uk/pricing](https://www.esendex.co.uk/pricing/) |
| Brevo | ~0.0109 (3p) | ? | ? | ? | ? | ? | [emailtooltester](https://www.emailtooltester.com/en/reviews/brevo/pricing/) |
| Infobip, CM.com, Clickatell, Bandwidth, 8x8, Kaleyra | calculator / quote | ? | ? | ? | ? | ? | see §4, 9, 11, 8, 10, 14 |
| MSG91 (reference) | — | — | ₹0.16–0.25 (~0.002–0.003) + GST | — | — | — | shared context |

**US carrier pass-through fees, per SMS**
- AT&T $0.0035, T-Mobile $0.0045, Verizon $0.0045–0.005 ([Twilio](https://www.twilio.com/en-us/sms/pricing/us), [Plivo](https://www.plivo.com/sms/pricing/us/), [Telnyx](https://telnyx.com/pricing/messaging)).

**WhatsApp vendor fee on top of Meta**

| Vendor | Fee | Source |
|---|---|---|
| Vonage | from €0.0001 (~$0.00016) | [link](https://www.vonage.com/communications-apis/messages/features/whatsapp/pricing/) |
| Plivo | $0.001 + $10 per WABA per month | [link](https://www.plivo.com/whatsapp/pricing/) |
| Telnyx | $0.004, $0 platform fee | [link](https://telnyx.com/pricing/whatsapp) |
| Twilio | $0.005, inbound and outbound | [link](https://www.twilio.com/en-us/whatsapp/pricing) |
| Bird | all-in US: marketing $0.030, utility/auth/service $0.0084 | [link](https://bird.com/pricing/whatsapp.md) |
| Infobip | ~30–55% over Meta (3p) | [link](https://www.ycloud.com/vs-infobip) |
| Sinch, CM.com | contract / bundles | §3, §9 |

**Email**

| Vendor | Price | Source |
|---|---|---|
| Bird | Free 1k; Startup $15 for 50k ($0.30 per 1k); Growth $80–1,039 ($0.80–0.42 per 1k); overage $0.90 per 1k | [link](https://bird.com/pricing/email.md) |
| Mailgun | Free 100/day; $15 / 10k; $35 / 50k; $90 / 100k; overage $1.10–1.80 per 1k | [link](https://www.mailgun.com/pricing/) |
| Mailjet | Free 6k; $19 / 15k; $29 / 15k Premium | [link](https://www.mailjet.com/pricing/) |
| SendGrid | Essentials $19.95; Pro $89.95; 60-day trial (3p) | [link](https://www.usecarly.com/blog/sendgrid-pricing/) |
| Brevo | Starter $9; Standard $18 (5k); Professional $499 (150k) (3p) | [link](https://checkthat.ai/brands/brevo/pricing) |

**Voice (US)**

| Vendor | Prices | Source |
|---|---|---|
| Twilio | Out $0.014/min; in $0.0085 local, $0.022 toll-free; numbers $1.15 / $2.15; AI ConversationRelay $0.07/min | [link](https://www.twilio.com/en-us/voice/pricing/us) |
| Bird | Out $0.0049 + $0.004 handling; in $0.009 / $0.018; numbers $0.50 | [link](https://bird.com/pricing/voice.md) |
| Telnyx | AI voice agent ~$0.06/min all-in (3p) | [link](https://pricingsaas.com/companies/telnyx.conversationalai/diffs/2026W39) |

**Verify / OTP**
- Twilio: $0.05 per successful verification plus channel fee ([link](https://www.twilio.com/en-us/verify/pricing)).
- Bird: no verification fee, channel cost only ([link](https://bird.com/pricing.md)).

**Platform / number fees**

| Vendor | Fee | Source |
|---|---|---|
| Twilio | 10DLC brand $4.50 + vetting $41.50 + campaign $15 + $1.50–10/mo | [link](https://www.twilio.com/docs/trust-hub/registrations/a2p-10dlc-brand) |
| Bird | 10DLC brand $4.50, vetting $15, $1.50–30/mo | [link](https://bird.com/pricing/sms.md) |
| Sinch | 10DLC number $1 + $1 setup | [link](https://sinch.com/pricing/sms/) |
| Plivo | long code $0.50/mo; short code $1,500 + $500/mo | [link](https://www.plivo.com/sms/pricing/us/) |
| Infobip | AgentOS from $530/mo (3p) | [link](https://www.ycloud.com/vs-infobip) |

---

## 17. What Relay should do (ranked)

1. **Ship the channels the UI already sells.**
   - WhatsApp, Email and Voice are sandbox-only (`internal/connector/smpp.go:802` routes every non-SMS channel to the fallback connector), yet the UI shows them as live.
   - Every global competitor has at least SMS plus WhatsApp in production.
   - The fastest credible path: WhatsApp through a Tech Provider embedded-signup flow (copy the Telnyx docs pattern), and email through SES in ap-south-1.
2. **Make India the wedge.**
   - Global players charge $0.065–0.083 for India over international routes, and Plivo does not support domestic India at all.
   - Relay already has DLT entity / header / template registration and SMPP binds.
   - But `NoDNDRegister` refuses all promotional traffic (`internal/sending/dnd.go:38`). That must be fixed before marketing the wedge.
3. **Adopt Bird's AI-native distribution:**
   - `.md` versions of every docs and pricing page, plus `llms.txt`.
   - A hosted MCP server.
   - A CLI and SDKs, generated from `openapi/control.json`, which already has 211 operations.
4. **Publish an all-inclusive price page per country,** in the Bird/Plivo style, with a CSV download. Name the carrier and DLT fees explicitly. Relay already has rate cards, so this is presentation work.
5. **Give Verify a real fraud engine.**
   - Today the verify fraud counters exist (`internal/api/verify.go:493`), but send paths write `fraud_flag: "none"` (`internal/sending/batch.go:257`).
   - Twilio bundles Fraud Guard into its $0.05, and its Trustpilot page shows a $484 fraud-bill complaint. Making Verify free with channel-only billing, as Bird does, is a strong hook.
6. **Use trust as the product.** The category scores 1.1–2.1 on Trustpilot. Concretely:
   - Published support SLAs.
   - A human-reviewed registration desk with a turnaround SLA.
   - Written reasons for every suspension.
   - A public status page (Relay has none).
7. **Close the table-stakes gaps:** online payment gateway, GST invoices, sub-accounts / reseller, SSO, RCS carousel and suggestions, number lookup, public status page, SOC 2 roadmap.

---

## 18. Unverified items ("?" and "✓?") to confirm

- **Vonage:** all non-US SMS prices. The official pages return 403.
- **Telnyx:** international SMS prices (rendered client-side), signup credit amount, and the public pricing API path.
- **Sinch:** all non-US/CA SMS prices.
- **Infobip:** list prices (calculator only).
- **CM.com:** per-country SMS prices and the Trustpilot profile match.
- **Clickatell:** prices (calculator only).
- **Bandwidth:** pricing page (403).
- **8x8 and Kaleyra:** all prices.
- **Brevo:** official prices (page did not render). Figures shown are 3p.
- **SendGrid:** official prices (3p only).
- **Bird–CM.com offer:** outcome after Mar 2026.
- **Bird 2026 layoff:** date. Single 3p source.
- **Feature claims marked ✓?:** taken from prior public knowledge and not re-checked this session. This covers most rows 19–23, 25–33 and 45–62 for vendors other than Twilio, Bird, Telnyx and Plivo.
- **Twilio:** WhatsApp Flows, catalog and calling support; current RCS country list.
- **Meta:** the "1,000 free service messages per number" allowance. Vendor-reported (Bird, Sinch, Zoho); Meta's own page was not checked.
- **Telnyx WhatsApp Business Calling:** a GlobeNewswire syndication was found, but the full release was not read.
- **Twilio SIGNAL 2026 RCS announcements:** none confirmed.
- **Relay rows 17 (RCS agent registration UI) and 30 (TTS/STT):** not checked in code this session.
