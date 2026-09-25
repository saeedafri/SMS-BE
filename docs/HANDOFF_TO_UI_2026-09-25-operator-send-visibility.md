# Operator console: who sent what, to whom, and what happened (25 Sep 2026)

The operator console can now answer, for any tenant:

- which **campaigns** and **journeys** a tenant ran, and **who in the tenant** created or started them
- every **message** (SMS, RCS, WhatsApp, Email, Voice): **to whom**, **when**, through which **sender/carrier**, and its outcome: **queued / sent / delivered / read / failed / rejected**
- **how many** went out, were delivered, read or failed: per campaign, per journey, per tenant, per channel, and platform-wide
- a **CSV export** of any filtered message list

Six new read-only `GET` routes under `/v1/operator`. They use the operator session token like every other console route. **They are not in `openapi.json` yet.** The backend serves them now (mounted directly, like `/v1/events`). §6 is the contract fragment to add, and the wire shapes won't change when you add it.

---

## 1. What changed underneath (so the numbers make sense)

| Before | Now |
|---|---|
| A campaign had no record of who created it | `campaigns.created_by_*`: user id, name and email, copied when the campaign is created, so the record survives the user being removed |
| A journey had no author | `journeys.created_by_*` **and** `activated_by_*`: whoever last activated or resumed it, i.e. whoever is answerable for what it is sending now |
| A single send (`POST /v1/messages`) had no sender | each message stores `sent_by_kind` (`user` or `api_key`) and `sent_by_id` |
| RCS `READ` was folded into delivered and then dropped | `read_at` is stored. The message stays `delivered` internally. The console shows it as **`read`** |

**Older data:** anything created before this deploy has `createdBy: null` / `sentBy: null`. Show **"Not recorded"**, not a blank. A blank reads like a system send, and these weren't.

## 2. How delivery is known (for the help text / tooltips)

- **SMS:** we ask the operator for a delivery receipt on every submit. The operator returns a `deliver_sm` receipt (`stat:DELIVRD` means the handset acknowledged it). That flips the message to `delivered`, and `deliveredAt` is **the time the handset got it**, not the time we heard. Any other `stat` means `failed`, with the operator's code in `errorCode`, e.g. `UNDELIV:001`.
- **RCS:** the carrier calls our webhook. `DELIVERED` means `delivered`. `READ` (sent by the handset itself) sets `readAt` and the status becomes `read`. `FAILED`/expiry means `failed`.
- **No receipt within 48 h:** the message is marked expired and shows as `failed` (state `expired`). The held money is refunded.
- **Money:** delivered is charged. Failed, expired and rejected cost 0 (the hold is released).

## 3. Status vocabulary

`status` on a message is what to show. `state` is the internal state, which is useful in a detail drawer.

| status | state(s) | meaning |
|---|---|---|
| `queued` | queued, submitting | not yet handed to a carrier |
| `sent` | submitted, accepted | carrier has it, no receipt yet |
| `delivered` | delivered | handset acknowledged it |
| `read` | delivered + `readAt` set | handset reported it opened (RCS only) |
| `failed` | undelivered, carrier_rejected, expired | carrier said no, or never answered |
| `rejected` | rejected | **we** refused it before sending (no wallet balance, DND, unapproved template…). `errorCode` says why |

In the `messages` counts on campaigns and journeys, **`read` is a subset of `delivered`**, not a separate bucket: `total = queued + sent + delivered + failed + rejected`.

## 4. The routes

All take the operator bearer token. A missing token or a tenant token gives **401**. A bad parameter gives **422** with `{"error":{"code":"validation_failed","message":"…"}}` naming the parameter. It is never silently ignored.

Common query parameters:

- `page` (≥1, default 1), `limit` (1–200)
- `from`, `to`: either `2026-09-25` (a whole IST day, and `to` is **inclusive**) or RFC 3339.

### 4.1 `GET /v1/operator/campaigns`

Filters: `tenantId`, `status` (`scheduled|queued|sending|paused|sent|failed|cancelled`), `channel` (`SMS|RCS|WHATSAPP|EMAIL|VOICE`, which also matches the fallback channel), `q` (searches campaign name **and creator name/email**), `from`/`to` (on `createdAt`), `page`, `limit`. Newest first.

