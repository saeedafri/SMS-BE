-- +goose Up

-- A Verify service is a reusable OTP configuration: which channels to try, in
-- what order, how long a code lives, how many guesses are allowed.
CREATE TABLE verify_services (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name              text NOT NULL,
    -- Channel configs are a small ordered list read and written as a whole,
    -- never queried across, so jsonb beats a child table and a join here.
    channels          jsonb NOT NULL DEFAULT '[]'::jsonb,
    fallback_order    text[] NOT NULL DEFAULT '{}',
    code_length       integer NOT NULL DEFAULT 6 CHECK (code_length IN (4,6,8)),
    code_ttl_seconds  integer NOT NULL DEFAULT 300 CHECK (code_ttl_seconds > 0),
    max_attempts      integer NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    max_per_phone     integer NOT NULL DEFAULT 5,
    window_seconds    integer NOT NULL DEFAULT 3600,
    cooldown_seconds  integer NOT NULL DEFAULT 60,
    region_allowlist  text[] NOT NULL DEFAULT '{}',
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX verify_services_tenant ON verify_services (tenant_id, created_at DESC);

-- One OTP challenge.
--
-- The code is stored as a SHA-256 hash, never in the clear. An OTP is a
-- short-lived credential and a database leak must not hand an attacker live
-- codes for every pending login in the system. A fast hash is right here: the
-- code expires in minutes and is rate-limited, so the slow-hash argument that
-- applies to passwords does not.
CREATE TABLE verifications (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    service_id   uuid NOT NULL REFERENCES verify_services(id) ON DELETE CASCADE,
    msisdn       text NOT NULL,
    country      text NOT NULL DEFAULT '',
    channel      text NOT NULL,
    code_hash    bytea NOT NULL,
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','verified','incorrect','locked','expired','rate_limited')),
    attempts_used integer NOT NULL DEFAULT 0,
    max_attempts  integer NOT NULL DEFAULT 3,
    cost_minor    bigint NOT NULL DEFAULT 0,
    currency      text NOT NULL DEFAULT 'INR',
    fraud_flag    text NOT NULL DEFAULT 'none',
    expires_at    timestamptz NOT NULL,
    verified_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX verifications_service ON verifications (service_id, created_at DESC, id DESC);
-- Supports the per-phone rate-limit lookup without scanning the table.
CREATE INDEX verifications_rate_limit ON verifications (tenant_id, msisdn, created_at DESC);

ALTER TABLE verify_services ENABLE ROW LEVEL SECURITY;
ALTER TABLE verify_services FORCE  ROW LEVEL SECURITY;
ALTER TABLE verifications   ENABLE ROW LEVEL SECURITY;
ALTER TABLE verifications   FORCE  ROW LEVEL SECURITY;

CREATE POLICY verify_services_isolation ON verify_services
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY verifications_isolation ON verifications
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON verify_services TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON verifications TO sms_app;

-- +goose Down
DROP TABLE verifications;
DROP TABLE verify_services;
