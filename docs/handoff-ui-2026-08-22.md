# UI handoff — 2026-08-22

**The live site is running a frontend 48 commits behind the backend.** That is
the single fact behind the approvals crash, the missing page headings, and the
signup that refuses everyone. Everything else in this document follows from it.

```
origin/saeedafri/session-labelling-and-e2e-config   48 commits ahead
origin/master  (what Vercel deploys)                 0 commits ahead
merge conflicts                                      0     — clean fast-forward
```

Verified independently: `/admin/tenants` on the live site has **zero `<h1>`
elements**. The headings added on the branch are not there, so the deployed
build predates them.

Nothing in this document has been pushed to your `master`. The fixes are
committed on the branch and on the backend's `main`; landing them on the live
frontend is yours to do.

---

## Two things are broken on live right now

### 1. `/admin/approvals` crashes — "Approvals could not be loaded"

**Symptom.** The operator console's Approvals page renders an error boundary.
The API is fine: `GET /v1/operator/approvals` returns `200` with 6 items.

**Cause.** The approval queue has three item types — `sender`, `template` and
`registration`. A compliance registration has **no channel**, because it is what
unblocks *every* channel in a country. The deployed view renders the channel
column unconditionally:

```tsx
// origin/master — src/app/(admin)/admin/approvals/approval-queue-view.tsx
render: (i) => <ChannelTag channel={i.channel} />,
```

`getChannel(undefined)` returns `REGISTRY[undefined]` → `undefined`, then
`def.tag` throws. One thrown render kills the whole Server Components tree, so
the page becomes an error boundary rather than a table with one bad row.

It stayed invisible until a real customer submitted a DLT registration. The
first one is in your production queue now: tenant **SaqibSMS**, `pe_rtm_entity`,
submitted 2026-08-21.

**Fix (already on the branch).**

```tsx
render: (i) =>
  i.itemType === "registration" ? (
    <span className="text-[12px] text-ink-soft">—</span>
  ) : (
    <ChannelTag channel={i.channel} />
  ),
```

**Why the tests missed it.** The demo fixture only ever contained senders and
templates, so no spec ever rendered the third item type. The fixture now seeds a
pending US `tcr_brand` registration for Northwind Logistics, and
`e2e/operator-approvals.spec.ts` asserts it renders. Mutation-checked: putting
the deployed line back fails **4 specs**.

**Also fixed:** the Type filter offered only Sender and Template. An operator
could see registrations but never narrow to them. `type=registration` was
already valid in the contract and the backend — only the `<select>` was missing
the option.

### 2. Signup returns 403 for everyone

**Symptom.**

```
POST /v1/auth/signup
→ 403  {"error":{"code":"forbidden",
         "message":"Relay is invite-only right now. Ask your account manager for an invite code."}}
```

**Cause.** Public signup was closed behind an invite code (a stranger could
otherwise self-register on the demo, and separately fund their own wallet). The
deployment sets `SIGNUP_INVITE_CODE`; the frontend never sends the matching
`X-Invite-Code` header, so every signup is refused.

This one is ours: the backend half shipped without the UI half.

**Fix.** The signup form takes an invite code and forwards it as a header. See
*What changed in SMS-UI* below.

The current code is **`relay-invite-2026`**. To turn the gate off instead, unset
`SIGNUP_INVITE_CODE` on the API host and restart.

---

## What to do

This is a sequence — each step depends on the one before it.

1. **Land the branch.** `saeedafri/session-labelling-and-e2e-config` → `master`.
   It is a clean fast-forward: 48 ahead, 0 behind, no conflicts.
2. **Let Vercel redeploy `master`.** That alone fixes the approvals crash and
   brings the operator compliance workflow, live updates, security headers and
   the page headings live.
3. **Regenerate the contract types.** `npm run types:api` — `openapi.json` moved
   (new endpoints below). Committed on the branch, but re-run it after any
   backend contract change.
4. **Confirm on the live site**: `/admin/approvals` renders, `/admin/tenants`
   has an `<h1>`, and a signup with the invite code succeeds.

If you would rather not merge yet, pointing Vercel's production branch at
`saeedafri/session-labelling-and-e2e-config` gets the same result — but `master`
stays behind and the next person rediscovers all of this.

---

## New backend surface

Everything below is live on `https://sms-api.saqibsaeed.cloud` today. Re-run
`npm run types:api` to pick up the types.

| Endpoint | What it does | Notes for the UI |
|---|---|---|
| `POST /v1/messages` | Sends one message | **New.** `202` with `{id, status, segments, costMinor, currency, errorCode}`. A refusal is also a `202` — read `status` and `errorCode`, don't assume 2xx means sent. `429` when the tenant exceeds its rate tier. |
| `POST /v1/operator/routes` | Adds a route to a corridor | **New.** Created **disabled** and last in its carrier group. |
| `DELETE /v1/operator/routes/{id}` | Removes a route | **New.** `422` if the route is still active — disable it first. |
| `POST /v1/auth/signup` | Unchanged shape | Now accepts an optional `X-Invite-Code` **header** and can answer `403`. |
| `POST /v1/operator/routes/{id}/enable` | Unchanged | Now answers `422` for a grey route unless the deployment sets `ALLOW_GREY_ROUTES`. Surface the message; it explains itself. |
| `GET /v1/operator/approvals` | Unchanged | Now includes `itemType: "registration"` items, which carry **no `channel`**, and `fields` instead of `header`/`body`. |
| `GET /v1/events` | SSE stream | Already consumed by `LiveUpdates` on the branch. |

