-- +goose Up

-- A complaint suppresses the address too, under its own reason.
ALTER TABLE suppressions DROP CONSTRAINT suppressions_reason_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_reason_check
    CHECK (reason IN ('opted_out_keyword', 'manual', 'hard_bounce', 'imported_dnc', 'complaint'));

-- An email sender publishes three DKIM CNAMEs, the custom MAIL FROM MX and SPF,
-- and a DMARC record that is advised but never required. One row per host and
-- kind, in the order the wizard shows them.
ALTER TABLE sender_dns_records DROP CONSTRAINT sender_dns_records_record_type_check;
ALTER TABLE sender_dns_records ADD CONSTRAINT sender_dns_records_record_type_check
    CHECK (record_type IN ('SPF', 'DKIM', 'DMARC', 'MAIL_FROM_MX', 'MAIL_FROM_SPF'));
ALTER TABLE sender_dns_records DROP CONSTRAINT sender_dns_records_sender_id_record_type_key;
ALTER TABLE sender_dns_records ADD CONSTRAINT sender_dns_records_one_per_host
    UNIQUE (sender_id, record_type, host);
ALTER TABLE sender_dns_records
    ADD COLUMN dns_type        text CHECK (dns_type IN ('CNAME', 'TXT', 'MX')),
    ADD COLUMN priority        integer,
    ADD COLUMN required        boolean NOT NULL DEFAULT true,
    ADD COLUMN position        integer NOT NULL DEFAULT 0,
    ADD COLUMN last_checked_at timestamptz;

-- The custom MAIL FROM domain an email sender uses, and when its identity was
-- last read from the provider.
ALTER TABLE sender_ids
    ADD COLUMN mail_from_domain text,
    ADD COLUMN email_checked_at timestamptz;

-- A tenant's email standing: an operator pause, the provider's enforcement,
-- or a reinstatement. Only enabled and reinstated may send.
ALTER TABLE tenants ADD COLUMN email_sending_status text NOT NULL DEFAULT 'enabled'
    CHECK (email_sending_status IN ('enabled', 'paused', 'enforced', 'reinstated'));

-- The opaque token in a marketing email's unsubscribe link. Read with no
-- session, through the operator pool, so it carries no RLS policy for tenants.
CREATE TABLE email_unsubscribe_tokens (
    token      text        PRIMARY KEY,
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
ALTER TABLE email_unsubscribe_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE email_unsubscribe_tokens FORCE  ROW LEVEL SECURITY;
CREATE POLICY email_unsubscribe_tokens_isolation ON email_unsubscribe_tokens
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY email_unsubscribe_tokens_operator ON email_unsubscribe_tokens
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());
GRANT SELECT, INSERT, DELETE ON email_unsubscribe_tokens TO sms_app;

-- Provider events already applied (SES events, WhatsApp statuses). A
-- redelivered event finds its id here and changes nothing: one suppression,
-- consent write or settlement per event, ever.
CREATE TABLE provider_event_receipts (
    event_id    text        PRIMARY KEY,
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    received_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE provider_event_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_event_receipts FORCE  ROW LEVEL SECURITY;
CREATE POLICY provider_event_receipts_isolation ON provider_event_receipts
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT ON provider_event_receipts TO sms_app;

-- An email campaign's open and click tracking, {opens, clicks}; null elsewhere.
ALTER TABLE campaigns ADD COLUMN email_tracking jsonb;

-- +goose Down
ALTER TABLE campaigns DROP COLUMN email_tracking;
DROP TABLE provider_event_receipts;
DROP TABLE email_unsubscribe_tokens;
ALTER TABLE tenants DROP COLUMN email_sending_status;
ALTER TABLE sender_ids DROP COLUMN email_checked_at, DROP COLUMN mail_from_domain;
ALTER TABLE sender_dns_records DROP COLUMN last_checked_at, DROP COLUMN position,
    DROP COLUMN required, DROP COLUMN priority, DROP COLUMN dns_type;
ALTER TABLE sender_dns_records DROP CONSTRAINT sender_dns_records_one_per_host;
ALTER TABLE sender_dns_records ADD CONSTRAINT sender_dns_records_sender_id_record_type_key
    UNIQUE (sender_id, record_type);
ALTER TABLE sender_dns_records DROP CONSTRAINT sender_dns_records_record_type_check;
ALTER TABLE sender_dns_records ADD CONSTRAINT sender_dns_records_record_type_check
    CHECK (record_type IN ('SPF', 'DKIM', 'DMARC'));
ALTER TABLE suppressions DROP CONSTRAINT suppressions_reason_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_reason_check
    CHECK (reason IN ('opted_out_keyword', 'manual', 'hard_bounce', 'imported_dnc'));
