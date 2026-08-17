-- +goose Up
-- Security-relevant things people do to their own account, recorded as they
-- happen.
--
-- This existed only as a derivation before: the operator console listed recent
-- SESSIONS and called every one of them a "login" event. That is wrong in two
-- directions. It cannot show the other nine event types the contract names —
-- an API key being created, MFA being turned off, a teammate being promoted —
-- because none of those leaves a session behind. And it cannot show a login
-- whose session has since been deleted, so revoking a session quietly erased
-- the evidence that the login ever happened. An audit trail you can erase by
-- using the product is not an audit trail.
--
-- So events are stored, once, at the moment the thing occurs.
CREATE TABLE user_activity (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Nullable, and ON DELETE SET NULL rather than CASCADE. Removing a team
    -- member must not delete the record of what they did — that is precisely
    -- the history an investigation needs. The denormalised name and email below
    -- are what the row is read by once the user row is gone.
    user_id     uuid        REFERENCES users(id) ON DELETE SET NULL,
    user_name   text        NOT NULL,
    user_email  text        NOT NULL,
    event_type  text        NOT NULL CHECK (event_type IN (
                    'login','mfa.enroll','mfa.disable','api_key.create',
                    'api_key.revoke','api_key.rotate','session.revoke',
                    'team.invite','team.role_change','sso.config_change')),
    detail      text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now()
);

-- The operator console reads this newest-first, optionally narrowed to one
-- tenant or one event type. id breaks ties in the sort so keyset pagination
-- cannot loop or skip when two events share a timestamp — which they do, because
-- a single request can record more than one.
CREATE INDEX user_activity_recent ON user_activity (occurred_at DESC, id DESC);
CREATE INDEX user_activity_tenant ON user_activity (tenant_id, occurred_at DESC);

ALTER TABLE user_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_activity FORCE  ROW LEVEL SECURITY;

-- Both clauses. USING governs SELECT, UPDATE and DELETE but NOT INSERT: a policy
-- with only USING silently rejects every insert with no error anyone can search
-- for. Every policy in this schema carries both.
CREATE POLICY user_activity_isolation ON user_activity
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- No UPDATE and no DELETE for the application role, by design and not by
-- oversight. These rows are evidence: the code that writes them appends, and
-- nothing on the request path can rewrite history. Cascading a tenant deletion
-- still works because that runs as the table owner.
GRANT SELECT, INSERT ON user_activity TO sms_app;

-- +goose Down
DROP TABLE user_activity;
