# Backend request — Operator console: pagination, and two Audit gaps

**From:** Relay frontend (`sms-platform-frontend`)
**Date:** 26 August 2026
**Commits:** `2a95d7d`, `480aaec` (the Operator console redesign, ten screens)
**Verified against:** the live API at `https://sms-api.saqibsaeed.cloud` and
`github.com/saeedafri/SMS-BE@main`, 26 August 2026 — see Section 6 for the exact commands
**Contract status:** no change proposed yet — this asks for two additive changes, described below

---

## Section 0 — Read this first

The Operator console redesign is complete: all ten screens now use the same approved list
treatment — a **range summary** ("Showing 1 to 20 of 54 activities") beside a numbered pager, at
20 rows a page.

Two things are true of the operator list endpoints today, and the second one surprised us:

1. **No operator page schema carries `total`**, so the range summary has no denominator.
2. **Seven of the eight operator list endpoints ignore `cursor` and `limit` entirely**, even
   though the contract declares both. `GET /v1/operator/tenants?limit=1` returns all 19 rows.

So this is not "you page correctly and are missing one field." Server-side pagination is not
implemented on these endpoints at all. The frontend absorbs that today (Section 3), which is why
nothing is broken and nothing here is blocking.

### You have already built exactly what is being asked for

`GET /v1/messages` is a **correct, complete** implementation of the pattern — verified live:

```
GET /v1/messages?limit=2                    -> 2 rows, total=2231, nextCursor="MjAyNi0wOC0yM1..."
GET /v1/messages?limit=2&cursor=<that>      -> 2 DIFFERENT rows, total=2231
GET /v1/messages?limit=2&status=delivered   -> total=1818   <-- total tracks the filter
```

`GET /v1/contacts` does the same. This request is "extend that to the operator endpoints," not a
new pattern to design. `docs/BACKEND_DESIGN.md` already commits to it in writing: *"keyset
pagination — which the contract already mandates (opaque `nextCursor`). We encode
`(created_at, id)`, never `OFFSET`."*

### The contract still flows frontend → backend

`openapi.json` in this repo is the source of truth. If you accept the changes below, they land
there first and the frontend regenerates. **We have not made those edits** — the shapes below are
a proposal to agree or amend.

---

## Section 1 — Measured state of every operator list endpoint

Live responses, 26 August 2026, operator session. "rows" is what came back; the request asked for
`limit=5` where the endpoint declares `limit`.

| Endpoint | Declares `cursor`/`limit` | Honours `limit` | Returns `nextCursor` | Returns `total` | Rows live |
|---|---|---|---|---|---|
| `GET /v1/operator/tenants` | yes | **no** — `limit=1` → 19 rows | `null` always | **no** | 19 |
| `GET /v1/operator/approvals` | yes | **no** | `null` always | **no** | 7 |
| `GET /v1/operator/audit-log` | yes | **no** — `limit=1` → 10 rows | `null` always | **no** | 10 |
| `GET /v1/operator/support/tickets` | yes | **no** | `null` always | **no** | 3 |
| `GET /v1/operator/user-activity` | yes | **yes** | never set | **no** | 5 |
| `GET /v1/operator/abuse-queue` | no | n/a | n/a | **no** | 3 |
| `GET /v1/operator/routes` | no | n/a | n/a | **no** | 21 |
| `GET /v1/operator/rates` | no | n/a | n/a | **no** | 19 + overrides |

### 1.1 Confirmed in your source, not inferred from responses

Counting parameter reads and response fields per handler in `internal/api/operator.go@main`:

| Handler | reads `Params.Limit` | reads `Params.Cursor` | sets `NextCursor` | sets `Total` |
|---|---|---|---|---|
| `GetTenants` (L200) | – | – | – | – |
| `GetApprovalQueue` (L1091) | – | – | – | – |
| `GetAbuseQueue` (L1336) | – | – | – | – |
| `GetRateCard` (L482) | – | – | – | – |
| `GetRoutes` (L567) | – | – | – | – |
| `GetAuditLog` (L602) | – | – | – | – |
| `GetOperatorSupportTickets` (L1567) | – | – | – | – |
| `GetUserActivity` (L1915) | **yes** | – | – | – |

`GetUserActivity` is the one that reads `Limit`, passes it to `store.UserActivityFilter`, and then
returns `gen.GetUserActivity200JSONResponse{Entries: entries}` — no cursor, no total. It is the
closest to done and the cheapest to finish.

