-- +goose Up

-- The contract's TenantStatus has four values — active, throttled, suspended,
-- pending — but this table's CHECK constraint only ever allowed three. A tenant
-- that has signed up and is waiting on approval had nowhere to sit, so the
-- operator console could not represent the one state where an operator most
-- needs to act.
--
-- Caught by a seed insert failing, not by a test: nothing exercised the fourth
-- value because nothing could create it.
ALTER TABLE tenants DROP CONSTRAINT tenants_status_check;
ALTER TABLE tenants ADD CONSTRAINT tenants_status_check
    CHECK (status IN ('active', 'suspended', 'throttled', 'pending'));

-- +goose Down
ALTER TABLE tenants DROP CONSTRAINT tenants_status_check;
ALTER TABLE tenants ADD CONSTRAINT tenants_status_check
    CHECK (status IN ('active', 'suspended', 'throttled'));
