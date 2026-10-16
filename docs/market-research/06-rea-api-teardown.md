# 06 — REA teardown of competitor SDKs and API specs vs Relay

Prepared 16 Oct 2026. Static analysis only: no competitor code was installed or run, no competitor API was called, and the live Relay API was not called.

## TL;DR

- Twilio's public spec has **1,570 operations** in 60 files, and Telnyx's has **1,422** in 219 tags. Relay's contract has **211**. Resend has 139, Bandwidth 55 and the Vonage specs sampled here have 88. Raw operation counts flatter the large vendors, because Twilio carries legacy duplicates (chat v1/v2/v3, ip_messaging v1/v2) and Telnyx counts AI, SIM and IoT work. In the areas Relay actually sells (SMS, RCS, verify, campaigns, billing), the gap is in breadth of primitives, not in raw count.
- **Every SMS-first competitor ships an official SDK and Relay ships none.** The same is true of number lookup (HLR, line type, SIM swap, fraud score): all seven global SMS vendors examined have it, and Relay does not.
- The capabilities competitors expose that Relay lacks, in rough priority order:
  1. number lookup / HLR with SIM-swap and fraud scoring
  2. sub-accounts API (create, transfer credit and balance, move numbers)
  3. official SDKs with webhook-signature helpers
  4. MMS
  5. a cross-channel content/template API (cards, carousels, list pickers, quick replies)
  6. Verify on voice, WhatsApp, email and silent network auth, all live
  7. short codes and toll-free verification
  8. number search, purchase and porting
  9. voice call control, recordings, transcriptions and SIP
  10. OTT channels beyond WhatsApp (Viber, Messenger, Instagram, Telegram, LINE, KakaoTalk, WeChat, Apple Messages for Business)
- **Relay leads on India DLT.** Plivo, Vonage and Infobip take DLT IDs as plain send parameters (`dlt_entity_id`/`dlt_template_id`, `entity-id`/`content-id`, `principalEntityId`). Relay manages the DLT entity, header and template registry and refuses sends that are not registered. No global SDK examined exposes a DLT registry API (bounded search; see §6).

## 1. Method

### 1.1 Acquisition (packages are untrusted data)

Each package went into its own new empty directory under `scratchpad/rea/<vendor>/`. It was fetched with `npm pack <pkg>` and unpacked with `tar xzf`. Nothing was `npm install`ed or executed. Every Python script that read package or spec data ran with `python3 -I`.

| Package (npm, accessed 2026-10-16) | Version | Publisher / provenance |
|---|---|---|
| `twilio` | 6.2.0 | Twilio |
| `@vonage/server-sdk` (+ `messages` 1.27.1, `verify2` 1.22.1, `voice` 1.24.1, `sms` 1.24.1, `number-insight-v2` 1.15.1, `subaccounts` 1.19.1) | 3.30.1 | Vonage |
| `plivo` | 4.79.0 | Plivo |
| `telnyx` | 7.26.0 | Telnyx (Stainless-generated) |
| `@sinch/sdk-core` 1.7.0 (+ `sms`, `verification`, `voice`, `conversation`, `numbers`, `number-lookup`, `fax`, `elastic-sip-trunking`, `provisioning`, all 1.7.0) | 1.7.0 | Sinch |
| `@infobip-api/sdk` | 0.3.2 | repo `infobip-community/infobip-api-node-sdk`. This is a **community** org, not Infobip's main org |
| `messagebird` | 4.0.1 | MessageBird / Bird |
| `@sendgrid/mail` | 8.1.6 | Twilio SendGrid |
| `bandwidth-sdk` | 8.3.1 | Bandwidth (OpenAPI-generated; ships `bandwidth.yml`) |
| `@bandwidth/messaging` | 4.1.7 | Bandwidth (legacy). Downloaded but not analysed; `bandwidth-sdk` supersedes it |
| `mailgun.js` | 14.1.0 | Mailgun |
| `resend` | 6.33.0 | Resend |
| `msg91` | 2.2.8 | **Official**: description "Official NodeJS package for msg91 services"; maintainers include `msg91com` |
| `gupshup` | 1.1.0 | **Unofficial**: maintainer `techinnover`, no repository. The npm search found no official Gupshup Node SDK; `gupshup-integrations` (publisher `srik_gupshup`) is "integration web components", not an API client |
| `exotel` | 1.0.1 | **Unofficial**: maintainer `ramniquesingh`, depends on the deprecated `request` |
| Kaleyra | — | **No official messaging SDK on npm.** Only `@kaleyra/video-*` (video plugins) and a third-party `@tiledesk/tiledesk-kaleyra-proxy` |

URLs: `https://www.npmjs.com/package/<name>` for each row (accessed 2026-10-16).

### 1.2 REA tools used and why

| Tool | Why |
|---|---|
| `analyze_javascript_application` (`detail: "summary"`, `format: "directory"`) | Builds a static application graph of each extracted package without running it. Gives artifact SHA-256, file and parse inventory, module/export graph and coverage. Run once per package (23 targets). |
| `get_evidence_bundle` (`detail: "summary"`, filters) | Recovers the parent analysis ID for Telnyx, whose summary display exceeded the 10 MB MCP frame (`resource_constraint`, `resource: transport`). The guide calls for this recovery path. |
| `trace_application_feature` (module/api seeds, `incoming`) | Confirms that a named feature module exists in the shipped graph, and recovers which index re-exports it and the export-name list. Used for Twilio Lookups/Content/Verify/Toll-free, Vonage Verify2 channels and silent auth, Vonage Number Insight v2, Vonage Subaccounts, Sinch Number Lookup, Telnyx 10DLC/RCS, and Resend `fetch`. |
| `compare_javascript_export_shapes` | Cross-vendor return-shape comparison (Vonage NI v2 vs Sinch NumberLookup, and Vonage Verify2 `Channels` vs Vonage Messages `Channels`). Both came back **unknown** (see §7). |
| `export_evidence_bundle` | Saves the full session (64 records, 2 unknowns, 758,560,035 bytes) to `scratchpad/rea-evidence-bundle.json`, so the IDs below can be checked after the MCP session closes. |

**Important limitation, observed rather than assumed.** REA's JavaScript analyser in this server records **modules, imports, exports and syntax relationships**. It does **not** record string literals as graph entities. The `request` semantic family retained 0 relations for every axios/`node:https`-based SDK. Resend, which calls `fetch`, retained 12, but the `api: fetch` seed still returned `no-match` (Evidence `ev_9cc49ea2b1813704af3fcaf2d4dacda177ce9b159f0d370721816451a1c4df91`). An earlier string-seed trace of Plivo's base URL also returned `no-match`; that trace belongs to the first, pre-reset session and was not reproduced. REA's own limitation text says an unmatched seed "is not evidence that the feature is absent".

The tools were therefore split this way:

