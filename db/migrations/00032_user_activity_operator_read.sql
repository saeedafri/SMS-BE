-- +goose Up
-- The operator console reads user activity across every tenant, and could not.
--
-- user_activity shipped with only the tenant-isolation policy, so the console's
-- query ran on the operator pool, matched nothing, and returned an empty table.
-- That is the worst possible failure for an audit screen: "nobody did anything"
-- and "I cannot see what anybody did" look identical, and the reader has no way
-- to tell which one they are looking at.
--
-- Same mechanism as every other operator widening in 00019: an additive policy
-- gated on the app.operator session flag, which is set only on the operator
-- code path. Policies are OR'd, so tenant isolation is unchanged for tenant
-- traffic.
--
-- FOR SELECT, not FOR ALL. The other operator policies allow writes because the
-- console genuinely edits those tables — it approves senders, suspends tenants,
-- changes rates. It has no business writing activity history: these rows are
-- the record of what people did, and an operator who can author them can author
-- an alibi. Reading is the whole job here.
CREATE POLICY user_activity_operator_read ON user_activity
    FOR SELECT USING (acting_as_operator());

-- +goose Down
DROP POLICY user_activity_operator_read ON user_activity;
