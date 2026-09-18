# Postpaid credit on account — handoff to UI (18 Sep 2026)

Built to `BACKEND_REQUEST_postpaid-credit-on-account.md` against `openapi.json`
at `d55aa63`. All seven endpoints, the changed credit response, and the two new
`TenantDetail` fields. Ask 47 was left where it is: `GET /v1/operator/tenants/{id}/wallet`
still answers `{"balances":[…]}` and nothing more was built on it.

Your `src/mocks/invoices-state.ts` was the spec we checked ourselves against,
and it is right about seven of the eight rules. **Two things in it need to
change** — §A and §B below. Both are money, so please read those first.

---

## A. The due date must be computed in the tenant's timezone, not UTC

`monthEnd()` uses `getUTCMonth()`. A credit issued at **02:00 IST on 1 October**
is 20:30 UTC on 30 September, so a UTC month end falls due at **05:29 IST that
same morning**: an invoice raised on the 1st, due before breakfast.

Your own `Invoice` schema already defines `periodStart`/`periodEnd` as IST. Two
invoice types disagreeing about when a month ends is the kind of thing that only
surfaces in an argument with a customer.

We compute it in the tenant country's zone — `Asia/Kolkata` for IN, `Asia/Dubai`
for AE, `Europe/London` for GB, `America/New_York` for US, UTC for anything we
have no confirmed zone for.

Test that names it: `TestAMonthEndsInTheTenantsOwnTimezoneNotInUTC`.

## B. The credit limit must count money the tenant has already paid

Your check is `outstandingFor(...) + totalMinor > limit`. `outstandingFor` is
Σ`max(0, total − received)` and never sees `unappliedMinor`.

```
limit         ₹1,00,000
owes                  0
overpaid       ₹50,000    ← held, no invoice has claimed it
issue     ₹1,00,000 + 18% = ₹1,18,000

your check:  0 + 118,000 > 100,000   → refused
what happens: the held 50,000 settles it on the very next pass
              → 68,000 outstanding, comfortably inside the limit
```

So a tenant who paid in advance is refused credit they have already paid for.
The check is the one place in your design that does not use the derived view.
We use:

```
projected = max(0, outstanding + proposedTotal − unapplied)
```

Tests: `TestTheCreditLimitMeasuresWhatIsOwedAndCreditsWhatIsAlreadyPaid`,
and end-to-end `TestAnOverpayingTenantIsNotBlockedByTheirOwnCreditLimit`.

---

## What is live

### `POST /v1/operator/tenants/{id}/wallet/credit`

Answers **201 `IssueCreditResult`**, as you specified:

```json
{
  "entry":   { "type": "topup", "amountMinor": 5000000, "balanceAfterMinor": … },
  "invoice": { "number": "INV-2026-09-001", "taxableMinor": 5000000,
               "taxRatePercent": 18, "taxMinor": 900000, "totalMinor": 5900000,
               "receivedMinor": 0, "outstandingMinor": 5900000, "status": "due",
               "dueAt": "2026-09-30T23:59:59.999999+05:30", "note": …, "issuedBy": … }
}
```

- **Only `taxableMinor` reaches the wallet.** The tax is owed, not spendable —
  crediting the gross would hand the tenant sending power they never bought.
- `taxRatePercent` omitted uses the tenant country's rate (IN 18, GB 20, AE 5,
  US 0); a supplied `0` means no tax. Different answers, never conflated.
- The wallet movement and the invoice are **one transaction**. Credit with no
  invoice is money given away; an invoice with no credit is a bill for nothing.
- Past the credit limit → **422**, naming the shortfall:
  *"This would take Acme Retail INR 6200.00 past their INR 100000.00 credit
  limit. They already owe INR 99000.00."* Checked before any write.
- Same `reference` twice → **409**. Enforced by a unique index on
  `(tenant_id, reference)`, not a read-then-write: two operators pressing Credit
  at the same moment would both pass a check and both insert.

`reference` now means your own credit reference. The uniqueness that used to
live on the ledger's description string moved onto the invoice, where it belongs.

### The six new routes

| Route | Notes |
|---|---|
| `GET …/tenants/{id}/invoices` | `AccountInvoicePage`, newest first. `totals` over the **whole filtered set**, never the page. |
| `GET …/tenants/{id}/invoices/export` | Streamed CSV, same filter, operator columns appended. |
| `GET …/tenants/{id}/payments` | `TenantPaymentPage`, newest receipt first. |
| `POST …/tenants/{id}/payments` | 201 `TenantPayment` with its allocations. Duplicate UTR → 409. |
| `POST …/payments/{paymentId}/void` | 200. Reason 3–200 chars required (422), already void → 409. |
| `PUT …/tenants/{id}/credit-limit` | 200 `TenantDetail`. `null` removes the limit. |
| `GET /v1/billing/account-invoices` + `/export` | The customer's own, `note` and `issuedBy` always null. |

