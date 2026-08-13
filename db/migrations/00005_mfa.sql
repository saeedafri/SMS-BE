-- +goose Up
-- An MFA challenge is the state between "password was correct" and "second
-- factor proved". It is deliberately NOT a session: a caller holding only a
-- challenge token can do nothing except present a TOTP code.
CREATE TABLE mfa_challenges (
    token_hash bytea PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

-- Recovery codes are hashed like any other credential, and single-use.
CREATE TABLE mfa_recovery_codes (
    code_hash bytea PRIMARY KEY,
    user_id   uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    used_at   timestamptz
);
CREATE INDEX mfa_recovery_codes_user ON mfa_recovery_codes (user_id) WHERE used_at IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON mfa_challenges TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mfa_recovery_codes TO sms_app;

-- +goose Down
DROP TABLE mfa_recovery_codes;
DROP TABLE mfa_challenges;
