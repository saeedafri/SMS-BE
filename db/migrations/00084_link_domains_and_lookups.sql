-- +goose Up

-- A tenant's own domain for its short links. Usable only once active: both DNS
-- records verified, then the HTTPS certificate serving, one step per check.
-- A host belongs to one tenant: the redirect resolves a code whatever host it
-- arrives on, and two owners of one host would be two answers to one link.
CREATE TABLE link_domains (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain         text        NOT NULL CHECK (domain = lower(domain) AND length(domain) <= 253),
    status         text        NOT NULL DEFAULT 'pending_dns' CHECK (status IN
                                   ('pending_dns', 'validating', 'issuing_certificate', 'active', 'failed')),
    verify_token   text        NOT NULL DEFAULT encode(gen_random_bytes(12), 'hex'),
    cname_status   text        NOT NULL DEFAULT 'pending' CHECK (cname_status IN ('pending', 'verified', 'failed')),
    txt_status     text        NOT NULL DEFAULT 'pending' CHECK (txt_status IN ('pending', 'verified', 'failed')),
    failure_reason text,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX link_domains_domain ON link_domains (domain);
CREATE INDEX link_domains_tenant ON link_domains (tenant_id, created_at DESC);
ALTER TABLE link_domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE link_domains FORCE  ROW LEVEL SECURITY;
CREATE POLICY link_domains_isolation ON link_domains
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY link_domains_operator ON link_domains
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());
GRANT SELECT, INSERT, UPDATE, DELETE ON link_domains TO sms_app;

-- A short link made on a tenant's own domain. No cascade: a domain a link
-- still uses cannot be deleted.
ALTER TABLE short_links ADD COLUMN domain_id uuid REFERENCES link_domains(id);

-- The last line-type lookup of each number a tenant has looked up, shown on
-- the contact. Absent means never looked up, which is not the same as mobile.
CREATE TABLE number_lookups (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    msisdn     text        NOT NULL,
    result     jsonb       NOT NULL,
    checked_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, msisdn)
);
ALTER TABLE number_lookups ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_lookups FORCE  ROW LEVEL SECURITY;
CREATE POLICY number_lookups_isolation ON number_lookups
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON number_lookups TO sms_app;

-- +goose Down
DROP TABLE number_lookups;
ALTER TABLE short_links DROP COLUMN domain_id;
DROP TABLE link_domains;
