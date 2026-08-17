-- +goose Up
-- routes.status was invented as enabled/disabled. The contract's RouteStatus
-- enum is active/disabled.
--
-- This is the fifth instance of the same class of bug, and it fails the same
-- silent way as the other four: the API answers 200 with a value the frontend
-- resolves against a fixed registry, the lookup misses, and the cell renders as
-- nothing. A route that is working looks like a route with no status at all —
-- and "disabled" happens to be spelled the same in both, so exactly the routes
-- that are FINE are the ones that disappear.
--
-- Found by scripts/enum-check.py, which exists because of the previous four.
ALTER TABLE routes DROP CONSTRAINT routes_status_check;
UPDATE routes SET status = 'active' WHERE status = 'enabled';
ALTER TABLE routes ALTER COLUMN status SET DEFAULT 'active';
ALTER TABLE routes ADD CONSTRAINT routes_status_check
    CHECK (status IN ('active', 'disabled'));

-- +goose Down
ALTER TABLE routes DROP CONSTRAINT routes_status_check;
UPDATE routes SET status = 'enabled' WHERE status = 'active';
ALTER TABLE routes ALTER COLUMN status SET DEFAULT 'enabled';
ALTER TABLE routes ADD CONSTRAINT routes_status_check
    CHECK (status IN ('enabled', 'disabled'));
