# Stage 3 — Money Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wallet, ledger, top-up, payment methods, auto-recharge, pricing, invoices and usage work end to end against the real UI, with an append-only ledger whose sum always equals the materialised balance.

**Architecture:** An append-only `wallet_ledger` enforced by trigger, with `wallet_balances` materialised in the same transaction as every entry so reads are O(1). Money is `BIGINT` minor units everywhere. Payment capture sits behind a `PaymentGateway` interface whose only implementation today records a manual/simulated capture — a real provider is a new file, not a rewrite.

**Tech Stack:** unchanged — Go 1.26, chi, pgx, Postgres with RLS

**Spec:** `../SMS-UI/openapi.json`

## Global Constraints

- Everything from Stages 0–2 still applies.
- **Money is `int64` minor units paired with a currency. No floats, no `NUMERIC`, anywhere** — not in Go, not in the schema, not in a test fixture.
- **The ledger is append-only, enforced by a trigger**, not by convention. `UPDATE` and `DELETE` on `wallet_ledger` must raise.
- **`sum(ledger) == wallet_balances.balance_minor` is an invariant with a test that can fail.** Every operation that moves money asserts it afterwards.
- `LedgerEntry.amountMinor` is **always positive**; the sign is implied by `type` (`charge` debits, `topup`/`auto_recharge` credit). Storing a signed amount would let the same entry mean two things.
- Balances are per `(tenant, currency)`. A tenant may hold several.
- Pagination is keyset on `(created_at, id)`, never `OFFSET`.
- India GST is **18%** on INR invoices; every other currency is **0** — the contract says so explicitly, and no other country's tax rules are modelled.

## Payment gateway decision

No provider is wired up. `POST /v1/wallet/topup` records a **manual capture**: the ledger entry
is written and the balance moves, which is exactly right for bank-transfer and invoice-paid
customers, and is a real business model rather than a stub.

`internal/domain/billing.PaymentGateway` has one method, `Capture`. Adding Razorpay or Stripe
means one new file implementing it plus a config switch. Building this now behind an interface
costs an afternoon; retrofitting it after the ledger is live costs a migration.

## Operations in scope (14)

Wallet (9): `listWalletBalances`, `listLedger`, `topUpWallet`, `listPaymentMethods`,
`addPaymentMethod`, `removePaymentMethod`, `setDefaultPaymentMethod`, `listAutoRecharge`,
`updateAutoRecharge`
Billing (4): `estimateCost`, `listInvoices`, `getInvoice`, `getUsage`
Pricing (1): `listPricing`

---

### Task 1: Ledger schema with an append-only trigger

**Files:** `db/migrations/00009_wallet.sql`; extend `internal/store/tenant_isolation_test.go`

- [ ] **Step 1: Write the migration** — `wallet_balances (tenant_id, currency)` PK; `wallet_ledger` append-only; `payment_methods`; `auto_recharge_configs`; `invoices` + `invoice_line_items`; `pricing_rates` (global, no tenant). All tenant-owned tables get RLS `USING` **and** `FOR INSERT WITH CHECK`.

- [ ] **Step 2: Write the append-only trigger**

```sql
CREATE FUNCTION reject_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger is append-only (attempted %)', TG_OP;
END;
$$;
CREATE TRIGGER wallet_ledger_append_only
    BEFORE UPDATE OR DELETE ON wallet_ledger
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();
```

- [ ] **Step 3: Test that the trigger fires** — an `UPDATE` and a `DELETE` against a seeded row must both error. A trigger nobody has seen reject anything is not a control.

- [ ] **Step 4: Extend the isolation test** to the five new tenant-owned tables. Commit.

---

### Task 2: Ledger domain — the money invariant

**Files:** `internal/domain/billing/ledger.go`, `ledger_test.go`; `internal/store/wallet.go`

**Interfaces:** `billing.EntryType`; `store.AppendLedgerEntry(ctx, pool, identity, entry) (LedgerEntry, error)`; `store.WalletBalances(...)`; `store.LedgerPage(ctx, ..., currency, cursor, limit)`

- [ ] **Step 1: Write the failing tests** — a credit raises the balance and a charge lowers it; `balanceAfterMinor` on each entry matches the running total; a charge that would overdraw is refused and writes nothing; concurrent appends to one wallet stay consistent (run 20 goroutines, assert `sum(ledger) == balance`); keyset pagination returns every entry exactly once across pages with a stable order; `nextCursor` is null on the final page.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

The balance update and the ledger insert happen in **one** transaction with `SELECT … FOR UPDATE`
on the balance row. Without the lock, two concurrent charges both read the same balance and the
second overwrites the first — money silently invented.

---

### Task 3: Wallet endpoints

**Files:** `internal/api/wallet.go`, `wallet_test.go`

- [ ] **Step 1: Write the failing tests** — balances start empty; a top-up creates the wallet and returns a `LedgerEntry` with positive `amountMinor` and type `topup`; a top-up naming an unknown payment method → 422; a top-up of zero or a negative amount → 422; `member` role → 403 on every mutation; ledger pagination; payment method add/list/delete/set-default with exactly one default at all times; deleting the default promotes another; deleting the last one leaves none; auto-recharge round-trips and rejects `enabled: true` with no payment method.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

---

### Task 4: Pricing and cost estimation

**Files:** `internal/domain/billing/pricing.go`, `pricing_test.go`; `internal/api/pricing.go`

- [ ] **Step 1: Write the failing tests for segment counting** — GSM-7 bodies segment at 160 chars, then 153 per segment when concatenated; a body containing any non-GSM-7 character switches to UCS-2 at 70, then 67; boundaries tested exactly at 160/161 and 70/71. This is the arithmetic every cost estimate depends on, so it gets its own tests.

- [ ] **Step 2: Write the failing tests for `estimateCost`** — cost equals `recipients × segments × perSegmentMinor`; an unknown country/channel pair → 422; `costMinorMin == costMinorMax` when no fallback is possible; the currency matches the country's regime.

- [ ] **Step 3–5: Run (fail), implement, run (pass), commit.**

---

### Task 5: Invoices and usage

**Files:** `internal/api/billing.go`, `billing_test.go`

- [ ] **Step 1: Write the failing tests** — a new tenant has no invoices and gets an empty page with `nextCursor: null`; an invoice's `totalMinor == subtotalMinor + taxMinor`; INR invoices carry `taxRatePercent: 18` and every other currency `0`; a cross-tenant invoice id → 404; usage returns all three required groupings as empty arrays for a new tenant.

- [ ] **Step 2–4: Run (fail), implement, run (pass), commit.**

---

### Task 6: Contract validation and UI verification

- [ ] **Step 1** All 14 operations into `contract_test.go`, success and failure shapes.
- [ ] **Step 2** Extend `e2e-check.sh`: top up, then assert the balance appears on `/billing`, and that the ledger entry renders.
- [ ] **Step 3** `pnpm test` + `pnpm typecheck`.
- [ ] **Step 4** Prove the validator still catches drift.
- [ ] **Step 5** Update `ROADMAP.md`; commit.

---

## Stage 3 exit criteria

- [ ] `make check` green; 14 operations contract-validated
- [ ] Ledger `UPDATE`/`DELETE` provably rejected by the trigger
- [ ] `sum(ledger) == balance` holds under concurrent writes
- [ ] No float or `NUMERIC` anywhere in the money path
- [ ] Overdraft refused, writing nothing
- [ ] Billing screens render live data with mocks off
- [ ] SMS-UI's own tests and typecheck still pass
