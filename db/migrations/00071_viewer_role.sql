-- +goose Up

-- A read-only team role.
--
-- The three existing roles all write: a member can send, import and edit. A
-- finance or compliance colleague who needs to see campaigns, logs and invoices
-- without being able to change any of it had no role to be given, so the choice
-- was between over-privileging them and not inviting them. 'viewer' is that
-- role; the API refuses every write from it in one place (viewer_guard.go).
ALTER TABLE tenant_users DROP CONSTRAINT tenant_users_role_check;
ALTER TABLE tenant_users ADD CONSTRAINT tenant_users_role_check
    CHECK (role IN ('owner','admin','member','viewer'));

-- +goose Down
UPDATE tenant_users SET role = 'member' WHERE role = 'viewer';
ALTER TABLE tenant_users DROP CONSTRAINT tenant_users_role_check;
ALTER TABLE tenant_users ADD CONSTRAINT tenant_users_role_check
    CHECK (role IN ('owner','admin','member'));
