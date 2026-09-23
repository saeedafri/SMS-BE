# Per-tenant daily send ceiling — handoff to UI (23 Sep 2026)

A commercial control we have not had: a ceiling on how much **volume** one
tenant may send in a day. `00040` already caps a tenant's send **rate**
(`throttledRatePerSecond`) to honour a carrier's contracted TPS. This is a
different thing — a tenant can sit comfortably under the rate ceiling all day
and still hand us a hundred thousand messages.

The backend half is built and tested, and ships with this commit. **Nothing in this is visible to
a customer**, and the second half — the operator console control — needs three
additions to `openapi.json`, which you own. They are in §4.

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

Once those three land I will build the handler against them the same day — the
store layer underneath (`store.SetSendCap`, `store.ReadSendUsage`) is already
written and tested.

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
