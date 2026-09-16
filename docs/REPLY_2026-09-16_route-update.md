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

## 1. PATCH /v1/operator/routes/{id} — BLOCKED, and here is what on

> It's already in openapi.json

It isn't, as far as we can see. On `origin/master` at `dbdc6ea`, the contract has:

```
/v1/operator/routes            GET, POST
/v1/operator/routes/{id}       DELETE            ← no PATCH
/v1/operator/routes/{id}/move-up | move-down | enable | disable   POST
```

Three things referenced in your message are not pushed anywhere we can reach:

1. **`PATCH /v1/operator/routes/{id}` in `openapi.json`** — not on `master`, and
   not on any other branch on origin or upstream (we checked all of them).
2. **`docs/api-contract/BACKEND_REQUEST_route-update.md`** — not in the repo.
   We have not seen the full rules or the tests you expect.
3. **The `route-update` check in `scripts/check-backend-asks.cjs`** — the id
   does not exist, so `--only route-update` errors rather than going red.

Our handlers are generated from `openapi.json`, so the endpoint cannot exist
here until it exists there. Push those three and this is a short job — most of
it is already written, see below.

## What we built for item 1 anyway

The half that does not depend on the contract is in and tested:
`store.UpdateRouteCost` changes `cost_per_segment_minor` and nothing else, with
`TestRepricingARouteChangesTheCostAndNothingElse` asserting that country,
channel, carrier, priority, status, label and currency all come back unchanged.
A mutation that also bumped priority turns it red.

When the contract lands, what remains is: the generated handler, the four 422s
(unknown field, missing cost, negative cost, non-integer), `route.update` in the
audit log, and the `rejectUnknownFields` entry. Call it half a day.

## One thing worth agreeing on before we build it

Repricing does **not** reprice messages already sent — their cost was taken at
send time and is recorded on the message. So a corridor repriced mid-campaign
produces two prices in one campaign's billing, which is correct but will look
odd on a screen that shows one route and one price. If you want the console to
say something about that, tell us and we will surface the route's price history.

## Also, on your FYI

Videocon is live and bound (`103.153.58.105:4444`, transceiver, 10 TPS, healthy)
— and worth knowing before you add the first India SMS route: that connection
needed **no** protocol overrides at all, so its `protocol` is `{}`.
