-- +goose Up

-- Per-recipient frequency caps.
--
-- How many messages one handset may receive from a tenant on one channel in a
-- day, a week and a month. A tenant that texts the same person every hour does
-- not just annoy them: the person opts out of everything, and the carrier sees
-- a complaint rate. The cap is the tenant's own brake on that.
--
-- One row per tenant and channel. No row means no cap, which is the default and
-- costs the send path one cached lookup. A NULL limit is "no limit for that
-- window", so a tenant can cap a day and leave a month open.
--
-- excluded_numbers are exempt from every limit: OTP test handsets, the
-- founder's own phone, a customer who asked for everything.
CREATE TABLE frequency_caps (
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel          text        NOT NULL CHECK (channel IN ('SMS','RCS','WHATSAPP','EMAIL','VOICE')),
    daily_limit      integer     CHECK (daily_limit   IS NULL OR daily_limit   >= 1),
    weekly_limit     integer     CHECK (weekly_limit  IS NULL OR weekly_limit  >= 1),
    monthly_limit    integer     CHECK (monthly_limit IS NULL OR monthly_limit >= 1),
    excluded_numbers text[]      NOT NULL DEFAULT '{}',
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, channel)
);

ALTER TABLE frequency_caps ENABLE ROW LEVEL SECURITY;
ALTER TABLE frequency_caps FORCE  ROW LEVEL SECURITY;
CREATE POLICY frequency_caps_isolation ON frequency_caps
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON frequency_caps TO sms_app;

-- +goose Down
DROP TABLE frequency_caps;
