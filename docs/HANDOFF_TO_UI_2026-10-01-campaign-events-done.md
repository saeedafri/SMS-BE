# H4 campaign events: done and live (1 Oct 2026)

Reply to `HANDOFF_TO_BACKEND_2026-10-01-campaign-events.md` (tracker H4).
**Live on `a2b2e25`** (`/healthz` `commit`), API restarted 18:39:47 IST.
No `openapi.json` change, as you asked.

## What you get

| Ask | Built as |
|---|---|
| **E1** `campaign.status_changed` | Published immediately, never coalesced, on: launch start (`sending`), landing (`sent` / `failed`, including a launch that errors part-way), pause, resume, cancel, the scheduler claiming a scheduled campaign (`queued`), and the stuck-campaign sweep. `objectId` = campaign id |
| **E2** `campaign.progress` | Fired on every message change of a campaign: each delivery report (delivered / read / failed), each reconciler expiry, each late SMPP submit answer, and once per fan-out page (the queued/sent counts). Coalesced exactly as you proposed: `SET relay:campaign:<id>:progress NX PX 1000`. The first change publishes at once; a change that finds the gate held schedules **one** trailing publish for when the gate clears, so the last change is always followed by an event within ~1 s |
| **E3** `GET /v1/operator/events` | Same SSE frame, same 25 s heartbeat, same `: connected` opener. Operator session only. Fed by `relay:operator:events`, which now carries **every** tenant event (campaign and the five existing types). `tenantId` says whose |

Payload unchanged: ids only, no counts or text. Publishing is fire-and-forget:
a Redis outage never fails or slows a campaign.

## Your questions

1. **Where the trailing event comes from.** The settle path itself (`Service.settle`), not the settle
   worker: that is where a count changes, so it is the only place that can guarantee the event after
   the final report. The trailing publish is a timer set when the gate is busy.
2. **`campaign.progress` on the operator stream for big tenants.** No reason to filter it: it is
   already capped at one per campaign per second. If the console ever watches thousands of live
   campaigns at once, tell us and we'll revisit.

## Acceptance, as run

| # | Result |
|---|---|
| T1, T4, T6 | ✅ Test against the real Redis on the server: the owner's stream gets every status change at once, another tenant's stream gets nothing, the operator stream gets the same frame with the owner's `tenantId` |
| T2, T3 | ✅ Test: a 2.5 s burst of changes produced at most one frame per second, and the last frame came after the last change, within 1.1 s. A mutation that drops the trailing event fails it |
| T5 | ✅ **Live**: no token `401`, tenant token `401`, junk token `401` on `/v1/operator/events`; tenant token on `/v1/events` `200` + `: connected`. Operator token → `200` **not yet run** live or in a test (we hold no operator login on the server) |
| T7 | ✅ Test with Redis unreachable: campaign events return promptly and never fail the caller. We did **not** stop the shared Redis to check it live |
| Send path | ✅ Test: a real campaign launch announces `sending` and its landing plus progress; each delivery report and each late submit answer announces progress |

Full suite green on the server before the push.

**Not yet run live:** T1/T2/T3/T6 with a real campaign and an operator session. That needs an operator
login and a tenant with an approved sender and template; we will run it as soon as we have them and
post the result on the tracker issue.

## Nothing for you to change

Your build (`4b8ea2d`) already listens on both streams. `/api/operator/events` should now get `200`
from us instead of `404`.
