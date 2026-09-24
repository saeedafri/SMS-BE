-- +goose Up

-- WHO a cap withheld, not just how many.
--
-- tenant_send_usage already counts what was withheld per day. A count answers
-- "how much did this cost the customer" and cannot answer the question an
-- operator actually gets asked, which is "did this particular number get the
-- message". That one has no other source: a withheld contact has no message
-- row by design, so nothing in ClickHouse knows they were ever considered.
--
-- Deriving it later from the list instead was the alternative, and it is wrong
-- the moment the list changes — a contact added after the send would read as
-- having been withheld by it. This records the decision when it is made.
CREATE TABLE withheld_contacts (
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    contact_id  uuid        NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    -- Exactly one of these is set. A campaign fan-out cut them, or a journey
    -- step could not carry them because the day's ceiling was spent.
    campaign_id uuid        REFERENCES campaigns(id) ON DELETE CASCADE,
    journey_id  uuid,
    withheld_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT withheld_contacts_has_one_source CHECK (
        (campaign_id IS NOT NULL AND journey_id IS NULL)
        OR (campaign_id IS NULL AND journey_id IS NOT NULL))
);

-- One row per contact per campaign. A campaign that is paused and resumed
-- re-reads the page it stopped on, and without this the same withholding would
-- be recorded twice and the operator would read a number twice the truth.
CREATE UNIQUE INDEX withheld_contacts_once_per_campaign
    ON withheld_contacts (campaign_id, contact_id) WHERE campaign_id IS NOT NULL;

-- The operator's read: everyone this send withheld, in a stable order.
CREATE INDEX withheld_contacts_by_campaign
    ON withheld_contacts (campaign_id, contact_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX withheld_contacts_by_tenant
    ON withheld_contacts (tenant_id, withheld_at DESC);

ALTER TABLE withheld_contacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE withheld_contacts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON withheld_contacts USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON withheld_contacts FOR INSERT WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY withheld_contacts_operator ON withheld_contacts
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- No UPDATE and no DELETE: a withholding happened or it did not.
--
-- This table grows with every capped send — a lakh-contact list at 70% adds
-- thirty thousand rows a send. That is comfortable for Postgres and cheap to
-- index, but it is not free forever: if a tenant sends daily at that size it
-- wants a retention policy, and there is deliberately none here because
-- deleting somebody's record of what we did not send is not a decision this
-- migration should make quietly.
GRANT SELECT, INSERT ON withheld_contacts TO sms_app;

-- +goose Down
DROP TABLE withheld_contacts;
