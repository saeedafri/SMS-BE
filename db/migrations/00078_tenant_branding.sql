-- +goose Up

-- White-label branding: a tenant's own name, logo, colours and sign-in domain.
--
-- custom_domain is unique across tenants and only ever SERVED once verified:
-- the owner proves control of it by publishing the token below as a DNS TXT
-- record, so nobody can claim login.somebody-else.com and have our public
-- lookup theme it, or lock the real owner out of claiming it.
CREATE TABLE tenant_branding (
    tenant_id          uuid        PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    display_name       text        CHECK (display_name IS NULL OR length(display_name) BETWEEN 1 AND 80),
    logo_url           text        CHECK (logo_url IS NULL OR length(logo_url) <= 2048),
    primary_color      text        CHECK (primary_color   IS NULL OR primary_color   ~ '^#[0-9a-fA-F]{6}$'),
    secondary_color    text        CHECK (secondary_color IS NULL OR secondary_color ~ '^#[0-9a-fA-F]{6}$'),
    support_email      text        CHECK (support_email IS NULL OR length(support_email) <= 254),
    custom_domain      text        CHECK (custom_domain IS NULL OR custom_domain = lower(custom_domain)),
    domain_token       text        NOT NULL DEFAULT encode(gen_random_bytes(16), 'hex'),
    domain_verified_at timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tenant_branding_domain ON tenant_branding (custom_domain) WHERE custom_domain IS NOT NULL;

ALTER TABLE tenant_branding ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_branding FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_branding_isolation ON tenant_branding
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_branding TO sms_app;

-- The public lookup has no session, so it reads by host across tenants through
-- the operator pool, additively as 00019 established.
CREATE POLICY tenant_branding_operator ON tenant_branding
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP TABLE tenant_branding;
