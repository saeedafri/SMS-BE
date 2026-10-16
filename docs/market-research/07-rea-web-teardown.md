# 07 — REA web teardown: competitor public surfaces and Relay's live UI

Captured 15 Oct 2026 (batch 1) and 16 Oct 2026 (batch 2). Every external fact below comes from a
public page loaded on those dates. Nothing was submitted: no form was filled in, no account was
created, and no Relay API call carried credentials.

## 1. Method

| Step | Tool | What it gives |
|---|---|---|
| Browser | Headless Chrome 155 on loopback CDP port 9334, fresh profile, killed at the end | One tab per target URL |
| Bundle analysis | REA `analyze_web_bundle` (script sources on, source maps requested) | Chunk graph, bytes, framework/bundler fingerprints, endpoint and route candidates. **Evidence ID** per run (`ev_…`) |
| Page inspection | REA `inspect_web_page` (accessibility text on) | DOM, accessibility tree (form labels), resources, storage key names |
| Screenshots | REA `capture_web_screenshot` | Viewport PNG, content-addressed |
| Page snapshot (non-REA) | My own CDP `Runtime.evaluate` script (`document.body.innerText`, `performance` resource hosts, `window` globals, visible form fields), in my own headless browser | Rendered pricing text, third-party hosts, signup fields. Marked **[CDP]**; REA did not do this |
| Static fetch (non-REA) | `curl` of public JS/HTML (Plivo `_astro/*.js`, MSG91 `__NEXT_DATA__`, Bird `runtime-env.js`, Relay chunks) | Pricing data and config shipped to every browser. Marked **[curl]** |

REA rules applied. **Observation** means it was seen in captured bytes or rendered text.
**Inference** means it was reasoned from observations. **Unknown** means it was not established.
Static bundle analysis never shows that code ran. Every REA run reported `source_maps:
unavailable`: no target served a map from its own origin, and Relay's `.map` URLs return 403.

### Evidence index

| ID | Target | Evidence |
|---|---|---|
| E1 | Relay `/signup` bundle | `ev_7b26c3ab7275146977bfa6b640dc1569708604ed68fa19b484622dd585467747` |
| E2 | Relay `/signup` page + a11y | `ev_76f27188caffd072f10c85d05f83222bba27f328c837598f4860a938ec90eb5f` |
| E3 | Relay `/login` bundle | `ev_9da37f4f37dcc701e74a176084beee337185e2eba13bc6360849ff9a045a5c71` |
| E4 | Relay `/operator/login` bundle | `ev_593876c9749158ada847c9c14dd3743c74960c661648915e99319adf5ca58201` |
| E5 | Relay `/signup` screenshot | `ev_57274cb11a35a144ca0004443b84f8d8f9a883ca0ebf84a8bc55dcc87405f16e` |
| E6 | MSG91 `control.msg91.com/signin` bundle | `ev_ea8fbde3f7afdd01094221439ecfa082e3b8db872075b5ebb41e3a150744c14f` |
| E7 | MSG91 signin screenshot | `ev_c02967a74774c45401d8296487f8308e58b1b34b6cd85b76cd3439f276fcc114` |
| E8 | MSG91 `/in/pricing/sms` bundle (10.5 MB response, retained not inlined) | `ev_9e3625f4ee81ec8693d49db07a4461e766ca2cb6a9366ec622384bec76ba4de9` |
| E9 | Bird `app.bird.com` bundle | `ev_ddb737755f5e749cdd9c39fb7f8bd8d67c6b219deb1d657a600b3cb955259717` |
| E10 | Infobip `portal.infobip.com/login` bundle | `ev_89fae8b3f7c1090c47fc7837a04d5b4adb48ff71134439357de4fe1bc891f247` |
| E11 | Twilio console login bundle (0 scripts captured, see §3.1) | `ev_e9c130760ac4eea61f31007047322292f81db7c834edd5ec893f05cbdaeaf283` |
| E12 | Twilio `/sms/pricing/in` bundle | `ev_28cdf047fef263dcbcdd0d18aa8420f1994715514607130d902c4bd9be355635` |
| E13 | Telnyx `/pricing/messaging` bundle | `ev_ba5454e476889938bec041b0468da123217c0755e539e8cc2b8ad74c6963de72` |
| E14 | Plivo `/sms/pricing/in/` bundle | `ev_66fc6afb5203407997ada80e1237a05e12f640587ffbfe3b0ec1083a0720feac` |
| E15 | Twilio signup (`login.twilio.com/u/signup`) bundle | `ev_2f7293087e31e9faae9bfe6c017141c3cd5f1812605a1674a7d8fc19b14df264` |

