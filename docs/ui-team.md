# For the frontend team

**This page lists only the things the backend cannot fix on its own**, plus one
place where we need a change to the contract.

Everything else in the browser suite is ours, and where a fix belonged in your
repo we made it there and wrote it up rather than filing it here — see
[Handoff: frontend changes](handoff-ui.md) for all 51 files, uncommitted, with
the reasoning for each.

Nothing here is a complaint. Most of what we found was the mock quietly doing
something a real server is not allowed to do, which is exactly what a mock is
for. It just has to be settled before the specs go green against a real backend.

---

## Summary

| # | Item | What we need |
|---|---|---|
| 1 | **Campaign progress is simulated from the clock** | A joint decision |
| 2 | **Journey funnel expects a number the data does not support** | A joint decision |
| 3 | **`LedgerEntryType` is missing `refund`** | One line in `openapi.json` |

That is the whole list. It was seven items; four are now closed, three of them
by changes we made in your repo.

---

!!! success "Closed since the last version of this page"
    - **Filter links do not navigate** — was the largest item at 11 specs, and we
      had it filed as "a bug in your component we did not want to guess at".
      That was ours to investigate, and it is now **fixed**. Two causes stacked:
      the hrefs were query-only (`"?range=7d"`), which `next/link` resolves
      against the app root rather than the current route; and underneath that,
      the App Router refuses same-route search-param navigations on these views
      entirely — it fetches the RSC payload, gets a valid 200, and discards it
      without a `pushState`. `useRouter().push()` fails identically. A new
      `FilterLink` (a plain `<a>`) fixes it; verified on all six screens. Full
      write-up, including the seven things we ruled out, in
      [Handoff, Group 1](handoff-ui.md) — **worth reading before your next
      Next.js upgrade.**
    - **Eighteen specs depended on an authentication bypass** — **fixed** in your
      repo: five spec files never signed in, and `auth.spec.ts:8` asserted that
      an anonymous visitor is handed a live session. See Handoff, Group 2.
    - **Mock-only UI that specs assert on** — **fixed** on both sides. A new
      `showDevCodes()` gate opens the dev hints behind a second explicit flag,
      and the backend now issues matching fixed values for the verify code, the
      password-reset token and the email-verification token under
      `ENABLE_DEV_ENDPOINTS`. See Handoff, Group 3.
    - **Fixture ids that are not valid UUIDs** — **fixed** in your repo;
      `inbox.spec.ts` now uses the ids we seed.

---

## 1. Campaign progress is simulated from the wall clock

MSW derives a campaign's live progress from `sendStartedAt` + `sendDurationMs`,
computing a trajectory that advances as real time passes. Reading the same
campaign twice, a second apart, returns two different progress figures.

Our backend stores a `status` and the real counts, updated as the send pipeline
actually processes the batch.

Both are defensible. The mock's version is better for a demo — the bar visibly
moves. Ours is better for a customer — the number is what really happened. The
specs that assert on a mid-flight progress figure cannot be satisfied by a stored
status, whatever we seed.

**What we suggest.** A short call. The options as we see them:

- **(a)** Assert on the terminal states only (`sent`, `failed`) and drop the
  mid-flight assertions. Least work, and it is what a customer actually relies on.
- **(b)** We add a real progress endpoint backed by live counts from the send
  pipeline, and the mock switches to reading it. More work on our side, but the
  bar moves for real. We are happy to do this if you want it.

We lean towards **(b)** for the product and **(a)** for unblocking the suite now.

---

## 2. The journey funnel expects a number the data does not support

**This is the one we would most like to talk through**, because we think the
number in the spec cannot be produced by any real backend — including a correct
one.

`automation.spec.ts` asserts that the "Diwali welcome flow" journey funnel shows
`exitedSuppressed = 2`.

Where that 2 comes from is documented, in admirable detail, in your own fixture
at `src/mocks/journeys-state.ts:415`:

> See this task's plan text for the full derivation: `hash(JO1_ID) + 1160`, mod
> `1e8`, formatted as `"+9198"` + last-8-digits, equals the seeded
> `opted_out_keyword` suppression `+919876590001` in `suppressions-state.ts`.
> Recipients (1200) must stay > 1160 or the collision falls outside range. […]
> because the synthetic-identity formula is linear in `index`, enrollee 1161
> ALSO lands on the OTHER seeded suppression `+919876590002`.

