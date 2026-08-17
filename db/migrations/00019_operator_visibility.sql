-- +goose Up

-- The operator console reads across every tenant, which row-level security is
-- specifically built to prevent. Something has to give, and the options are:
--
--   1. Give operators a BYPASSRLS database role. One connection string leak and
--      every policy in the system is void, everywhere, silently.
--   2. Read operator data through the migration role. Same problem, plus that
--      role can also drop tables.
--   3. Add an explicit policy that opens the door only while a session flag is
--      set, and set that flag only on the operator code path.
--
-- This is option 3. `app.operator` is a session GUC set with SET LOCAL inside a
-- transaction, so it lasts exactly as long as that transaction and cannot leak
-- into the next query on a pooled connection. Every widening is visible here in
-- one place, and a tenant request that never sets the flag is unaffected —
-- tenant isolation is still absolute for tenant traffic.
--
-- Policies are OR'd, so these are additive: the existing tenant policies keep
-- working exactly as before.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION acting_as_operator() RETURNS boolean AS $$
BEGIN
    -- The `true` second argument makes a missing setting return NULL instead of
    -- raising, which is the normal case for every tenant request.
    RETURN current_setting('app.operator', true) = 'on';
END;
$$ LANGUAGE plpgsql STABLE;
-- +goose StatementEnd

CREATE POLICY tenants_operator_read ON tenants
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

CREATE POLICY sender_ids_operator_read ON sender_ids
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

CREATE POLICY templates_operator_read ON templates
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

CREATE POLICY support_tickets_operator_read ON support_tickets
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

CREATE POLICY support_messages_operator_read ON support_messages
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

CREATE POLICY sessions_operator_read ON sessions
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY IF EXISTS sessions_operator_read ON sessions;
DROP POLICY IF EXISTS support_messages_operator_read ON support_messages;
DROP POLICY IF EXISTS support_tickets_operator_read ON support_tickets;
DROP POLICY IF EXISTS templates_operator_read ON templates;
DROP POLICY IF EXISTS sender_ids_operator_read ON sender_ids;
DROP POLICY IF EXISTS tenants_operator_read ON tenants;
DROP FUNCTION IF EXISTS acting_as_operator();
