# Per-tenant daily send ceiling — handoff to UI (23 Sep 2026)

A commercial control we have not had: a ceiling on how much **volume** one
tenant may send in a day. `00040` already caps a tenant's send **rate**
(`throttledRatePerSecond`) to honour a carrier's contracted TPS. This is a
different thing — a tenant can sit comfortably under the rate ceiling all day
and still hand us a hundred thousand messages.

The backend half is **built, tested and live** — `18d683d` (the ceiling),
`93c4e05` (admins only), deployed and verified against the public API. No tenant
is capped yet, so nothing has changed for anybody: the feature is inert until an
operator sets a ceiling.

**Nothing in this is visible to a customer.** The second half — the operator
console control — needs four additions to `openapi.json`, which you own. They
are in §4, and §4.4 is the one that will fail your build if it is missed.

---

## 1. What it does

One nullable column, `tenants.send_cap_per_day`.

- **NULL means uncapped, and it is the default.** There is no allow-list table
  and no flag. A tenant with no cap is uncapped — one column instead of two
  states that can disagree. What we would have called "whitelisted" is simply
  the absence of a ceiling, so no migration touches the tenants you have not
  decided about.
- **Zero is a real ceiling** and means "send nothing". It is told apart from
  NULL on purpose: an operator stopping one tenant's bulk traffic without
  suspending their account is a thing that happens.

The ceiling spans the day, not the campaign. A per-campaign cut would be lifted
by splitting one send in two, which is the first thing anybody who noticed it
would try. The day is the **tenant's own** calendar day, via the same zone table
invoicing uses (`billing.BillingLocationFor`) — a UTC day would reset an Indian
tenant's allowance at 05:30 in the morning.

## 2. Where it bites, and where it deliberately does not

**The quote is clipped before the customer approves it.** `EstimateCampaign`
reduces the recipient count and the price to what the ceiling admits. This is
the part that matters most, and it is worth saying why: if the wizard promised a
hundred thousand, the campaign row recorded a hundred thousand, and seventy
thousand went out, then the campaign detail, the delivery rate and the invoice
would each report a different truth about the same send. Clipping the quote is
what keeps all three agreeing.

**A scheduled campaign is quoted against the day it will send**, not against
today. A tenant who has spent today's allowance and schedules something for next
week gets a full quote for next week — otherwise the wizard would tell them
their campaign reaches nobody, for a day it is not going to run on.

One consequence for the wizard: `POST /v1/campaigns/estimate` carries no
scheduled date, so its preview is quoted against **today**. The quote frozen on
the campaign at create time is the one that governs, and that one does know the
schedule. For a tenant with a ceiling who schedules ahead on a day they have
already spent, the preview can therefore read lower than the campaign they end
up with. If that bothers you, add `scheduledAt` to the estimate request body in
the same patch as §4 and I will use it.

**A withheld recipient is a non-event.** No message row, no wallet hold, no
ledger entry, no error code, nothing in the customer's message log — the same
non-event as a contact the channel cannot address. `sent` goes down; `failed`
does **not** go up. A ceiling that manufactured failures would wreck the
delivery rate the customer reads, on messages that were never attempted.

**We never charge for what we withheld.** That falls out of the design rather
than being enforced on top of it, and it is the line I would not cross: a
ceiling that took money for messages it decided not to send is not a margin
control, it is a billing defect. Test:
`TestACeilingSendsWhatItAdmitsAndChargesForNothingElse` asserts the wallet moves
by exactly the clipped size.

**`POST /v1/messages` is capped too**, or a loop around it would lift the whole
thing. See §3 — that one has a consequence for you.

**Verification codes are exempt.** `verify.go` does not call any of this. An OTP
withheld is a customer's own user locked out of their account, which is an
incident, not a margin.

## 3. The two things a customer can actually notice

I would rather you hear these from me than from a support ticket.

**(a) The recipient count drops with no line item next to it.** A customer
uploads a hundred thousand and the wizard says seventy. That screen currently
explains every drop it shows — suppressed, variable-skipped, unreachable. This
one will be a bare smaller number with no reason beside it. That is the actual
cost of the feature being secret, and there is no version of it that both hides
the ceiling and explains the gap. **Please do not invent a reason string for it
in the UI** — attributing it to suppression or reachability would put a specific
false statement on the screen, which is a different and worse thing than an
unexplained number.

**(b) The single-send refusal is a 429.** It reuses the *same* body the existing
send-rate budget returns, deliberately: a customer who can tell the two apart
can find the ceiling's size by bisecting the point it starts refusing.

The message is `"Too many sends right now. Retry in a moment."` — note it does
**not** point at `GET /v1/developer/rate-limit`, the way the existing
`rate_limited` message does, because that page would show them a budget they
have not exceeded. If you mirror this refusal anywhere in the dashboard, keep it
vague in the same way. `TestTheSingleSendPathIsCappedToo` fails if the words
"cap", "ceiling", "daily", "quota" or "allowance" appear in that response.

## 4. What we need from `openapi.json`

All three are operator-side. **Nothing goes on a tenant-facing schema** — not
`Campaign`, not `CampaignEstimate`, not `SendMessageResult`, not `Me`.

**4.0 All three are admin-only.** See §4.4 — the route and the fields both need
a `403`, and `GET /v1/operator/tenants/{id}` needs one it does not have today.

**4.1 `TenantDetail` gains three fields** (it is served only on
`/v1/operator/tenants/{id}`):

