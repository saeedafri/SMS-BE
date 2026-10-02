# Features Relay was missing against Sigmo: built, deployed, screens needed (2 Oct 2026, updated with 11 and 12)

We compared Relay page by page with Sigmo (the Trustsignal portal) and built what we lacked.
**Live on `953ecfd`** (`/healthz` `commit`), API restarted 10:42:49 IST, migrations 69-76 applied.
None of the new routes is in `openapi.json`: they are mounted beside the contract, so there is no
generated client for them. Add them to the contract from the shapes below, or call them directly.
Every route is session-only (no API key), JSON, and uses Relay's `{"error":{"code","message"}}` envelope.
Roles: owner and admin write, member reads, **viewer** reads only (see 6).

**What "tested" means here, plainly.** Each feature has tests written red-then-green on the server
against the real Postgres, ClickHouse and Redis, and the full suite is green. On the **live API** we
re-ran the validation and read paths, and the whole short-link flow (create, tap, 302, click counted,
unknown 404). We did **not** send real SMS on live to prove frequency caps, drip, fallback, chatbots or
the vendor webhook formats: the live carrier is real, and we won't text a stranger. Those are proven
by tests only. Each section says which.

---

## 1. Frequency cap (per person)
How many messages one handset may receive per channel per day, week and month. Stops over-messaging
before people opt out.

- `GET /v1/frequency-caps` -> `{"caps":[{"channel","dailyLimit","weeklyLimit","monthlyLimit","excludedNumbers"}]}`
  (a channel with no cap is absent).
- `PUT /v1/frequency-caps/{channel}` (SMS, RCS, WHATSAPP, VOICE; not EMAIL). Body
  `{"dailyLimit":3,"weeklyLimit":10,"monthlyLimit":null,"excludedNumbers":["+919810000001"]}`.
  Each limit 1-100000 or `null`; daily <= weekly <= monthly else 422; at most 1000 exclusions, normalised to
  E.164, de-duplicated. An empty body clears the cap. Owner/admin only (403 for member).
- A capped message is **refused**, costs nothing, and is logged with `errorCode` **`frequency_cap`**.
  Please add `frequency_cap` to `MessageRefusalCode` and a plain-English line for it.
- Days are the tenant's own calendar day. If Redis is down the cap steps aside rather than stopping sends.

**Screen:** Settings > "Message limits". One card per channel: three number inputs (blank = no limit), an
"excluded numbers" tag input, Save. Show the 422 text inline.
**Proof:** tests (daily, weekly, exclusion, next-day reset, Redis down). Live: put/get/validation only.

## 2. Short links with click tracking
- `POST /v1/links` `{"destination","campaignId"?,"recipient"?,"label"?,"appendClickId"?,"expiresAt"?}` -> 201
  `{"code","shortUrl","destination",...}`. `shortUrl` is `https://<api host>/l/<code>`.
- `POST /v1/links/bulk` `{"destination","recipients":[...up to 1000],...}` -> `{"links":[...]}` one code per
  distinct recipient, so each person gets a trackable link.
- `GET /v1/links?campaignId&page&limit` -> `{"links":[{...,"clicks","humanClicks"}],"total"}`.
- `GET /v1/links/clicks?campaignId&code&from&to&includeBots=true&page&limit` -> `{"clicks":[{"clickedAt","isBot","device","os","recipient","campaignId","destination"}],"total"}`.
  Bots are hidden unless `includeBots=true`.
- `GET /v1/links/stats?campaignId&from&to` -> `{"links","clicks","humanClicks","botClicks","uniqueClickers","byDevice":{},"byDay":[{"day","clicks"}]}`.
- `GET /l/{code}` is public: 302 to the destination (with `click_id=<code>` if `appendClickId`), 404 unknown,
  410 expired. `HEAD` never counts. Link-preview fetchers (WhatsApp, Telegram, curl...) are recorded but
  flagged `isBot`.
- Destinations must be http(s), no credentials, not Relay itself, and pass the country's URL rule (India
  refuses public shorteners).