`nextCursor: null` in the JSON is the generated struct's zero value serialising, not a handler
deciding there is no next page. Worth knowing, because it means a client cannot distinguish
"there is no more data" from "paging is not implemented" — both look identical on the wire.

---

## Section 2 — The ask

### 2.1 Add `total` to five page schemas

```jsonc
"AuditLogPage": {
  "type": "object",
  "required": ["entries", "total", "nextCursor"],
  "properties": {
    "entries":    { "type": "array", "items": { "$ref": "#/components/schemas/AuditLogEntry" } },
    "total":      { "type": "integer", "description": "Rows matching the filter, ignoring cursor and limit." },
    "nextCursor": { "type": "string", "nullable": true }
  }
}
```

Same shape for `TenantPage`, `ApprovalQueuePage`, `UserActivityPage`, `SupportTicketPage` — and
`SuppressionPage`, which has the identical gap on the customer side (already raised as **D3** in
`BACKEND_REQUEST_design-system-v2.md`; this document supersedes that entry).

### 2.2 Actually honour `cursor` and `limit`

For the five endpoints that declare both. `GET /v1/messages` is the reference — same
`(created_at, id)` keyset encoding, same opaque base64 cursor.

### 2.3 The one thing to get right

`total` must be **the count after filters, before paging**. On
`/v1/operator/audit-log?range=30d&tenantId=X&cursor=Y&limit=20` it is the number of rows matching
`range` **and** `tenantId` — not the page size, not the table size.

Your `/v1/messages` already does this correctly (`total` fell from 2231 to 1818 under
`status=delivered`), so the pattern is established. A `total` that ignored filters would be worse
than none: the footer would read "Showing 1 to 20 of 4,182" on a filtered view holding 30 rows,
and an operator would reasonably conclude the filter was broken.

---

## Section 3 — What the frontend does today, and the trap in it

Every redesigned list needs three numbers for its footer: window start, window end, and grand
total. A keyset page supplies the first two; it cannot supply the third. So each RSC page
**exhausts the cursor** and counts what arrived:

```ts
// src/app/(admin)/admin/support/page.tsx
const ticketPage = await fetchAllPages<SupportTicket>(async (p) => {
  const page = await getOperatorSupportTickets({ tenantId, status, category, ...p });
  return { entries: page.tickets, nextCursor: page.nextCursor };
});
```

`fetchAllPages` (`src/lib/operator/fetch-all.ts`) follows `nextCursor` up to 25 pages × 500 rows,
then degrades honestly — the footer says "the first N tickets loaded" rather than claiming a total
nobody supplied.

### 3.1 Read this part before you ship paging

**Today this costs exactly one request per screen.** Because `nextCursor` is always `null`, the
loop runs once and gets the whole collection. The frontend is not currently paying a round-trip
penalty.

**The moment you implement `cursor`/`limit` without `total`, that becomes N round trips** — the
loop will start walking pages it did not have to walk before, to compute a number you could have
sent in one field. Implementing 2.2 without 2.1 would make our cost strictly worse than doing
nothing.

> **So: ship `total` in the same change as the cursor, or ship `total` first. Never the cursor
> alone.**

This is the single most actionable line in this document.

---

## Section 4 — Not requested

### 4.1 `AbuseQueuePage` and `RoutePage` — notice only

Neither declares paging and neither needs it yet: both are bounded by *configuration*, not by
customer activity. Routes grow when you add a corridor; the abuse queue is worked down by
operators as routine. Returning them whole is legitimate at this scale, and the frontend's
client-side paging over a complete response is correct for it — the total is real, because the
whole set genuinely arrived.

If either becomes unbounded (a per-tenant route table; an abuse queue running to thousands during
an incident) it needs paging first. The frontend is already shaped to consume it.

### 4.2 `RateCard` — no change

Two parallel arrays (`defaults`, `overrides`) rendered as two tables. Paginating it means two
cursors in one response or splitting the endpoint. Not worth it for 44 rows.

### 4.3 `GET /v1/conversations` — a smaller sibling gap, FYI

Returns `total` correctly but **ignores `limit`** (`limit=3` → 4 rows) and returns no
`nextCursor`. Not an operator screen and not part of this request, but it is the same class of
bug and worth folding into the same pass.

---

## Section 4A — Two more gaps found on the live API, unrelated to pagination

Both were found probing the deployment on 26 August 2026 while verifying the above. Neither is a
pagination issue; both affect the same Audit screen.

### 4A.1 `AuditLogEntry.detail` is empty on almost every row (**please fix**)

