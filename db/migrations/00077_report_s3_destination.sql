-- +goose Up

-- Where a tenant's scheduled reports are also written: their own S3 bucket.
--
-- One destination per tenant. The secret key is sealed with the server key and
-- never returned; the API shows the key id's last four characters only.
-- last_status/last_at say whether the most recent upload worked, so a customer
-- whose IAM policy changed finds out from the screen, not from an empty bucket.
CREATE TABLE report_s3_destinations (
    tenant_id         uuid        PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    bucket            text        NOT NULL,
    region            text        NOT NULL,
    prefix            text        NOT NULL DEFAULT '',
    access_key_id     text        NOT NULL,
    sealed_secret_key text        NOT NULL,
    last_status       text,
    last_error        text,
    last_at           timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE report_s3_destinations ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_s3_destinations FORCE  ROW LEVEL SECURITY;
CREATE POLICY report_s3_destinations_isolation ON report_s3_destinations
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON report_s3_destinations TO sms_app;

-- +goose Down
DROP TABLE report_s3_destinations;