REA keeps evidence for each session, and the 15 Oct session ended mid-task. E1–E3 and E6–E14
are recorded here as REA returned them. They may not be retrievable through
`get_evidence_bundle` in a later session.

## 2. Coverage

| Target | Result |
|---|---|
| twilio.com SMS IN/US, WhatsApp, Voice IN, signup, console login, status | Captured |
| SendGrid pricing | `sendgrid.com/en-us/pricing` redirects to `twilio.com/en-us/sendgrid`. No price table rendered |
| Infobip pricing, signup, portal login | Captured |
| Sinch SMS pricing | Captured. The price table renders `$0.00` until a country is picked (country select had 0 options at capture) |
| Vonage SMS pricing | **Blocked**: Cloudflare "Just a moment…" challenge (Turnstile) |
| Plivo SMS IN pricing, console | Captured. `/accounts/register/` redirects to login |
| Telnyx messaging pricing, portal, status | Captured. `#/sign-up` lands on sign-in |
| Bird pricing, app | Captured. `/auth/sign-up` redirects to login |
| MSG91 SMS/WhatsApp/RCS/Email/Voice pricing, signup, signin | Captured (pricing via `__NEXT_DATA__`) |
| Gupshup pricing | `gupshup.io/pricing` redirects to `gupshup.ai/isv-partners/whatsapp-api/pricing`. Captured |
| Exotel pricing | Captured. Plans are behind "View Pricing Details"; no numbers in the rendered text |
| Kaleyra | `kaleyra.com/pricing/` redirects to `tatacommunications.com/kaleyra/cpaas` (lead form only) |
| Route Mobile | Home page only, no public pricing |
| Netcore | `netcorecloud.com/pricing/` redirects to `netcore.ai/pricing/`, which returns **403** to headless Chrome |
| Airtel IQ | `airtel.in/business/b2b/airtel-iq` redirects to `airtel.in/b2b/whatsapp-api` (lead form) |
| Unifonic pricing | Captured |
| Zenvia pricing | **Blocked**: Cloudflare "Attention Required" |
| status.twilio.com, status.plivo.com, status.telnyx.com | Captured. `status.msg91.com` redirects to `atlassian.com/software/statuspage` (see §3.6) |
| Relay `/login`, `/signup`, `/operator/login` | Captured |

## 3. Per-vendor findings

### 3.1 Twilio (incl. SendGrid)
URLs: twilio.com/en-us/sms/pricing/in, /sms/pricing/us, /whatsapp/pricing, /voice/pricing/in,
/try-twilio, console.twilio.com, status.twilio.com. Accessed 15–16 Oct 2026.

**Observations**
- Marketing site: Adobe Experience Manager (`/etc.clientlibs/…`, `@granite/*` requires), webpack
  runtime (E12, high confidence), 34 scripts, ~1.9 MB raw JS on the India SMS page. The largest
  chunk is 747 KB. Algolia search paths `/1/indexes/*/queries` (E12).
- Third parties [CDP]: Segment (`cdn.segment.com`, `api.segment.io`), Adobe Launch (`_satellite`),
  VWO A/B testing (`_vwo_*` globals, `dev.visualwebsiteoptimizer.com`), TrustArc consent, GA4,
  Bing UET, Meta pixel, LinkedIn Insight, Yahoo Japan tags, Wistia player, plus
  `powerrobotflower.com`/`jscloud.net` (bot or fraud beacons; vendor not identified).
- Signup (`login.twilio.com/u/signup`) [CDP + E15]: Auth0 Universal Login (`cdn.auth0.com`,
  `id.auth.twilio.com`), Cloudflare Turnstile (`turnstile` global), Arkose Labs (bundle fetches
  `status.arkoselabs.com/api/v2/status.json`, E15), Amplitude, Segment. Fields: first name, last
  name, email, password, ToS checkbox (required), marketing-consent checkbox. Copy: "Start your
  free trial and unlock all Twilio products".
- Console login: email-first field only (`email_input`). Datadog RUM (`DD_RUM`) and Segment. E11
  captured 0 scripts: the page had redirected to `twilio.com/login?g=…` and REA's origin scope
  excluded them.
