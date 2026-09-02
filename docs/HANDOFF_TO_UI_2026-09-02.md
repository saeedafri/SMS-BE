# Handoff to the frontend team — 2 September 2026

Everything below was found by running your Playwright suite against the **deployed**
backend (`sms-api.saqibsaeed.cloud` via an SSH tunnel), not against mocks and not against a
local stack. Nothing in `SMS-UI` was changed to produce it — the one exception is noted in §2
and it is a two-line generated file.

Final run: **272 passed, 0 failed, 1 flaky, 9 skipped** in 31.8 minutes.

---

## 1. The `admin` role has no browser coverage at all

`TeamRole` is `owner | admin | member`. The suite covers two of the three:

| Role | Where it is exercised |
|---|---|
| `owner` | every one of the 272 tests signs in as one |
| `member` | 6 tests in `rbac-enforcement.spec.ts` |
| `operator` | the whole `/admin` console |
| **`admin`** | **nothing** |

This is worth a spec because `admin` is *not* a middle ground in the API. `canManageSettings`
returns true for both `owner` and `admin`, so an admin reaches billing, compliance, developer
and settings exactly as an owner does. There is exactly **one** rule separating them:

> only an owner may create another owner — enforced on both `POST /v1/team/invite` and
> `PATCH /v1/team/{id}`

We had a test for the invite half and none for the promote half. An admin who could not invite
a new owner could still promote an existing member to owner — the same escalation with one
extra step. That is now covered backend-side
(`TestAnAdminCannotPromoteAnExistingMemberToOwner`), verified by deleting the guard and
watching the call return `200` with `"role":"owner"`.

**What we would like from you:** an `admin`-role case in `rbac-enforcement.spec.ts` asserting
that an admin sees the same nav as an owner, and that the "promote to owner" control is absent
or refused. The `/api/dev/set-my-role` hook already takes `admin`.

## 2. `api-types.ts`: the prettier step in your handoff is backwards

`handoff-2026-09-01-contract-batch-to-next-session.md` §5.3 says to run
`npx prettier --write src/lib/api-types.ts` immediately after `pnpm types:api`, to avoid a
~20,000-line phantom diff.

On `master` today that instruction **creates** the phantom diff rather than preventing it. The
committed file is the generator's raw **4-space** output; prettier reformats it to 2-space and
produces a 22,610-line diff. Without prettier the same change is 2 lines.

```
pnpm types:api                       →   2 insertions
pnpm types:api && prettier --write   →  11,366 insertions / 11,244 deletions
```

Worth correcting in that handoff before it costs a third agent a session.

## 3. PR #2 is still unmerged, and `master` is missing things production serves

Generating the backend from `master`'s `openapi.json` alone deletes 510 lines of the generated
client and breaks the build. `master` is missing:

| Missing from `master` | Why it matters |
|---|---|
| `OperatorLoginSessionResult.token` / `.expiresAt` | the exact field whose absence locked staff out of the operator console |
| `total` on 6 page schemas | your own asks #1 and #5 |
| `SendMessageRequest.variables` | RCS personalisation |
| `Template.carrierRegistration` | carrier template registration |
| `/v1/rcs/capabilities`, `/v1/templates/{id}/carrier-registration` | 2 paths, 4 schemas |

We deliberately did **not** merge `master` into the PR branch: the twelve conflicts are all in
source files your redesign rewrote, and they are yours to resolve.

One change did go the other way. `master` declares `registrationId` at the top level of the
`POST /v1/registrations` body and the PR branch did not, so the backend was dropping it
silently — the customer typed their DLT id, the form said saved, and the column stayed null.
The property was copied verbatim from `master` onto the PR branch and `pnpm types:api` re-run,
so that hunk will merge as a no-op. That is the only `SMS-UI` change we made.

## 4. Lint is red on the PR branch, green on master

`pnpm lint` reports **5 errors, 27 warnings** on the PR branch, all
`react/no-unescaped-entities` (bare apostrophes) in files we never touched:

```
src/app/(dashboard)/audience/import/import-editor.tsx
src/app/(dashboard)/automation/new/page.tsx        (×2)
src/app/(dashboard)/support/new/page.tsx           (×2)
```

Your handoff records `master` at 0 errors / 32 warnings, and the branch is 51 commits behind,
so these are almost certainly already fixed upstream and will disappear on merge. Flagging it
only so a red lint gate on that branch is not mistaken for a regression.

Separately: `.eslintrc.json` exists on `master` but not on the PR branch, so `pnpm lint` there
drops into the interactive "Strict / Base / Cancel" setup prompt and exits 1 before linting
anything. That will also resolve on merge.

## 5. Two flaky specs, both passing on retry

Neither reproduces reliably; both passed on retry in the same run, and each failed in a
*different* run, which points at load rather than a defect.

| Spec | Symptom |
|---|---|
| `analytics.spec.ts:101` fraud signal card | failed in 1.2s, passed on retry in 6.3s |
| `settings-team.spec.ts:70` change a member's role | failed in 8.2s, passed on retry |

## 6. The 9 skipped tests are correct, not gaps

Worth writing down so nobody "fixes" them:

- **8 RCS specs** (`rcs-capabilities`, `rcs-carrier-template`) — `test.skip(true, "this
  deployment has no RCS carrier configured")`. Production has no Airtel or Vi credentials, so
  the skip is accurate.
- **`billing.spec.ts:70` top-up** — skips when no payment processor is configured. The spec's
  own comment has it right: the backend now refuses a tenant-initiated capture when the gateway
  is nil, because before that it credited the ledger with no charge at all. Asserting a
  successful top-up against this deployment would be asserting the bug.

## 7. One thing to know before you next run the suite against a live backend

`/v1/dev/reset-mock-state` rebuilds the fixture, and rebuilding the fixture clears and re-lays
the `routes` table. A backend migration had changed the uniqueness rule on `routes` without
updating the seed, so the `DELETE` committed and the `INSERT` was refused — leaving the table
**empty**, which means no corridor has a carrier path and no tenant can send at all. Running
your suite against production emptied it, and the rows had to be restored from a backup.

Fixed on our side three ways: the seed data now satisfies the constraint, the clear and rebuild
share one transaction so a future violation fails without taking sending down, and
`internal/demoseed` has tests where it previously had none.

Nothing is required from you. It is written down because the failure mode — a reset hook that
silently disarms the platform — is worth knowing about if you ever point the suite at a
deployment you care about.

## 8. Route ordering changed, and two of your specs depended on the old model

`routes.priority` used to be unique per *carrier*, so `IN/SMS` had Jio Direct at 1, Jio via
Aggregator A at 2, **and** Airtel Direct separately at 1. It is now unique per
country × channel, which is what "the order the corridor is tried in" actually means.

`src/mocks/routes-state.ts` still carries the old numbering. The fixture now takes that file's
**order** and renumbers it 1..n per corridor, which keeps
`operator-routes.spec.ts:51` and `:85` passing — they move Jio Direct down and expect Jio via
Aggregator A to take its place. If you renumber the mock, keep those two adjacent.