```
GET /v1/operator/audit-log?range=90d   ->  10 rows
                                            9 with detail == ""
                                            0 with targetLabel == ""
action=rate.default_update   detail=""   targetLabel="AE VOICE"
action=registration.approve  detail=""   targetLabel="US tcr_brand"
action=route.disable         detail=""   targetLabel="Jio Direct"
```

`store.RecordOperatorAction(ctx, pool, actor, action, tenantID, tenantName, targetLabel, detail)`
stores `NULLIF(detail,'')` — and most call sites in `internal/api/operator.go` pass `""`.

This matters more than it looks. `detail` is the **row header** of the Audit table — the most
prominent column on the screen, and the line an operator quotes into an incident write-up. Blank
on nine rows out of ten, the audit log stops being readable as a narrative: you can see that a
rate was updated, but not to what.

**The frontend has mitigated this**, so the screen is not broken today: the Detail column now
falls back to `targetLabel` when `detail` is empty (`auditDetailText` in
`src/app/(admin)/admin/audit/audit-view.tsx`, with tests). That is a fallback, not a fix — a
target label names *what* was touched, it does not describe *what changed*.

**The ask:** populate `detail` at the call sites with the sentence the field was designed for —
`"Changed the AE VOICE default rate from ₹0.42 to ₹0.45"` rather than `""`. The schema already
documents it and the column already exists. `tenant.suspend` is the one action that does populate
it today (`"Suspended Acme Retail"`), so the intended shape is already established in your own
code.

### 4A.2 The live API emits audit actions the contract does not declare

`AuditAction` in `openapi.json` has 19 values. `internal/api/operator.go` calls
`RecordOperatorAction` with at least four that are not among them:

| Emitted by the backend | In `AuditAction`? | Seen live? |
|---|---|---|
| `registration.approve` | **no** | **yes** — in the deployed audit log right now |
| `registration.reject` | **no** | not yet |
| `route.create` | **no** | not yet |
| `route.delete` | **no** | not yet |

Consequence: the Audit screen's **Action filter** is built exhaustively from the generated enum,
so `registration.approve` rows are visible in the table but **cannot be filtered for** — during a
compliance incident, which is exactly when someone would filter for them. The generated
TypeScript union is also simply wrong about what the API can return.

**The ask:** add the four values to `AuditAction` in `openapi.json`. Then we regenerate and the
filter picks them up with no further frontend change.

> A note on scope: `*.decided` (`sender.decided`, `template.decided`, `registration.decided`) and
> `tenant.status_changed` also appear as string literals in the backend, but they look like
> event-bus topics rather than audit actions and we did not trace every call site. If any of them
> does reach `operator_audit_log`, it needs adding too — you can settle that faster than we can.

---

## Section 5 — What changes on our side

Nothing to coordinate. Per endpoint:

1. You add `total` (and honour `cursor`/`limit`) and update `openapi.json`.
2. We run `pnpm types:api`.
3. That screen's page drops `fetchAllPages`, passes `cursor`/`limit` through, reads `total`.
4. `usePagedRows` (`src/ui/paged-list.tsx`) swaps its client slice for the server's page.

The rendered footer does not change — same copy, same numbers, same component. **Ship one
endpoint at a time**; an added field is not a breaking change, so nothing breaks in between.

---

## Section 6 — Verification

Reproduce the whole of Section 1:

```bash
BASE=https://sms-api.saqibsaeed.cloud
T=$(curl -s -X POST "$BASE/v1/operator/login" -H 'content-type: application/json' \
     -d '{"email":"ops@relay.internal","password":"relay-ops-dev"}' \
     | sed -n 's/.*"token":"\([^"]*\)".*/\1/p' | head -1)

# limit is ignored: every one of these returns the same 19 rows
for L in 1 3 5 1000; do
  curl -s -H "Authorization: Bearer $T" "$BASE/v1/operator/tenants?limit=$L" \
    | node -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>{const j=JSON.parse(d);
        console.log('limit=$L ->', j.tenants.length, 'rows, total=', j.total ?? 'ABSENT')})"
done
```

And the acceptance test for each endpoint you fix:

```bash
# 1. limit is honoured and a cursor is issued
curl -s -H "Authorization: Bearer $T" "$BASE/v1/operator/audit-log?range=90d&limit=5" \
  | jq '{rows:(.entries|length), total, nextCursor}'
# expect: rows == 5, total == full count (>5), nextCursor != null

# 2. the cursor walks — page 2 ids must differ from page 1
# 3. total tracks the filter
curl -s -H "Authorization: Bearer $T" \
  "$BASE/v1/operator/audit-log?range=90d&action=route.enable&limit=5" | jq '.total'
# expect: strictly less than step 1's total.
# Unchanged => total ignores filters, which is the bug this section exists to catch.

# 4. walking the cursor to exhaustion yields exactly `total` rows
```

