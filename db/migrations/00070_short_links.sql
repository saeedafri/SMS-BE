-- +goose Up

-- Short links with click logs.
--
-- A link is made for one destination, optionally for one recipient and one
-- campaign, and carries a short code on our own host. The redirect has no
-- session: a recipient taps it on a handset. So the code is the only thing that
-- authorises the read, and both tables enable row security WITHOUT forcing it,
-- exactly as media_assets does, so the migration role the redirect uses can
-- find a code across tenants while the application role stays scoped.
--
-- link_clicks records every hit, including link-preview bots, flagged rather
-- than dropped: a customer reconciling clicks against their own analytics needs
-- both numbers, and the dashboard shows the human one by default.
CREATE TABLE short_links (
    code        text        PRIMARY KEY,
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    destination text        NOT NULL CHECK (length(destination) <= 2048),
    campaign_id uuid,
    recipient   text,
    label       text        CHECK (label IS NULL OR length(label) <= 120),
    append_click_id boolean NOT NULL DEFAULT false,
    expires_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX short_links_tenant ON short_links (tenant_id, created_at DESC);
CREATE INDEX short_links_campaign ON short_links (tenant_id, campaign_id) WHERE campaign_id IS NOT NULL;

CREATE TABLE link_clicks (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text        NOT NULL REFERENCES short_links(code) ON DELETE CASCADE,
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    clicked_at timestamptz NOT NULL DEFAULT now(),
    is_bot     boolean     NOT NULL DEFAULT false,
    device     text        NOT NULL DEFAULT 'unknown',
    os         text        NOT NULL DEFAULT 'unknown',
    ip_hash    text,
    user_agent text        CHECK (user_agent IS NULL OR length(user_agent) <= 300)
);
CREATE INDEX link_clicks_tenant_time ON link_clicks (tenant_id, clicked_at DESC);
CREATE INDEX link_clicks_code ON link_clicks (code, clicked_at DESC);

ALTER TABLE short_links ENABLE ROW LEVEL SECURITY;
CREATE POLICY short_links_isolation ON short_links
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON short_links TO sms_app;

ALTER TABLE link_clicks ENABLE ROW LEVEL SECURITY;
CREATE POLICY link_clicks_isolation ON link_clicks
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT ON link_clicks TO sms_app;

-- +goose Down
DROP TABLE link_clicks;
DROP TABLE short_links;