- **REA**: artifact identity, inventory, module graph and export names.
- **Source-text reads of the same extracted bytes** (`grep`/`ls`/`jq` on files REA hashed): base URLs, endpoint paths and enum literals. These are labelled **[src]** below. They observe shipped bytes but are not REA Evidence.
- **Public OpenAPI specs** fetched with `curl` (§4): operation counts.

### 1.3 Session note

The first REA session was lost when the run paused for a rate limit and the scratchpad was cleared. Everything below comes from the second session (re-downloaded, same package versions). Evidence IDs are session-scoped, so use the exported bundle to check them.

## 2. REA artifact ledger

Columns: **Parent** is the `analyze_javascript_application` Evidence ID. **View** is the summary view's Evidence ID. Files is relevant/parsed.

| Target | Parent Evidence | View Evidence | Root SHA-256 | Files | Findings | Graph coverage |
|---|---|---|---|---|---|---|
| twilio | `ev_ad03220ae6dbc33ac5b3ecfeee96bc1a8cb59182c6c9cbf08f1a5cadbe7b4cc1` | `ev_698457ff85bce08dc2845d69a7dee03a653b24c6f5332b8d03a41b21ee9e3413` | `683c8323e7b0c30c…` | 1519/1518 | 7811 | partial |
| @vonage/messages | `ev_efe82ebd9add3fb586c363464e646f69aee1f542c32a3ee0922fddc06a8c6fbb` | `ev_613c282ab82119cafa3f03047ea632799b039b8feb4da2ed6158d739949baf5f` | `feff9b792339c9bb…` | 1310/1308 | 8567 | partial |
| @vonage/verify2 | `ev_cda9e205f1f1ddb05dfce4e6c653916d2ab7ccec9323822ab59e2f24809b40e9` | `ev_1c25d44888883b37ce14a2f4095519e95a18407a9ae84ccb2d4cee2563a4feaf` | `82eb0ce03bfc967d…` | 218/216 | 1279 | complete |
| @vonage/voice | `ev_fba766bc61fee8fd699d6b6c08a29c1f5ebc07b13df99c33233fb1346e2b2578` | `ev_af77f6a0b75f54362243aed9bebd918c431e5a99c3d20ed92a70ae166786c1cb` | `b721032acdee1fdf…` | 602/600 | 3926 | partial |
| @vonage/sms | `ev_a94eee0cfb5024bb9f6247bd7d525ccdeb1457680d1378f2d9ab8ce37bf3f421` | `ev_a9c625d7a7f0cda0e1688812a666ecaa515df9c48a9aef7fcac8081f3d7a2227` | `94318975863f5133…` | 146/144 | 930 | complete |
| @vonage/number-insight-v2 | `ev_739b5acbcdf7e9a6ed7f4c3b3d9747719bea83d83681c40228f949641d73cf6e` | `ev_53902e5c0dddfdc8cfc96f3b1ed8e4a09950d8124bf5b27609ee4c8f3c6ac4a6` | `ce6e1f8f984a6337…` | 98/96 | 498 | complete |
| @vonage/subaccounts | `ev_58fe0b71184107878e93c9c7c9b7ab7af15c926dac86631639f8e423a8704e78` | `ev_5e5715b7677089a6a5600a4b9df6649a7545af5ee4cc32bb2178b46a2807af20` | `88e11e00d1827530…` | 133/131 | 650 | complete |
| @vonage/server-sdk | `ev_9084c406a5b0e2c93d8be647403d0cd0ec2677f06ae8d2e0e8a2729e4eb66ca4` | `ev_51dece3431434cb6c21888b5a0d33bafcf3c055831014f0195c760733ff0c0ff` | `ebf2f41df0cb6448…` | 13/11 | 254 | complete |
| plivo | `ev_7699f6ed5e1c07fc3f4dcad88db6aca4c7aee53a89090d0a557e8c4e8ca3e0f0` | `ev_7fd239ad97c695c1701e2e2e5756749b112eabce74f1c341de47fd14b5fbe33c` | `afb4ec4ef3eb3ad8…` | 100/99 | 968 | partial |
| telnyx | `ev_a3efc8bf69c3ef5e9bdc01ea84039dc69d2f844bc80a28b1b1edcff54fd87e72` | `ev_5e8242cc54244fc91e15c9148725322b50f15da6e5fa78cadd2b14535eed73ed` | `0a62f0c0afaa6e6a…` | n/a (display exceeded 10 MB; 6,945 limitations) | — | retained, complete-record |
| @sinch/conversation | `ev_002858f44a181df97b3c69831eab5a1c067a3a3e61fa8425e35fb1605903c4ae` | `ev_085c65349230e04958a1203f98a8a5a441dbaaf0a519d8199a83cf60151aca4a` | `f6332763bc95951a…` | 1225/816 | 2593 | partial |
| @sinch/sms | `ev_986fec79014afd19c1809e468613f01fa71e860dae5ecc715a83d3b651b2bc8f` | `ev_df3affa9b61450e3bd35d4f30c472cda58ef50ab9431bfecd5f61c840d36ae31` | `c56b64eaa0524c31…` | 337/224 | 773 | complete |
| @sinch/verification | `ev_f834668276fc0c75dc156aad3cf2b209fbbd59603ba3622dabbaf63b6deb307c` | `ev_a3ecf6783cbecaacac2f235a06ebd4fd6ef848ec0d2ea26fcbebcb8a906f0d22` | `8eccb1c4401646cb…` | 253/168 | 685 | complete |
| @sinch/number-lookup | `ev_4115a5863bd328da595e311c41daa1cd3a4e0d8ad7e7555661fefd5326e5f19f` | `ev_d014832af28468745adc29b971a07a14e4fd3b83e64b2a7893399e4da51b0b6d` | `32a4fbba21c96fda…` | 94/62 | 188 | complete |
| @infobip-api/sdk | `ev_2bf57be0bd8f2b8eb0593b6920cb0f0eb9a2f2ddc821e1502ce2f6b70d32eaeb` | `ev_142a2d9b0c09096db74fbf91149655e2dd8b4a2ba4408fbcd7289d9e81abd76d` | `8895c0aaffe4bdca…` | 56/36 | 256 | partial: semantic graph of `dist/sdk.esm.js` and `sdk.cjs.production.min.js` exceeded the result budget (static and module facts were kept) |
| bandwidth-sdk | `ev_91fc2887c8a4f71f8fe0663c11f3f253311cf21ca8caab117d727a24393e9338` | `ev_b8d9987099ff98dda8362adc2fb6a6f501f5fb21928eb21948cbde8508e6fac3` | `5e5576e75f49c42a…` | 1144/1036 | 2998 | partial |
| messagebird | `ev_3132b6ba28b9ee5136c18c861470eda43cba268cd0037efd70f16953c8463142` | `ev_0a91b11b34336edd244d11173c3520cd7556eaa9e57a29395ca6d82c29de6cf7` | `50942e473c77c82b…` | 64/59 | 330 | partial (1 parse failure) |
| @sendgrid/mail | `ev_8aa297490c12f97741044e6c17c82390e8ef1a61e53e3238fb379d243a87fcfa` | `ev_338f3ef4b9f73a89e5daf2722f815d30c3daf4e0025ec640e4bc265a081a38ce` | `a94a32489d18954f…` | 7/6 | 28 | complete |
| mailgun.js | `ev_1369f0583accae15071016906d4e5035aa6028e31a1f6e00ae5bd895963c70db` | `ev_fdda12390ddac8d9b8e3b45d1a3d3e8f2939c67d85b811575927c7e17c7e0e99` | `647548fbec827060…` | 200/197 | 895 | partial |
| resend | `ev_c85c972f33467dd6d74f6cfd5e47e01afcab22512a605c429d0a9fe26f8ba7e9` | `ev_f73f1b99dff6b26203d70ce724f5dc47782d98e90cff21794021e5f283297a40` | `e59628eab64cc9af…` | 5/4 | 454 | complete |
| msg91 | `ev_2a1fe2d1e210a5e7bc79aab01e86f4ce8c6840dfcab71bb4bad8452a17e02371` | `ev_c805b9d60584694518b08db172dc4663aa2517e454a54dac4e1a6e73526b9035` | `196abafad49ce279…` | 5/4 | 9 | partial: the worker ran out of heap on `dist/src/index.js` |
| gupshup (unofficial) | `ev_cb51135d4cfdd69f20b6c395e6347a299c8d64a00a7e916feeea091d674944f4` | `ev_2af9f95963d773386a08a0a1fbc8123340e248682970a9452f09ee371aef55d3` | `02ac1daea0f2f356…` | 12/8 | 39 | complete |
| exotel (unofficial) | `ev_9d6e2d151e556e6385a5f3eb94ba60ba3b2e62d1b5b3cf9d2bd2ca3296f85b04` | `ev_2d607276a7b4e374854ec6d348f192ae0eaccc61ce0e5db704809df2031f3834` | `a420fd3f9c2233f1…` | 3/2 | 8 | complete |

