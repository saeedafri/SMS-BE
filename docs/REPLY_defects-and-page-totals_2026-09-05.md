# Reply — defects and page totals

**To:** Relay frontend
**Date:** 5 September 2026
**Status:** Sections 1 and 2 implemented. Section 4's two questions answered below.

---

## Section 1 — the 500 on `POST /v1/operator/senders/{id}/approve`

**Fixed.** Your diagnosis was right down to the mechanism.

Approve and reject share the code that decides a sender, but approve runs one
extra step first: a readiness check that refuses to approve an email sender whose
DNS is unverified or a voice sender whose caller ID is unverified. That check read
the sender row directly and returned its "no rows" error raw, so it never reached
the call that knows how to report a missing sender. Reject skips the check
entirely, which is why its 404 was correct.

The not-found is now mapped where it arises, so both verbs answer the same
`404 {"code":"not_found","message":"No such sender."}`, byte for byte.
`TestDecidingASenderThatDoesNotExistIs404WhicheverDecisionItIs` asserts the two
together so they cannot drift apart again.

## Section 2 — `total` on the wallet ledger and the invoice list

**Both implemented.** `GET /v1/wallet/ledger` and `GET /v1/billing/invoices` now
return `total` beside `nextCursor`.

Two details worth having:

- **The ledger's total follows the filter.** `?currency=INR` returns the count of
  INR entries, not of every entry. A footer that disagreed with the rows above it
  would be worse than no footer. Guarded by
  `TestTheLedgerTotalFollowsTheCurrencyFilter`.
- **The count is exact and cheap, and you should not label it approximate.** It is
  a plain `count(*)` inside the same transaction as the page, so the total and the
  rows describe one consistent state rather than two moments either side of a
  concurrent charge. Both tables are per-tenant behind row-level security, so the
  count is over one tenant's rows. If either ever grows enough to hurt we will
  tell you before it does, rather than silently switching you to an estimate.

We have added both to `openapi.json` in the union we generate from, exactly as
described in your Section 3 — please declare them on your side when convenient.

## Section 3 — the fields we shipped ahead of you

Noted, and **none of them will be removed.** The six `total` fields, the
`limit`/`nextCursor` on `GET /v1/support/tickets`, and the two `AuditAction`
values are all deliberate and all staying.

One addition you will want to declare at the same time: this batch adds
`campaign.pause`, `campaign.resume` and `campaign.cancel` to
`UserActivityEventType`. They are emitted by the three halt endpoints. Same
pattern as the `AuditAction` values — we emit, you declare.

## Section 4.1 — `refund`, holds, and whether the displayed balance is net

**1. Is `refund` only ever a released hold?**

Today, yes — every `refund` row in the ledger is a released hold, written by one
of three paths: a carrier refusing at submit, a delivery report saying the message
did not arrive, and the reconciler expiring a message the carrier never reported
on. There is no operator-initiated goodwill credit and no payment reversal, so
nothing else can produce one.

If you want those to be distinguishable later, the right time to split the type is
before the first one is issued, not after. We have no plan to issue either yet;
say the word and we will add `goodwill` and `reversal` as separate types rather
than overloading this one.

**2. Is the displayed balance net of holds?**

**Yes — there is no separate held pool.** This is the important part of the answer
and it means `heldMinor` would have nothing to report.

Our hold is not a reservation against a balance. It is a real debit, written the
moment before the message goes to the carrier, and `balance_after_minor` is the
spendable balance with that debit already applied. If the message is not
delivered, a second entry credits it back. So a customer with "₹1,000 of holds
outstanding" does not exist as a state: that ₹1,000 has already left the balance
they are shown, and either stays gone or comes back.

Concretely — a wallet at ₹1,000 sending one ₹0.12 message reads ₹999.88
immediately, not "₹1,000 with ₹0.12 held". Your wallet screen is therefore already
correct and needs no available-versus-total distinction.

**3. How long does a hold live?**

Bounded, and never indefinitely. Three things resolve it:

- a carrier rejection at submit releases it in the same request;
- a delivery report settles it when it arrives — delivered keeps the charge,
  undelivered releases it;
- for a message the carrier never reports on at all, the reconciler sweeps every
  15 minutes and expires anything past the validity window, releasing the hold and
  landing the message as `expired` rather than leaving it queued forever.

The third exists precisely so a lost report cannot leave a customer charged for a
message nobody can prove arrived. `expired` and `undelivered` are deliberately
different labels: "the carrier said it failed" and "the carrier never said
anything" are different facts to anyone debugging a route.

**So: no contract change needed here.** `refund` is the only missing enum member,
and it means exactly one thing.

## Section 4.2 — `Template.carrierRegistration`

**You already have the full schema — it is in PR #2**, as `CarrierTemplateRegistration`,
which is exactly why your `master` cannot see it and your probe found only one
observed value. Merging PR #2 gives you the declaration and this question
disappears. For completeness, that schema is:

```jsonc
"CarrierTemplateRegistration": {
  "required": ["status"],
  "properties": {
    "status":            { "enum": ["not_submitted","pending","approved","rejected"] },
    "vendor":            { "enum": ["airtel","vi"], "nullable": true },
    "carrierTemplateId": { "type": "string", "nullable": true },
    "rejectionReason":   { "type": "string", "nullable": true },
    "submittedAt":       { "type": "string", "nullable": true },
    "updatedAt":         { "type": "string", "nullable": true }
  }
}
```

Answering your specific questions against it:

- **The status vocabulary is those four**, and `status` is the only required key.
  `not_submitted` is the initial state and is not the same as absent: a template
  that has never been sent to a carrier is a real, renderable state.
- **It is ONE object, not a list — today.** A deployment is configured with a
  single RCS vendor (`RCS_VENDOR`), so a template has at most one carrier
  registration at a time, and `vendor` names which. You are right that Jio, Airtel
  and Vi approve separately; if we ever run more than one vendor at once this
  becomes a list. It is not one yet, and declaring a list now would mean shipping
  a shape with exactly one element for the foreseeable future.
- **It carries a rejection reason and both timestamps**, as above.
- **RCS only.** No other channel populates it. WhatsApp has its own approval model
  through Meta, which is not this field.

Ours and the carrier's approvals are separate gates and stay separate: a template
we approved and the carrier has not is refused at submit with
`carrier_template_not_approved`, distinct from `template_not_approved`, because
the two go to different people to fix.

## Section 5 — the open items

- **Signup now refuses a country with no operating regime.** `GB` and `AE` both
  return `422` and create nothing. A stub regime is refused for the same reason as
  an unknown one: it has no registration objects, so a sender could never be
  approved and the customer would discover that after onboarding. Guarded by
  `TestSignupRefusesACountryWeDoNotOperateIn`.
- **The campaigns/journeys envelope and `description` are still open.** They are
  real and we have not done them in this batch; they are a contract change on your
  side first.

**The two `API PROBE … DELETE ME` tenants are still there, deliberately.** We have
a standing instruction not to delete anything from the production database. They
are inert — neither can send, and the signup guard means no more can be created —
so we would rather leave them than set a precedent of deleting rows on request.
If you want them gone, say so explicitly and we will get it authorised first.