- **India caveat for customers:** DLT needs link domains whitelisted. Our `/l/` host must be whitelisted on
  the customer's DLT account before a body containing it will be accepted by the operator. Worth a hint in the UI.

**Screens:** Campaign detail > "Links" tab (list with clicks, then a click log table with a "show bots"
toggle and a device/day chart); a "Create tracked link" dialog on the campaign wizard message step.
**Proof:** tests plus **live** end to end (create, 302 with click_id, click recorded, 404, bad scheme 422).

## 3. Error stats and latency stats
- `GET /v1/analytics/errors?range=24h|7d|30d|90d&channel&country` ->
  `{"messages","failed","failureRate","errors":[{"channel","code","class","count","shareOfFailures"}]}`.
- `GET /v1/analytics/latency?range&channel&country` ->
  `{"delivered","byCarrier":[{"channel","carrier","delivered","p50Ms","p90Ms","p99Ms","avgMs"}],"histogram":[{"label","count"}]}`
  (five fixed buckets: under 5s, 5-30s, 30s-1m, 1-5m, over 5m). Delivered messages only. Default range 7d.

**Screens:** Analytics gets "Errors" and "Latency" tabs: a ranked bar list of codes with share, and a
carrier table plus histogram.
**Proof:** tests, and **live** (the demo tenant returns real data: e.g. 93% of its failures are `EXPIRED`).

## 4. Drip sending
Send a campaign in instalments: N recipients, wait M minutes, repeat.
- `POST /v1/campaigns` now accepts two extra body fields: **`dripBatchSize`** (1-100000) and
  **`dripIntervalMinutes`** (1-1440). Both or neither, else 422. They are read off the body today; once you
  add them to `CampaignCreate` nothing on our side changes.
- `GET /v1/campaigns/{id}/drip` -> `{"dripping","batchSize","intervalMinutes","nextBatchAt"}`.
- `PUT /v1/campaigns/{id}/drip` `{"batchSize","intervalMinutes"}` (or `{}` to clear). Only on a **scheduled**
  campaign that has not started, else 409.
- Between instalments the campaign's status is **`scheduled`** with `scheduledAt` = the next instalment;
  `nextBatchAt` repeats it. Pause and cancel work between and during instalments; a resume carries on, it does
  not start over. A list that ends exactly on an instalment boundary finishes instead of parking.