Not analysed with REA, read as source text only [src]: `@sinch/voice`, `@sinch/numbers`, `@sinch/fax`, `@sinch/elastic-sip-trunking`, `@sinch/provisioning`, `@bandwidth/messaging`.

### 2.1 Feature traces (`trace_application_feature`, direction `incoming`)

| Seed (module) | App (parent) | Trace Evidence | Result |
|---|---|---|---|
| twilio `lib/rest/lookups/v2/phoneNumber.js` | `ev_ad0322…` | `ev_e1c9130e5160f64361daacdccd48fd1f523cc22870adabbaddae6bbbdcee637e` | matched 1; 3 nodes / 2 edges (containment); source graph partial |
| twilio `lib/rest/content/v1/content.js` | `ev_ad0322…` | `ev_52028a518d13977934952f54f8cd10584f513a6e77946626b9a7f5b588d81045` | matched 1; 5 nodes / 6 edges; reached `lib/rest/Content.d.ts` |
| twilio `lib/rest/verify/v2/service/verification.js` | `ev_ad0322…` | `ev_64cda3c10f1371f4699b850de04cb89f5194c1c2cc0c2cde157f1d86d5d4bb04` | matched 1; containment only |
| twilio `lib/rest/messaging/v1/tollfreeVerification.js` | `ev_ad0322…` | `ev_ea17c84935210ff545dbcbbc65f61bfe9d61880509c9297ed34e5a4ce87c3203` | matched 1; containment only |
| vonage verify2 `dist/cjs/enums/channels.js` | `ev_cda9e2…` | `ev_f61f5e95cca9f08947c581648752056508d2213457baff6d6d48bcd4ff272dc4` | complete-within-source; 48 nodes / 89 edges. Index re-exports `RCSWorkflow`, `EmailWorkflow`, `VoiceWorkflow`, `WhatsAppWorkflow`, `WhatsAppInteractiveWorkflow`, `SilentAuthWorkflow`, `SMSWorkflow`, `Template`, `TemplateFragment` (observed export names) |
| vonage verify2 `dist/cjs/types/workflows/silentAuthWorkflow.js` | `ev_cda9e2…` | `ev_01812eaeae0a8d099f11e1b951b3173b97471933dd439319ae8289b8953eabac` | complete-within-source; 23 nodes; `SilentAuthWorkflowRequest`, `SilentAuthCallback`, `SilentAuthStatus` |
| vonage NI v2 `dist/cjs/enums/insight.js` | `ev_739b5a…` | `ev_10c653e1a3a2ab75f2dcf23ea36f29eaa72b9ba20709a82fd2b343b389769bb6` | complete-within-source; exports `FraudCheck`, `FraudScore`, `SimSwap`, `RiskRecommendation`, `Label` |
| vonage subaccounts `dist/cjs/subaccount.js` | `ev_58fe0b…` | `ev_f6c534101f070df954395df2c21c11f9c6bf28a59f0414596bce0a2d4f4b10d9` | complete-within-source; index exports `BalanceTransfer`, `CreditTransfer`, `NumberTransfer`, `SubAccount*` (21 names) |
| sinch number-lookup `dist/index.js` | `ev_4115a5…` | `ev_8d1eef9dc7bb35441ebe94aaa630920365e8472d5fa421517c7301df2e817f76` | complete-within-source; `package.json:main` → module exporting `NumberLookup` |
| telnyx `resources/messaging-10dlc` (contains) | `ev_a3efc8…` | `ev_e47206b6a9911511a6f6df90da2d9035dfbcdcd2fceb1ed4b42a72ab80ef91cb` | 171 seed matches; `client.d.ts` exports 1,520 names, including `Messaging10dlc`, `ManagedAccounts`, `NumberLookup`, `Email*` (domains, inboxes, templates, validations, unsubscribe groups) and `Fax*` |
| telnyx `resources/rcs.js` | `ev_a3efc8…` | `ev_70a5b758c7f2fb38f2e78d1a5cb1d608b3478a524ad498b4c241288f3ee618f7` | matched 1; containment only |
| resend `api: fetch` | `ev_c85c97…` | `ev_9cc49ea2b1813704af3fcaf2d4dacda177ce9b159f0d370721816451a1c4df91` | **no-match**, which is not evidence of absence |

**What a trace proves.** A trace is observed graph membership plus *inferred* import/re-export edges. It does not show that the code runs or which HTTP call it makes.

## 3. Per-vendor findings

Labels: **[REA]** = an Evidence-backed graph fact. **[src]** = a source-text read of the REA-hashed package bytes. **[spec]** = public OpenAPI. **[inf]** = analyst inference.