- Pricing (rendered text):
  - **SMS India** outbound $0.0832/segment.
  - **SMS US**: long code / toll-free / short code all $0.0083 out and in. MMS $0.022 out,
    $0.0165–0.02 in.
  - Failed-message processing fee $0.001 per failed message. 10DLC onboarding fees apply.
  - **WhatsApp**: Twilio fee $0.005 per message in or out, plus Meta fee; utility and
    authentication templates "start at $0.0034/message". Service-window messages $0.00 Meta fee.
    Calculator country list of ~150 countries plus "All other countries".
  - **Voice India**: outbound local $0.0699/min, mobile $0.0496/min. Browser/app and SIP
    $0.0040/min. SIP refer $0.10.
  - Number from "$1.15/mo".
- Status page: Atlassian Statuspage, with subscribe by email (OTP), SMS (OTP, country-code
  select) and webhook. reCAPTCHA.

**Inferences**: Twilio stacks three bot defences (Turnstile, Arkose, captcha on status) at signup
and on public forms. Price pages are CMS-rendered HTML, not a JS price feed.
**Unknown**: phone verification and KYC steps after the first signup screen (not submitted).

### 3.2 Infobip
URLs: infobip.com/pricing, infobip.com/signup, portal.infobip.com/login. Accessed 15–16 Oct 2026.

**Observations**
- Portal login: 5 scripts, ~43 KB. The main one is a CDN-proxy service worker
  (`/portal-cdn-proxy/cdn-proxy-service-worker/1.2.1/app.js`, E10). Sentry (`SENTRY_RELEASE`) and
  Amplitude. Field: "Email or username" (identifier-first).
- Signup fields [CDP]: company email, first name, last name, country, **company website**,
  marketing opt-in, T&C acceptance. reCAPTCHA. Legal text: "I confirm that data provided is
  accurate … I have the authority to bind the contracting party legally". "START FREE TRIAL",
  "No credit card required".
- Pricing page: "Pay-as-you-go" plus monthly subscriptions. "€0" toll-free. No per-country
  rates in the rendered text.
- Marketing stack: Usercentrics CMP, GTM, Amplitude, Demandbase, ZoomInfo, 6sense-style
  company-ID beacons (`company-target.com`), Clarity, Bing, Reddit, LinkedIn, Google Ads,
  server-side tagging (`sst.infobip.com`).

**Inference**: the "company website" field plus the legal attestation is a lightweight business
check (KYB) at signup.

### 3.3 Sinch
URL: sinch.com/pricing/sms/ (15–16 Oct 2026).
**Observations**: country and currency selects load empty and are filled by JS. Rendered rates
are `$0.00` before selection. "Sinch aims to update prices at midnight UTC". "Sign up and get a
free trial today". GA only.
**Unknown**: India and US rates (not rendered in the capture window).

### 3.4 Plivo
URLs: plivo.com/sms/pricing/in/, console.plivo.com (16 Oct 2026).

**Observations**
- Astro site with React islands (`_astro/*.js`, `astro-island`), 37 scripts (E14). The whole
  price book ships to the browser as `_astro/pricing-data.DWQg86XB.js` (**911 KB**) and
  `sms-pricing-page.DGJAEIfq.js` (244 KB) [curl]. Country detection calls
  `https://ipapi.co/json/` (E14 endpoint).
- **India SMS** (from the JS): Domestic **₹0.20/sms**, ILDO $0.08/sms, inbound "Not Supported".
  Long code ₹250/month.
- **US SMS**: long code and short code $0.0077 out and in, toll-free $0.0079.
- **India voice**: ₹0.38/min local (from Plivo-IN numbers), ₹0.25/min IP. India local number
  rental $2.35, "BETA", voice-only.
- **US voice**: $0.0115/min out, $0.0055/min in. Short code $1,000/month.
- India per-network table still lists **Aircel** circles (Aircel shut down in 2018).
- Console login: Work Email plus password, reCAPTCHA. LogRocket session replay, Stripe.js,
  HubSpot, Clarity, PostHog (marketing site), G2 tracking, Quora and Bing pixels.
  `/accounts/register/` redirects to login.
- Copy: "Create your account and receive trial credits", "no credit card".

**Inference**: Stripe on the console login means card billing. The Aircel rows suggest the
network table is not curated.

### 3.5 Telnyx
URLs: telnyx.com/pricing/messaging, portal.telnyx.com, status.telnyx.com (15–16 Oct 2026).

**Observations**
- Pricing page: 9 scripts. Two are about 600 KB each and served from `/x1ok/…` paths (E13). The
  endpoint candidates (`google.com/ccm/geo`, `gclid`) match Google Tag Manager code. Rendered
  text: "$0.004 a part. Then the carrier, at cost". Worked example 1,000,000 × $0.004 = $4,000,
  and volume tier $0.0038. Per-part rates from $0.0005 to $0.0135 across products.
