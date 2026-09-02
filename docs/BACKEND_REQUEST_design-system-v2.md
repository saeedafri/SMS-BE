# Backend request — the `redesign/design-system-v2` branch

**From:** Relay frontend (`sms-platform-frontend`)
**Date:** 25 August 2026
**Branch:** `redesign/design-system-v2` (7 commits, 730 files, ahead of `master`)
**Contract status:** merged in this repo's `openapi.json`; types regenerated (`pnpm types:api`)

---

## Section 0 — Read this first

This branch is a **presentation migration**. Nearly all of its 730 changed files are UI: the
whole app moved onto the Relay design-system primitives. It deliberately did **not** rewrite
domain logic, endpoints or payload shapes.

So the backend surface it needs is small. **The entire contract delta is 67 lines of
`openapi.json`, and it is three items** — one new endpoint, two new response fields, and one
hygiene fix.

Everything else in this document is Section 3: gaps the redesign *found and wrote down instead
of inventing around*. None of Section 3 is required to ship this branch.

### The contract still flows frontend → backend

Your `openapi/control.json` is a symlink to this repo's `openapi.json`, and your
`oapi-codegen.yaml` says to regenerate whenever it changes. That is still how this works:

```bash
# in the backend repo, after pulling this branch's openapi.json
make generate
```

`make generate` gives you the new route and the new struct fields. It does **not** give you the
two fields' *values* — item A below is the only real computation in this request.

### Summary

| # | Item | Kind | Blocking? | Apparent state on `sms-api.saqibsaeed.cloud` |
|---|---|---|---|---|
| **A** | `messageCount` on `UsageByCampaign` and `UsageByJourney` | New required response fields | **Yes** | Unverified — please run §4.2 |
| **B** | `POST /v1/automation/journeys/{id}/unarchive` | New endpoint | **Yes** | **Route appears to already exist** — please confirm behaviour, §4.1 |
| **C** | Duplicate `callerIdNumber` key in `Sender` | Spec hygiene | No | Semantically a no-op; re-generate to be safe |
| **D** | Six contract gaps the redesign documented | Requests, not requirements | No | Not built anywhere |

---

## Section 1 — Item A: `messageCount` on campaign and journey spend rows (**blocking**)

### 1.1 What changed

Two schemas gained one required integer each:

```jsonc
// components.schemas.UsageByCampaign
"required": ["campaignId", "campaignName", "channel", "currency", "messageCount", "amountMinor"],
"properties": {
  "messageCount": { "type": "integer" }   // NEW, required
}

// components.schemas.UsageByJourney
"required": ["journeyId", "journeyName", "channel", "currency", "messageCount", "amountMinor"],
"properties": {
  "messageCount": { "type": "integer" }   // NEW, required
}
```

Served by `GET /v1/billing/usage` (schema `UsageReport`, arrays `byCampaign` and `byJourney`).
`UsageByChannel.messageCount` already existed and is unchanged — this makes the three groupings
consistent.

### 1.2 Why

The redesigned usage screen (`/billing/usage`) renders all three breakdowns as one table shape:
**Name · Channel · Messages · Spend · Share**. Before this change the by-channel table could
state volume and the by-campaign / by-journey tables could not, so the same screen had two
different column sets for no reason a customer could see.

We are asking the backend for it rather than deriving it client-side on purpose: the client
would have to fetch every campaign's message log and re-count, which is a large amount of I/O to
recompute a number the ledger already knows.

### 1.3 Definition of "billable", exactly

**Same source that already backs `UsageByChannel.messageCount`.** Concretely, from our reference
implementation: a message counts when its status is `delivered` **or** `read`. Nothing else
counts — not `queued`, not `sent`, not `failed`.

The count is scoped to the request's `range` window (`AnalyticsRange`: `7d` / `30d` / `90d`) and,
when the optional `currency` is supplied, to that currency — identical scoping to `amountMinor`.
Note the spec declares no default for `range`; our mock treats an absent `range` as `30d`, and we
assume you already do the same since this parameter is not new.

### 1.4 The one real trap: do not sum per charge row

This is the part worth reading twice.

- A **campaign** settles exactly **one** ledger charge row.
- A **journey** settles **one charge row per settled send step**, so an active journey routinely
  has several.

`messageCount` is a property of the **campaign or journey**, not of a charge row. If you
accumulate it row-by-row the way `amountMinor` is accumulated, a journey's volume gets multiplied
by its step count — a silent overcount with no error to catch it.

Our reference implementation (`src/mocks/handlers.ts`, `buildUsageReport`) handles this two ways,
both of which are the intended semantics:

- On the **row**: `messageCount` is **set**, never summed. `amountMinor` on the same row **is**
  summed across that entity's charge rows.
- On the **channel aggregate**: a journey's volume is added once, guarded by a
  `journeyMessageCounted` set, while its `amountMinor` is added for every charge row.

The spec text records this: *"Counted once per journey even when the journey has several settled
send steps."*

Put plainly, per range and currency:

```
row.amountMinor   = SUM(charge.amountMinor)  over that entity's charge rows
row.messageCount  = COUNT(messages WHERE status IN ('delivered','read'))  for that entity
```

### 1.5 One ambiguity we want you to settle deliberately

`UsageByCampaign` carries both `campaignId` and `currency`, which leaves open whether a campaign
that somehow charged in two currencies is **one row or two**.

Our reference mock is inconsistent about this and we are not treating it as authoritative:
`byCampaignMap` is keyed by campaign id alone (so a second currency overwrites), while
`byJourneyMap` is keyed by `journeyId|currency` (so it splits). It has never mattered because
every seeded sender is India-only.

**Our recommendation: key both by `entityId|currency`,** i.e. split into one row per currency,
matching the journey behaviour and `UsageByChannel`'s existing `channel|currency` grouping. It
never loses money to an overwrite. Tell us which you implement and we will align the mock and add
the assertion — we would rather match you than guess.

---

## Section 2 — Item B: journey unarchive (**blocking, but likely already done**)

### 2.1 The endpoint

```
POST /v1/automation/journeys/{id}/unarchive     operationId: unarchiveJourney
```

Full behavioural spec — state machine, reference implementation, required test cases, curl
sequence — is already written up in **`docs/api-contract/BACKEND_REQUEST_journey-unarchive.md`**
(sections 3–7). That document stands unchanged; this one does not repeat it.

The one rule worth restating here, because it is the easy thing to get wrong: unarchiving is
**two** transitions, not one. A journey with `activatedAt IS NULL` returns to `draft`; one with
`activatedAt` set returns to `paused`. **Never** to `active` — sending must never resume without
an explicit resume. And `activatedAt` itself is never modified.

### 2.2 It looks like you have already shipped this

Our own docs still say the deployed backend 404s this route. **That appears to be out of date.**
Probing the live API unauthenticated today:

```
POST /v1/definitely-not-a-real-route                         -> 404
POST /v1/automation/journeys/{id}/nonsense                   -> 404
POST /v1/automation/journeys/{id}/archive                    -> 401   (known-good route)
POST /v1/automation/journeys/{id}/unarchive                  -> 401   <-- exists
```

Unknown paths 404 and real paths 401, so the 401 on `unarchive` says the route is registered.

**What we need from you: confirmation of behaviour, not implementation.** We could not
authenticate from this environment to test the transition itself. Please run §4.1 and paste the
real output. If it is in and correct, we will mark the earlier request doc closed and drop the
mock-only caveat from our Automation notes.

---

## Section 3 — Item C: a duplicate JSON key in `Sender` (hygiene, non-blocking)

`master`'s `openapi.json` declared `callerIdNumber` **twice inside the same properties object**,
in both `Sender` and the operator sender item — and the two declarations disagreed:

```jsonc
// first occurrence
"callerIdNumber": { "type": "string",
  "description": "…Required for Voice senders; the operator verifies ownership…" }

// second occurrence, same object
"callerIdNumber": { "type": "string", "nullable": true,
  "description": "Verified outbound caller-ID number, e.g. +14155550100 (Voice senders only)" }
```

This branch deletes the first of each. **The authoritative definition is the nullable one**, and
almost certainly always was for you: `JSON.parse` keeps the last duplicate, which is what any
standard OpenAPI toolchain — including `oapi-codegen` — would have seen.

So this should be a no-op. It is flagged only because a parser that took the *first* key would
have generated a non-nullable field, and that is worth ruling out in one grep on your side.
Nothing about Voice sender behaviour changes; the field itself, added 7 August, is unchanged.

> Related, frontend-side only, no action needed: our committed `src/lib/api-types.ts` had drifted
> stale and was missing `callerIdNumber` from the create-sender request body. Regenerating on this
> branch fixed it. The contract always had it and you already implement it.

---

## Section 4 — Item D: gaps we found and deliberately did not paper over

None of these block this branch. Every one was hit during the redesign, judged a real contract
change rather than a presentation problem, and written down instead of worked around. They are
listed worst-consequence-first.

### D1. `GET /v1/campaigns` and `GET /v1/automation/journeys` are unpaginated

Both return a **bare unbounded array** — no `cursor`, no `limit`, no `*Page` wrapper:

```jsonc
"/v1/campaigns":            { "get": { "parameters": [] } }   // -> Campaign[]
"/v1/automation/journeys":  { "get": { "parameters": [] } }   // -> Journey[]
```

The redesigned list screens paginate **client-side** (`CAMPAIGNS_PAGE_SIZE = 5`,
`JOURNEYS_PAGE_SIZE`), which means the browser downloads and holds every campaign a tenant has
ever run in order to display five of them. That is fine at demo-seed scale and degrades badly for
a real high-volume tenant.

This is the most consequential item in Section 3 — it is a load-bearing scaling limit, not a
polish item. Fixing it means new `*Page` schemas plus `cursor`/`limit` parameters, and it is a
frontend-first contract change we would write. Flagging now so it is on your roadmap, not to
request work today.

### D2. `GET /v1/support/tickets` cannot page, but its operator sibling can

```
/v1/support/tickets            params: status, category                     -> SupportTicketPage
/v1/operator/support/tickets   params: tenantId, status, category, cursor, limit -> SupportTicketPage
```

Same response schema, and `SupportTicketPage` already carries `nextCursor` — the customer
endpoint just has no way to ask for the next page. The customer Support screen therefore runs a
presentation-only pager. Adding `cursor`/`limit` to the customer endpoint would let us swap in
the real `CursorPagination` component with no other change.

### D3. `SuppressionPage` has no `total`

```
ContactPage        -> contacts, total, nextCursor
MessageLogPage     -> messages, total, nextCursor, campaignName, journeyName
SuppressionPage    -> suppressions, nextCursor          <-- no total
SupportTicketPage  -> tickets, nextCursor               <-- no total
```

`MessagePage`, `ConversationPage` and `VerificationAttemptPage` all carry `total` too. Because
`SuppressionPage` does not, the suppressions footer cannot state a grand total the way every
contacts footer does — a visible inconsistency in the same screen family.

### D4. `Campaign` and `Journey` have no description field

Both list designs called for a one-line subtitle under the name. Neither schema has anywhere to
put one, so the redesign shipped without the subtitle rather than inventing a field or deriving
prose on the client. If a description is wanted, it is a contract addition plus a create/edit
form field — a product decision first.

---

## Section 5 — Verification

### 5.1 Item B — unarchive (needs a real session token)

```bash
BASE=https://sms-api.saqibsaeed.cloud
TOKEN=...   # session token for founder@acme.test

# a journey that has run: expect status "paused", activatedAt UNCHANGED
curl -s -X POST "$BASE/v1/automation/journeys/$JID/unarchive" -H "Authorization: Bearer $TOKEN"

# re-read it: the change persisted, it was not just echoed
curl -s "$BASE/v1/automation/journeys/$JID" -H "Authorization: Bearer $TOKEN"

# not idempotent: unarchiving a non-archived journey is 422
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  "$BASE/v1/automation/journeys/$JID/unarchive" -H "Authorization: Bearer $TOKEN"
# expect 422
```

A journey that had **never** been activated must come back as `draft`, not `paused`.

### 5.2 Item A — `messageCount`

```bash
curl -s "$BASE/v1/billing/usage?range=30d" -H "Authorization: Bearer $TOKEN" \
  | jq '{byCampaign: .byCampaign[0], byJourney: .byJourney[0]}'
```

Both objects must carry an integer `messageCount`. Then the check that actually matters — pick a
journey with **two or more settled send steps** and confirm its `messageCount` is its real
delivered+read volume and **not** that volume multiplied by its step count (§1.4).

### 5.3 Done means

1. `make generate` run against this branch's `openapi.json`.
2. `byCampaign[]` and `byJourney[]` each return an integer `messageCount`, per §1.3.
3. A multi-step journey's `messageCount` is verified un-multiplied (§1.4) — this is the one
   regression test we would want to see written.
4. §1.5 answered: which grouping key you implemented.
5. §5.1 output pasted back, so item B can be closed.

Please do not change any other endpoint. If anything in this document disagrees with
`openapi.json`, **`openapi.json` wins** — and if it disagrees with your existing code, stop and
tell us rather than resolving it locally.

---

## Appendix — provenance

Everything above is derived mechanically, not from memory:

- Contract delta: `git diff master..HEAD -- openapi.json` (67 lines).
- `messageCount` semantics: `src/mocks/handlers.ts`, `buildUsageReport`.
- Consumer: `src/app/(dashboard)/billing/usage/usage-view.tsx`.
- Pagination and schema facts in §4: read out of `openapi.json` directly.
- Live-API state in §2.2: unauthenticated HTTP probes, 25 August 2026. Authenticated probes were
  not run from this environment, which is why §5 asks you to run them.