### Twilio (`twilio` 6.2.0)

- **38 product domains, each with its own host** [src, `lib/rest/*Base.js`]. The hosts include `accounts`, `api`, `bulkexports`, `chat`, `content`, `conversations`, `events`, `flex-api`, `iam`, `insights`, `intelligence`, `knowledge`, `lookups`, `marketplace`, `memory`, `messaging`, `monitor`, `notify`, `numbers`, `oauth`, `pricing`, `proxy`, `routes`, `serverless`, `studio`, `supersim`, `sync`, `taskrouter`, `trunking`, `trusthub`, `verify`, `video`, `voice` and `wireless`, each at `.twilio.com`. Root artifact: [REA `ev_ad0322…`].
- **Lookups v2** [REA trace `ev_e1c913…`; src `lookups/v2/phoneNumber.js`]. Fields: `caller_name`, `carrier_name`, `mobile_country_code`/`mobile_network_code`, `last_sim_swap`/`swapped_in_period`, `call_forwarding_enabled`, `is_number_reassigned`, `sms_pumping_risk_score`, `carrier_risk_category`, `line_type`. It also does identity match (`first_name_match`, `date_of_birth_match`, `national_id_match`, `address_*_match`, `summary_score`). Other modules: `bucket`, `rateLimit`, `lookupOverride`, `query`.
- **Verify v2** [REA trace `ev_64cda3…`; src]:
  - Channels: `VerificationChannel = "sms" | "call" | "email" | "whatsapp" | "sna"` (`verification.d.ts:7`). `sna` is silent network auth.
  - Modules: `safelist`, `verificationAttempt`, `verificationAttemptsSummary`, `form`, `template`. Under service: `rateLimit`, `messagingConfiguration`, `entity`/`newFactor`/`challenge` (TOTP/push), `accessToken` and `webhook`.
  - [inf] Twilio's "Fraud Guard" is a server-side setting; the SDK shows `safelist` and the Lookups `sms_pumping_risk_score`, not a Fraud Guard object.
- **Content API v1** [REA trace `ev_52028a…`; src `content/v1/content.d.ts`]. Rich types: `twilioText`, `twilioMedia`, `twilioLocation`, `twilioListPicker`, `twilioCallToAction`, `twilioQuickReply`, `twilioCard`, `twilioCarousel`, `twilioCatalog`, `twilioFlows`, `twilioSchedule`. `contentAndApprovals` handles WhatsApp approval state.
- **Messaging v1/v2** [src]:
  - Messaging Services (`usecase`, `stickySender`, `mmsConverter`, `smartEncoding`, `scanMessageContent`, `fallbackToLongCode`, `areaCodeGeomatch`, `validityPeriod`).
  - 10DLC (`brandRegistration`, `usecase`, `externalCampaign`), `tollfreeVerification` [REA trace `ev_ea17c8…`], link shortening (`linkshorteningMessagingService`, `domainConfig`, `domainCerts`).
  - v2: `channelsSender`, `typingIndicator`. [inf] `channelsSender` is the WhatsApp/RCS sender onboarding surface.
- **API 2010** [src `api/v2010/account`]: `message`, `call`, `conference`, `queue`, `recording`, `transcription`, `shortCode`, `incomingPhoneNumber`, `availablePhoneNumberCountry`, `address`, `outgoingCallerId`, `sip/{domain, credentialList, ipAccessControlList}`, `usage/{record, trigger}`, `balance`, `key`/`signingKey`, `validationRequest`.
- **Conversations v1** [src]: `conversation`, `conversationWithParticipants`, `participantConversation`, `addressConfiguration`, `service`, `role`, `user`, `credential`.
- **Auth** [src]: HTTP Basic (AccountSid/AuthToken or API key), plus `ClientCredentialProvider` and `OrgsCredentialProvider` (OAuth). The spec's securityScheme is `accountSid_authToken: basic` [spec]. Webhooks are signed with `X-Twilio-Signature`; helpers `validateRequest`/`validateRequestWithBody` [src].

### Vonage (`@vonage/*`)

- **Packages.** `server-sdk` 3.30.1 bundles 17 packages: accounts, applications, auth, conversations, messages, number-insights, numbers, pricing, redact, reports, server-client, sms, users, verify, verify2, video, voice [src `package.json`; REA `ev_9084c4…`].
- **Messages API channels** [src `messages/dist/cjs/enums/Channels.js`; REA `ev_efe82e…`]: `sms`, `mms`, `whatsapp`, `messenger`, `viber_service`, `rcs`, `email`. The classes directory has `SMS`, `MMS`, `WhatsApp`, `Messenger`, `Viber`, `RCS`, `Email`, plus abstract text/image/audio/video/file/vCard messages.
- **Verify v2** [REA traces `ev_f61f5e…`, `ev_01812e…`; src]:
  - Channels: `sms`, `whatsapp`, `whatsapp_interactive`, `voice`, `rcs`, `email`, plus a separate `SilentAuthChannel = silent_auth`.
  - Methods: `newRequest`, `checkCode`, `nextWorkflow`, `cancel`, and template plus template-fragment CRUD.
- **Number Insight v2** [REA trace `ev_10c653…`; src]: insights `fraud_score` and `sim_swap`; exports `RiskRecommendation` and `Label`. Number Insight v1 is still in the spec (4 ops) [spec].
- **Subaccounts** [REA trace `ev_f6c534…`; src]: `createSubAccount`, `getSubAccount`, `updateSubAccount`, `listSubAccounts`, `getPrimaryAccount`, `transferCredit`, `listCreditTransfers`, `transferBalance`, `listBalanceTransfers`, `transferNumber`.
- **Voice NCCO actions** [src]: `Connect`, `Conversation`, `Input`, `Notify`, `Record`, `Stream`, `Talk`, `Wait`.
- **SMS (India)** [src `@vonage/sms`]: carries `entity-id`/`content-id` (`entityId`/`contentId`). [inf] These are the India DLT pass-through parameters.
- **Hosts** [spec servers]: `api.nexmo.com` (`/v1`, `/v2/verify`, `/ni`, `/accounts`), `rest.nexmo.com`, `{api-region}.nexmo.com/v1`. The hosts were not observed in the downloaded sub-package bytes ([inf] they live in `@vonage/server-client`, which was not downloaded).
- **Auth** [spec; src]: Bearer (JWT, application) and Basic (key/secret). JWT appears in the SDK bytes.

### Telnyx (`telnyx` 7.26.0)

