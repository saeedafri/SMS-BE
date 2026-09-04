# Reply — campaign pause, resume and cancel

**To:** Relay frontend
**Date:** 5 September 2026
**Status:** Built, tested, and deployed. Three routes, the full transition matrix,
the timestamps, `counts.cancelled`, and the concurrency guard.

---

## What is done

All three routes answer, each returning the **full updated `Campaign`** as asked:

```
POST /v1/campaigns/{id}/pause    200 Campaign | 401 | 404 | 409
POST /v1/campaigns/{id}/resume   200 Campaign | 401 | 404 | 409
POST /v1/campaigns/{id}/cancel   200 Campaign | 401 | 404 | 409
```

- `pausedAt` and `cancelledAt` are **always emitted**, null when unset. No
  `omitempty` on either.
- `counts.cancelled` is always emitted, `0` when nothing was cancelled.
- The transition matrix is exactly yours — pause from `sending`/`queued`/
  `scheduled`, resume from `paused` only, cancel from any non-terminal, everything
  else `409`, `404` checked first. All 21 cells are asserted in one table test,
  `TestTheCampaignHaltTransitionMatrix`, so no cell can quietly rot.
- Cancel-while-paused keeps **both** instants. `pausedAt` is deliberately not
  cleared, because the earlier one is the campaign's real stop time — your §4.2.
- Concurrent halts take a row lock before reading the status, so exactly one of
  eight simultaneous pauses wins and the rest get `409`. Asserted.
- Audit: all three write a `user_activity` row with the actor. That adds
  `campaign.pause`, `campaign.resume` and `campaign.cancel` to
  `UserActivityEventType` — please declare them.

## How the brake actually works, because it is not what you assumed

Worth being concrete, since your §0 correctly says our side is not a clock.

**Fan-out is a loop inside a request, not a background dispatcher.** Creating a
campaign sends it, page by page, five hundred recipients at a time, and returns
when it is finished. There is no queue to drain and no worker to signal.

So the brake is: **the loop reads the campaign's status between pages.** A pause
arriving from another request changes the row; the loop sees it before starting
the next page and stops. One indexed read per five hundred recipients is what "no
further recipient is dispatched" costs. A page already handed to a carrier is not
recalled, which is the behaviour your §2.1 explicitly allows.

**Resume restarts fan-out from a stored cursor.** The loop persists its keyset
cursor into the contact list after every page, so a resume continues from the
recipient the pause stopped at — nobody sent twice, nobody skipped. That cursor is
also written on a crash path, so a campaign that dies mid-fan-out resumes from its
last completed page rather than re-sending everyone before it.

**Resume dispatches in the background and returns immediately** with
`status: "sending"` and `pausedAt: null`. This is the one place resume
deliberately differs from create: a resume that sent inline would answer "sent" to
a screen about to render a Pause button, and would hold the connection open for
the length of the remaining run.

Two guards fell out of building it, both of which we got wrong first:

- `MarkCampaignSending` now refuses to write over `paused` or `cancelled`. Fan-out
  marks a campaign sending on its way in, and between a resume returning and its
  dispatch goroutine starting there is a window in which a second halt can land.
  Without the guard that halt was silently undone.
- `SetCampaignStatus` refuses to write over `cancelled`. Cancel is terminal, and
  both the landing write and the stuck-campaign sweep would otherwise turn a
  campaign somebody deliberately stopped back into `sent`.

## Billing — you were worrying about a problem we do not have

Your §3 assumes settlement is deferred to campaign completion, and builds
high-water marks and delta charges to make halts safe against that. **We never
deferred it.** Money moves per page, as the page is dispatched: one wallet
movement covering that page's five hundred, taken before anything reaches a
carrier.

So your three rules hold, for a different reason than you expected:

- **3.1, settle at every halt** — already true. A paused campaign has been charged
  for everything it dispatched, up to the page it stopped on. Nothing is off the
  books and the wallet on screen is correct during the pause.
- **3.2, a completed run bills exactly once** — we bill once per page rather than
  once per campaign, and always have. A campaign paused three times and resumed
  three times bills exactly what one that ran straight through bills, because the
  charge follows dispatch and dispatch happens once per recipient either way. A
  campaign that never pauses produces precisely the ledger it produces today.
- **3.3, cancel bills what delivered and nothing else** — the unsent remainder was
  never dispatched, so it was never charged, so there is nothing to refund. Asserted
  by `TestACancelledCampaignDispatchesNobodyAndIsNeverLandedAsSent`: a cancelled
  campaign's wallet does not move at all.

One difference from your worked example worth flagging: **we charge on dispatch,
not on delivery.** Your example bills 30,000 delivered. We bill 30,000 dispatched,
and then credit back any of those that the carrier rejects or that never arrive —
the hold-and-release model in the other reply. Same money in the end, more ledger
rows on the way, and it is the model that was already there.

## `counts.cancelled` — how it is computed, and why not the other way

**It is derived, not stored, and no message rows are written for cancelled
recipients.**

Fan-out creates a message row when it reaches a recipient. A campaign cancelled at
30,000 of 100,000 has 30,000 rows; the other 70,000 have no row at all — never
dispatched, never charged, never queued. So `counts.cancelled` is
`recipients − (everything recorded)`, and `0` for any campaign that was not
cancelled.

We considered writing the 70,000 rows so that "every undispatched recipient moves
to `MessageStatus.cancelled`" would be literally true at the row level, and decided
against it: it adds no information the subtraction does not already carry, and it
turns cancelling a large campaign into a hundreds-of-thousands-of-rows write at
exactly the moment someone is urgently trying to stop something.

**Your invariant still holds and is tested:** after a cancel, `counts.queued` is
`0` and `counts.cancelled` holds what was outstanding, so the funnel's shared track
is never double-counted. `TestACancelledCampaignReportsItsUndispatchedRecipientsAsCancelled`.

The consequence for you: `MessageStatus.cancelled` is in the enum and is not
currently produced by any message row. If you would rather we materialise them, say
so — it is a straightforward change, it is just one we would not choose.

## §4.3, scheduled campaigns — the honest answer

Pause, resume and cancel on a `scheduled` campaign all work and set the right
state. But **there is no scheduler.** Nothing in the platform dispatches a campaign
at its scheduled time today; `status = 'scheduled'` is recorded and never acted on.

So "resuming re-arms the schedule" and "if the instant has passed, start
immediately" are both no-ops until a scheduler exists. We would rather tell you
that than let you build a screen on top of a promise nothing keeps. The scheduler
is its own piece of work and is not in this batch.

## Your open question — a `reason` on cancel

**No, we do not need one.** The audit row already records who cancelled what and
when, which is the question that gets asked afterwards. A free-text reason on a
customer-initiated action tends to arrive empty or arrive as "test", and adding a
required field later is the breaking change you are rightly trying to avoid. If
your UI wants to offer one, add it to the contract as optional and we will store
it — but do not hold this batch for it.

## How to verify

Your §6 script works as written, with one difference to expect: because resume
dispatches in the background, a campaign with a short list may reach `sent`
between your resume and your cancel, and the cancel then correctly answers `409`.
Use a campaign with a list long enough to still be sending, or check the status
returned by the resume itself.
