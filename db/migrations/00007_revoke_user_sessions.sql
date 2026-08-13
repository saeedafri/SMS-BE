-- +goose Up
-- A password reset must log the user out everywhere. That write was silently
-- doing nothing, and the way it failed is worth recording.
--
-- Password reset runs with no tenant scope — the caller is holding a reset
-- token, not a session, so there is no tenant to scope to. RLS on `sessions`
-- therefore matched zero rows, and UPDATE reports success on zero rows. The
-- reset returned 204 while every stolen session stayed alive: exactly the
-- attack the flow exists to stop, failing silently.
--
-- The general lesson: RLS turns an unscoped write into a no-op, not an error.
-- Any write that legitimately spans tenants has to be explicit about it, which
-- is what this function is.
--
-- A user's sessions can also span tenants, so revocation is keyed on user_id
-- alone. That is correct for a password change: the credential that was reset
-- authenticates the person, not one membership.

-- +goose StatementBegin
CREATE FUNCTION revoke_user_sessions(p_user_id uuid)
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_count integer;
BEGIN
    UPDATE sessions SET revoked_at = now()
    WHERE user_id = p_user_id AND revoked_at IS NULL;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$;
-- +goose StatementEnd

GRANT EXECUTE ON FUNCTION revoke_user_sessions(uuid) TO sms_app;

-- +goose Down
DROP FUNCTION revoke_user_sessions(uuid);
