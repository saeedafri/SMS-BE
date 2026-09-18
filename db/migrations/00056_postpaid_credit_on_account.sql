-- +goose Up

-- Postpaid credit on account: a tenant's wallet is loaded before their money
-- arrives, and each load is an invoice they owe.
--
-- The structural decision, which the whole feature rests on: these two tables
-- hold ONLY what was issued and what arrived. Which invoice a payment settled,
-- how much an invoice has received, whether it is overdue, what a tenant owes
-- — none of it is stored. All of it is computed on every read from these rows
-- (internal/domain/billing/postpaid.go).
--
-- That is what makes an invoice turn overdue the instant its due date passes,
-- with no scheduled job that can fail and leave a screen stating the wrong
-- thing about money. It is also why voiding a payment needs no unwinding
-- logic: drop it from the pass and every dependent figure corrects itself.
-- Storing allocations beside the payments would give one number two homes, and
-- the day they disagree is a day about money.

-- What was issued. No status column and no received_minor: both are answers,
-- not facts.
CREATE TABLE account_invoices (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Human-facing, INV-YYYY-MM-NNN, what both sides quote in correspondence.
    number           text        NOT NULL,
    currency         text        NOT NULL CHECK (currency IN ('INR','USD','GBP','AED')),
    -- The credit that reached the wallet, before tax: the sending power bought.
    taxable_minor    bigint      NOT NULL CHECK (taxable_minor > 0),
    tax_rate_percent int         NOT NULL CHECK (tax_rate_percent BETWEEN 0 AND 100),
    -- Charged ON TOP of taxable_minor, never carved out of it. Loading 50,000
    -- means 50,000 reaches the wallet and the tenant owes 50,000 plus this.
    tax_minor        bigint      NOT NULL CHECK (tax_minor >= 0),
    total_minor      bigint      NOT NULL CHECK (total_minor > 0),
    issued_at        timestamptz NOT NULL DEFAULT now(),
    -- The end of the month the credit was issued in, in the TENANT'S timezone,
    -- unless the operator named a date. Computed in Go rather than here so that
    -- "end of month" means the same thing as Invoice.period_end.
    due_at           timestamptz NOT NULL,
    -- The operator's own reference for this credit — an invoice number, a PO.
    -- NOT a bank UTR: at the moment credit is issued no money has moved.
    reference        text        NOT NULL,
    -- Operator-only. Never served on the customer's route; withheld at the
    -- projection rather than trusted to each caller.
    note             text,
    issued_by        text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- The arithmetic the whole feature depends on, checked by the database so
    -- that no code path can write an invoice whose parts do not add up.
    CONSTRAINT account_invoices_total_adds_up CHECK (total_minor = taxable_minor + tax_minor)
);

-- A reference is issued to a tenant once, so an operator who presses the button
-- twice — or two operators booking the same credit at the same moment — issues
-- one invoice. This lives here rather than on wallet_ledger.description, where
-- 00055 put it: a money invariant keyed on a string prefix is one refactor away
-- from silently not holding.
CREATE UNIQUE INDEX account_invoices_reference ON account_invoices (tenant_id, reference);
CREATE UNIQUE INDEX account_invoices_number ON account_invoices (tenant_id, number);
-- The allocation pass reads every invoice for one tenant and currency in
-- settlement order. This is the index it walks.
CREATE INDEX account_invoices_settlement
    ON account_invoices (tenant_id, currency, due_at, issued_at, number);

-- What arrived. No allocations column: which invoices a payment settled is
-- derived, and a voided payment settles nothing without any row being deleted.
CREATE TABLE tenant_payments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    currency     text        NOT NULL CHECK (currency IN ('INR','USD','GBP','AED')),
    amount_minor bigint      NOT NULL CHECK (amount_minor > 0),
    -- The bank's transfer reference (UTR).
    reference    text        NOT NULL,
    -- When the money actually landed, per the bank — not when it was typed in.
    -- An operator reconciling a week of transfers enters them in whatever order
    -- they read the statement, and allocation follows the money, not the typing.
    received_at  timestamptz NOT NULL,
    recorded_at  timestamptz NOT NULL DEFAULT now(),
    recorded_by  text,
    -- A payment is never deleted. A mistyped UTR that simply vanished would
    -- leave a reconciliation nobody can follow.
    voided_at    timestamptz,
    void_reason  text,
    voided_by    text,
    CONSTRAINT tenant_payments_void_is_complete CHECK (
        (voided_at IS NULL AND void_reason IS NULL AND voided_by IS NULL)
        OR (voided_at IS NOT NULL AND void_reason IS NOT NULL))
);

-- One transfer is banked once. The duplicate guard is the reason a payment is
-- recorded once per transfer rather than once per invoice it settles.
CREATE UNIQUE INDEX tenant_payments_reference ON tenant_payments (tenant_id, reference);
CREATE INDEX tenant_payments_receipt
    ON tenant_payments (tenant_id, currency, received_at, recorded_at, id);

-- The most a tenant may OWE at one time. Null means no limit, which is a
-- deliberate act and not a default — hence nullable with no default rather than
-- a sentinel. The currency is the tenant country's own; a limit denominated in
-- money they are not billed in would cap nothing.
ALTER TABLE tenants ADD COLUMN credit_limit_minor    bigint
    CHECK (credit_limit_minor IS NULL OR credit_limit_minor BETWEEN 0 AND 1000000000);
ALTER TABLE tenants ADD COLUMN credit_limit_currency text
    CHECK (credit_limit_currency IS NULL OR credit_limit_currency IN ('INR','USD','GBP','AED'));
ALTER TABLE tenants ADD CONSTRAINT tenants_credit_limit_has_currency CHECK (
    (credit_limit_minor IS NULL AND credit_limit_currency IS NULL)
    OR (credit_limit_minor IS NOT NULL AND credit_limit_currency IS NOT NULL));

-- 00055 keyed "this transfer was credited once" on wallet_ledger.description
-- matching 'Bank transfer %'. `reference` now means the operator's own credit
-- reference, not a UTR, so that description is no longer true and the invariant
-- has a better home above. The ledger keeps its rows; only the index goes.
DROP INDEX wallet_ledger_transfer_reference;

ALTER TABLE account_invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_invoices FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON account_invoices USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON account_invoices FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
-- Additive, as 00019 established: open only while app.operator is set, which
-- only the operator pool ever sets. A tenant request is unaffected.
CREATE POLICY account_invoices_operator ON account_invoices
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

ALTER TABLE tenant_payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_payments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_payments USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON tenant_payments FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_payments_operator ON tenant_payments
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

GRANT SELECT, INSERT ON account_invoices TO sms_app;
-- UPDATE, because voiding writes voided_at on an existing row. No DELETE: a
-- payment is reversed, never removed.
GRANT SELECT, INSERT, UPDATE ON tenant_payments TO sms_app;

-- +goose Down
DROP TABLE tenant_payments;
DROP TABLE account_invoices;
ALTER TABLE tenants DROP CONSTRAINT tenants_credit_limit_has_currency;
ALTER TABLE tenants DROP COLUMN credit_limit_currency;
ALTER TABLE tenants DROP COLUMN credit_limit_minor;
CREATE UNIQUE INDEX wallet_ledger_transfer_reference
    ON wallet_ledger (tenant_id, description)
    WHERE entry_type = 'topup' AND description LIKE 'Bank transfer %';
