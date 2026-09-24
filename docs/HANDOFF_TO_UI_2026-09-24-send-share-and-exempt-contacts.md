# Send cap, rebuilt: a share of each send, and contacts no cap may touch (24 Sep 2026)

**This supersedes `HANDOFF_TO_UI_2026-09-23-send-cap.md`.** That one described a
cap as an absolute number of messages per day, and a "whitelist" that meant a
tenant with no cap at all. Both were wrong about what the feature is for. If you
have not started on §4 of that document, stop — the contract ask has changed.

---

## 1. What was wrong, and what it is now

The old model capped a tenant at *N messages per day* and called an uncapped
tenant "whitelisted". The real requirement is two different things:

| | Old | Now |
|---|---|---|
| The cap | `send_cap_per_day`, an absolute daily count | **`send_cap_percent`, a share of each send** |
| The exemption | a whole tenant having no cap | **`contacts.always_send` — individual contacts no cap may withhold** |
| Who gets cut on a repeat | the same tail, every time | **least-recently-carried first** |

A share scales. One setting covers a send of a hundred and a send of a lakh, and
nobody re-derives a ceiling when a customer's list grows.

The exemption is the part that did not exist at all before. It is per **contact**:
the customer's own staff, the numbers a regulator samples, the accounts under
contract. A cap that can silently skip those is not a margin control, it is a
liability.

## 2. The arithmetic, exactly

A hundred reachable contacts, a 70% cap, fifteen of them exempt:

```
audience            100
share               70%  ->  70 carried, 30 withheld
of the 70:          15 exempt (all of them) + 55 chosen from the remaining 85
```

**The exempt count TOWARD the share, they are not added on top.** Seventy is what
the operator set, so seventy is what goes. The exemption decides *who* is in the
seventy, not how many there are.

The one case where the exemption overrides the number: when the exempt alone
exceed the share. Twenty exempt contacts under a 10% cap send twenty, not ten — a
guarantee a cap can override is not a guarantee.

Rounded **up**: 70% of three is three, not two. Rounding down at every page turns
a 70% cap into something materially lower over a long list, and the operator set
70 rather than 65.

## 3. Rotation, and why the record exists

`contacts.last_capped_send_at` records when a capped send last carried a contact.
The next send orders its candidates by that column, oldest first, nulls first.

Without it the same thirty contacts are cut on every send and **never receive
anything, ever** — invisible on every report, and the thing a customer eventually
notices before we do. With it, coverage spreads across repeated sends.

It records who was **selected**, not who was delivered to. A contact the gate
then refuses still had their turn; counting it otherwise would hand them the
front of the queue permanently.

## 3a. Who was NOT reached — `withheld_contacts`

`tenant_send_usage.withheld` counts what a cap took off. A count answers "how
much did this cost the customer" and cannot answer the question an operator
actually gets asked, which is **"did this particular number get the message?"**

That one has no other source. A withheld contact has no message row by design,
so nothing in ClickHouse knows the send ever considered them. Deriving it later
from the list is wrong the moment the list changes — a contact added afterwards
would read as having been withheld by a send that never saw them. So the
decision is recorded when it is made:

```
withheld_contacts (tenant_id, contact_id, campaign_id | journey_id, withheld_at)
```

Exactly one of `campaign_id` / `journey_id` is set, enforced by a CHECK. A
campaign that is paused and resumed re-reads the page it stopped on, so the
campaign rows are unique per (campaign, contact) — without that the operator
would read a number twice the truth.

Two limits worth stating rather than discovering:

- When the **daily** ceiling stops a fan-out outright, the page in hand is
  recorded but the untouched tail of the list is not. Treat the figure as "at
  least this many" in that case. The **share** cap has no such gap: it cuts
  every page it reads.
- The table grows with every capped send — a lakh-contact list at 70% adds
  thirty thousand rows a send. Comfortable for Postgres, but a tenant sending
  daily at that size will want a retention policy. There deliberately is none
  yet: deleting somebody's record of what we did not send is not a decision a
  migration should make quietly.

Operator read, from the box:

```
operator-admin withheld <campaign-uuid>
```

## 4. The invariant that has not changed, and must not

**Every number the customer sees is a number that actually happened.**

- The quote is clipped before they approve it, so they approve 70 and 70 is what
  goes out.
- They are billed for 70. Nothing is held or charged for a withheld contact.
- A withheld contact has no message row, so no error code and nothing in their
  log — the same non-event as a contact the channel cannot address.
- `sent` goes down; `failed` does **not** go up.

