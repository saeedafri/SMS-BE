-- +goose Up

-- The first binary this platform accepts. Every byte it has handled until now
-- has been JSON: there is no multipart handling, no object-storage client and
-- no storage dependency anywhere in the tree.
--
-- The row is first-class rather than a URL column on whatever needed the file,
-- and that is not tidiness. A carrier does not consume our URL: Airtel and Vi
-- each have their own file endpoints and reference vendor-hosted media ids, so
-- template media is a two-step path -- customer to us, us to the vendor -- and
-- the vendor's id needs somewhere to live. A URL string on the agent has
-- nowhere to put one.
CREATE TABLE media_assets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    purpose      text        NOT NULL,
    content_type text        NOT NULL,
    byte_size    bigint      NOT NULL,
    -- Read off the FILE, never off the request. A client-supplied dimension is
    -- a claim, not a measurement.
    width        integer,
    height       integer,
    -- Where the bytes are, relative to the media root. Tenant-prefixed so
    -- isolation is enforceable at the path and not only in the query that
    -- produced the URL.
    storage_key  text        NOT NULL UNIQUE,
    -- The vendor's own id for this file, once it has been pushed to a carrier.
    -- Null until then, and null forever for assets no carrier ever sees.
    vendor       text,
    vendor_media_id text,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT media_assets_purpose_known CHECK (purpose IN (
        'agent_logo', 'agent_hero', 'template_media', 'verification_document')),
    CONSTRAINT media_assets_vendor_id_pairs CHECK (
        (vendor IS NULL) = (vendor_media_id IS NULL)
    )
);

CREATE INDEX media_assets_tenant ON media_assets (tenant_id, created_at DESC);

-- Same reasoning as every other tenant table: the prefix keeps a mistake in one
-- query from becoming a cross-tenant read, and this policy keeps it from
-- becoming one in any query.
ALTER TABLE media_assets ENABLE ROW LEVEL SECURITY;
CREATE POLICY media_assets_tenant_isolation ON media_assets
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- The application role reaches these through row-level security; without the
-- grant the policy never gets a chance to run and every write is a 500.
GRANT SELECT, INSERT, UPDATE, DELETE ON media_assets TO sms_app;

-- +goose Down
DROP TABLE media_assets;