```json
{
  "total": 42,
  "campaigns": [{
    "id": "…", "name": "Diwali offer",
    "tenantId": "…", "tenantName": "Acme Retail",
    "channel": "RCS", "fallbackChannel": "SMS", "country": "IN",
    "status": "sent",
    "sender": "ACMERT", "template": "Diwali 2026", "listName": "All customers",
    "recipients": 120000,
    "withheld": 36000,
    "estimatedCostMinorMin": 1200000, "estimatedCostMinorMax": 1440000, "currency": "INR",
    "retryOf": null,
    "createdBy": { "userId": "…", "name": "Priya Shah", "email": "priya@acme.in" },
    "createdAt": "…", "scheduledAt": null, "sendStartedAt": "…",
    "pausedAt": null, "cancelledAt": null,
    "messages": { "total": 84000, "queued": 0, "sent": 1200, "delivered": 80100,
                  "read": 51230, "failed": 2500, "rejected": 200, "costMinor": 961200 }
  }]
}
```

`withheld` is the recipients the tenant's send cap held back (see the 24 Sep handoff). `messages.costMinor` is what was actually charged. The estimate is what the user approved.

### 4.2 `GET /v1/operator/journeys`

Filters: `tenantId`, `status` (`draft|active|paused|archived`), `channel` (any send step on it), `q`, `from`/`to`, `page`, `limit`.

```json
{
  "total": 3,
  "journeys": [{
    "id": "…", "name": "Welcome series", "tenantId": "…", "tenantName": "Acme Retail",
    "status": "active", "triggerType": "list_entry", "listName": "New sign-ups",
    "channels": ["RCS", "SMS"],
    "recipients": 0, "enrolled": 5120, "withheld": 0,
    "createdBy":   { "userId": "…", "name": "…", "email": "…" },
    "activatedBy": { "userId": "…", "name": "…", "email": "…" },
    "createdAt": "…", "activatedAt": "…",
    "messages": { "total": 9800, "queued": 0, "sent": 12, "delivered": 9500,
                  "read": 0, "failed": 250, "rejected": 38, "costMinor": 114000 }
  }]
}
```

### 4.3 `GET /v1/operator/messages`

The message log across tenants. Filters:

| param | meaning |
|---|---|
| `tenantId`, `campaignId`, `journeyId` | narrow to one |
| `channel` | the channel asked for **or** the one that carried it, so an RCS campaign's SMS fallbacks show under SMS |
| `status` | `queued|sent|delivered|read|failed|rejected` |
| `source` | `campaign|journey|api` (`api` = a single send from the dashboard or the API) |
| `recipient` | exact phone/email, **or the last digits** of a number (operators are usually given those) |
| `from`, `to` | on `createdAt`. **Default: the last 7 days.** With `campaignId`/`journeyId` the default is the last 92 days, so a campaign's messages are all there. **At most 92 days apart**, otherwise 422 |

```json
{
  "total": 3, "from": "…", "to": "…",
  "messages": [{
    "id": "…", "tenantId": "…", "tenantName": "Acme Retail",
    "source": "campaign",
    "campaignId": "…", "campaignName": "Diwali offer", "journeyId": null, "journeyName": null,
    "channel": "RCS", "deliveredChannel": "SMS",
    "country": "IN", "sender": "ACMERT", "templateId": "…",
    "to": "+919876543210", "email": null,
    "status": "delivered", "state": "delivered",
    "errorCode": null, "errorClass": null,
    "segments": 1, "costMinor": 12, "currency": "INR",
    "carrier": "JIO", "routeId": "…", "carrierRef": "4471902",
    "sentBy": { "kind": "user", "via": "campaign", "id": "…",
                "name": "Priya Shah", "email": "priya@acme.in", "keyPrefix": null },
    "createdAt": "…", "sentAt": "…", "deliveredAt": "…", "readAt": null, "updatedAt": "…"
  }]
}
```

`sentBy`:
- `via: "campaign"`: the campaign's creator
- `via: "journey"`: whoever last activated the journey
- `via: "direct"`, `kind: "user"`: a dashboard user
- `via: "direct"`, `kind: "api_key"`: an API key. `name` is the key's name, `keyPrefix` e.g. `sk_live_ab12`, and `email` is null
- `null`: not recorded (older than this deploy)

### 4.4 `GET /v1/operator/messages/{id}`

Same object as a list row, plus the **timeline**:

