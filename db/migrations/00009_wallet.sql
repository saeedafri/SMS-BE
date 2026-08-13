-- +goose Up
-- Every money column in this file is BIGINT minor units (paise, cents) paired
-- with a currency. There is no NUMERIC and no floating point anywhere in the
-- money path, by design: a float cent is a rounding bug waiting for volume.

CREATE TABLE wallet_balances (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    currency      text        NOT NULL CHECK (currency IN ('INR','USD','GBP','AED')),
    balance_minor bigint      NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, currency)
);

-- amount_minor is ALWAYS positive; the sign is implied by entry_type. Storing a
-- signed amount would let one row mean two different things depending on who
-- read it, and the contract is explicit that the API field is positive too.
CREATE TABLE wallet_ledger (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    currency            text        NOT NULL CHECK (currency IN ('INR','USD','GBP','AED')),
    entry_type          text        NOT NULL
                                    CHECK (entry_type IN ('topup','auto_recharge','charge','refund','adjustment')),
    amount_minor        bigint      NOT NULL CHECK (amount_minor > 0),
    balance_after_minor bigint      NOT NULL,
    description         text        NOT NULL DEFAULT '',
    campaign_id         uuid,
    campaign_name       text,
    journey_id          uuid,
    journey_name        text,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- Keyset pagination reads this index; OFFSET would degrade as the ledger grows.
CREATE INDEX wallet_ledger_page ON wallet_ledger (tenant_id, currency, created_at DESC, id DESC);

-- Append-only, enforced rather than assumed. A convention that only lives in
-- code review stops holding the first time someone writes a "quick fix" script
-- against the database directly.
-- +goose StatementBegin
CREATE FUNCTION reject_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger is append-only (attempted %)', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER wallet_ledger_append_only
    BEFORE UPDATE OR DELETE ON wallet_ledger
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TABLE payment_methods (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    brand      text        NOT NULL CHECK (brand IN ('visa','mastercard','amex')),
    last4      text        NOT NULL CHECK (last4 ~ '^[0-9]{4}$'),
    is_default boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- At most one default per tenant, enforced by the index rather than by the
-- handler remembering to clear the old one.
CREATE UNIQUE INDEX payment_methods_one_default
    ON payment_methods (tenant_id) WHERE is_default;

CREATE TABLE auto_recharge_configs (
    tenant_id           uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    currency            text        NOT NULL CHECK (currency IN ('INR','USD','GBP','AED')),
    enabled             boolean     NOT NULL DEFAULT false,
    threshold_minor     bigint      NOT NULL DEFAULT 0 CHECK (threshold_minor >= 0),
    topup_minor         bigint      NOT NULL DEFAULT 0 CHECK (topup_minor >= 0),
    payment_method_id   uuid REFERENCES payment_methods(id) ON DELETE SET NULL,
    last_failure_at     timestamptz,
    last_failure_reason text,
    PRIMARY KEY (tenant_id, currency)
);

CREATE TABLE invoices (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    currency         text        NOT NULL CHECK (currency IN ('INR','USD','GBP','AED')),
    period_start     timestamptz NOT NULL,
    period_end       timestamptz NOT NULL,
    status           text        NOT NULL DEFAULT 'open',
    subtotal_minor   bigint      NOT NULL DEFAULT 0,
    tax_rate_percent int         NOT NULL DEFAULT 0,
    tax_minor        bigint      NOT NULL DEFAULT 0,
    total_minor      bigint      NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX invoices_page ON invoices (tenant_id, created_at DESC, id DESC);

CREATE TABLE invoice_line_items (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id    uuid   NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    tenant_id     uuid   NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    description   text   NOT NULL,
    quantity      bigint NOT NULL,
    unit_minor    bigint NOT NULL,
    amount_minor  bigint NOT NULL
);

-- Rate cards are platform-wide, not tenant-owned, so no RLS here. Per-tenant
-- overrides arrive with the operator console in Stage 9.
CREATE TABLE pricing_rates (
    country           text   NOT NULL,
    channel           text   NOT NULL,
    category          text,
    per_segment_minor bigint NOT NULL CHECK (per_segment_minor >= 0),
    currency          text   NOT NULL,
    PRIMARY KEY (country, channel, category)
);

-- Reference rates so a fresh install can estimate a cost. Deliberately round
-- placeholder numbers, not a commercial claim — real cards land with the
-- operator console.
INSERT INTO pricing_rates (country, channel, category, per_segment_minor, currency) VALUES
    ('IN','SMS','',      12,  'INR'),
    ('IN','RCS','',      35,  'INR'),
    ('IN','WHATSAPP','', 80,  'INR'),
    ('US','SMS','',      75,  'USD'),
    ('US','RCS','',      200, 'USD'),
    ('GB','SMS','',      35,  'GBP'),
    ('AE','SMS','',      12,  'AED');

ALTER TABLE wallet_balances ENABLE ROW LEVEL SECURITY;
ALTER TABLE wallet_balances FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wallet_balances USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON wallet_balances FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE wallet_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE wallet_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wallet_ledger USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON wallet_ledger FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE payment_methods ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_methods FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_methods USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON payment_methods FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE auto_recharge_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE auto_recharge_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON auto_recharge_configs USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON auto_recharge_configs FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoices FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoices USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON invoices FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE invoice_line_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_line_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoice_line_items USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON invoice_line_items FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON wallet_balances TO sms_app;
-- No UPDATE or DELETE grant on the ledger: the trigger is the enforcement, and
-- withholding the privilege is the second lock on the same door.
GRANT SELECT, INSERT ON wallet_ledger TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON payment_methods TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON auto_recharge_configs TO sms_app;
GRANT SELECT, INSERT, UPDATE ON invoices TO sms_app;
GRANT SELECT, INSERT ON invoice_line_items TO sms_app;
GRANT SELECT ON pricing_rates TO sms_app;

-- +goose Down
DROP TABLE pricing_rates;
DROP TABLE invoice_line_items;
DROP TABLE invoices;
DROP TABLE auto_recharge_configs;
DROP TABLE payment_methods;
DROP TRIGGER wallet_ledger_append_only ON wallet_ledger;
DROP FUNCTION reject_ledger_mutation();
DROP TABLE wallet_ledger;
DROP TABLE wallet_balances;
