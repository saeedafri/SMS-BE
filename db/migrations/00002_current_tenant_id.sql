-- +goose Up
-- Every RLS policy in this schema resolves the caller's tenant through this
-- one function, rather than inlining current_setting.
--
-- The reason is a bug the isolation tests caught: after a transaction that ran
-- SET LOCAL app.tenant_id, the setting does not go back to *absent* on that
-- connection — it goes back to the empty string. Since connections are pooled,
-- the next query to borrow that connection evaluated ''::uuid and failed with
-- SQLSTATE 22P02. That is an intermittent production 500 that appears only
-- under connection reuse, which is exactly the kind of failure that is
-- miserable to diagnose later.
--
-- nullif(..., '') collapses both "never set" and "reset to empty" to NULL, so
-- the policy comparison yields NULL and the row is filtered out. Unscoped
-- access reads nothing instead of erroring: it fails closed, quietly.

-- +goose StatementBegin
CREATE FUNCTION current_tenant_id() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT nullif(current_setting('app.tenant_id', true), '')::uuid $$;
-- +goose StatementEnd

DROP POLICY tenant_isolation ON tenants;

CREATE POLICY tenant_isolation ON tenants
    USING (id = current_tenant_id());

GRANT EXECUTE ON FUNCTION current_tenant_id() TO sms_app;

-- +goose Down
DROP POLICY tenant_isolation ON tenants;

CREATE POLICY tenant_isolation ON tenants
    USING (id = current_setting('app.tenant_id', true)::uuid);

DROP FUNCTION current_tenant_id();
