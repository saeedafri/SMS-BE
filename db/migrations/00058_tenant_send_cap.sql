-- +goose Up

-- A ceiling on how much one tenant may send in a day.
--
-- 00040 already caps a tenant's send RATE. This caps their send VOLUME, which
-- is a different commercial control: the rate ceiling honours a carrier's
-- contracted TPS, this one honours what we are willing to carry for a customer
-- in a day. A tenant can sit under the rate ceiling all day and still hand us a
-- hundred thousand messages.
--
-- NULL means uncapped, and it is the default. There is no allow-list table and
-- no flag: a tenant with no cap is uncapped, which is one column instead of two
-- states that can disagree. Zero is a real value and means "send nothing" — the
-- CHECK admits it deliberately, because an operator stopping a tenant's bulk
-- traffic without suspending their account is a thing that happens.
ALTER TABLE tenants
    ADD COLUMN send_cap_per_day integer
        CHECK (send_cap_per_day IS NULL OR send_cap_per_day >= 0);

-- What the cap actually did, per tenant per day.
--
-- accepted is what we let through and is the number the cap is measured
-- against. withheld is what the cap took off, kept because "how much did this
-- cap cost the customer" is a question somebody will ask the day a customer
-- disputes a send — and deriving it later from message rows is impossible by
-- construction, since a withheld recipient has no message row.
--
-- day is the tenant's OWN calendar day, computed in Go from the tenant
-- country's zone (billing.BillingLocationFor) and written here already
-- resolved. A UTC day would reset an Indian tenant's allowance at 05:30 in the
-- morning, which is neither midnight nor any boundary they would recognise.
-- Storing it as `date` rather than deriving it from a timestamp is what keeps
-- the primary key — and therefore the upsert — exact.
CREATE TABLE tenant_send_usage (
    tenant_id uuid   NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    day       date   NOT NULL,
    accepted  bigint NOT NULL DEFAULT 0 CHECK (accepted >= 0),
    withheld  bigint NOT NULL DEFAULT 0 CHECK (withheld >= 0),
    PRIMARY KEY (tenant_id, day)
);

ALTER TABLE tenant_send_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_send_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_send_usage USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON tenant_send_usage FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
-- Additive, as 00019 established: open only while app.operator is set.
CREATE POLICY tenant_send_usage_operator ON tenant_send_usage
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- UPDATE, because the counter is an upsert on a row that already exists for
-- most of the day. No DELETE: a day's usage is a record of what we did.
GRANT SELECT, INSERT, UPDATE ON tenant_send_usage TO sms_app;

-- +goose Down
DROP TABLE tenant_send_usage;
ALTER TABLE tenants DROP COLUMN send_cap_per_day;
