# Operator wallet credit — handoff to UI (17 Sep 2026)

Operators can now recharge any tenant's wallet from the console. Backend is
built, tested and deployed. The UI needs one button and one dialog.

## 1. Contract — please commit this first

The endpoint is **not yet in your `openapi.json` on master**. We added it to our
local copy to generate the backend, and the exact 58-line change is in
`SMS-BE/docs/contract-patches/2026-09-17-operator-wallet-credit.patch`:

```bash
cd sms-platform-frontend
git apply ../SMS-BE/docs/contract-patches/2026-09-17-operator-wallet-credit.patch
```

It adds four things, nothing else:
- path `GET /v1/operator/tenants/{id}/wallet` (`operationId: getTenantWallet`)
- path `POST /v1/operator/tenants/{id}/wallet/credit` (`operationId: creditTenantWallet`)
- schema `CreditTenantWalletRequest`
- `"wallet.credit"` in the `AuditAction` enum

If you want a different shape, tell us before you build the screen.

## 2. Reading the balance first

```http
GET /v1/operator/tenants/{tenantId}/wallet
Authorization: Bearer <operator token>
```

Answers **200** with one entry per currency the tenant holds, ordered by currency:

```json
[{ "currency": "INR", "balanceMinor": 250000 }]
```

A tenant that has never held money answers `[]` — not 404. **401** without an
operator token, **404** for an unknown tenant. It is the same number the tenant
sees at `GET /v1/wallet/balances`.

Show this in the Credit dialog so the operator sees the balance before and
after. There is no wallet figure on `TenantDetail`, so this is the only
operator-side read.

## 3. The credit endpoint

```http
POST /v1/operator/tenants/{tenantId}/wallet/credit
Authorization: Bearer <operator token>
Content-Type: application/json

{
  "currency": "INR",
  "amountMinor": 5000000,
  "reference": "UTR123456789012",
  "note": "Paid against invoice 42"
}
```

| Field | Rules |
|---|---|
| `currency` | required — `INR`, `USD`, `GBP` or `AED` |
| `amountMinor` | required — whole number, **1 to 1,000,000,000** (paise for INR, so ₹0.01 to ₹1 crore). `5000000` = ₹50,000.00 |
| `reference` | required — bank UTR or invoice number, **6 to 64 characters** |
| `note` | optional, max 200 chars — audit log only, the tenant never sees it |
| anything else | **422** `Unknown field(s): …` |

Available to **every** operator (admin and operator roles).

## 4. Responses

| Status | When | Body |
|---|---|---|
| **201** | Credited | a `LedgerEntry` — `type: "topup"`, `amountMinor`, `currency`, `balanceAfterMinor`, `description: "Bank transfer <reference>"` |
| **401** | No operator token, or a tenant token | `Sign in to the operator console.` |
| **404** | Tenant id unknown or malformed | `No such tenant.` |
| **409** | This reference was already credited to this tenant | `Reference UTR… has already been credited to <tenant name>.` |
| **422** | Validation (see table above) | message names the field |

## 5. Why the reference matters — build the UI around it

**The same reference can be credited to a tenant only once.** That makes the
button safe to double-click, safe on a flaky network retry, and safe when two
operators book the same transfer at the same moment (enforced by a database
unique index, not just a check).

So on **409**, show the message as information ("already credited"), not as an
error to retry — the money is already in.

## 6. Suggested UI

- **Where:** tenant detail page in the operator console → **Credit wallet** button.
- **Dialog fields:** currency (default the tenant's country currency), amount in
  rupees (convert to paise: `Math.round(rupees * 100)`), reference, optional note.
- **Confirm step before sending** — show the tenant name and formatted amount:
  *"Credit ₹50,000.00 to Acme Retail, reference UTR123456789012?"*. There is no
  undo on this endpoint.
- **After 201:** show the new balance from `balanceAfterMinor`, refresh the
  tenant's wallet/ledger views.
- The audit log filter can now include **`wallet.credit`**; each row names the
  operator, the tenant, the reference (as the target) and the amount + note.

## 7. What the tenant sees

`GET /v1/wallet/balances` goes up immediately. `GET /v1/wallet/ledger` shows a
`topup` entry described **"Bank transfer <reference>"**. The note is not shown.

## 8. Same logic as the CLI

`operator-admin credit-wallet <owner-email> <currency> <amount> <reference>`
now books through the same backend function, so a transfer credited from the
server can't be credited again from the console, and vice versa.

## 9. Tests (backend)

All four failed before the change (501) and pass after:

- operator credits → 201, tenant balance and ledger show it, `wallet.credit` audit row exists
- same reference twice → 409, balance moves once
- zero, negative, over ₹1 crore, missing/unknown currency, short/missing reference, unknown field → 422, balance unchanged
- tenant token → 401; unknown tenant → 404
- operator reads the balance: `[]` for a new tenant, the credited amount after, the same number the tenant sees, 401 for a tenant token, 404 for an unknown tenant

Mutations that turn them red: removing the duplicate-reference mapping, and
removing the unknown-field guard.
