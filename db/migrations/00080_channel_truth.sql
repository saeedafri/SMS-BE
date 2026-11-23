-- +goose Up

-- Which channels the operator has switched on for a tenant. No row means the
-- channel's default (SMS and RCS on, every other channel off), so a tenant
-- created before this table, or one nobody has touched, keeps sending exactly
-- as it did. A send goes out only when the channel is live on this deployment
-- AND switched on here.
CREATE TABLE tenant_channels (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel    text        NOT NULL CHECK (channel IN ('SMS', 'RCS', 'WHATSAPP', 'EMAIL', 'VOICE')),
    enabled    boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, channel)
);

ALTER TABLE tenant_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_channels FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_channels_isolation ON tenant_channels
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_channels_operator ON tenant_channels
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_channels TO sms_app;

-- The last go-live check of each channel, as the operator console shows it.
-- Platform-wide, not per tenant: it is a fact about this deployment.
CREATE TABLE channel_checks (
    channel    text        PRIMARY KEY CHECK (channel IN ('SMS', 'RCS', 'WHATSAPP', 'EMAIL', 'VOICE')),
    checked_at timestamptz NOT NULL,
    passed     boolean     NOT NULL
);
GRANT SELECT, INSERT, UPDATE ON channel_checks TO sms_app;

-- Messages the sandbox carried and the tenant paid for, refunded by
-- cmd/sandbox-refund (decision D-A: compensate, never rewrite the ledger).
-- The primary key is what makes a second run refund nothing twice.
CREATE TABLE sandbox_refunds (
    message_id   uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    amount_minor bigint      NOT NULL CHECK (amount_minor > 0),
    currency     text        NOT NULL,
    refunded_at  timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE sandbox_refunds ENABLE ROW LEVEL SECURITY;
ALTER TABLE sandbox_refunds FORCE  ROW LEVEL SECURITY;
CREATE POLICY sandbox_refunds_isolation ON sandbox_refunds
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT ON sandbox_refunds TO sms_app;

-- +goose Down
DROP TABLE sandbox_refunds;
DROP TABLE channel_checks;
DROP TABLE tenant_channels;
