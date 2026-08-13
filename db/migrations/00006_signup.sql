-- +goose Up
-- Signup cannot run under RLS, and the reason is easy to miss: a policy's
-- USING clause governs SELECT/UPDATE/DELETE, not INSERT. With FORCE ROW LEVEL
-- SECURITY and no WITH CHECK clause, every INSERT into tenants is denied — so
-- creating the very first tenant was impossible for the application role.
--
-- Adding a permissive WITH CHECK would be the wrong fix: it would let any
-- authenticated request insert a row for any tenant id it liked. Instead the
-- whole of signup is one SECURITY DEFINER function, which also makes it
-- atomic: a tenant with no owner, or a user belonging to nothing, is
-- unrecoverable through the API, so it must be all-or-nothing.

-- +goose StatementBegin
CREATE FUNCTION signup_tenant_owner(
    p_org_name text, p_country text,
    p_email citext, p_full_name text, p_password_hash text)
RETURNS TABLE (tenant_id uuid, user_id uuid)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_tenant_id uuid;
    v_user_id   uuid;
BEGIN
    INSERT INTO tenants (name, country) VALUES (p_org_name, p_country)
    RETURNING id INTO v_tenant_id;

    INSERT INTO users (email, name, password_hash)
    VALUES (p_email, p_full_name, p_password_hash)
    RETURNING id INTO v_user_id;

    INSERT INTO tenant_users (tenant_id, user_id, role)
    VALUES (v_tenant_id, v_user_id, 'owner');

    RETURN QUERY SELECT v_tenant_id, v_user_id;
END;
$$;
-- +goose StatementEnd

GRANT EXECUTE ON FUNCTION signup_tenant_owner(text, text, citext, text, text) TO sms_app;

-- +goose Down
DROP FUNCTION signup_tenant_owner(text, text, citext, text, text);
