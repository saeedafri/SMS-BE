# Operator console: Live campaigns (30 Sep 2026)

A new top-level nav item for the operator: **Live campaigns**. It shows every
tenant's campaigns and how each one is doing right now, narrows to one tenant
through a search-as-you-type box, and drills into a campaign down to each
number it was sent to and whether it was delivered.

Two new routes, one extended route, and two existing ones. All `GET`, all take
the operator bearer token. Missing token or a tenant token: **401**. Bad
parameter: **422** `{"error":{"code":"validation_failed","message":"…"}}`
naming it. Like the 25 Sep routes they are mounted directly and **not in
`openapi.json` yet**; §5 is what to add.

---

## 1. Tenant search box: `GET /v1/operator/tenants/suggest` (NEW)

| param | meaning |
|---|---|
| `q` | part of the tenant's name, any case, at most 100 chars; or a full tenant id. Empty = tenants A→Z |
| `limit` | 1–50, default 10 |

```json
{ "tenants": [
  { "id": "…", "name": "Acme Retail", "country": "IN", "status": "active" }
] }
```

- Names **starting with** `q` come first, then the rest alphabetically.
- `%` and `_` are searched literally.
- `status` is the standing the tenants page shows: `active | pending | suspended | throttled`.
- Debounce ~200 ms and fire on each keystroke; it's a single indexed query.
- Picking a tenant sets `tenantId` on §2 and §4.

## 2. Campaign list: `GET /v1/operator/campaigns` (EXTENDED)

Unchanged from 25 Sep §4.1 (same row shape, same filters: `tenantId`, `channel`,
`q`, `sender`, `senderId`, `from`/`to`, `page`, `limit`), with one change:

**`status` now takes several values, comma-separated.** The live tab is:

```
/v1/operator/campaigns?status=scheduled,queued,sending,paused
```

Single values still work. Any unknown value in the list gives 422.

Suggested tabs: **Live** (`scheduled,queued,sending,paused`) · **Finished**
(`sent,failed,cancelled`) · **All** (no `status`).

Each row already carries `messages: {total, queued, sent, delivered, read,
failed, rejected, costMinor}`, so the table can show a progress bar without a
second request.

## 3. Campaign overview: `GET /v1/operator/campaigns/{id}` (NEW)

The same object as a list row, plus:

```json
{ "...every campaign-row field...": "",
  "progress": { "expected": 3, "created": 3, "inFlight": 2, "settled": 1, "percent": 33.33 },
  "deliveryRate": 0,
  "byChannel": [ { "channel": "SMS", "messages": 3, "queued": 0, "sent": 2, "delivered": 0,
                   "read": 0, "failed": 1, "rejected": 0, "segments": 3,
                   "costMinorByCurrency": { "INR": 0 }, "deliveryRate": 0 } ],
  "failureReasons": [ { "status": "failed", "errorCode": "UNDELIV:001", "messages": 1 } ],
  "lastActivityAt": "2026-09-30T15:12:04.113Z" }
```

| field | meaning |
|---|---|
| `progress.expected` | messages the campaign will create: recipients less `withheld`, or what it created if more |
| `progress.created` | messages created so far |
| `progress.inFlight` | queued or with the carrier, no receipt yet |
| `progress.settled` | delivered + failed + rejected: nothing more will happen to these |
| `progress.percent` | settled ÷ expected, 2 dp, never above 100. `null` only when nothing is expected |
| `deliveryRate` | delivered ÷ (delivered + failed), 2 dp. `null` until something settles |
| `byChannel` | by the channel that **carried** it (an RCS campaign's SMS fallbacks show under SMS). Same fields as the summary route |
| `failureReasons` | failed and refused messages grouped by `status` + carrier/our `errorCode`, most common first, top 20. `errorCode` may be null |
| `lastActivityAt` | when any of its messages last changed. `null` before the first send |

`byChannel` and `failureReasons` are `[]`, never `null`. Unknown or malformed id: **404**.

**Live refresh:** poll this route every 5 s while `status` is `queued` or
`sending`, every 30 s for `scheduled`/`paused`, and stop for
`sent|failed|cancelled`. Poll the list (§2, Live tab) every 10 s.

## 4. Per-number detail (EXISTING, 25 Sep §4.3 / §4.4)

- `GET /v1/operator/messages?campaignId={id}`: one row per recipient: `to`,
  `status` (`queued|sent|delivered|read|failed|rejected`), `sentAt`,
  `deliveredAt`, `readAt`, `errorCode`, `carrier`, `renderedText` (the exact text
  sent), `templateName`, `dlr`. Filter with `status=` ("who did not get it"
  is `status=failed`) and `recipient=` (full number or last digits).
  With `campaignId` the date window defaults to 92 days, so every message is there.
- `GET /v1/operator/messages/{id}`: that row plus the `events` timeline.
- `GET /v1/operator/messages/export?campaignId={id}`: CSV.

## 5. Screen

1. **Header:** tenant search box (§1) with a clear ×. Tabs Live / Finished / All.
2. **Table** (§2): Tenant · Campaign · Channel · Status · Progress bar
   (`messages.total − queued − sent` of `recipients − withheld`) · Delivered ·
   Failed · Rejected · Started (`sendStartedAt`) · Created by.
3. **Row click → overview** (§3): progress bar, delivery rate, cards for
   Delivered / In flight / Failed / Rejected / Read, by-channel table, failure
   reasons table, "last activity 12 s ago".
4. **Below it the per-number table** (§4) with status filter chips and the
   recipient search. Row click opens the message drawer with the timeline.

## 6. Contract: please add to `openapi.json`

Operation ids `suggestOperatorTenants`, `getOperatorCampaign`; `status` on
`listOperatorCampaigns` becomes a comma-separated string (`style: form,
explode: false`, items the campaign statuses). Responses 200, 401, 422 (+404 on
the campaign). Shapes as in §1 and §3; every field always present.