- **193 top-level resource modules** [src `resources/*.js`]. `client.d.ts` exports 1,520 names [REA trace `ev_e47206…`].
- **Messaging**: `messages`, `messaging-profiles`, `messaging-10dlc`, `messaging-tollfree`, `messaging-hosted-numbers`, `messaging-optouts`, `messaging-url-domains`, `alphanumeric-sender-ids`, `short-codes`, `rcs` [REA `ev_70a5b7…`], `whatsapp`, `whatsapp-message-templates`, `mobile-push-credentials`.
- **Email** (new): `email-domains`, `email-inboxes`, `email-messages`, `email-templates`, `email-threads`, `email-events`, `email-blocks`, `email-unsubscribe-groups`, `email-validations`.
- **Verify** [src]: channel literals `'sms' | 'call' | 'flashcall' | 'whatsapp'`, plus `verify-profiles` and `verified-numbers`.
- **Lookup** [src]: `number-lookup` with `'carrier' | 'caller-name'`.
- **Accounts and billing**: `managed-accounts`, `spend-limits`, `billing-groups`, `usage-reports`, `invoices`, `balance`, `charges-*`. Agent payment rails: `x402`, `machine-payments` [src].
- **Voice and infrastructure**: `calls`, `conferences`, `queues`, `recordings`, `recording-transcriptions`, `texml`, `fax-applications`/`faxes`, SIP `connections`/`credential-connections`/`fqdn-connections`/`ip-connections`, `porting-orders`, `portouts`, and AI (`ai`, `assistants`, `llm-token-gateway`, `voice-clones`).
- **Auth** [src]: `Bearer ${apiKey}` and `oauth/token`. Base URL `https://api.telnyx.com/v2`.
- **Webhooks** [spec]: 61 webhook event-type enum literals across message/call/verification/fax/number_order/porting_order. Message events are `message.sent`, `message.finalized`, `message.received`, `message.read`, `message.echo`.

### Plivo (`plivo` 4.79.0)

- **Resources** [src `dist/resources`; REA `ev_7699f6…`]: `messages`, `call`, `conferences`, `multiPartyCall`, `recordings`, `endpoints` (SIP), `numbers`, `lookup`, `verify`, `verifyCallerId`, `brand`, `campaign`, `profile` (10DLC), `tollfree_verification`, `powerpacks`, `maskingSession`, `phlo` (visual flows), `media`, `pricings`, `accounts` (Subaccount appears 31×), and compliance (`complianceApplications`, `endUsers`, `loa`, `hostedMessagingNumber`).
- **Hosts** [src]: `api.plivo.com/v1/`, `lookup.plivo.com/v1/`, `phlorunner.plivo.com/v1`.
- **Messages carry India DLT fields** [src `messages.js`]: `dlt_entity_id`, `dlt_template_id`, `dlt_template_category`. The same file contains `MMS`/`mms` (19 occurrences) and `whatsapp` (9 occurrences).
- **Webhook signing** [src]: `validateSignature` and `validateV3Signature` (`X-Plivo-Signature-V3`).

### Sinch (`@sinch/*` 1.7.0)

- **Conversation API channels** [src `models/v1/enums.d.ts`; REA `ev_002858…`]: `SMS`, `MMS`, `RCS`, `WHATSAPP`, `MESSENGER`, `INSTAGRAM`, `VIBER`, `VIBERBM`, `TELEGRAM`, `LINE`, `KAKAOTALK`, `KAKAOTALKCHAT`, `WECHAT`, `APPLEBC` (14 channels). API groups: `app`, `capability`, `consents`, `contact`, `conversation`, `events`, `messages`, `templates-v1`, `templates-v2`, `transcoding`, `webhooks`, `project-settings`.
- **Verification** [REA `ev_f83466…`; src]: `startSms`, `startFlashCall`, `startCallout`, `startPhoneCall`, `startData`, `startSeamless`, `startWhatsApp`, plus `report*` variants.
- **Number Lookup** [REA trace `ev_8d1eef…`; src]: `carrier`, `rnd` (reassigned-number detection), `simSwap`.
- **SMS** [REA `ev_986fec…`; src]: `batches`, `groups`, `inbounds`, `delivery-reports`, `callbacks`.
- **Elastic SIP trunking** [src]: `sip-trunks`, `sip-endpoints`, `access-control-list`, `credential-lists`, `call-blocking-rules`, `country-permissions`, `calls-history`. Fax hosts: `fax.api.sinch.com`, `voice.api.sinch.com`.
- **Auth** [src]: OAuth2 (project), `ApplicationSigned` HMAC with `x-timestamp` for voice/verification.

### Infobip (`@infobip-api/sdk` 0.3.2; community-org SDK)

- **Endpoints observed** [src `dist/sdk.esm.js`; REA `ev_2bf57b…` partial]:
  - SMS: `/sms/2/text/advanced`, `/sms/2/binary/advanced`, `/sms/1/{bulks, reports, logs, inbox/reports, preview}`.
  - WhatsApp: `/whatsapp/1/message/{text, template, image, video, audio, document, sticker, location, contact, interactive/buttons, interactive/list, interactive/product, interactive/multi-product}`, `/whatsapp/2/senders/{sender}/templates`.
  - Email: `/email/3/send`, `/email/1/{bulks, domains, logs, reports}`, `/email/2/validation`.
  - 2FA: `/2fa/2/{applications, pin}`.
  - Auth: `/auth/1/{oauth2/token, session}`.
- **India DLT** [src]: `principalEntityId` and `regional` blocks are present.
- **Auth** [src]: `App <key>`, `IBSSO` session, Bearer (OAuth2), Basic.
- [inf] This SDK covers only SMS, WhatsApp, Email and 2FA. Infobip's RCS, Viber and voice products are **not** in it, so do not treat this SDK as Infobip's full surface. No official Infobip OpenAPI repository was found on GitHub; the only hit was `infobip/infobip-openapi-mcp` (GitHub search, accessed 2026-10-16).

### Bandwidth (`bandwidth-sdk` 8.3.1)

- **APIs** [src `dist/api`; REA `ev_91fc28…`]: `messages`, `multi-channel` (channels `SMS`, `MMS`, `RBM`, i.e. RCS Business Messaging), `media`, `calls`, `conferences`, `recordings`, `transcriptions`, `endpoints`, `mfa`, `phone-number-lookup`, `toll-free-verification`, `statistics`.
- **Hosts** [src]: `messaging.bandwidth.com/api/v2`, `voice.bandwidth.com/api/v2`, `mfa.bandwidth.com/api/v1`, `api.bandwidth.com/v2`, OAuth `api.bandwidth.com/api/v1/oauth2/token`.
- **Shipped spec**: the package includes `bandwidth.yml` (counted in §4). Auth: Basic and OAuth2 [spec].

### MessageBird (`messagebird` 4.0.1)

- **Resources** [src `lib/messagebird.js`; REA `ev_3132b6…`]: `balance`, `hlr`, `lookup`, `messages`, `mms`, `voice_messages`, `verify`, `conversations`, `contacts`, `groups`, `callflows`, `calls`, `recordings`, `transcriptions`, `webhooks`.
- **Hosts and auth**: `rest.messagebird.com`, `conversations.messagebird.com`, `voice.messagebird.com`. Auth is `AccessKey <key>`; webhooks are signed with JWT (`webhook-signature-jwt.js`).
- [inf] The SDK predates the Bird rebrand, so it may not reflect current Bird APIs.

