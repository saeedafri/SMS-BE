-- +goose Up
CREATE TABLE registrations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    country          text        NOT NULL,
    object_key       text        NOT NULL,
    status           text        NOT NULL DEFAULT 'pending_review',
    rejection_reason text,
    external_id      text,
    fields           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- Submitting the same DLT entity twice is a conflict, not a second
    -- application — the regulator holds one record per tenant per object.
    UNIQUE (tenant_id, country, object_key)
);

CREATE TABLE sender_ids (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    header           text        NOT NULL,
    channel          text        NOT NULL,
    country          text        NOT NULL,
    status           text        NOT NULL DEFAULT 'pending_review',
    rejection_reason text,
    external_id      text,
    -- Channel-specific columns are nullable by design: the contract's SenderId
    -- carries WhatsApp, Email and Voice fields that are null for an SMS sender.
    -- Keeping them here rather than in a side table means adding a channel does
    -- not add a join.
    waba_id          text,
    display_name     text,
    phone_number     text,
    email_domain     text,
    from_address     text,
    from_name        text,
    caller_id_number text,
    voice_code       text,
    voice_verified   boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- The same header can serve different channels or countries; the same
    -- header twice on one channel in one country cannot.
    UNIQUE (tenant_id, header, channel, country)
);

CREATE TABLE templates (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sender_id        uuid        NOT NULL REFERENCES sender_ids(id) ON DELETE CASCADE,
    name             text        NOT NULL,
    channel          text        NOT NULL,
    country          text        NOT NULL,
    body             text,
    category         text,
    variables        text[]      NOT NULL DEFAULT '{}',
    cta_url          text,
    status           text        NOT NULL DEFAULT 'pending_review',
    rejection_reason text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX templates_sender ON templates (tenant_id, sender_id);

ALTER TABLE registrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE registrations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON registrations USING (tenant_id = current_tenant_id());

ALTER TABLE sender_ids ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender_ids FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sender_ids USING (tenant_id = current_tenant_id());

ALTER TABLE templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON templates USING (tenant_id = current_tenant_id());

-- INSERT needs its own WITH CHECK policy: a policy's USING clause governs
-- SELECT, UPDATE and DELETE only. Stage 1 learned this when signup silently
-- could not insert a tenant at all, and the failure mode is nasty — the INSERT
-- is simply refused rather than returning anything diagnostic.
--
-- Unlike signup, these inserts happen inside an already-scoped transaction, so
-- the check can be the ordinary tenant match rather than a SECURITY DEFINER
-- function.
CREATE POLICY tenant_insert ON registrations FOR INSERT
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON sender_ids FOR INSERT
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON templates FOR INSERT
    WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON registrations TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON sender_ids TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON templates TO sms_app;

-- +goose Down
DROP TABLE templates;
DROP TABLE sender_ids;
DROP TABLE registrations;