- Stack: OneTrust, Marketo (Munchkin), Segment, Clearbit, ZoomInfo, Salesloft, 6sense, StackAdapt,
  Vector, Reddit/X/LinkedIn/Meta pixels, Bugsnag, OpenAI pixel (`bzr.openai.com`).
- Portal: email field, **Google sign-in** (`accounts.google.com`), Cloudflare Turnstile, Bugsnag,
  Survicate, an internal telemetry ingest (`prism-ingest.telnyx.tech`). "Didn't receive a
  verification email?"
- Status page, 16 Oct: live incident "EU Messaging Update Issues — downtime in one of our
  datastores in europe region", ongoing for 3 hours. Two voice maintenances in progress.

**Inference**: `/x1ok/` is a first-party proxy path for GTM (server-side tagging pattern).

### 3.6 MSG91 (closest India comparator)
URLs: msg91.com/in/pricing/{sms,whatsapp,rcs,email,voice}, msg91.com/signup,
control.msg91.com/signin (15–16 Oct 2026).

**Observations**
- Marketing site: Next.js (`__NEXT_DATA__`, buildId `lESRbIU497_XVTu0Vq8o3`). Prices sit in
  `props.pageProps.pricingData` [curl]. E8 is the REA analysis of the SMS page; its full response
  is 10.5 MB, so REA retained it instead of returning it inline.
- **SMS India** (`pricingData`, two route columns keyed `4` and `106`):

  | Credits bought | Route 4 ₹/SMS | Route 106 ₹/SMS | Pack ₹ |
  |---|---|---|---|
  | 5,000 | 0.25 | 0.25 | 1,250 |
  | 16,500 | 0.20 | 0.22 | 3,300 |
  | 30,000 | 0.18 | 0.20 | 5,400 |
  | 60,000 | 0.17 | 0.19 | 10,200 |
  | 1,17,648 | 0.17 | 0.19 | 20,000 |
  | 4,50,000 | 0.17 | 0.19 | 76,500 |
  | 9,62,500 | 0.16 | 0.18 | 1,54,000 |

  Page text: "get up to ₹0.13 per SMS" via sales.
- **RCS plans** (monthly): Launch ₹2,000 (2,000 free credit), Build ₹5,000 (5,000 credit),
  Grow ₹10,000 (10,000 credit). The feature flag **"RCS iPhone Delivery" is `included: true`
  only on Grow**. All plans `postPaidAllowed: true`.
- **WhatsApp**: plan "Titan" ₹500/month with a fixed ₹500 discount for 2 cycles. Page sections
  include `whatsappVoice` and `tax`.
- **Email**: Free (5,000 emails, ₹0.035 overage, 50 validations), Starter ₹2,000/mo (1 lakh,
  ₹0.02), Basic ₹4,600 (2.5 lakh, ₹0.015), Alpha ₹7,500 (5 lakh, ₹0.01). DMARC compliance and
  Handlebars templating are gated off on Free.
- **Voice India**: landline ₹0.50/min (dial plan 8). 245-country list.
- Product taxonomy (`products` JSON): apps Hello (contact center), Campaign (event automation),
  Segmento (contacts), OTP widget. Channels SMS, SendOTP, Email, WhatsApp, Voice, RCS.
- **Signup** [CDP]: Email, **Mobile Number (required)**, "how did you hear" select (9 options).
  "OTP invisible verification".
- **Signin** (E6, E7): jQuery 2.1.1 + Bootstrap + jquery.validate, ~2.4 MB raw JS across 17
  scripts. Three ~500–600 KB GTM containers served first-party from `/sea4/…`. Fields: email,
  mobile with country code, OTP code fields (`mgToken`, `mgTokenM`), reCAPTCHA.
- Third parties on signin: Segment, Hotjar, Microsoft Clarity, Sentry, GA4/Google Ads,
  DoubleClick, Meta pixel, AdRoll, Cloudflare Insights.
- `status.msg91.com` redirects to `atlassian.com/software/statuspage`, which lists Statuspage
  plans at $29 / $99 / $399 / $1,499.

**Inferences**: routes `4` and `106` are most likely two SMS route classes (e.g.
transactional vs promotional). They are not labelled in the data. Mobile plus OTP at signup is
MSG91's identity gate. The `status.msg91.com` redirect is what Statuspage does for an
unconfigured custom domain, so MSG91 has no working public status page (unverified).