```json
{ "...every field above...": "",
  "events": [
    { "from": "",        "to": "queued",    "errorCode": null, "detail": "",     "occurredAt": "…" },
    { "from": "queued",  "to": "accepted",  "errorCode": null, "detail": "",     "occurredAt": "…" },
    { "from": "accepted","to": "delivered", "errorCode": null, "detail": "",     "occurredAt": "…" },
    { "from": "delivered","to": "delivered","errorCode": null, "detail": "read", "occurredAt": "…" }
  ] }
```

Render `detail: "read"` as "Read". The timeline is kept for **30 days**. After that `events` is `[]` but the message itself is still there, according to the tenant's retention setting (30–365 days). Say "Timeline no longer kept" rather than showing an empty box. Unknown id: 404.

### 4.5 `GET /v1/operator/messages/summary`

Same filters as 4.3. Grouped totals for a dashboard:

```json
{
  "from": "…", "to": "…",
  "totals": { "messages": 84000, "queued": 0, "sent": 1200, "delivered": 80100, "read": 51230,
              "failed": 2500, "rejected": 200, "segments": 91000,
              "costMinorByCurrency": { "INR": 961200 },
              "deliveryRate": 96.97 },
  "byChannel": [ { "channel": "RCS", "...same fields as totals": "" } ],
  "byTenant":  [ { "tenantId": "…", "tenantName": "Acme Retail", "...same fields": "" } ]
}
```

`deliveryRate` = delivered ÷ (delivered + failed), as a percentage to 2 dp. In-flight messages and our own refusals are excluded because they say nothing about delivery. It's `null` when nothing has settled. `byChannel` groups by the channel that **carried** the message. Both lists are biggest first.

### 4.6 `GET /v1/operator/messages/export`

Same filters as 4.3. Returns `text/csv` with columns:

`createdAt, tenantName, tenantId, source, campaignName, journeyName, channel, deliveredChannel, sender, to, email, status, errorCode, sentAt, deliveredAt, readAt, segments, costMinor, currency, carrier, sentByName, sentByEmail, sentByKeyPrefix, messageId`

The export is capped at **100,000 rows**. The response header `X-Export-Truncated: true` means the cap was hit. Tell the operator to narrow the dates rather than presenting it as complete.

## 5. Screens to build

1. **Sends → Campaigns** table: Tenant · Campaign · Channel (+fallback) · Created by · Created/started · Status · Recipients · Delivered · Read · Failed · Rejected · Withheld · Cost. Filters: tenant picker, status, channel, date range, search box. Row click opens the **campaign messages** view (4.3 with `campaignId`).
2. **Sends → Journeys** table: same idea, with Created by **and** Activated by, channels as chips, Enrolled.
3. **Sends → Messages** log: Time · Tenant · Source (campaign/journey name or "Direct") · Channel · To · Status chip · Sent by · Delivered at · Read at · Cost. Filters: every param in 4.3, and a recipient search box ("last digits OK"). Row click opens a **drawer** (4.4) with the timeline and carrier details (`carrier`, `carrierRef`, `errorCode`).
4. **Overview cards** from 4.5: Messages, Delivered, Read, Failed, Rejected, Delivery rate, Spend. Plus a by-channel and a by-tenant table.
5. **Export** button on the messages log. It downloads 4.6 with the current filters and warns on truncation.
6. A tenant's detail page can reuse 1 and 3 with `tenantId` fixed.

## 6. Contract: please add to `openapi.json`

Operation ids: `listOperatorCampaigns`, `listOperatorJourneys`, `listOperatorMessages`, `getOperatorMessage`, `getOperatorMessageSummary`, `exportOperatorMessages`. Schemas: the shapes in §4 verbatim (every field is always present, and the nullable ones are marked `null` in the examples). Responses: 200, 401, 422 (+404 on the single message).

Once they're in the contract, tell us. The backend moves them behind the generated interface with no change on the wire, and the contract tests start validating every response.

## 7. Not included, and why

- **`campaign.create` / `journey.activate` in the user-activity log.** `UserActivityEventType` is a closed enum in the contract, so emitting new values would break your parser. The author is on the campaign/journey row itself (§4.1/4.2), so nothing is lost. If you want them in the audit timeline too, add the three values to the enum and we'll emit them.
- **Tenant-facing `read`.** The tenant message log (`GET /v1/messages`) still shows `delivered` for a read message. `MessageStatus` already declares `read`. Say if the tenant dashboard should show it too; it's a small change.
- **Audit entry for exports.** `AuditAction` is also a closed enum. Add `messages.export` if you want exports of phone numbers audited. We recommend it.