### MSG91 (`msg91` 2.2.8, official)

- **Endpoints** [src `dist/src/index.js`]: `api.msg91.com/api/v5/{flow/, otp, otp/retry, otp/verify}`, `control.msg91.com/api/v5/campaign/api/campaigns`.
- **Coverage** [REA `ev_2a1fe2…`]: partial (heap limit). [inf] The SDK is thin (SMS flow, OTP, campaigns). WhatsApp, email, voice and RCS are not in it, although MSG91 sells them (see 02-competitors-global.md).

### Gupshup / Exotel / Kaleyra (India)

- **Gupshup**: no official Node SDK. The unofficial `gupshup` package [REA `ev_cb5113…`] wraps `https://api.gupshup.io/sm/api/v1` with `sendTextMessage` only [src].
- **Exotel**: the unofficial `exotel` package [REA `ev_9d6e2d…`] wraps `/v1/Accounts/…/Sms/send` and `Calls/connect` [src].
- **Kaleyra**: no messaging SDK on npm.
- [inf] **The Indian incumbents do not compete on developer SDKs**, so an official Relay SDK would be a differentiator in India specifically.

### Email-only vendors

- **Resend 6.33.0** [REA `ev_c85c97…`; src]: `emails`, `domains`, `apiKeys`, `webhooks`, `templates`, `broadcasts`, `contacts`, `contactProperties`, `segments`, `topics`, `automations`, `inboxes`, `suppressions`, `logs`, `usage`, `oauthGrants`. Depends on `standardwebhooks` (webhook signing).
- **Mailgun.js 14.1.0** [REA `ev_1369f0…`; src]: `/v3/{domains, routes, lists, ip_pools, ips, stats/total}`, `/v4/address/validate[/bulk]`, `/v4/inbox/{tests, seedlists, results, providers}`, `/v5/accounts/subaccounts`, `/v1/analytics/{logs, metrics, usage/metrics}`, `/v1/dkim/keys`, `/v1/keys`.
- **@sendgrid/mail 8.1.6** [REA `ev_8aa297…`; src]: `/v3/mail/send` only, with `setApiKey` and `setTwilioEmailAuth`.

## 4. OpenAPI operation counts (public specs, fetched 2026-10-16)

Counter: `scratchpad/scripts/count_ops.py`, run with `python3 -I`. It counts path × HTTP-method pairs.

| Vendor | Source | Ops | Notes |
|---|---|---|---|
| Twilio | https://github.com/twilio/twilio-oai `spec/json/*.json` (60 files) | **1,570** | Largest areas: api_v2010 197, conversations_v1 103, flex_v1 73, trusthub_v1 61, taskrouter_v1 61, messaging_v1 58, verify_v2 57, chat_v2/ip_messaging_v2 54 each, sync_v1 48, numbers_v2 47, memory_v1 46, video_v1 39, serverless_v1 39, voice_v1 32, supersim_v1 31, intelligence_v2 29, proxy_v1 25, trunking_v1 24, events_v1 22, studio_v1 20, accounts_v1 20, insights_v1 17, notify_v1 15, lookups_v2 10, content_v1 9 (+v2 2), pricing_v1+v2 13, messaging_v2 7 |
| Telnyx | https://github.com/team-telnyx/openapi `openapi/spec3.json` | **1,422** (960 paths, 219 tags) | Largest tags: Porting Orders 53, Assistants 50, Call Commands 42, Missions 41, TeXML 35, WhatsApp Phone Numbers 34, SIM Cards 26, Reputation 22, Conversations 21, Email Inboxes 19, Brands 16, Verify 16, Campaign 15, Messaging 15, WhatsApp Business Accounts 14, MDR Usage Reports 13, Call Recordings 11, Messages 11, RCS Agents 10, WhatsApp Message Templates 10, RCS 9, Managed Accounts 8 |
| Vonage | `https://developer.vonage.com/api/v1/developer/api/file/<name>?format=json&vendorId=vonage` (12 specs) | **88** in sampled specs | conversation 20, verify.v2 15, voice 11, subaccounts 9, account 7, verify v1 6, numbers 5, reports 5, number-insight v1 4, pricing 3, messages 2, sms 1. **Incomplete**: the `number-insight-v2` and `application` spec names returned errors, and video, users and redact were not fetched |
| Resend | https://github.com/resend/resend-openapi `resend.json` | **139** (87 paths) | Inboxes 26, Contacts 13, Emails 10, Broadcasts 10, Webhooks 10, Domains 9, Automations 9, Templates 7 |
| Bandwidth | `bandwidth.yml` shipped inside `bandwidth-sdk` 8.3.1 | **55** (37 paths) | Recordings 10, Conferences 9, Toll-Free Verification 9, Calls 5, Endpoints 5, Media 4, MFA 3, Phone Number Lookup 3, Transcriptions 3, Messages 2, Multi-Channel 1, Statistics 1. Covers the SDK's APIs only, not Bandwidth's Dashboard (numbers) API |
| Plivo, Sinch, Infobip, MessageBird, MSG91, Gupshup | — | not counted | No public machine-readable spec found in a bounded GitHub search (accessed 2026-10-16). Unknown, not zero |
| **Relay** | `openapi/control.json` | **211** (170 paths) | operator 70, developer 17, auth 12, campaigns 9, automation 9, wallet 9, verify 8, rcs 7, sender-ids 7, conversations 6, templates 6, contact-lists 6, billing 6, analytics 5, contacts 4, support 4, team 4, messages 3, suppressions 3, registrations 3, alerts 2, data-retention 2, me 2, sessions 2, sso 2, media 1, pricing 1, tenant 1 (path-prefix grouping; spec tags cover only 8 ops). 39 more routes are mounted outside the spec (shared context) |

Normalisation [inf]:

- **Twilio** includes deprecated duplicates (chat v1/v2/v3 and ip_messaging v1/v2 total 189 ops) plus contact-centre products (Flex, TaskRouter) that Relay does not sell.
- **Telnyx** includes AI assistants, IoT SIM and networking.
- **Relay's 70 operator ops** are an internal back-office surface that competitors do not publish.
- **Customer-facing, Relay has about 141 ops.**

## 5. Endpoint-capability matrix

Legend:

- **Y**: observed in the shipped SDK [src/REA] or spec.
- **S**: spec only.
- **P** (Relay): in the contract but partial or sandbox-only.
- **N** (Relay): not in `control.json` and no code found.
- **—**: not observed in the bounded search. This means unknown, not absent.
- **?**: ambiguous.

