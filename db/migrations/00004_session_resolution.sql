-- +goose Up
-- Authentication has a bootstrapping problem: RLS on `sessions` keys off
-- current_tenant_id(), but the middleware does not know the tenant until it has
-- resolved the token — and an unscoped read of an RLS table returns nothing.
--
-- The wrong fixes are (a) give the app role RLS-bypass, which hands it every
-- tenant's sessions for every query, or (b) drop RLS on sessions, which removes
-- the guarantee for the endpoints that list and revoke them.
--
-- Instead these two SECURITY DEFINER functions run as the table owner and
-- return exactly one tuple each, and only to a caller who already possesses the
-- secret. The exposure is limited to "if you hold a valid token you may learn
-- which tenant it belongs to", which is precisely what authentication is.
-- Everything after this point runs scoped, under RLS, as normal.

-- +goose StatementBegin
CREATE FUNCTION resolve_session(p_token_hash bytea)
RETURNS TABLE (session_id uuid, tenant_id uuid, user_id uuid)
LANGUAGE sql SECURITY DEFINER STABLE
SET search_path = public
AS $$
    SELECT id, tenant_id, user_id
    FROM sessions
    WHERE token_hash = p_token_hash
      AND revoked_at IS NULL
      AND expires_at > now()
$$;
-- +goose StatementEnd

-- Login knows the user only after verifying their password; it still needs the
-- tenant to scope everything that follows.
-- +goose StatementBegin
CREATE FUNCTION resolve_user_tenant(p_user_id uuid)
RETURNS TABLE (tenant_id uuid, role text, status text)
LANGUAGE sql SECURITY DEFINER STABLE
SET search_path = public
AS $$
    SELECT tenant_id, role, status
    FROM tenant_users
    WHERE user_id = p_user_id
    ORDER BY created_at
    LIMIT 1
$$;
-- +goose StatementEnd

-- Signup must insert the first tenant_users row before any session exists, so
-- there is no tenant scope to satisfy the RLS policy with.
-- +goose StatementBegin
CREATE FUNCTION create_tenant_membership(p_tenant_id uuid, p_user_id uuid, p_role text)
RETURNS void
LANGUAGE sql SECURITY DEFINER
SET search_path = public
AS $$
    INSERT INTO tenant_users (tenant_id, user_id, role) VALUES ($1, $2, $3)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION create_session(
    p_tenant_id uuid, p_user_id uuid, p_token_hash bytea,
    p_device text, p_browser text, p_ip text, p_expires_at timestamptz)
RETURNS uuid
LANGUAGE sql SECURITY DEFINER
SET search_path = public
AS $$
    INSERT INTO sessions (tenant_id, user_id, token_hash, device, browser, ip_address, expires_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    RETURNING id
$$;
-- +goose StatementEnd

GRANT EXECUTE ON FUNCTION resolve_session(bytea) TO sms_app;
GRANT EXECUTE ON FUNCTION resolve_user_tenant(uuid) TO sms_app;
GRANT EXECUTE ON FUNCTION create_tenant_membership(uuid, uuid, text) TO sms_app;
GRANT EXECUTE ON FUNCTION create_session(uuid, uuid, bytea, text, text, text, timestamptz) TO sms_app;

-- +goose Down
DROP FUNCTION create_session(uuid, uuid, bytea, text, text, text, timestamptz);
DROP FUNCTION create_tenant_membership(uuid, uuid, text);
DROP FUNCTION resolve_user_tenant(uuid);
DROP FUNCTION resolve_session(bytea);
