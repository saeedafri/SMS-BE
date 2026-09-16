-- +goose Up

-- An RCS operator account, managed like an SMPP connection instead of through
-- the environment: several operators at once, changed without a deploy.
--
-- One row per account, not per customer brand. The brand is the agent, and
-- its operator id stays on rcs_agent_carrier_launches. Jio is the exception in
-- where the secret lives — it issues a secret per assistant — so its secrets
-- hold an assistant-id-to-secret map; see connector.JioRCS.
CREATE TABLE rcs_connections (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    label           text NOT NULL,
    -- Which adapter speaks to it. The operator name on routes and launches is
    -- this upper-cased (connector.RCSIntegrations).
    vendor          text NOT NULL CHECK (vendor IN ('airtel', 'vi', 'jio', 'google')),
    environment     text NOT NULL CHECK (environment IN ('live', 'test')),
    -- Hosts and account identifiers. Nothing secret.
    settings        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(settings) = 'object'),
    -- A sealed JSON object, encrypted with CONNECTION_ENCRYPTION_KEY like a
    -- bind password, never readable back through any endpoint.
    secrets_sealed  text,
    secrets_set_at  timestamptz,
    -- Always created disabled: putting traffic on it is a separate decision.
    status          text NOT NULL DEFAULT 'disabled' CHECK (status IN ('active', 'disabled')),
    health_status   text NOT NULL DEFAULT 'unchecked'
                    CHECK (health_status IN ('unchecked', 'ok', 'error')),
    last_error      text,
    last_checked_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- One live account per operator per environment. Two would leave "which
-- account sent this" to chance, and a delivery report could not say.
CREATE UNIQUE INDEX rcs_connections_one_active
    ON rcs_connections (vendor, environment) WHERE status = 'active';

GRANT SELECT, INSERT, UPDATE, DELETE ON rcs_connections TO sms_app;

-- +goose Down
DROP TABLE rcs_connections;