**Screen:** Campaign wizard "Schedule" step: a "Send in batches" switch with two inputs ("Send [500] every
[10] minutes"). Campaign detail: a line "Batch 2 of ~6, next at 14:10" from `nextBatchAt`.
**Proof:** tests (instalments, boundary, pause). Live: validation only (needs a real send).

## 5. Test send
`POST /v1/messages/test` `{"senderId","templateId"?,"body"?,"variables"?,"recipients":["9810000001", ...1-5]}`
-> 202 `{"results":[{"recipient","messageId","status","failureCode"}]}`. A real, billed send through the real
gate, so a passing test means the campaign will pass; a refusal says why. At most five distinct numbers.
**Screen:** a "Send test" button on the wizard review step with a 1-5 number input and per-number result chips.
**Proof:** tests. Live: validation only.

## 6. Viewer role
`viewer` is a fourth team role (`TeamRole` needs `viewer`): reads everything its tenant can, and every write
is refused with 403 `forbidden` ("View-only members cannot make changes."), except its own sign-in
(logout, password, two-step, ending its own sessions). Invite and role-change accept `viewer`.
**Screen:** add "View only" to the invite and role dropdowns; hide or disable write controls for a viewer
(the API is the guarantee, the UI is courtesy). **Proof:** tests, covering ten write routes.

## 7. Native webhook formats (WebEngage, MoEngage, CleverTap)
An ordinary endpoint created through `/v1/developer/webhooks` can declare it is a customer's account at one
of these. Relay then posts each event in that product's own event shape, signed as usual.
- `GET /v1/developer/webhooks/{id}/integration` -> `{"type":"default|webengage|moengage|clevertap","headerNames":[...],"supported":[...]}`.
- `PUT /v1/developer/webhooks/{id}/integration` `{"type":"clevertap","headers":{"X-CleverTap-Account-Id":"...","X-CleverTap-Passcode":"..."}}`;
  `{"type":"default"}` turns it off. Vendor types need 1-10 headers (the customer's API key). **Header values
  are never returned**, only names. Relay's own headers (`X-Relay-*`, `Content-Type`, `Host`...) cannot be
  overridden; values are single-line, up to 2000 characters. Owner/admin only.
- Event names become "Relay Message Delivered" etc.; nested data is flattened (`a_b`), nulls dropped, and
  the person is the phone number. The delivery log keeps Relay's own payload, so a resend re-formats.
- The customer still pastes their vendor's URL into the endpoint (https only).

**Screen:** on a webhook endpoint, an "Integration" dropdown (Default / WebEngage / MoEngage / CleverTap) that
reveals header inputs (password-style, write-only, show existing header **names** as "set").
**Proof:** unit tests of each vendor shape, an end-to-end test that the endpoint receives CleverTap's body
and the customer's headers, and that "default" restores Relay's envelope. Not run against the real vendors.

## 8. Fallback after a failed send
Not an API change. A campaign with a fallback leg (e.g. RCS then SMS) used to choose the leg **before**
sending. Now, when the carrier reports a **definite failure** on the first leg, the fallback leg sends that
person too, through the same gate, consent, suppression and wallet rules, **once**. Not on `expired` (silence),
not for a paused or cancelled campaign, not for the fallback's own failure. The fallback message is filed under
the same campaign with `deliveredChannel` = the fallback channel.
**Screen:** nothing new; the existing "delivered via SMS (fallback)" display just starts showing more rows.
**Proof:** tests (once only, consent, suppression, cancelled, no third hop). Live: not run (needs a real failure).

## 9. Keyword chatbots
A customer texts a keyword and gets an automatic reply.
- `GET /v1/chatbots` -> `{"chatbots":[{"id","name","channel","senderId","templateId","replyBody","active","keywords","createdAt"}]}`.
- `POST /v1/chatbots` `{"name","channel":"SMS|RCS|WHATSAPP","senderId","templateId"?,"replyBody"?,"keywords":[...1-20],"active"?}` -> 201.
  Keywords are lower-cased and de-duplicated; 1-40 characters. `GET/PATCH/DELETE /v1/chatbots/{id}`
  (PATCH cannot change `channel`). Owner/admin write.
- 409 if another flow on that channel already answers a keyword. STOP-class words (STOP, UNSUBSCRIBE, CANCEL,
  END, QUIT, OPTOUT) are reserved: 422.
- The sender must be approved and on that channel. **India (and any regime that requires registered
  templates) needs a `templateId`**, and the template must have **no variables**: a keyword reply has nothing
  to fill them with. Both are 422 at creation.
- Behaviour: one reply per person per flow per minute (a loop guard); a STOP never gets a reply; the reply is
  also filed in the inbox thread as an outbound message. Inbound SMS and RCS are wired. WhatsApp inbound is not
  wired yet (see below).

**Screens:** Chatbots list and create/edit form (name, channel, keywords chips, sender, template picker filtered
to variable-free approved ones, active switch).
**Proof:** tests (reply once, case/space, STOP, other channel, paused, all validation). Live: list and the
validation refusals only (the demo tenant has no variable-free template).

## 10. Small hardening you will notice
- Request bodies over **1 MB** (32 MB for contact import) now answer **413** `payload_too_large`.
- A query value outside a contract enum (`?status=bogus`, `?range=1y`, `?channel=FAX`) now answers **422**
  `... must be one of: ...` instead of 200 with an empty page. A route that already refused with its own text
  keeps its text.
- Contract drift to fix on your side: `ApprovalType` (operator approvals `type`) lacks **`rcs_agent`**, which
  the server accepts and the console uses; we allowed it explicitly.

## 11. Report export to the customer's own S3 bucket
Scheduled reports are still emailed. When a destination is set, each send also writes a CSV to the bucket.
- `GET /v1/report-export/s3` -> `{"configured","bucket","region","prefix","accessKeyIdLast4","lastStatus":"ok|failed|null","lastError","lastAt"}`.
- `PUT /v1/report-export/s3` `{"bucket","region","prefix"?,"accessKeyId","secretAccessKey"}`. Validated as a real
  S3 bucket name, region like `ap-south-1`, no `..` in the prefix. The secret is sealed and **never returned**; only the
  last four characters of the key id come back. Owner/admin only. `DELETE` removes it.
- `POST /v1/report-export/s3/test` writes one small object and returns `{"ok":true,"key"}` or `{"ok":false,"error":"... AccessDenied"}`.
  Use it on a "Test connection" button so a customer finds a bad IAM policy before the first report is due.
- Files land at `<prefix>/<daily|weekly|monthly>/<date>-<id>.csv`: a totals row, then one row per day.
- A failing bucket never stops the email; the failure shows in `lastStatus`/`lastError`.
**Screen:** Analytics > Scheduled reports > "Also save to S3": five fields, Save, Test connection, and a status line.
**Proof:** the request signer matches AWS's own published example signature, so it is correct, not just
self-consistent; tests cover upload, signing, a refusing bucket, validation and roles. **Not run against a real bucket.**

## 12. White-label branding
- `GET /v1/branding` -> `{"configured","displayName","logoUrl","primaryColor","secondaryColor","supportEmail","customDomain","domainVerified","dnsRecord"?}`.
- `PUT /v1/branding` with any of those fields (logo must be `https`, colours `#RRGGBB`, support email a bare address,
  domain a host name). `DELETE` clears it. Owner/admin write.
- A custom domain is stored immediately but **not served** until the owner proves control: the response carries
  `dnsRecord` (`TXT _relay-verify.<domain> = relay-verify=<token>`); after they publish it, `POST /v1/branding/verify-domain`
  checks DNS and flips `domainVerified`. Changing the domain drops the proof. One account per domain (409 otherwise).
- `GET /v1/branding/public?host=login.acme.com` is **unauthenticated**: for a verified domain it returns the name, logo, colours and support
  email so a custom sign-in page can theme itself; every other host, verified or not, is the same 404.
**Scope, plainly:** this stores and serves the branding and proves domain ownership. It does **not** point DNS at us or
issue a certificate for the custom domain, and does not change the sender of Relay's emails. Those are infrastructure
work (a CNAME to the app, a TLS certificate per domain) that the UI deployment would own.
**Screens:** Settings > "Branding": name, logo URL, two colour pickers, support email, a "Custom domain" field with the DNS
instructions and a "Verify" button. The sign-in page should call `/v1/branding/public?host=<its own host>` on load.
**Proof:** tests, including that an unverified or changed domain is never served and that the public lookup leaks no token. Not exercised live yet.

---

## Still missing against Sigmo (not built; do not build screens yet)
| Gap | Why not |
|---|---|
| Real WhatsApp sending, WhatsApp chatbots and QR links | A direct Meta Cloud API connector also needs per-template creation and approval sync, and we have no Meta credentials to test against. We would rather not ship a sender we cannot prove. A BSP such as Trustsignal may be the shorter route; we will look once we can read its WhatsApp API docs. |
| Sub-accounts / resellers | Moving credit between a parent and a child must be one atomic ledger operation across two tenants, and the wallet ledger is append-only and per tenant today. It also needs your decisions: who is invoiced, how child pricing works, whether a parent may sign in as a child. |

## What we need back
1. Screens for sections 1-9, 11 and 12 as above (priority: 2 links, 4 drip, 1 frequency cap, 9 chatbots, 3 stats, 12 branding, 11 S3).
2. `frequency_cap` in the refusal vocabulary, `viewer` in `TeamRole`, `rcs_agent` in `ApprovalType`.
3. Tell us if you want any of these moved into `openapi.json` by us instead.