Relay cells cite `openapi/control.json` paths. Live versus sandbox status comes from the shared context: `internal/connector/smpp.go:801` sends any non-SMS channel to the fallback (sandbox) connector.

| Capability | Twilio | Vonage | Telnyx | Plivo | Sinch | Infobip SDK | Bandwidth | MsgBird | MSG91 SDK | **Relay** |
|---|---|---|---|---|---|---|---|---|---|---|
| SMS send + DLR webhooks | Y | Y | Y | Y | Y | Y | Y | Y | Y | **Y** `POST /v1/messages`, HMAC webhooks |
| Batch/bulk send endpoint | Y (notify) | — | Y | Y (bulk dst) | Y `batches` | Y `/sms/1/bulks` | — | — | Y flow | **P** (campaigns, not a batch messages endpoint) |
| MMS | Y | Y | Y | Y | Y | — | Y | Y | — | **N** (not in channel enum, `internal/api/enums.go:33`) |
| WhatsApp | Y | Y | Y | Y | Y | Y (13 msg types) | — | Y | — | **P** (sandbox only) |
| RCS | Y? (`channelsSender`) | Y | Y | — | Y | — | Y (RBM) | — | — | **Y** `/v1/rcs/agents*`, `/v1/rcs/capabilities` (live via one aggregator) |
| Viber / Messenger / Instagram / Telegram / LINE / Kakao / WeChat / Apple | — | Viber, Messenger | — | — | all 8 | — | — | — | — | **N** |
| Email as a channel | (SendGrid) | Y | Y (9 resources) | — | — | Y | — | — | — | **P** (sandbox) |
| Voice call control / IVR | Y | Y (NCCO) | Y | Y (+PHLO) | Y | — | Y | Y | — | **P** (VOICE sandbox; only `/v1/sender-ids/{id}/voice-*`) |
| Recordings / transcriptions | Y | Y (Record) | Y | Y | — | — | Y | Y | — | **N** |
| SIP trunks / domains / endpoints | Y | — | Y | Y | Y | — | Y | — | — | **N** |
| Number lookup / HLR / line type | Y | Y (NI v1/v2) | Y | Y | Y | — | Y | Y | — | **N** |
| SIM-swap / reassigned / fraud score | Y | Y | — | — | Y (simSwap, rnd) | — | — | — | — | **N** |
| Verify / OTP API | Y | Y | Y | Y | Y | Y (2FA) | Y (MFA) | Y | Y | **Y** `/v1/verify/services*` (8 ops) |
| Verify over voice | Y | Y | Y | ? | Y | — | Y | Y | — | **P** (channel exists, sandbox) |
| Verify over WhatsApp | Y | Y | Y | ? | Y | — | — | — | — | **P** (sandbox) |
| Verify over email | Y | Y | — | — | — | — | — | — | — | **P** (sandbox) |
| Verify over RCS | — | Y | — | — | — | — | — | — | — | **?** (RCS is a channel id; a verify-over-RCS path was not confirmed) |
| Silent network auth / flash call | Y (`sna`) | Y (`silent_auth`) | flashcall | — | Y (seamless, flashcall) | — | — | — | — | **N** |
| Verify fraud / attempt analytics | Y (attempts, safelist) | Y (fraud_score) | — | — | — | — | — | — | — | **Y** `/v1/verify/services/{id}/analytics`, `/attempts` |
| Cross-channel content/template API | Y (11 rich types) | Y (verify templates) | Y (WA templates) | — | Y (templates v1/v2) | Y (WA templates) | — | — | Y (flow) | **P** `/v1/templates*` (single rich card, no carousel) |
| Conversations / omnichannel inbox | Y | Y | Y | — | Y | — | — | Y | — | **P** `/v1/conversations*` (6 ops; SMS/RCS) |
| Sender pools / messaging service (sticky, geomatch, fallback) | Y | — | Y (profiles) | Y (powerpacks) | — | — | — | — | — | **P** (sender-ids + routing; RCS→SMS fallback) |
| Short codes | Y | — | Y | — | — | — | — | — | — | **N** |
| Toll-free verification | Y | — | Y | Y | — | — | Y | — | — | **N** |
| US 10DLC brand / campaign | Y | — | Y | Y | — | — | — | — | — | **P** `/v1/registrations*` (3 ops) |
| India DLT parameters on send | — | Y (entity/content id) | ? (bounded grep hit, unverified) | Y | — | Y (principalEntityId) | — | — | ? (likely via flow) | **Y** (registry + gate; §6) |
| Number search / buy / port | Y | Y | Y | Y | Y | — | (separate API) | — | — | **N** |
| Sub-accounts API (create, transfer credit/numbers) | Y (v2010 Accounts) [inf] | Y (11 methods) | Y (managed-accounts) | Y | — | — | — | — | — | **N** |
| Usage records / reports | Y (usage record, bulkexports) | Y (reports) | Y (MDR/CDR) | — | — | Y (logs/reports) | Y (statistics) | — | — | **P** `/v1/billing/usage`, `/v1/analytics*` |
| Usage triggers / alerts | Y (usage trigger, monitor alerts) | — | Y (notifications) | — | — | — | — | — | — | **P** `GET/PATCH /v1/alerts` |
| Spend limits | — | — | Y | — | — | — | — | — | — | **N** (prepaid wallet only) |
| Pricing API | Y | Y | Y | Y | — | — | — | — | — | **Y** `GET /v1/pricing` |
| Balance / wallet | Y | Y | Y | Y | — | — | — | Y | — | **Y** `/v1/wallet*` (9 ops) |
| Media upload | Y | — | Y | Y | — | — | Y | — | — | **Y** `POST /v1/media` |
| Webhook endpoints API (CRUD, test, resend) | Y (events sinks) | — | Y (deliveries) | — | Y (conv webhooks) | — | — | Y | — | **Y** `/v1/developer/webhooks*` (8 ops) |
| Webhook signature helper in SDK | Y | Y (JWT) | — | Y (V3) | Y (HMAC) | — | — | Y (JWT) | — | **N** (no SDK; server-side HMAC exists) |
| Official server SDK | Y | Y | Y | Y | Y | community | Y | Y | Y | **N** |
| OAuth / scoped API keys | Y | Y (JWT apps) | Y | — | Y | Y | Y | — | — | **P** `/v1/developer/api-keys*`, `/scopes` (no OAuth client grant) |
| IP allowlist | — | — | Y (access-ip) | — | — | — | — | — | — | **Y** `/v1/developer/ip-allowlist*` |
| Fax | (gone) | — | Y | — | Y | — | — | — | — | **N** |
| Video / rooms | Y | Y | Y | — | — | — | — | — | — | **N** (out of scope) |