### API keys authenticate now

`sk_live_…` / `sk_test_…` keys previously authenticated **nothing** —
`ResolveAPIKey` had no callers, so every key a customer pasted into their code
was decoration. Keys now work, but only on `POST /v1/messages`, and only with
the scope for the sender's channel (`send:sms`, `send:rcs`, …). A key on any
other endpoint is a `401`, deliberately: a key carries scopes and no role, and
no other handler checks them.

The developer settings page's advertised rate tier is now enforced —
100 req/s live, 10 req/s test.

---

## Behaviour changes worth knowing

| Was | Is now |
|---|---|
| A gate refusal (unapproved sender, bad number, no balance) → `500` | `202` with `status: "failed"` and an `errorCode` the customer can act on |
| Rate card `costReferenceMinor` null on all 14 rates | Populated. The filter compared route status to `"enabled"`, a value routes have not held since migration 00029 renamed it to `"active"` — so it excluded every route. The same word made every corridor report 100% margin. |
| Campaigns could sit at `sending` forever | Fan-out lands a terminal status on every exit path, and a sweep reconciles any campaign the process died inside |
| Demo campaigns claimed 1,840 and 1,200 recipients against 414 seeded messages each | Both derive from one constant; the seeder refuses to finish if they disagree |
| Grey routes could be enabled from the console | Refused unless `ALLOW_GREY_ROUTES` is set |
| `/v1/operator/*` reachable from anywhere | Restricted by `OPERATOR_IP_ALLOWLIST` when set (currently unset — see *Open decisions*) |

---

## Two things the UI team must wire up

### Operator sign-in can now answer with a challenge

`POST /v1/operator/login` used to return `AuthSession` directly. It now returns a
discriminated union, exactly like customer login:

```jsonc
{ "kind": "session",       "session":   { "token": "…", "expiresAt": "…" } }
{ "kind": "mfa_challenge", "challenge": { "challengeToken": "…", "methods": […] } }
```

Reading `token` off the top level silently yields `undefined` — the backend's own
test harness did exactly that and every operator spec failed with a 401 that
looked like an authorisation bug. The branch, the challenge screen at
`/operator/login/mfa`, and the enrolment card at `/admin/security` are all
implemented on the branch.

`GET /v1/operator/me` gained `mfaEnabled`, and its `role` enum now includes
`admin` — the database has allowed that since migration 18, so an admin's own
profile was a value outside its own enum.

### Signup needs the invite code field

Covered above. The field is on the form and travels as `X-Invite-Code`.

## What changed in SMS-UI

**On the branch, already committed:**

- Operator compliance approval workflow (queue, review, approve/reject)
- Live updates over SSE — approvals, role changes and tenant status reach an
  open screen without a reload
- Five security headers, and `robots.txt` disallowing indexing
- `<h1>` on Tenants, Approvals, Routes and Abuse review — four console pages a
  screen reader announced as untitled
- GB and AE no longer read as a dead end. Nothing in the backend gates sending
  on a registration, so those tenants onboard like any other; the screen said
  "Not available yet", which customers read as "you cannot use this country"

**Uncommitted in your working copy** (this session, local only):

- `approval-queue-view.tsx` — the Registration option in the Type filter
- `e2e/operator-approvals.spec.ts` — the spec that would have caught the crash
- The signup invite-code field and header

---

## Running the whole stack locally

No tunnel, no VPS. Postgres, Redis and ClickHouse all run on the machine.

```bash
# 1. dependencies
brew services start postgresql@16 redis
cd ~/clickhouse-data && nohup ~/clickhouse-bin/clickhouse server > ch.log 2>&1 &

# 2. backend
cd SMS-BE
make migrate-up && make migrate-test      # 00035 adds the operator read policy
python3 scripts/clickhouse_migrate.py sms_dev
make seed
set -a && . ./.env && set +a && ./bin/control-api    # :8080

# 3. frontend — .env.local points at 127.0.0.1:8080, mocks off
cd SMS-UI && pnpm dev                                # :3000

# 4. the suites
npx playwright test --project=chromium
npx vitest run
cd ../SMS-BE && go test ./... -count=1
```

`.env.local` in SMS-UI now points at the local API rather than the SSH tunnel.
The tunnel variant is still documented in `.env.e2e.example`.

---

## Open decisions

- **`OPERATOR_IP_ALLOWLIST` is unset**, so the operator console — which sees
  every customer — is still reachable from the whole internet. It needs your
  office or VPN range. The API now says so at every boot, at ERROR level when
  staff also lack a second factor, because a warning among warnings is how this
  stayed open in the first place.
- **The seeded operator password** is still the one published in this
  repository. Every boot now names the account and the command:
  `operator-admin set-password ops@relay.internal`. Nothing rotates it for you —
  changing a password unasked locks out whoever is using it.
- ~~Operator MFA does not exist.~~ **Built.** TOTP, recovery codes, and a
  challenge on sign-in, mirroring the customer flow but sharing none of its
  storage. Staff enrol at `/admin/security`.
- **`app.saqibsaeed.cloud` has no DNS record.** It only ever appeared in
  `deploy/env.production.example`, which has been corrected. The Vercel URL is
  the only live frontend.
- **~85 smoke-test messages** are in the demo tenant's log from testing the send
  API. `POST /v1/dev/reset-mock-state` clears them and rebuilds the fixture.
