-- +goose Up

-- Fallback after a failed send.
--
-- A campaign with a fallback leg used to choose the leg BEFORE sending, from a
-- capability check ("is this handset RCS-capable"). A handset the check called
-- capable that then bounced, which is the ordinary case on a carrier that only
-- learns a number is not RCS when it tries, got nothing at all.
--
-- When the carrier reports a definite failure on the primary leg, the fallback
-- leg now sends. One row per failed message is the claim that makes it happen
-- exactly once however many times the carrier repeats the report or two workers
-- process it: whoever inserts the row sends, whoever conflicts does not.
CREATE TABLE fallback_sends (
    message_id  uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    campaign_id uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fallback_sends_campaign ON fallback_sends (tenant_id, campaign_id);

ALTER TABLE fallback_sends ENABLE ROW LEVEL SECURITY;
ALTER TABLE fallback_sends FORCE  ROW LEVEL SECURITY;
CREATE POLICY fallback_sends_isolation ON fallback_sends
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, DELETE ON fallback_sends TO sms_app;

-- +goose Down
DROP TABLE fallback_sends;
