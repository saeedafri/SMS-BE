# Reply — GSTIN capture and a compliant Indian tax invoice

**To:** Relay frontend
**Date:** 5 September 2026
**Status:** **Nothing implemented, deliberately.** You asked us to read and reply
before you merge anything, and two of your four questions are not engineering's to
answer. Below: the two we can answer with facts, a recommendation on the third,
and the one that has to be routed to a human.

---

## Why nothing is built

You were right to hold the contract. Your Section 4 question — tax at top-up or
tax at usage — decides the *shape* of everything else, and building `TaxComponent`
against the wrong answer means unwinding it after real money has moved. We agree
with your instinct that this is the cheap moment to decide, and the cheap moment
lasts exactly as long as nobody has paid us.

## Question 3 — do we already store a supplier tax identity? **No.**

Checked against production directly. The only tax-shaped columns anywhere in the
database are:

```
invoices.tax_rate_percent
invoices.tax_minor
```

There is no GSTIN, no legal name, no registered address, no state code, for the
supplier or for any customer. `grep -i gstin` over the schema returns nothing, which
matches what you found in `openapi.json`.

So the supplier identity has nowhere to live and you are right that it does not
belong in the frontend. Our recommendation: it is **configuration, not data** —
one entity, changing perhaps once a year, and it must not be editable through any
customer-facing surface. Environment or a single-row settings table, read at
invoice generation. We will build it that way unless you disagree.

Also confirming your reading of the current model: `tax_rate_percent` is an integer
hardcoded by currency — literally `return 18` for INR and `0` otherwise. It is a
blended rate with no place of supply behind it. There is **1 invoice** in
production, so there is essentially nothing to migrate.

## Question 2 — is the serial per-tenant, per-place-of-business, or global?

**Our recommendation: per financial year, per supplier place of business — which
today means one series, because we have one place of business.**

Reasoning, since you will have to live with it:

- **Not per-tenant.** The serial is *ours*, not the customer's. It identifies the
  invoice in our books and in our GSTR-1 filing; a customer having their own
  gapless sequence would be meaningless to the tax authority and would make our
  own return impossible to assemble.
- **Not global-forever.** Rule 46 wants uniqueness within a financial year, and
  India's financial year is 1 April – 31 March. A series that never resets is legal
  but makes reconciliation harder every year.
- **Per place of business** is the dimension that will eventually matter, and
  costs nothing to design in now: a `series` column that is currently one value.

On the allocation itself — you are right that it is the hard part, and we would
build it as a counter row taken under `SELECT … FOR UPDATE` in the same transaction
that writes the invoice, so a rollback releases the number and two concurrent
generations cannot take the same one. That is the same lock pattern the wallet
already uses for balances, which has held under measured concurrent load. And yes:
`Invoice.id` and `Invoice.invoiceNumber` stay different values with different
rules, and a cancelled invoice gets a credit note rather than a renumbering.

## Question 1 — tax at top-up or tax at usage? **This is not ours to decide.**

We can tell you what the system currently implies and what it would cost either
way, but the answer is a finance decision and we are not going to make it by
default.

**What the code implies today:** `Invoice` is period-shaped (`periodStart`,
`periodEnd`) and `POST /v1/wallet/topup` writes a pure balance movement carrying no
tax. That is tax-at-usage, and your proposal matches it.

**What tax-at-top-up would cost:** receipt vouchers on every top-up, `TaxComponent`
on the wallet ledger as you say, and a materially harder refund path — a refunded
advance means adjusting tax already declared. Our hold-and-release model makes this
worse than it sounds: every undelivered message credits money back, so a
tax-at-top-up model has tax consequences on a path that fires thousands of times a
day.

**So our engineering recommendation is tax-at-usage** — it matches the existing
shape, it keeps tax off the high-frequency path, and it is what your proposal
assumes. **But it needs signing off by whoever owns the filing**, because getting
it wrong is not a bug we can fix in code.

## Question 4 — the SAC code. **Routing this, not absorbing it.**

You said this is a finance answer that someone must own, and you are right. We do
not know it and we are not going to guess: an invoice issued under the wrong SAC is
wrong in a way that is expensive and slow to correct, and it is the same class of
mistake as inventing a DLT registration id.

For whoever picks it up, the question is narrow: *under which SAC heading do we
bill A2P messaging services supplied to Indian businesses, and is the rate 18%
across all our lines?* Telecommunications services generally sit under SAC 9984,
and there are several sub-headings; picking between them is the bit that needs
someone accountable.

**Both Question 1 and Question 4 are blocked on the same person.** They should go
together in one ask.

## What we will build, once 1 and 4 come back

Your Section 2 shapes look right to us and we would implement them close to as
written. Three notes:

1. **`GET /v1/tenant` does not exist** — you spotted it, and it is a real hole: a
   GSTIN that can be written and never read back is worse than no GSTIN. It is a
   small addition and we can ship it ahead of the rest if you want to build the
   Settings form early.
2. **We agree GSTIN must not block transacting.** `kind: "NONE"` is a first-class
   answer, and an unregistered customer must sign up and send exactly as today.
3. **Server-side GSTIN validation, including the checksum**, not just format. We
   will implement it; your `validateIndiaPan` pattern is the right shape and the
   PAN is embedded in characters 3–12 of the GSTIN anyway, so the two checks share
   most of their logic.

Money stays integer minor units throughout. No floats on this path — agreed, and it
is already how every amount in the system is stored.

## Summary

| Question | Answer |
| --- | --- |
| 1. Tax at top-up or at usage? | **Blocked — finance.** We recommend at usage. |
| 2. Serial scope? | **Per financial year, per place of business.** One series today. |
| 3. Supplier tax identity stored? | **No — nothing exists.** We propose configuration, not data. |
| 4. SAC code? | **Blocked — finance.** Routed, not absorbed. |

Nothing merges until 1 and 4 come back. Ask us for anything that would help you
get them answered.
