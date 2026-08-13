-- +goose Up
CREATE TABLE contact_lists (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE contacts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    msisdn       text        NOT NULL,
    email        text,
    country      text        NOT NULL,
    fields       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- Consent is per channel, so it is a map rather than a boolean: a contact
    -- can be opted in to SMS and unknown for WhatsApp, and sending on the
    -- wrong one is the compliance failure this shape exists to prevent.
    consent      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    consented_at jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- One row per phone number per tenant. Re-importing the same number is an
    -- update, not a duplicate — duplicates mean sending twice and billing twice.
    UNIQUE (tenant_id, msisdn)
);
CREATE INDEX contacts_page ON contacts (tenant_id, created_at DESC, id DESC);

CREATE TABLE contact_list_members (
    list_id    uuid NOT NULL REFERENCES contact_lists(id) ON DELETE CASCADE,
    contact_id uuid NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    PRIMARY KEY (list_id, contact_id)
);
CREATE INDEX contact_list_members_contact ON contact_list_members (tenant_id, contact_id);

-- Suppression is global to a tenant, not per list: someone who sends STOP must
-- never be messaged again regardless of which list they appear on.
CREATE TABLE suppressions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    identity   text        NOT NULL,
    msisdn     text,
    email      text,
    reason     text        NOT NULL
                           CHECK (reason IN ('opted_out_keyword','manual','hard_bounce','imported_dnc')),
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, identity)
);
CREATE INDEX suppressions_page ON suppressions (tenant_id, created_at DESC, id DESC);

-- Import idempotency: a resubmitted key returns the first result rather than
-- importing twice. A duplicated import means duplicate sends and duplicate
-- charges, so this is a correctness control, not a convenience.
CREATE TABLE idempotency_keys (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scope      text        NOT NULL,
    key        text        NOT NULL,
    response   jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, scope, key)
);

ALTER TABLE contact_lists ENABLE ROW LEVEL SECURITY;
ALTER TABLE contact_lists FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON contact_lists USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON contact_lists FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE contacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE contacts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON contacts USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON contacts FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE contact_list_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE contact_list_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON contact_list_members USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON contact_list_members FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE suppressions ENABLE ROW LEVEL SECURITY;
ALTER TABLE suppressions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON suppressions USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON suppressions FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON idempotency_keys FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON contact_lists TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON contacts TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON contact_list_members TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON suppressions TO sms_app;
GRANT SELECT, INSERT ON idempotency_keys TO sms_app;

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE suppressions;
DROP TABLE contact_list_members;
DROP TABLE contacts;
DROP TABLE contact_lists;
