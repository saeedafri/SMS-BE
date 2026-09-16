# Reply: route update, duplicate labels, unknown connection — 16 Sep 2026

Items 2 and 3 are done and on `main`. Item 1 — the main ask — is blocked on
something on your side, and it is a small thing. Details below.

## 2. Duplicate route labels → 409 — DONE

A second route with the same label in the same country and channel now answers
409 `conflict`:

> That corridor already has a route with this label. The label is how an
> operator tells two paths apart, so give this one a different name.

The same label in a *different* corridor is still allowed — "Videocon direct" on
IN/SMS and on IN/RCS are different paths, and the contract's wording ("that
corridor") agrees.

Enforced by a unique index, `routes_corridor_label` on (country, channel,
label), not by a read-then-write — two operators adding the same label at the
same moment would both pass a check and both insert. Migration
`00054_route_label_unique_per_corridor.sql`. I checked production first: no
corridor holds duplicate labels today, so the index applies cleanly on deploy.

Note it is (country, channel), not (country, channel, carrier) — the same label
under two different carriers in one corridor is just as ambiguous on the screen
where an operator picks one to disable.

## 3. Unknown connectionId → 422 — DONE

Creating a route whose `connectionId` names nothing now answers 422
`validation_failed`:

> No connection has that id. Create the operator bind first, or leave
> connectionId out and attach it once the bind exists.

It was a foreign-key error reaching the handler as a 500, exactly as you said.

## 1. PATCH /v1/operator/routes/{id} — DONE

Built to the letter of §2, including the `rejectUnknownFields` entry.

| Rule | Answer |
|---|---|
| `{"costPerSegmentMinor": 7}` | 200, every other field unchanged |
| `{"costPerSegmentMinor": 0}` | 200 — zero is a value, not "unset" |
| `-1`, `1.5`, `{}`, no body | 422, nothing changes |
| Any other key, including a typo | 422 `Unknown field(s): …`, nothing changes |
| Unknown or malformed id | 404 "No such route." |
| Tenant token or none | 401 |
| Audit | one `route.update`: "Changed the cost of the Vi RCS Direct route from 47 to 48 INR per segment" |

Six tests cover your list, all red first. Three mutations turn them red:
dropping the `rejectUnknownFields` entry, accepting an absent cost, and
accepting a negative one.

One note on the first of those. Checking only the status code was not enough:
an unregistered path records no body keys, so the handler refused everything for
a different reason and the test stayed green with the guard removed. The test
now asserts the refusal NAMES the offending key.

**We owe you an apology on timing.** Your commit `86cf0d6` was on
`SAQIBJH/sms-platform-frontend`; we had fetched only the `saeedafri` fork and
read stale refs, and told you the contract was missing. It was there. We now
fetch both remotes before answering.

## One thing worth agreeing on before we build it

Repricing does **not** reprice messages already sent — their cost was taken at
send time and is recorded on the message. So a corridor repriced mid-campaign
produces two prices in one campaign's billing, which is correct but will look
odd on a screen that shows one route and one price. If you want the console to
say something about that, tell us and we will surface the route's price history.

## Verified on production

Deployed 00:24 IST, 17 Sep; migration 54 ran before the swap.

| Probe on the live API | Result |
|---|---|
| Index `routes_corridor_label` exists | `UNIQUE … (country, channel, label)` |
| `POST /v1/operator/routes` with an existing label | **409** `conflict`, with the message above |
| `POST /v1/operator/routes` with a made-up `connectionId` | **422** `validation_failed`, with the message above |
| Rows written by either probe | **0** |
| `check-backend-asks.cjs --only route-update` | **1/1 satisfied** against sms-api.saqibsaeed.cloud |
| PATCH `{"carrier":"AIRTEL"}` | **422** `Unknown field(s): carrier.` |
| PATCH `{"costPerSegmentMinor":-1}` | **422** "Cost per segment cannot be negative." |
| PATCH Vi RCS Direct 48 → 47 → 48 | **200** each time; priority 3, status active, label all unchanged |
| Audit rows for those two | both present, quoting the old and new cost |

Both probes were chosen so that nothing is created even if a guard had been
broken, and the row count confirms it.

## Also, on your FYI

Videocon is live and bound (`103.153.58.105:4444`, transceiver, 10 TPS, healthy)
— and worth knowing before you add the first India SMS route: that connection
needed **no** protocol overrides at all, so its `protocol` is `{}`.
