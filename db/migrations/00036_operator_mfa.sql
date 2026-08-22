-- +goose Up
-- Second factor for platform staff.
--
-- Customers have had TOTP since 00005. Staff — whose accounts are not scoped to
-- a tenant, and therefore see every customer on the platform — had a password
-- and nothing else, and that password had been a constant in this repository.
-- The most valuable account in the deployment was the least protected one.
--
-- Deliberately a mirror of the tenant tables rather than a shared one. The two
-- identity systems do not share a users table, a sessions table or a token
-- namespace, and the moment a challenge row could belong to either kind of
-- account, one missing branch turns a customer's second factor into an
-- operator session.
ALTER TABLE operator_users
    ADD COLUMN mfa_secret  text,
    ADD COLUMN mfa_enabled boolean NOT NULL DEFAULT false;

CREATE TABLE operator_mfa_challenges (
    token_hash  bytea PRIMARY KEY,
    operator_id uuid        NOT NULL REFERENCES operator_users(id) ON DELETE CASCADE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);

-- Recovery codes are hashed like any other credential, and single-use. An
-- operator who loses their phone must not need a database edit to get back in.
CREATE TABLE operator_recovery_codes (
    code_hash   bytea PRIMARY KEY,
    operator_id uuid        NOT NULL REFERENCES operator_users(id) ON DELETE CASCADE,
    used_at     timestamptz
);

-- The application role reaches these the same way it reaches operator_users and
-- operator_sessions: through the operator handlers only. They carry no RLS for
-- the same reason those two do not — an operator is not scoped to a tenant, so
-- there is no tenant column to scope by.
GRANT SELECT, INSERT, UPDATE, DELETE
    ON operator_mfa_challenges, operator_recovery_codes TO sms_app;

-- +goose Down
DROP TABLE operator_recovery_codes;
DROP TABLE operator_mfa_challenges;
ALTER TABLE operator_users DROP COLUMN mfa_enabled, DROP COLUMN mfa_secret;
