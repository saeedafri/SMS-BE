-- +goose Up

-- A throttled tenant needs a number, not just a flag.
--
-- throttled_at alone made "throttled" a boolean, so an operator could mark a
-- tenant throttled but not say to WHAT — which is the single most common reason
-- to throttle anyone: honouring a carrier's contracted TPS.
--
-- The invariant is that this column is non-null if and only if the tenant is
-- throttled, and status is derived from throttled_at. The CHECK below ties the
-- two together at the storage layer, so a transition out that forgets to clear
-- the rate fails loudly here instead of leaving the console reporting a live
-- ceiling on a tenant that no longer has one.
ALTER TABLE tenants
    ADD COLUMN throttled_rate_per_second integer,
    ADD CONSTRAINT tenants_throttle_rate_matches_state CHECK (
        (throttled_at IS NULL AND throttled_rate_per_second IS NULL)
        OR (throttled_at IS NOT NULL AND throttled_rate_per_second >= 1)
    );

-- +goose Down
ALTER TABLE tenants
    DROP CONSTRAINT tenants_throttle_rate_matches_state,
    DROP COLUMN throttled_rate_per_second;