### Done means

- `total` present and required on the five Section 2.1 schemas.
- `cursor`/`limit` honoured on the five that declare them.
- `total` proven filter-aware by step 3 on at least one filterable endpoint.
- Cursor exhaustion yields exactly `total` rows.
- `openapi.json` updated here so the frontend can regenerate.

---

## Section 7 — Priority

| Item | Priority | Why |
|---|---|---|
| `total` on `AuditLogPage`, `UserActivityPage`, `SupportTicketPage` | **Highest** | Genuinely unbounded — they grow with platform activity and never shrink. `GetUserActivity` already reads `Limit`, so it is the cheapest of the three. |
| `cursor`/`limit` on those same three | **High — but only alongside `total`** | See 3.1. Shipping paging without `total` makes the frontend's cost worse, not better. |
| `total` on `TenantPage`, `ApprovalQueuePage` | Medium | Grow with customer count and review throughput. Large, not unbounded per view. |
| `total` on `SuppressionPage` | Medium | Not an operator screen; same one-line fix, avoids a second pass. |
| **`AuditLogEntry.detail` empty on ~90% of rows** | **High** | The Audit table's row header, blank on nine rows in ten. Not pagination, same screen (4A.1). |
| **Four audit actions missing from `AuditAction`** | **High** | `registration.approve` is in the live log now and cannot be filtered for (4A.2). |
| `GET /v1/conversations` ignoring `limit` | Low | Same bug class (4.3). |
| `AbuseQueuePage`, `RoutePage` | Notice only | Configuration-bounded (4.1). |
| `RateCard` | None | Deliberately unchanged (4.2). |

---

## Appendix — provenance

Nothing above was typed from memory.

**Contract shapes** — read out of `openapi.json` at `480aaec`:

```bash
node -e "const s=require('./openapi.json').components.schemas;
  for (const n of ['TenantPage','ApprovalQueuePage','AbuseQueuePage','RoutePage','RateCard',
                   'AuditLogPage','UserActivityPage','SupportTicketPage','SuppressionPage',
                   'MessageLogPage','ContactPage','MessagePage','ConversationPage'])
    console.log(n, '->', Object.keys(s[n].properties||{}).join(','));"
```

```
MessageLogPage     -> messages,total,nextCursor,campaignName,journeyName   <-- the model
ContactPage        -> contacts,total,nextCursor
MessagePage        -> messages,total,nextCursor
ConversationPage   -> conversations,total,nextCursor
TenantPage         -> tenants,nextCursor            <-- no total
ApprovalQueuePage  -> items,nextCursor              <-- no total
AuditLogPage       -> entries,nextCursor            <-- no total
UserActivityPage   -> entries,nextCursor            <-- no total
SupportTicketPage  -> tickets,nextCursor            <-- no total
SuppressionPage    -> suppressions,nextCursor       <-- no total
AbuseQueuePage     -> items                         <-- no paging at all
RoutePage          -> routes                        <-- no paging at all
RateCard           -> defaults,overrides            <-- two arrays, deliberately unpaged
```

**Handler behaviour** — counted in `github.com/saeedafri/SMS-BE@main`:

```bash
git clone --depth 1 https://github.com/saeedafri/SMS-BE && cd SMS-BE
for h in GetTenants GetApprovalQueue GetAbuseQueue GetRateCard GetRoutes \
         GetAuditLog GetUserActivity GetOperatorSupportTickets; do
  n=$(grep -n "^func (s \*Server) $h(" internal/api/operator.go | cut -d: -f1)
  end=$(awk -v s="$n" 'NR>s && /^}/{print NR; exit}' internal/api/operator.go)
  body=$(sed -n "${n},${end}p" internal/api/operator.go)
  printf '%-28s Limit:%s Cursor:%s NextCursor:%s Total:%s\n' "$h" \
    "$(echo "$body"|grep -c 'Params.Limit')"  "$(echo "$body"|grep -c 'Params.Cursor')" \
    "$(echo "$body"|grep -c 'NextCursor')"    "$(echo "$body"|grep -c 'Total')"
done
```

**Frontend-side detail** per collection is in
`docs/redesign-operator-audit-support-security-report.md` §40–41 and
`docs/redesign-operator-report.md`, both carrying **BACKEND PAGINATION DEFERRED** against the
specific screens.