`TenantDetail` now carries `creditLimitMinor` and `creditLimitCurrency`, both
always present. The currency is the tenant country's own; setting a limit for a
country whose regime we have not confirmed answers 422 rather than guessing.

### How it is stored

Two tables hold facts — `account_invoices` (what was issued) and
`tenant_payments` (what arrived). **Nothing derived is stored**: no status
column, no `received_minor`, no allocations table. Status, received, outstanding,
totals and every allocation are computed on each read in
`internal/domain/billing/postpaid.go`.

That is the design you proposed and we agree with it. It is why an invoice turns
overdue at the instant its due date passes with no job to fail, and why voiding
is a single-column write with no unwinding logic.

Ceiling, named rather than optimised away: the settlement pass is
O(invoices × payments) per tenant per read. Fine well past the volumes you will
see this year; there is an index ready for when it is not.

### CSV

Customer columns, in order:

```
number,issuedAt,dueAt,status,currency,taxableMinor,taxRatePercent,
taxMinor,totalMinor,receivedMinor,outstandingMinor,reference
```

The operator's file is those plus `issuedBy,note` **appended**, so the
customer's file is a strict prefix. Money is minor units — a spreadsheet must be
able to sum the column. Built from the same rows the paged route serves, so the
file and the screen can never hold different sets; a test asserts the row count
equals the paged `total` under the same filter.

---

## Three small things on the contract

1. **The seven new paths have no `operationId`.** oapi-codegen derives names
   from the URL, so our handlers are called `GetV1OperatorTenantsIdInvoices` and
   `PostV1OperatorTenantsIdPaymentsPaymentIdVoid`. It works, but it welds our
   method names to your URL strings. `listTenantInvoices`, `exportTenantInvoices`,
   `listTenantPayments`, `recordTenantPayment`, `voidTenantPayment`,
   `setTenantCreditLimit`, `listAccountInvoices`, `exportAccountInvoices` would
   decouple us. Cheap for you, and we will re-run `make generate` on it.

2. **`TenantDetail.creditLimitCurrency` lists `null` inside its `enum`** as well
   as being `nullable: true`. The generator turns that into a real constant:

   ```go
   TenantDetailCreditLimitCurrencyLessThannil TenantDetailCreditLimitCurrency = "<nil>"
   ```

   Harmless today because we never emit it, but it is a valid enum member as far
   as any generated validator is concerned. Dropping `null` from the `enum` list
   and keeping `nullable: true` says the same thing without it.

3. **The `status` filter has no `overdue`.** The enum is `due|paid`, where `due`
   means "not fully settled". So the operator's most useful question — *who is
   late* — cannot be asked; they get `overdueMinor` in `totals` but no way to
   list the rows behind it, and filtering client-side breaks on page two.

   Our backend already accepts `?status=overdue` and answers correctly. Add it to
   the enum whenever you want it; nothing on our side needs to change.

---

## Checks we ran

- `internal/domain/billing/postpaid_test.go` — one test per rule in your §4,
  named after the rule. **Ten mutations, each turning exactly the test that names
  it red**: tax carved out instead of on top; month end in UTC; `partly_paid`
  checked before `overdue`; outstanding counting face value; overdue added to
  outstanding instead of being a subset; the credit limit ignoring money already
  paid; voided payments still settling; rupees settling dollar invoices; the
  customer projection trusting its caller; totals summed from the page.
- `internal/api/postpaid_test.go` — the HTTP surface, including that a refused
  credit, a refused payment and a refused void all leave every figure untouched.
- One that bit us and is worth you knowing: our unknown-field guard keys on the
  request path with **every** uuid segment replaced by `{id}`, so the void route
  registers as `…/payments/{id}/void`, not `{paymentId}`. Registered under the
  contract's spelling it silently never matched, and a void body carrying
  `voidedAt` answered 200 having ignored it.

## One thing we changed beyond the ask

`operator-admin credit-wallet` — the CLI door into the same act — was still
writing a bare ledger topup. Left alone it would have loaded a wallet with no
invoice: money given away that neither screen nor export would ever show. It now
goes through the same function the console does, prints the invoice number, and
the old single-purpose store function is deleted so there is no third way in.