So the mock invents 1,200 synthetic enrollees by hashing the journey's id, and
exactly two of those invented phone numbers happen to collide with the two
seeded suppression entries. The `2` is a property of that hash function and that
particular id.

**Our backend counts the real thing:** how many members of the journey's trigger
list are actually suppressed. With the seeded fixture, that is **0** — the four
seeded contacts are Priya, Arjun, Meera and Vikram, and none of their numbers is
on the suppression list. The two suppressed numbers, `+919876590001` and
`+919876590002`, are not contacts at all.

**We are deliberately not hard-coding a 2.** A compliance funnel that reports two
suppressed exits when the data says zero is worse than a failing test — it is the
exact number a customer would quote to a regulator.

**What we suggest.** Change the assertion to something the data supports. Either:

- **(a)** Assert the funnel renders and its numbers are internally consistent
  (entered ≥ completed + exited), rather than asserting an exact count. This is
  our preference — it survives fixture changes.
- **(b)** Tell us the count you want and we will seed contacts that genuinely
  produce it: put two suppressed numbers into the "Diwali 2026" list and the real
  count becomes 2, honestly. Say the word and we will do this; it is about
  fifteen minutes of work on our side.

---

## 3. `LedgerEntryType` is missing `refund`

**The one thing on this page that is a contract change rather than a decision.**

`components["schemas"]["LedgerEntryType"]` is:

```ts
LedgerEntryType: "charge" | "topup" | "auto_recharge";
```

The backend also writes **`refund`** rows. They are not hypothetical: a campaign
reserves funds for an estimated segment count, and when the real count comes in
lower the difference is released back to the wallet as a `refund` line. Any
tenant who has run a campaign has them.

**This does not break your UI today.** `billing-view.tsx` renders `{e.type}`
directly and treats anything that is not `"charge"` as a credit, so a refund
shows as a green `+` row — which is correct. We checked before writing this up,
rather than reporting it as breakage it is not.

But it is outside the declared union, so a stricter consumer would reject it, and
a generated type would not know the value exists.

**What we need.** Add `"refund"` to `LedgerEntryType` in `openapi.json`. We have
deliberately not touched that file — the contract is yours, and we would rather
you make the change than discover ours in a diff.

---

## What we are fixing on our side

For completeness, so you know what is *not* on this list. These are ours and in
progress:

- Fixture content that differs between MSW's seed and ours — we are porting the
  remaining values across.
- WhatsApp `qualityRating` and `messagingTier` — the contract declared them and
  we had no column for them. **Fixed**; the senders list now shows `green` and
  `10,000/day`.
- Email sender DNS records were attached to the wrong sender and left pending.
  **Fixed**; all three now sit on "Acme Notifications" and read `verified`.
- Approved registrations were not being given a registry id, so the compliance
  screen showed an empty "Registration ID". **Fixed**; `REG-PE_RTM_ENTITY-0001`
  is now assigned on approval, matching the mock's format.
- **Eight list endpoints ignored their query filters entirely** — including
  `environment` on the developer endpoints, which is a required parameter, so the
  test-mode page was listing live API keys. **Fixed**; all eight now filter. If you
  saw the inbox channel filter, the operator tenant filters, or the audit-log tenant
  filter appearing to do nothing, that was us, and it is fixed.
- Verify analytics had no data behind it and its fraud counts were hardcoded zeros.
  **Fixed**; the funnel, success rate and fraud cards now compute from real rows.

---

## How to run the suite against our backend

```bash
# terminal 1 — the backend
cd SMS-BE
make services-up && make migrate-up
set -a && . ./.env && set +a
ENABLE_DEV_ENDPOINTS=1 go run ./cmd/control-api

# terminal 2 — the fixture, then the specs
cd SMS-BE && make seed
cd ../SMS-UI && npx playwright test
```

Sign in with `founder@acme.test` / `relay-dev`. The operator console is
`ops@relay.internal` / `relay-ops-dev`.

One thing worth knowing: run **one** suite at a time. Two concurrent runs share
one database and corrupt each other's fixtures, which produces failures that look
like real bugs and are not. We lost a full run to this.

## Who to talk to

Reply on any of the three items above and we will turn it around the same day.

Items 1 and 2 are decisions, not work — tell us which behaviour you want and we
will implement it. Item 3 is one line in `openapi.json`.

Everything else we found, we fixed: on our side where it was ours, and in your
repo where it was not. The full list of changes we made in `SMS-UI` is in
[Handoff: frontend changes](handoff-ui.md), all uncommitted and waiting for your
review.