### 3.7 Bird (MessageBird)
URLs: bird.com/pricing, app.bird.com (15–16 Oct 2026).

**Observations**
- App login page loads **132 scripts, 19.5 MB raw JS** (E9). Built with Vite (rolldown runtime).
  Large chunks: `pkg-messagebird-appkit` 2.6 MB, `-editor` 2.1 MB, `Forms` 2.0 MB, `-apikit` 1.6 MB,
  `en-us` 1.4 MB, `emoji-mart` 1.2 MB, `pkg-messagebird-voice-react` 1.2 MB.
- Feature names in chunk paths (E9): `MessageComposer`, `MessageTemplateForm`, `TemplatePreview`,
  `useRcsPreviewMode`, `SenderSelector`, `ChannelsAdapter`, `AIAssistantDialog`,
  `StepUpAuthenticationBoundary`, `WorkspaceContext`, `permissions`, `crm`, `MonacoEditor`,
  `whatsapp-background`, `PartnersTermsAndConditionsRadio`.
- Public runtime config `app.bird.com/runtime-env.js` (`window.BirdEnv`) [curl]. Values redacted
  here. Keys include `PUBLIC_STRIPE_API_KEY` (`pk_live…`), `PUBLIC_AIRWALLEX_CLIENT_ID` and
  `…_TEST_CLIENT_ID`, `PUBLIC_BIRD_PAY_API_KEY`, `PUBLIC_DATADOG_CLIENT_TOKEN` (site
  datadoghq.eu), `PUBLIC_GOOGLE_MAPS_API_KEY`, `PUBLIC_RECAPTCHA_SITE_KEY`,
  `PUBLIC_SIFT_FRAUD_DETECTION_BEACON_ID`.
- The same config lists these hosts: `api.bird.com` (fallback `nest.messagebird.com`),
  `messaging.messagebird.com`, `crm-api.dna.messagebird.com`, `postgrest.dna.messagebird.com`,
  `wss://api.bird.com/connect`, `wss://webrtc.rtc.bird.com`, `voice-homer.eu-west-1.rtc.bird.com`,
  `ai.bird.com`, `meet.bird.com`. Release `v2026.10.06.5`. `PUBLIC_TOOLING_APP_URL` is
  `http://localhost:5666/`.
- Login: work email plus password, reCAPTCHA, Stripe.js, Sift, Datadog RUM.
- Pricing page: "Add money. Use it across Bird. Free tiers, pay-as-you-go". Rendered figures
  $0.0035, $0.005, $0.005 (email/messaging rows) and $0.50 / $24.95 (number rental rows).

**Inferences**: Bird runs one prepaid wallet across products and takes payments through Stripe
plus Airwallex. Sift does fraud scoring at signup and login. The keys listed are browser keys
that are meant to be public, not leaked secrets. The localhost tooling URL is a config slip.
**Unknown**: which product each Bird price belongs to (the row labels did not render with the
numbers).

### 3.8 Gupshup
URL: gupshup.ai/isv-partners/whatsapp-api/pricing (16 Oct 2026).
**Observations**: Next.js, OneTrust. "Gupshup charges a standard 0.001 USD per message fee". Meta
fees shown as 0.0000 until a country is chosen (filled by JS). Media above 64 KB is free. Voice
$0.0001/sec. "First 1,000 Service messages are free of WhatsApp fees".
**Inference**: the $0.001 markup is the floor that resellers price against.

### 3.9 Exotel
URL: exotel.com/pricing/ (15–16 Oct 2026).
**Observations**: product tabs (Contact Center, Business Phone System, Communication APIs, AI &
Automation). No numeric prices in the rendered text. Lists an "Exotel MCP Server" product.
Third parties: Pardot, Apollo, OptinMonster, Clarity, LinkedIn, Meta, OpenAI pixel, `ip-api.com`
geolocation, an `allincall.in` chat widget, `interactlabs.ai`.

### 3.10 Kaleyra (Tata Communications), Route Mobile, Airtel IQ, Netcore
- **Kaleyra**: `kaleyra.com/pricing/` redirects to `tatacommunications.com/kaleyra/cpaas`. Lead
  form with 8 required fields (first, last, business email, phone with 235-entry code list, job
  title, company, country with 255 options, consent). HubSpot, Marketo, Navattic product tours,
  6sense. No public price.
- **Route Mobile**: home page only. Nav shows "Pay Online" and "DLT" links. The 365squared
  firewall business features prominently. 6sense, ZoomInfo, Leadfeeder. No pricing.
