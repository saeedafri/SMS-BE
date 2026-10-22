-- +goose Up

-- The {{link}} variable: one tracked link per recipient, made as a campaign
-- sends.
--
-- campaigns.link is the campaign's ShortLinkSpec (destination, appendClickId,
-- expiresAt, label), NULL when its message has no {{link}}.
--
-- short_links.origin says who made a link. 'api' is everything that exists
-- today and everything POST /v1/links and /v1/links/bulk make, which stay
-- exactly as they are: bulk called twice still makes two sets. 'campaign' is a
-- link the sender made, and only those are unique per (campaign, recipient), so
-- a retry, a drip resume, a restart and a fallback leg all land on the one link
-- the first attempt made instead of minting a second.
ALTER TABLE campaigns ADD COLUMN link jsonb;

ALTER TABLE short_links ADD COLUMN origin text NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api', 'campaign'));
CREATE UNIQUE INDEX short_links_campaign_recipient
    ON short_links (tenant_id, campaign_id, recipient) WHERE origin = 'campaign';
-- The sender's read: every link already made for one page of a campaign's
-- recipients, whoever made it. Without it each page scans every link the
-- campaign has so far, and a 50,000-recipient send is quadratic: measured 49s
-- to make them, against the second.
CREATE INDEX short_links_campaign_recipient_lookup
    ON short_links (campaign_id, recipient) WHERE campaign_id IS NOT NULL;

-- +goose Down
DROP INDEX short_links_campaign_recipient_lookup;
DROP INDEX short_links_campaign_recipient;
ALTER TABLE short_links DROP COLUMN origin;
ALTER TABLE campaigns DROP COLUMN link;
