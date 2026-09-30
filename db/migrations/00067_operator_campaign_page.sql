-- +goose Up

-- The operator's campaign list reads every tenant's campaigns newest first.
-- campaigns_page leads with tenant_id, so without a tenant picked the list
-- sorted the whole table on every page load and every refresh of the live view.
CREATE INDEX campaigns_newest ON campaigns (created_at DESC, id DESC);

-- +goose Down
DROP INDEX campaigns_newest;