- **Airtel IQ**: the IQ URL redirects to the WhatsApp API page. Lead form: name, mobile (+91),
  company, company email. Taboola, Reddit, Leadfeeder, Clarity. No self-serve signup and no
  pricing seen.
- **Netcore**: 403 to headless Chrome. No data.

### 3.11 Unifonic
URL: unifonic.com/en/pricing (16 Oct 2026).
**Observations**: Connect **$499/mo** (WhatsApp API 25 msg/s, 10,000 campaign messages,
SMS/Voice/Email/WhatsApp). Engagement **$999/mo** (20,000 messages, chatbot 5,000 sessions, 1
agent seat, Messenger/Instagram). Intelligence custom (50 msg/s, AI agent co-pilot). HubSpot,
Hotjar, Clearbit, Knovvu voice assistant.

### 3.12 Vonage, Zenvia
Both returned Cloudflare challenge pages to headless Chrome. No data. Re-run in a headed browser.

## 4. Signup friction

| Vendor | Fields at first screen | Bot / fraud | KYC at signup | Trial |
|---|---|---|---|---|
| Twilio | first, last, email, password, ToS | Turnstile, Arkose, Auth0 | None seen | "Free trial", "No credit card required" |
| Infobip | company email, first, last, country, **company website**, ToS + legal attestation | reCAPTCHA | Light KYB (website + authority attestation) | "START FREE TRIAL", no card |
| MSG91 | email, **mobile (required)**, source | reCAPTCHA (signin), OTP | None seen; mobile OTP | Free email tier (5,000); RCS plan credits |
| Plivo | register redirects to login (work email, password) | reCAPTCHA, LogRocket | Unknown | "trial credits", "no credit card" |
| Telnyx | email; Google SSO | Turnstile, email verification | Unknown | Unknown |
| Bird | work email, password | reCAPTCHA, Sift | Unknown | "Start free", free tiers |
| Kaleyra / Tata | 8-field sales lead form | none seen | n/a (sales-led) | None |
| Airtel IQ | name, mobile, company, company email (lead form) | none seen | n/a (sales-led) | None |
| Unifonic | "Talk to an expert" only | n/a | n/a | None |
| **Relay** | full name, work email, password, org name, country (India (DLT) / United States (10DLC)) | **none** | none | none stated |

No vendor asked for KYC documents (GST, PAN, DLT entity ID) on the first screen. Self-serve
vendors collect them after signup. Indian vendors either gate on a mobile number with OTP
(MSG91) or route to a sales form (Kaleyra, Airtel, Route Mobile).

## 5. Pricing extracted (list prices, ex-tax, as shown)

| Vendor | SMS India | SMS US | WhatsApp | RCS | Voice India | Other |
|---|---|---|---|---|---|---|
| Twilio | $0.0832/seg | $0.0083/seg | $0.005 + Meta (util/auth from $0.0034) | n/a | $0.0699 local / $0.0496 mobile /min | failed-msg fee $0.001; number $1.15/mo |
| Plivo | ₹0.20 domestic, $0.08 ILDO | $0.0077 (TF $0.0079) | n/a | n/a | ₹0.38/min | IN long code ₹250/mo; US short code $1,000/mo |
| MSG91 | ₹0.25 → ₹0.16 slabs (sales ₹0.13) | n/a | Titan plan ₹500/mo | ₹2k / ₹5k / ₹10k plans; iPhone only on ₹10k | ₹0.50/min landline | Email free 5k; ₹2,000/mo for 1 lakh |
| Telnyx | n/a | $0.004/part + carrier fee (vol. $0.0038) | n/a | n/a | n/a | — |
| Gupshup | n/a | n/a | $0.001/msg + Meta | n/a | n/a | voice $0.0001/s |
| Unifonic | bundled | bundled | bundled | n/a | bundled | $499 / $999 per month |
| Bird | n/a | n/a | n/a | n/a | n/a | $0.0035, $0.005 rows; numbers $0.50 / $24.95 |
| Sinch, Exotel, Kaleyra, Route Mobile, Airtel, Netcore, Vonage, Zenvia | not obtained (JS-gated, sales-gated or blocked) | | | | | |

Pricing loaded by JS (rather than written into the HTML): Plivo ships its full price book as a
911 KB JS module. MSG91 ships plan JSON in `__NEXT_DATA__`. Sinch and Gupshup fill their tables
after a country is picked.

## 6. Third-party stack

