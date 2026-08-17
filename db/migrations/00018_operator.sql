-- +goose Up

-- The operator console: the internal side of the platform, where staff approve
-- senders, police abuse, set rates and route traffic.
--
-- Operators are NOT tenant users with an extra role. They are a separate
-- identity space with its own credentials and sessions, because an operator
-- acts ACROSS tenants — the exact thing every row-level-security policy in this
-- database exists to prevent. Modelling them as a privileged tenant user would
-- mean punching a hole through those policies for a role that, if compromised,
-- reaches every customer's data at once. Keeping the identities separate keeps
-- the tenant policies absolute.
CREATE TABLE operator_users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         citext      NOT NULL UNIQUE,
    name          text        NOT NULL,
    password_hash text        NOT NULL,
    role          text        NOT NULL DEFAULT 'operator'
                  CHECK (role IN ('operator', 'admin')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Mirrors the tenant sessions table: the token is stored hashed, never in the
-- clear, so a database leak does not hand over live sessions.
CREATE TABLE operator_sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operator_id    uuid        NOT NULL REFERENCES operator_users(id) ON DELETE CASCADE,
    token_hash     bytea       NOT NULL UNIQUE,
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX operator_sessions_operator ON operator_sessions (operator_id);

-- Carrier routes, ordered by priority within a country and channel. Priority is
-- an integer rather than a linked list because the console reorders by swapping
-- neighbours, and an integer column makes that one UPDATE instead of a rewrite.
CREATE TABLE routes (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country                text    NOT NULL,
    channel                text    NOT NULL,
    carrier                text    NOT NULL,
    label                  text    NOT NULL,
    priority               integer NOT NULL,
    compliance_standing    text    NOT NULL DEFAULT 'good'
                           CHECK (compliance_standing IN ('good', 'watch', 'blocked')),
    cost_per_segment_minor bigint  NOT NULL,
    currency               text    NOT NULL DEFAULT 'INR',
    status                 text    NOT NULL DEFAULT 'enabled'
                           CHECK (status IN ('enabled', 'disabled')),
    UNIQUE (country, channel, priority)
);

-- What a specific tenant pays, overriding the default rate card. Null tenant is
-- not allowed: a default belongs in pricing_rates, and letting one table mean
-- both invites a query that silently picks the wrong one.
CREATE TABLE rate_overrides (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    country           text        NOT NULL,
    channel           text        NOT NULL,
    category          text,
    per_segment_minor bigint      NOT NULL CHECK (per_segment_minor >= 0),
    currency          text        NOT NULL DEFAULT 'INR',
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, country, channel, category)
);

-- Every operator action that touches a customer, appended and never edited.
-- This is the record that answers "who suspended this tenant, and when" months
-- later, so it carries the tenant NAME as well as the id: a tenant deleted
-- afterwards would otherwise leave an audit entry pointing at nothing.
CREATE TABLE operator_audit_log (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    actor        text        NOT NULL,
    action       text        NOT NULL,
    tenant_id    uuid,
    tenant_name  text,
    target_label text,
    detail       text
);
CREATE INDEX operator_audit_log_recent ON operator_audit_log (occurred_at DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_audit_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'operator_audit_log is append-only (attempted %)', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- An audit trail an operator can quietly edit is not an audit trail. Same
-- guarantee the wallet ledger has, for the same reason.
CREATE TRIGGER operator_audit_log_append_only
    BEFORE UPDATE OR DELETE ON operator_audit_log
    FOR EACH ROW EXECUTE FUNCTION reject_audit_mutation();

-- Abuse flags live on the tenant: a tenant is either flagged or it is not, and
-- a separate table would allow two contradictory flags at once.
ALTER TABLE tenants ADD COLUMN flagged_at   timestamptz;
ALTER TABLE tenants ADD COLUMN flag_reason  text;
ALTER TABLE tenants ADD COLUMN throttled_at timestamptz;

-- The operator role reads and writes across tenants, so these tables carry no
-- RLS policy. That is deliberate and is why operator identities are separate:
-- the application role still cannot reach them.
GRANT SELECT, INSERT, UPDATE, DELETE ON operator_users, operator_sessions,
    routes, rate_overrides, operator_audit_log TO sms_app;

-- +goose Down
DROP TRIGGER IF EXISTS operator_audit_log_append_only ON operator_audit_log;
DROP FUNCTION IF EXISTS reject_audit_mutation();
ALTER TABLE tenants DROP COLUMN IF EXISTS flagged_at;
ALTER TABLE tenants DROP COLUMN IF EXISTS flag_reason;
ALTER TABLE tenants DROP COLUMN IF EXISTS throttled_at;
DROP TABLE IF EXISTS operator_audit_log;
DROP TABLE IF EXISTS rate_overrides;
DROP TABLE IF EXISTS routes;
DROP TABLE IF EXISTS operator_sessions;
DROP TABLE IF EXISTS operator_users;
