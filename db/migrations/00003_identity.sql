-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

ALTER TABLE tenants ADD COLUMN country text NOT NULL DEFAULT 'IN'
    CHECK (country IN ('IN','US','GB','AE'));
ALTER TABLE tenants ADD COLUMN capabilities text[] NOT NULL
    DEFAULT ARRAY['sms.send','rcs.send','compliance.manage','billing.view'];

-- Users are global rather than tenant-scoped: an email is unique across the
-- platform, and a person may later belong to more than one tenant. Membership
-- and role therefore live in tenant_users, which IS tenant-scoped and carries
-- the RLS policy.
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          citext      NOT NULL UNIQUE,
    name           text        NOT NULL,
    password_hash  text        NOT NULL,
    email_verified boolean     NOT NULL DEFAULT false,
    mfa_secret     text,
    mfa_enabled    boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tenant_users (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('owner','admin','member')),
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','invited')),
    invited_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

-- Sessions store only the SHA-256 of the token, never the token. A leaked
-- database dump must not hand an attacker working sessions.
--
-- These are opaque tokens rather than JWTs on purpose: the operator console
-- must be able to suspend a tenant and stop traffic within seconds, and
-- DELETE /v1/sessions/{id} must genuinely kill a session. A JWT stays valid
-- until it expires unless you add a denylist — at which point you have the
-- database lookup you were avoiding, plus a second mechanism to keep correct.
CREATE TABLE sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash     bytea       NOT NULL UNIQUE,
    device         text        NOT NULL DEFAULT 'Unknown',
    browser        text        NOT NULL DEFAULT 'Unknown',
    location       text        NOT NULL DEFAULT 'Unknown',
    ip_address     text        NOT NULL DEFAULT '',
    last_active_at timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    revoked_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_tenant_user ON sessions (tenant_id, user_id) WHERE revoked_at IS NULL;

-- Single-use, short-lived, hashed at rest for the same reason as sessions.
CREATE TABLE email_verifications (
    token_hash bytea PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE TABLE password_resets (
    token_hash bytea PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

ALTER TABLE tenant_users ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_users USING (tenant_id = current_tenant_id());

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sessions USING (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_users TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON sessions TO sms_app;
GRANT SELECT, INSERT, UPDATE ON users TO sms_app;
GRANT SELECT, INSERT, UPDATE ON email_verifications TO sms_app;
GRANT SELECT, INSERT, UPDATE ON password_resets TO sms_app;
GRANT INSERT, DELETE ON tenants TO sms_app;

-- +goose Down
DROP TABLE password_resets;
DROP TABLE email_verifications;
DROP TABLE sessions;
DROP TABLE tenant_users;
DROP TABLE users;
ALTER TABLE tenants DROP COLUMN capabilities;
ALTER TABLE tenants DROP COLUMN country;