| Vendor | Analytics / tags | Session replay / RUM / errors | Bot / fraud | Payments | Auth | Consent |
|---|---|---|---|---|---|---|
| Twilio | Segment, Adobe Launch, GA4, VWO, Amplitude | Datadog RUM (console) | Turnstile, Arkose | — | Auth0 | TrustArc |
| Infobip | GTM (server-side), Amplitude, Demandbase, ZoomInfo | Sentry, Clarity | reCAPTCHA | — | own | Usercentrics |
| Plivo | GTM, HubSpot, PostHog, G2 | LogRocket, Clarity | reCAPTCHA | **Stripe** | own | custom |
| Telnyx | Segment, Marketo, Clearbit, 6sense, Salesloft | Bugsnag | Turnstile | — | own + Google SSO | OneTrust |
| MSG91 | GTM (first-party `/sea4`), Segment, GA4, AdRoll | Hotjar, Clarity, Sentry | reCAPTCHA, OTP | none seen | own | — |
| Bird | GA4, Ahrefs | Datadog RUM | reCAPTCHA, **Sift** | **Stripe + Airwallex** | own + step-up auth | in-page |
| Exotel | GTM, Pardot, Apollo | Clarity | — | — | — | — |
| Unifonic | HubSpot | Hotjar, Clarity | — | — | — | — |
| Relay | **none** | **none** | **none** | none | own (cookie session) | none needed |

Razorpay was not seen on any captured public page, including the Indian vendors' login pages.
That does not mean they don't use it: billing pages sit behind login and were not captured.

## 7. UX insights Relay should copy

1. **Public, data-driven pricing page with a slab calculator** (MSG91 table + range slider,
   Plivo country switcher). Relay already has rate cards in the backend, so a public read-only
   price JSON would let the site render them without sales calls.
2. **Plan-gated features as explicit booleans** (MSG91 `features: [{name: "RCS iPhone Delivery",
   included}]`). This maps directly onto Relay's RCS fallback and Jio iPhone pricing story.
3. **Bot protection on signup and login**: every self-serve competitor runs Turnstile, reCAPTCHA
   or Arkose. Relay runs none (§8).
4. **Mobile + OTP at signup for India** (MSG91). It is cheap identity assurance and a natural
   fit with DLT entity onboarding.
5. **"Free trial / no credit card / trial credits" message** on pricing and signup (Twilio,
   Infobip, Plivo, Bird). Relay's signup says "Start sending in minutes" but promises no credit.
6. **Fee transparency**: Twilio publishes a failed-message fee. Telnyx shows "rate + carrier at
   cost" with a worked example. Relay's hold/refund billing could be explained the same way.
7. **Public status page with email, SMS and webhook subscriptions** (Twilio, Plivo, Telnyx on
   Statuspage). Telnyx's live EU incident on 16 Oct shows competitors disclosing incidents
   openly. MSG91 appears to have none, which Relay can beat.
8. **Identifier-first login** (Infobip "Email or username", Twilio email-first). It leaves room
   for SSO routing later. Relay's backend supports SSO settings only.
9. **Don't copy**: Bird's 19.5 MB login bundle, MSG91's 2.4 MB jQuery signin with three tag
   containers, and Bird's `localhost` URL in production config.

## 8. Relay live UI (sms-platform-frontend-nine.vercel.app)

**Observations** (E1–E5, plus curl probes on 15 Oct 2026)
- Next.js App Router with React 19 (E1/E3 fingerprints; `SMS-UI/package.json`: next ^15.2.0,
  react ^19.0.0).
- **Bundle weight**:

  | Page | Scripts | Raw JS |
  |---|---|---|
  | `/signup` (E1) | 18 | ~541 KB |
  | `/login` (E3) | 14 | ~447 KB |
  | `/operator/login` (E4) | 14 | ~448 KB |

  On every page, two shared 173 KB chunks dominate. Login and signup chunks plus polyfills
  together are 656 KB raw, ~205 KB gzip [curl]. Add 52 KB CSS and two woff2 fonts (141 KB).
  Roughly 1/40th of Bird and about 1/4 of MSG91's signin.
- **Zero third-party hosts** on all three pages [CDP]. No analytics, RUM or error tracker.
- Response headers [curl]:
  - CSP `default-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self';
    object-src 'none'` (script-src allows `'unsafe-inline'`).
  - HSTS `max-age=63072000; includeSubDomains; preload`, `X-Frame-Options: DENY`.
  - `server: Vercel`, `x-vercel-id: bom1::…` (Mumbai edge).
  - **`x-powered-by: Next.js`** (framework disclosure).
