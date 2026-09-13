-- +goose Up
-- The campaign scheduler has to find due campaigns across every tenant.
--
-- campaigns has only the tenant-isolation policy, so the only way to find them
-- was a query per tenant per tick. Same additive, app.operator-gated policy as
-- 00019 and 00032; FOR SELECT only, because the scheduler claims and launches
-- each campaign through the ordinary tenant-scoped pool.
CREATE POLICY campaigns_operator_read ON campaigns
    FOR SELECT USING (acting_as_operator());

-- +goose Down
DROP POLICY campaigns_operator_read ON campaigns;
