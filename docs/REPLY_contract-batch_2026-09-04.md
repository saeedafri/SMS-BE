# Reply — the contract batch is implemented

**To:** Relay frontend (`sms-platform-frontend`)
**Date:** 4 September 2026
**Re:** `BACKEND_REQUEST_contract-batch.md`
**Backend:** `github.com/saeedafri/SMS-BE@main`

All four are implemented, tested against the Hostinger datastores, and verified against the
deployed API. Your Section 7 checklist, answered in order, then the two open questions.

---

## Section 2 — `Idempotency-Key` on `POST /v1/messages`

**Already shipped**, before this request arrived. Tenant-scoped by primary key
`(tenant_id, scope, key)` under row-level security, so two tenants using the same key cannot
collide. Nothing prunes the table, so the ≥24h window is satisfied by keeping keys indefinitely.
A replay returns the stored `202` and sends nothing.

**One divergence you should know about, because it is deliberate and it is the opposite of what
Section 2 asks for.**

You ask that the key be recorded on the same commit as the send. Ours is recorded *after* the send
has an outcome, and the code says why:

> Stored only once the send has an outcome, so a crash mid-send leaves the key unclaimed and the
> client's retry is a real attempt rather than a replay of something that never happened.

The two orderings trade different failures, and neither is free:

| Order | Crash between send and record | Crash after claim, before send |
|---|---|---|
| record after outcome (ours) | retry **duplicates** the message | — |
| record first (yours) | — | retry returns a stored result for a message that **never went** |

A true same-commit guarantee is not reachable anyway: the carrier call is an external side effect
that no Postgres transaction can span. So the choice is which way to fail, and we picked "never
silently swallow a send that did not happen" over "never duplicate".

**We will switch to claim-first if you want it** — it is a small change — but it should be a
decision, not a default. Tell us which failure you would rather explain to a customer.

## Section 3 — the throttle rate

Done, with the invariant enforced in two places rather than one.

- Body required; `ratePerSecond` must be a whole number ≥ 1. Missing, `0`, negative, fractional,
  non-numeric and explicit `null` all return `422`.
- **`404` is checked before the rate.** A request with a bad id *and* a bad rate answers `404`,
  and there is a test that sends exactly that.
- `422` when the tenant is already throttled, and when it is not active. A refused re-throttle
  leaves the original ceiling intact — asserted, because a half-applied refusal is worse than
  either outcome.
- `additionalProperties: false` is enforced. Your mock's exact case — `{ratePerSecond: 40,
  status: "suspended"}` — is refused and the status is untouched.
- The audit detail names the ceiling and carries the reason:
  `Throttled Acme Retail to 40 messages/second — carrier TPS cap`.

**The invariant is enforced at the storage layer, not only in the handler.** A CHECK constraint
ties `throttled_rate_per_second` to `throttled_at`, so a future transition out that forgets to
clear the rate fails loudly instead of leaving the console reporting a live ceiling on a tenant
that no longer has one. Both existing exits — reinstate and suspend — clear it, and the test walks
both rather than asserting one.

## Section 4 — the webhook catalogue

- **All fourteen accepted**, and a misspelled event is now refused with `422` rather than stored.
  It was previously accepted verbatim, which meant a typo'd subscription silently never fired —
  indistinguishable from a broken integration on your side.
- **`message.inbound` fires first**, as you asked. It emits on every inbound message including a
  STOP, and the payload carries the message id, conversation id, contact id, channel, body, the
  matched keyword, and the timestamp — enough to reply without a second call.
- Delivery is best-effort and never fails the action that produced it. An inbound message that
  arrived is a fact, and a customer's endpoint being down must not turn it into a 500 for the
  carrier that delivered it. Every attempt is recorded either way.
- Endpoints not subscribed to an event are skipped. Proven by mutation: removing the subscription
  filter fails the test.
- The `approved` spelling is untouched, and a test asserts `sender.approved` and
  `template.approved` survive the round trip.

**Staged, per your own advice.** `message.inbound` is live; the remaining thirteen are accepted as
subscriptions but not yet emitted. The wallet pair specifically needs balance-crossing detection
threaded through the charge path — your point 3 is right, and an unconditional check would re-fire
forever while the balance stayed low, which is the same bug we hit with auto-recharge. That is the
next piece of work, not a thing we have quietly skipped.

## Section 5 — sender edit and delete

`PATCH`:

- `409` for `approved`, `blocked` and `expired`.
- Empty object is a no-op, not an error.
- `displayName` is refused with `422` on anything but WhatsApp.
- **Clearing is distinguished from omitting.** Both arrive as a nil pointer in the generated
  struct, so the raw body decides which request this is. Clearing a registry id the regime
  requires is refused; omitting the same field leaves it alone. Both directions are tested.
- The value the record would **end up with** is validated, through the same function the create
  path calls — one function, so they cannot drift.

`DELETE`:

- `204`, and `409` while anything references it.
- **All five kinds counted**, and the refusal names them: *"still used by 2 templates,
  1 campaign"*.
- Only two of the five are foreign keys. A journey's steps and a Verify service's channels carry
  `senderId` inside JSONB, so the database would let a delete through and strand them silently.
- **The fixture exercises each kind alone**, exactly as you warned. Proven by mutation: deleting
  the journey branch fails the journey case *only* — which is the check that would have caught it
  on your side.
- Delete is allowed in any status, verified included.

---

## Your two open questions

**1. Same key, different body.** We return the original result, unchanged. Rationale: the caller
is retrying, and a `422` at that point tells them nothing they can act on — they cannot tell
whether the first send happened. Returning the first answer is the only response that is true
regardless of what the second body says. Please update the description to say so.

**2. Does the throttle break need a migration window?** No. The endpoint had no external callers,
and our own tests were the entire blast radius. It is deployed.

## One thing we changed that you did not ask for

`rejectUnknownFields` matched literal URL paths with one hard-coded prefix special-case, so every
allowlist entry written with `{id}` matched nothing and the guard passed by never firing. That is
why the throttle body's `status` got through on the first run. It now reconstructs the contract
path by collapsing id-shaped segments, so `additionalProperties: false` is enforced on both new
routes and on the connection routes it was always meant to cover.

## Still outstanding on our side, unchanged

The DLR ingestion shape (WP2), payment-method shapes (WP3, blocked on the gateway choice), and the
RCS agent model and number inventory (WP9, WP10).

And one from us: **PR #2 is still unmerged**, so `master`'s `openapi.json` is missing
`OperatorLoginSessionResult.token` — the field whose absence locked staff out of the operator
console — plus `total` on six page schemas and the two RCS paths. We generate from a union of your
`master` and that branch to keep production working. Merging it retires the union.
