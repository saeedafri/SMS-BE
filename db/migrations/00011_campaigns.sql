-- +goose Up

-- A campaign is the batch send: one template, one sender, one contact list.
--
-- Its per-message rows live in ClickHouse with everything else, but the campaign
-- itself is in Postgres because it is small, mutable, and referenced by foreign
-- keys — exactly the workload ClickHouse is bad at.
CREATE TABLE campaigns (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                   text NOT NULL,
    channel                text NOT NULL,
    country                text NOT NULL,
    list_id                uuid REFERENCES contact_lists(id) ON DELETE SET NULL,
    sender_id              uuid NOT NULL REFERENCES sender_ids(id),
    template_id            uuid NOT NULL REFERENCES templates(id),

    -- Fallback is the cheapest possible model of "try RCS, fall back to SMS":
    -- three nullable columns rather than a second table, because a campaign has
    -- at most one fallback and a join buys nothing.
    fallback_channel       text,
    fallback_sender_id     uuid REFERENCES sender_ids(id),
    fallback_template_id   uuid REFERENCES templates(id),

    status                 text NOT NULL DEFAULT 'queued'
                           CHECK (status IN ('scheduled','queued','sending','sent','failed')),
    scheduled_at           timestamptz,
    send_started_at        timestamptz,

    -- The estimate, frozen at creation. It must not be recomputed on read: the
    -- user approved a specific number before launching, and a rate change
    -- afterwards must not silently rewrite what they agreed to.
    recipients             integer NOT NULL DEFAULT 0,
    segments_per_message   integer NOT NULL DEFAULT 1,
    cost_minor_min         bigint  NOT NULL DEFAULT 0,
    cost_minor_max         bigint  NOT NULL DEFAULT 0,
    currency               text    NOT NULL DEFAULT 'INR',

    -- Retry lineage. A retry is a new campaign pointing back at the one whose
    -- failures it is re-sending, so the original's numbers stay true rather
    -- than being mutated by a later attempt.
    retry_of               uuid REFERENCES campaigns(id) ON DELETE SET NULL,

    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX campaigns_page ON campaigns (tenant_id, created_at DESC, id DESC);
CREATE INDEX campaigns_retry_of ON campaigns (retry_of) WHERE retry_of IS NOT NULL;

ALTER TABLE campaigns ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaigns FORCE ROW LEVEL SECURITY;

-- WITH CHECK as well as USING: USING governs reads and the rows an update may
-- touch, but INSERT is checked only by WITH CHECK. Without it, creating a
-- campaign is denied with nothing diagnostic in the log.
CREATE POLICY campaigns_tenant_isolation ON campaigns
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- The app role owns no tables, so every new one needs an explicit grant. RLS
-- restricts WHICH rows it may touch; the grant is what lets it touch the table
-- at all, and missing it fails with a bare "permission denied".
GRANT SELECT, INSERT, UPDATE, DELETE ON campaigns TO sms_app;

-- +goose Down
DROP TABLE campaigns;
