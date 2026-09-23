-- +goose Up

-- A failed webhook delivery's pending retry, held here rather than in a
-- goroutine's sleep so a restart inside the retry window does not lose it.
--
-- One row per failed first attempt, advanced in place: attempt is the NEXT
-- attempt number to make, next_attempt_at when to make it. The delivery log
-- (webhook_events) stays the record of what was tried; this is only what is
-- still owed. state ends 'succeeded' or 'abandoned' and the row is kept, so a
-- retry that gave up is visibly terminal rather than silently absent.
CREATE TABLE webhook_retries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    endpoint_id     uuid NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_type      text NOT NULL,
    payload         jsonb NOT NULL,
    attempt         integer NOT NULL CHECK (attempt >= 2),
    next_attempt_at timestamptz NOT NULL,
    state           text NOT NULL DEFAULT 'pending'
                    CHECK (state IN ('pending','succeeded','abandoned')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- The worker's only question: what is pending and due.
CREATE INDEX webhook_retries_due ON webhook_retries (next_attempt_at)
    WHERE state = 'pending';

ALTER TABLE webhook_retries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_retries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_retries USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON webhook_retries FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
-- The due sweep reads across tenants through the operator pool, as the
-- campaign scheduler does; every write goes back through the tenant's own.
CREATE POLICY webhook_retries_operator ON webhook_retries
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

GRANT SELECT, INSERT, UPDATE ON webhook_retries TO sms_app;

-- +goose Down
DROP TABLE webhook_retries;
