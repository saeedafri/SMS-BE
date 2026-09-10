-- +goose Up

-- The operator console could not see an agent it was asked to approve.
--
-- rcs_agents shipped with tenant isolation and nothing else, so the approval
-- queue read nothing and approve answered 404 on an agent that plainly existed.
-- Row-level security was doing exactly what it was told; what it was told was
-- incomplete.
--
-- Same shape as templates_operator_read and sender_ids_operator_read: a second
-- policy rather than a disjunction inside the first, so the tenant rule stays
-- readable as one thing and the operator's reach is greppable by name.
CREATE POLICY rcs_agents_operator_read ON rcs_agents
    USING (acting_as_operator())
    WITH CHECK (acting_as_operator());

CREATE POLICY rcs_launch_operator_read ON rcs_agent_carrier_launches
    USING (acting_as_operator())
    WITH CHECK (acting_as_operator());

-- A verification document is the evidence the operator is being asked to judge,
-- and §6.2 of the request is explicit that they read it through the same
-- machinery with their own authorisation rather than through a bypass.
CREATE POLICY media_assets_operator_read ON media_assets
    USING (acting_as_operator())
    WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY media_assets_operator_read ON media_assets;
DROP POLICY rcs_launch_operator_read ON rcs_agent_carrier_launches;
DROP POLICY rcs_agents_operator_read ON rcs_agents;