**Email-vendor sub-matrix (Relay EMAIL is sandbox only).** Domain/DKIM management: Resend Y, Mailgun Y (`/v3/domains`, `/v1/dkim/keys`), SendGrid SDK — (mail send only), Relay **N**. Address validation: Mailgun Y (`/v4/address/validate`), Telnyx Y, Infobip Y, Relay **N**. Inbox placement testing: Mailgun Y (`/v4/inbox/*`). Inbound inboxes: Resend Y (26 ops), Telnyx Y. Suppressions: Resend Y, Relay **Y** `/v1/suppressions*` (channel-agnostic). Sub-accounts: Mailgun Y (`/v5/accounts/subaccounts`).

### 5.1 What competitors expose that Relay lacks (ranked by fit to Relay's India and RCS positioning) [inf]

1. **Number lookup / HLR + fraud signals** (all 7 global SMS vendors). It pairs naturally with Relay's verify fraud analytics and with DND scrubbing.
2. **Sub-accounts / reseller API** (Vonage, Telnyx, Twilio, Plivo, Mailgun). Already listed as absent in the shared context.
3. **Official SDKs + webhook-signature helpers** (every global vendor; India incumbents have none, which is a differentiation opening).
4. **Verify on non-SMS channels live**, plus silent network auth / flash call (Twilio, Vonage, Sinch, Telnyx).
5. **Content API with rich types** (cards, carousels, list pickers, quick replies) shared across RCS and WhatsApp (Twilio, Sinch).
6. **MMS** (8 of 9).
7. **Number inventory** (search, buy, port) and **short codes / toll-free verification** (US-market table stakes).
8. **Voice primitives**: call control, recordings, transcriptions, SIP.
9. **OTT breadth**: Viber, Messenger, Instagram, Telegram, LINE, Kakao, WeChat, Apple Messages for Business (Sinch has 14 channels).
10. **Usage triggers and spend limits** (Twilio, Telnyx).

## 6. Where Relay is ahead or different [inf, Relay facts from contract and shared context]

- **India DLT as a managed registry.** Competitors only pass DLT IDs per send:
  - Plivo: `dlt_entity_id`, `dlt_template_id`, `dlt_template_category`.
  - Vonage: `entity-id`, `content-id`.
  - Infobip: `principalEntityId`.

  Relay manages entities, headers and templates (`/v1/sender-ids*`, `/v1/templates*`, `/v1/registrations*`) and refuses unregistered sends (commit `4417d70`). In this bounded grep, no examined SDK exposes a DLT registry-management API. This is unknown for vendors with partial coverage (Telnyx: grep hit, not inspected).
- **Campaigns, journeys and automation in the same API** (`/v1/campaigns*` 9, `/v1/automation*` 9, `/v1/contact-lists*` 6). Among the SMS vendors, only MSG91 shows campaigns in its SDK.
- **Webhook management with test and resend** (`/v1/developer/webhooks/{id}/test-event`, `…/events/{eventId}/resend`) and **IP allowlist** match or exceed most SDK-visible surfaces.
- **RCS agent lifecycle** (`/v1/rcs/agents/{id}/verification`, `/launch`) and **RCS capability check** (`POST /v1/rcs/capabilities`) are comparable to Telnyx's "RCS Agents" (10 ops).

## 7. Limitations and unknowns (REA rules: observation vs inference)

- **Observation scope.** REA observed artifact bytes, module graph, exports and containment, with `authority: shipped-artifact` / `ast-static-analysis`. Import and re-export edges are **inferred** (`static-relationship-inference`). Nothing was executed, so **no claim here is about runtime behaviour or what the live APIs return.**
- **Endpoint lists are [src] reads, not REA Evidence.** REA's graph does not record string literals: the `request` family is 0 for axios-based SDKs, and string/API seeds return `no-match`. The paths are exact bytes from the hashed packages but were read with `grep`, which cannot see paths built dynamically. Lists are therefore lower bounds.
- **`compare_javascript_export_shapes` returned `unknown` both times**:
  - Vonage NI v2 vs Sinch NumberLookup (`ev_861d714dad479ae088953a554f49e4bc04d9efb78a176e5103d74fec52e205e2`): left selector `missing` (the CJS module exports `default`; named exports live in `.d.ts`/ESM chunks); right selector `unavailable` (class export, no static return shape).
  - Vonage Verify2 `Channels` vs Vonage Messages `Channels` (`ev_b8fd0b59e80919d2dcd1c178fcc25b2dcf5bc3e6146a323dd3bc1409515220ac`): both `missing`, because esbuild's `__export` getter pattern leaves only `default` bound.
  - No shape claims are made. The channel lists above come from [src] enum literals.
- **Partial coverage**:
  - twilio, @vonage/messages, @vonage/voice, plivo, @sinch/conversation, bandwidth-sdk, messagebird and mailgun.js have partial application graphs.
  - Infobip's two bundles exceeded the semantic budget.
  - msg91's worker ran out of heap.
  - Telnyx's summary display exceeded MCP transport; the parent was recovered via `get_evidence_bundle` and traced successfully.
- **Infobip, MSG91 and MessageBird SDKs understate their vendors' products.** "—" in their columns means "not in this SDK", not "vendor lacks it".
- **Vonage spec count (88) is a sample**: number-insight-v2, application, video, users and redact are missing.
- **Twilio RCS** ("Y?") is an inference from `messaging/v2/channelsSender` naming; the RCS type was not read.
- **Relay "P" ratings** rely on the shared-context finding that WHATSAPP, EMAIL and VOICE are sandbox-only (`internal/connector/smpp.go:801` sends non-SMS submissions to `r.Fallback`).
- **Unofficial packages** (gupshup, exotel) say nothing about those vendors' real APIs.
- **Evidence durability.** IDs are session-scoped. The exported bundle is at `/private/tmp/claude-501/-Users-mohdsaeedafri-All-Code-Base-SMS-BE/f4bb4907-8866-4d5a-b883-2ec3daf01d36/scratchpad/rea-evidence-bundle.json` (758 MB, 64 records), and scratch is not durable. Re-running `analyze_javascript_application` on the same package versions reproduces the graph (the Vonage verify2 parent ID was identical across both sessions), although some parent IDs differed between sessions (Plivo).

## 8. Reproduce

```bash
R=<scratch>/rea; mkdir -p $R/<pkg> && cd $R/<pkg> && npm pack <pkg>@<version> && tar xzf *.tgz   # never npm install
# REA: analyze_javascript_application {input_path: $R/<pkg>/package, detail: "summary", format: "directory"}
# Specs:
curl -sfL https://api.github.com/repos/twilio/twilio-oai/contents/spec/json | jq -r '.[].download_url' | xargs -n1 curl -sfLO
curl -sfLO https://raw.githubusercontent.com/team-telnyx/openapi/master/openapi/spec3.json
curl -sfLO https://raw.githubusercontent.com/resend/resend-openapi/main/resend.json
python3 -I count_ops.py files twilio/*.json; python3 -I count_ops.py tags spec3.json
```
