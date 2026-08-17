-- +goose Up

-- The DNS records an email sender must publish before it can send: SPF, DKIM
-- and DMARC. The contract carries these as SenderId.dnsRecords, and the
-- frontend's email sender screen renders one row per record with its own
-- verification state — so "verified" is per record, not per sender. A single
-- status column on sender_ids could not express "DKIM verified, DMARC still
-- pending", which is the normal middle state during onboarding.
CREATE TABLE sender_dns_records (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sender_id   uuid NOT NULL REFERENCES sender_ids(id) ON DELETE CASCADE,
    record_type text NOT NULL CHECK (record_type IN ('SPF', 'DKIM', 'DMARC')),
    host        text NOT NULL,
    value       text NOT NULL,
    status      text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'verified', 'failed')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- One record of each type per sender: publishing two DKIM rows for the
    -- same sender is a bug, not a second record.
    UNIQUE (sender_id, record_type)
);

CREATE INDEX sender_dns_records_sender ON sender_dns_records (sender_id);

ALTER TABLE sender_dns_records ENABLE ROW LEVEL SECURITY;

-- WITH CHECK as well as USING: USING governs SELECT, UPDATE and DELETE but NOT
-- INSERT, so a policy without it silently denies every insert.
CREATE POLICY sender_dns_records_tenant_isolation ON sender_dns_records
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

-- The app role owns no tables, so each new one needs its own grant.
GRANT SELECT, INSERT, UPDATE, DELETE ON sender_dns_records TO sms_app;

-- +goose Down
DROP TABLE sender_dns_records;