So please do not render a delivery count, a delivery rate or a progress bar from
anything other than what the API reports. If a screen would show "100 of 100
delivered" for a send that carried 70, that screen is wrong — the send really did
carry 70, and the customer really was billed for 70.

## 5. What we need from `openapi.json`

Smaller than the old §4, because the contact-level exemption stays off the
contract for now — see §6.

**5.1 `TenantDetail` gains one field** (operator-only, served only on
`/v1/operator/tenants/{id}`):

```jsonc
"sendCapPercent": {
  "type": "integer", "minimum": 0, "maximum": 100, "nullable": true,
  "description": "Share of each send this tenant may have, 0-100. Null means uncapped, which is the default. 100 is a cap somebody set deliberately and is not the same as null. Operator-only: never served on a tenant's own routes."
}
```

Keep `sendAcceptedToday` and `sendWithheldToday` from the previous document —
they still apply and now count what the share withheld too.

**5.2 A new operation**, mirroring `throttleTenant`:

```
POST /v1/operator/tenants/{id}/send-share
operationId: setTenantSendShare
body: SetSendShareRequest { percent: integer|null (0-100, required), reason?: string }
200 -> TenantDetail   401, 403, 404
```

`percent: null` lifts the cap. Lifting writes NULL rather than 100, so an
operator can tell "never capped" from "capped at everything".

**5.3 Audit action enum value: `tenant.send_share`.**

**5.3a Optional, and only if you want it on the console:** a read for who a send
did not reach.

```
GET /v1/operator/campaigns/{id}/withheld?page=&limit=
operationId: listWithheldContacts
200 -> { withheld: [{ msisdn, withheldAt, alwaysSend }], total }
401, 403, 404
```

Operator-only and admin-gated like everything else here. The store function
behind it (`store.ListWithheldForCampaign`) is already written and tested, so
this is a handler and nothing else. Skip it if the CLI is enough for now — we
would rather not add contract surface you are not going to build against.

**5.4 The `403`s, as before.** Admins only — a plain `operator` may neither set
the share nor see it. `TestTheSendCeilingNeverReachesANonAdmin` fails our build
if any route serves or sets it without one, and
`GET /v1/operator/tenants/{id}` still needs a 403 it does not have.

**Nothing goes on a tenant-facing schema** — not `Campaign`, not
`CampaignEstimate`, not `Contact`, not `Me`.

## 6. Why `alwaysSend` is NOT on the contract

It is a flag on `contacts`, and `Contact` is a **tenant-facing** schema. A
customer opening their own contact list and finding a flag they never set is the
one thing that gives the whole mechanism away.

So the exemption is operator-side only, and for now it is set from the box rather
than from the console:

```
operator-admin always-send <tenant-uuid> <msisdn> on|off
operator-admin send-share  <tenant-uuid> <percent|none>
operator-admin send-cap    <tenant-uuid> <n|none>     # the daily ceiling, still there
```

If the operator console should manage exemptions, that needs its own operator
route — `GET/POST /v1/operator/tenants/{id}/exempt-contacts` or similar —
returning msisdns rather than reusing `Contact`. Tell us if you want it and we
will spec it properly rather than bolting a field onto a schema the customer
reads.

## 7. The daily ceiling is still there

`send_cap_per_day` was not removed. It is NULL for everyone and costs nothing,
and it is the only thing that bounds `POST /v1/messages` — a share of one message
is not a meaningful quantity, so a share cap alone would leave a loop around the
single-send API uncapped. The two compose: a send is cut to its share, and what
survives still has to fit inside what is left of the day.

## 8. Tests

| Test | Answers |
|---|---|
| `TestSeventyPercentOfAHundredIsSeventy` | the headline arithmetic |
| `TestTheExemptAreCarriedAndCountTowardTheCap` | 15 exempt inside 70, not on top of it |
| `TestTheExemptGoEvenWhenTheyExceedTheCap` | the guarantee overrides the number |
| `TestTheLeastRecentlyCarriedGoFirst` | rotation order |
| `TestAZeroShareStillCarriesTheExempt` | 0% is not an exception to the exemption |
| `TestTheShareRoundsUp` | 70% of 3 is 3 |
| `TestACappedSendCarriesItsShareAndEveryExemptContact` | end to end, incl. the wallet moving by 70 not 100 |
| `TestARepeatedCappedSendReachesDifferentPeople` | two sends over one list reach all ten |
| `TestAWithheldContactIsNamed` | the withheld are recorded by identity, once each, and never an exempt one |
| `TestCarriedAndWithheldTogetherAccountForEveryone` | the two records add up to the whole audience |
