-- +goose Up

-- Today one RBM agent identity serves the entire deployment:
-- RCS_AIRTEL_AGENT_ID and RCS_VI_BOT_ID are read once at boot and handed to a
-- single connector, so every handset that receives an RCS message from Relay
-- sees the same name, logo and brand colour whichever customer sent it. Right
-- for one brand, wrong for a CPaaS — a retailer and a bank cannot share a
-- handset identity.
--
-- The customer owns the agent, and a tenant may hold several: a retailer
-- separating order notifications from marketing is the ordinary case, and the
-- two have different use cases, which carriers price and review differently.
-- That is why this is a collection under the tenant and never a column on it.
CREATE TABLE rcs_agents (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    display_name         text        NOT NULL,
    description          text,
    -- Brand assets live in media_assets and are referenced, not inlined: the
    -- same file may later need a vendor-hosted id recorded beside it, and a URL
    -- column has nowhere to put one.
    logo_asset_id        uuid        REFERENCES media_assets(id) ON DELETE SET NULL,
    hero_asset_id        uuid        REFERENCES media_assets(id) ON DELETE SET NULL,
    primary_color        text,
    phone_number         text,
    email                text,
    website              text,
    privacy_policy_url   text,
    terms_of_service_url text,
    use_case             text        NOT NULL,
    country              text        NOT NULL,
    -- The registry-issued entity id the customer already gave the compliance
    -- spine. Deliberately NOT a second identity record: a brand table would
    -- give one legal fact two sources of truth, and a customer approved once
    -- should not be asked to prove who they are again.
    registration_id      text,
    status               text        NOT NULL DEFAULT 'draft',
    rejection_reason     text,

    -- Verification is its own axis, not a status value. An agent can be
    -- verification_approved and still reach nobody, because each carrier
    -- admits it independently -- see rcs_agent_carrier_launches.
    verification_status      text        NOT NULL DEFAULT 'not_submitted',
    verification_contact_name  text,
    verification_contact_email text,
    verification_contact_phone text,
    verification_document_id   uuid      REFERENCES media_assets(id) ON DELETE SET NULL,
    verification_rejection_reason text,
    verification_submitted_at  timestamptz,
    verification_reviewed_at   timestamptz,

    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT rcs_agents_status_known CHECK (status IN (
        'draft', 'verification_submitted', 'verification_approved',
        'verification_rejected', 'launch_pending', 'live',
        'suspended', 'archived')),
    CONSTRAINT rcs_agents_use_case_known CHECK (use_case IN (
        'OTP', 'TRANSACTIONAL', 'PROMOTIONAL', 'MULTI_USE')),
    CONSTRAINT rcs_agents_verification_status_known CHECK (verification_status IN (
        'not_submitted', 'pending', 'approved', 'rejected')),
    -- A rejection the customer cannot read is a dead end. The reason is
    -- required exactly when the status is a rejection, in both directions, so
    -- neither a silent refusal nor an orphaned reason can be stored.
    CONSTRAINT rcs_agents_rejection_has_a_reason CHECK (
        (verification_status = 'rejected') = (verification_rejection_reason IS NOT NULL)
    )
);

CREATE INDEX rcs_agents_tenant ON rcs_agents (tenant_id, created_at DESC, id DESC);

ALTER TABLE rcs_agents ENABLE ROW LEVEL SECURITY;
CREATE POLICY rcs_agents_tenant_isolation ON rcs_agents
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- One carrier's admission of one agent.
--
-- A separate table rather than columns on the agent, because an agent can be
-- live on Airtel and pending on Vi at the same time, and a screen has to be
-- able to say WHICH carrier is blocking. This is the shape 00037 chose for
-- template registration, for the reason given there: collapsing the two
-- approvals makes a carrier's rejection look like ours and sends the customer
-- to the wrong team.
CREATE TABLE rcs_agent_carrier_launches (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id         uuid        NOT NULL REFERENCES rcs_agents(id) ON DELETE CASCADE,
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    carrier          text        NOT NULL,
    status           text        NOT NULL DEFAULT 'not_submitted',
    -- The id the CARRIER issues. Never a primary key here: it is null before
    -- launch, differs per carrier, and changes if an agent is re-submitted.
    carrier_agent_id text,
    rejection_reason text,
    submitted_at     timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT rcs_launch_status_known CHECK (status IN (
        'not_submitted', 'pending', 'approved', 'rejected')),
    -- Present once the carrier has issued one, absent before. Without this a
    -- 'pending' row with no id looks identical to one whose id was lost, and
    -- the inbound webhook -- which matches on the carrier's id and nothing
    -- else -- would silently never find it.
    CONSTRAINT rcs_launch_id_matches_status CHECK (
        (status = 'not_submitted' AND carrier_agent_id IS NULL)
     OR (status <> 'not_submitted')
    ),
    UNIQUE (agent_id, carrier)
);

-- THE TENANT-ISOLATION CONTROL, not a hygiene one.
--
-- An inbound RCS event arrives carrying the carrier's agent id and no tenant,
-- exactly as the template status webhook arrives with a carrier's template id
-- and no tenant. Once agents are per-tenant that field is the ONLY tenant
-- discriminator on the event. Two tenants colliding on a carrier's agent id
-- would each receive the other's delivery receipts and inbound messages, which
-- is the worst failure available in this feature.
CREATE UNIQUE INDEX rcs_launch_carrier_identity
    ON rcs_agent_carrier_launches (carrier, carrier_agent_id)
    WHERE carrier_agent_id IS NOT NULL;

CREATE INDEX rcs_launch_agent ON rcs_agent_carrier_launches (agent_id);

ALTER TABLE rcs_agent_carrier_launches ENABLE ROW LEVEL SECURITY;
CREATE POLICY rcs_launch_tenant_isolation ON rcs_agent_carrier_launches
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- The header an agent sends under. Nullable: an RCS sender registered before
-- agents existed still sends under the deployment-wide identity during the
-- migration, and a NOT NULL here would break every one of them.
ALTER TABLE sender_ids
    ADD COLUMN rcs_agent_id uuid REFERENCES rcs_agents(id) ON DELETE SET NULL;

-- Only RCS reaches a carrier through an agent. Letting another channel carry
-- one would invite a screen to show a field that can never become true --
-- the reasoning 00037 used for carrier_vendor.
ALTER TABLE sender_ids
    ADD CONSTRAINT sender_ids_agent_is_rcs CHECK (
        channel = 'RCS' OR rcs_agent_id IS NULL
    );

-- The application role reaches these through row-level security; without the
-- grant the policy never gets a chance to run and every write is a 500.
GRANT SELECT, INSERT, UPDATE, DELETE ON rcs_agents, rcs_agent_carrier_launches TO sms_app;

-- +goose Down
ALTER TABLE sender_ids DROP CONSTRAINT sender_ids_agent_is_rcs;
ALTER TABLE sender_ids DROP COLUMN rcs_agent_id;
DROP TABLE rcs_agent_carrier_launches;
DROP TABLE rcs_agents;
