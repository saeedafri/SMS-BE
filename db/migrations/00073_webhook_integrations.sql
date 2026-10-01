-- +goose Up

-- Native webhook integrations.
--
-- An endpoint can be a plain Relay webhook, or declare that it is a customer's
-- WebEngage, MoEngage or CleverTap account. Relay then posts the event in that
-- product's own event format, so the customer pastes their vendor's URL and a
-- key and does not stand up a translating server in between.
--
-- A separate table rather than columns on webhook_endpoints: most endpoints are
-- plain and cost nothing to read, and the vendor headers hold a credential. They
-- are sealed with the server key and never returned; the API shows names only.
CREATE TABLE webhook_integrations (
    endpoint_id      uuid PRIMARY KEY REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    integration_type text NOT NULL CHECK (integration_type IN ('webengage','moengage','clevertap')),
    sealed_headers   text,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE webhook_integrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_integrations FORCE  ROW LEVEL SECURITY;
CREATE POLICY webhook_integrations_isolation ON webhook_integrations
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_integrations TO sms_app;

-- +goose Down
DROP TABLE webhook_integrations;