```jsonc
"sendCapPerDay": {
  "type": "integer", "minimum": 0, "nullable": true,
  "description": "Ceiling on messages accepted from this tenant per day, in their own timezone. Null means uncapped, which is the default. Zero means the tenant sends nothing. Operator-only: never served on a tenant's own routes."
},
"sendAcceptedToday": {
  "type": "integer",
  "description": "Messages let through today, against sendCapPerDay. Zero for an uncapped tenant, who is not metered at all."
},
"sendWithheldToday": {
  "type": "integer",
  "description": "Messages the ceiling took off today. Not derivable from anywhere else: a withheld recipient has no message row by design."
}
```

**4.2 A new operation**, mirroring `throttleTenant` exactly:

```
POST /v1/operator/tenants/{id}/send-cap
operationId: setTenantSendCap
body: SetSendCapRequest { perDay: integer|null (min 0, required), reason?: string }
200 -> TenantDetail   401, 403, 404
```

`perDay: null` lifts the ceiling. Lifting must write NULL rather than a very
large number, or we leave a ceiling nobody meant to keep.

**4.3 One audit action enum value: `tenant.send_cap`.**

**4.4 A `403` on `setTenantSendCap`, and a `403` added to
`GET /v1/operator/tenants/{id}`.**

The ceiling is for admins only — a plain `operator` may neither set it nor see
it. The backend gate is shipped (`requireOperatorAdmin`, `93c4e05`), but a
handler cannot answer 403 on an operation whose contract does not declare one,
so both routes need it or the gate cannot be applied.

The tenant-detail route needs one because of 4.1: the moment `TenantDetail`
carries `sendCapPerDay`, every route that returns it can leak the ceiling to a
non-admin. We will withhold the three fields at the projection for a plain
operator rather than 403 the whole page — an operator still has a job to do on
that screen — but the 403 has to exist in the contract for the route that
*sets* it, and the detail route needs one for the day something on it becomes
admin-only outright.

A test fails the build if either lands without it:
`TestTheSendCeilingNeverReachesANonAdmin` reads your `openapi.json` and refuses
any operation that serves or sets the ceiling without a declared 403.

**Please hide the control for non-admins in the console too.** Not because we
are trusting the UI for it — the server refuses regardless — but because a
button that 403s is worse than no button.

**One thing you should know before wiring this up:** `operator_users.role` has
existed since migration 00018 and, until today, **nothing in the backend read
it**. It is rendered on `GET /v1/operator/me` and enforced on no route at all.
So a plain `operator` can currently suspend a tenant, throttle one, credit a
wallet, void a payment and set a credit limit — none of those declare a 403
either. The send ceiling is the first thing gated on role. Whether the rest
should be is a decision for the operator team, not something we changed
underneath you.

Once those three land I will build the handler against them the same day — the
store layer underneath (`store.SetSendCap`, `store.ReadSendUsage`) is already
written and tested.

## 4a. Who may set it

`admin` only. It is the highest role `operator_users` has — the CHECK admits
`operator` and `admin` and nothing else — so "super admin and admin" is
expressed today as `role = 'admin'`. If you want a genuine third tier above
admin, say so: that is a migration, a contract change and a decision about who
holds it, not a condition we can add quietly.

The gate refuses anything that is not exactly `admin`, including an unknown
role. A gate that refused only the one name it knew about would open itself the
day a third role is added.

## 5. Until then

Operators set it from the box, which needs no contract:

```
operator-admin send-cap <tenant-uuid> 70000    # cap
operator-admin send-cap <tenant-uuid> none     # lift
operator-admin send-cap <tenant-uuid>          # report ceiling + today's usage
```

It prompts for confirmation and names the tenant before writing.

## 6. A known ceiling, stated

`sendWithheldToday` counts what was clipped off a page the fan-out had already
read. When the ceiling stops a campaign outright, the untouched tail of the list
is **not** counted — counting it would mean walking the rest of the list to
learn a number nothing depends on. So treat that figure as "at least this many",
not as an exact total. If the operator console wants it exact, say so and I will
add the walk behind the console's own read rather than on the send path.

## 7. Tests

| Test | Answers |
|---|---|
| `TestATenantWithNoCeilingMaySendEverything` | a missing cap never reads as zero |
| `TestAZeroCeilingIsNotTheSameAsNoCeiling` | NULL and 0 stay distinct |
| `TestACeilingAdmitsWhatIsLeftOfTheDay` | usage adds, it does not replace |
| `TestADayEndsAtTheTenantsOwnMidnight` | the day is theirs, not UTC's |
| `TestTheQuoteIsAlreadyClippedToTheCeiling` | the number approved is the number sent |
| `TestACeilingSendsWhatItAdmitsAndChargesForNothingElse` | the wallet moves by the clipped size |
| `TestASecondCampaignSeesWhatTheFirstOneSpent` | the ceiling spans campaigns |
| `TestTheSingleSendPathIsCappedToo` | the API loop is covered, and gives nothing away |
| `TestAScheduledCampaignIsQuotedAgainstTheDayItWillSend` | a future send is quoted against a future allowance |
| `TestAnUncappedTenantSendsAsBefore` | the default path is untouched |
| `TestOnlyAnAdminPassesTheOperatorAdminGate` | a plain operator is refused, and so is any role that is not exactly `admin` |
| `TestTheSendCeilingNeverReachesANonAdmin` | no tenant-facing schema carries it, and no route serves it without a 403 |