- Source maps: `*.js.map` return **403**. REA got none (E1, E3, E4). `robots.txt` is
  `Disallow: /`. `sitemap.xml` 404.
- **Routes exposed**:
  - Chunk paths name the route groups `app/(auth)/login`, `app/(auth)/signup` and
    `app/(operator-auth)/operator/login`, so the existence of an operator console is public.
  - Public paths match `SMS-UI/src/middleware.ts:209`: `/login`, `/signup`, `/forgot` (200),
    `/reset-password` (200), `/verify-email` (200), `/operator/login`.
  - `/admin` redirects with 307 to `/operator/login?callbackUrl=%2Fadmin`.
  - Everything else, including `/.env`, `/api/health`, `/api/auth/*`, `/dashboard`,
    `/operator` and `/mfa`, redirects with 307 to `/login?callbackUrl=…`.
- **No secret-looking values** in any delivered chunk. Regexes run: `NEXT_PUBLIC_*`,
  `sk_/pk_(live|test)`, `AKIA…`, JWT `eyJ…`, `re_…`, `api[_-]key`, all with zero hits [curl]. The
  backend host `sms-api.saqibsaeed.cloud` does not appear in the client bundles. All calls go
  same-origin (consistent with `connect-src 'self'`). Storage on the signup page is empty (E2:
  no localStorage, sessionStorage, IndexedDB or cache keys).
- **Signup form** (E2 accessibility tree, E5 screenshot): Full name, Work email, Password ("At
  least 8 characters, with a letter and a number", show/hide toggle), Organization name,
  Country. Country has only 2 options: "India (DLT)" and "United States (10DLC)". No `required`
  attributes; validation runs in JS (`novalidate` form). No captcha, phone, OTP, ToS checkbox or
  invite-code field.
- Login: Email, Password, "Forgot password?" linking to `/forgot`, "Create account" linking to
  `/signup`. No SSO buttons. The operator login has the same two fields on a separate route.

**Inferences and risks** (report only, nothing changed)
1. **No bot or fraud control on signup or login**, unlike every self-serve competitor (§4). For a
   prepaid-wallet SMS platform that is a direct SMS-pumping and account-farming exposure.
2. **Invite-code mismatch**: the backend gates self-registration with `SIGNUP_INVITE_CODE`
   (`SMS-BE/internal/platform/config/config.go:46-54`, `internal/api/server.go:102-104`). The
   comment calls open signup "one half of a go-live blocker". The live signup form has no field
   for a code. So either production leaves the code empty (signup open to anyone) or signup
   cannot succeed from the UI. **Unknown which**: it was not tested, because that would mean
   submitting the form.
3. **Test-hook proxies ship in the UI**:
   - `SMS-UI/src/app/api/dev/` holds 18 POST proxy routes, including `set-my-role`,
     `drain-wallet` and `seed-tenants`.
   - They deliberately have no environment guard (`src/app/api/dev/set-my-role/route.ts:6-12`).
     The middleware matcher also exempts `api/dev` (`src/middleware.ts:209`).
   - Production safety rests entirely on the backend's `ENABLE_DEV_ENDPOINTS` staying off
     (default off, `SMS-BE/internal/platform/config/config.go:39-44`, `cmd/control-api/main.go:110`).
   - Not probed live: probing would mean POSTing to a mutating hook. The production env value
     should be confirmed.
4. **API paths answer with 307 HTML, not 401**: the tenant branch of the middleware
   (`src/middleware.ts:157-163`) redirects `/api/*` to `/login`. Only the operator branch
   (`:99-103`) returns 401. A logged-out `fetch` or EventSource gets HTML, which is the failure
   mode the `:99-101` comment describes.
5. `x-powered-by: Next.js` is cheap to drop (`poweredByHeader: false`). Low severity.
6. The 2-country signup select advertises IN and US only (consistent with live regimes in the
   context notes). GB/AE stubs are not exposed.

**Unknowns**: post-login bundle weight and routes (no credentials used). Whether production sets
`SIGNUP_INVITE_CODE` or `ENABLE_DEV_ENDPOINTS`.

## 9. Open items
- Re-run Vonage, Zenvia and Netcore in a headed browser (Cloudflare or 403 blocked headless).
- Sinch, Exotel and Gupshup per-country rates need a country selection, which is a page
  interaction REA does not perform.
- Bird price-to-product mapping is unresolved.
- No vendor's post-signup KYC flow was observed (by design, no accounts were created).
