-- +goose Up

-- A journey is an automated sequence: a trigger, then an ordered list of
-- send and wait steps.
--
-- Steps are jsonb rather than a child table. They are read and written as a
-- whole ordered list, never queried across, and the ordering IS the data —
-- a child table would need a position column and a join to reconstruct
-- something the application always wants complete anyway.
CREATE TABLE journeys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          text NOT NULL,
    status        text NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft','active','paused','archived')),
    trigger_type  text NOT NULL CHECK (trigger_type IN ('list_entry','scheduled')),
    trigger_list_id uuid REFERENCES contact_lists(id) ON DELETE SET NULL,
    trigger_run_at  timestamptz,
    steps         jsonb NOT NULL DEFAULT '[]'::jsonb,

    -- The enrollment cohort size, frozen at creation for the same reason a
    -- campaign's estimate is: the user approved a number.
    recipients    integer NOT NULL DEFAULT 0,

    activated_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX journeys_page ON journeys (tenant_id, created_at DESC, id DESC);

ALTER TABLE journeys ENABLE ROW LEVEL SECURITY;
ALTER TABLE journeys FORCE  ROW LEVEL SECURITY;

CREATE POLICY journeys_isolation ON journeys
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON journeys TO sms_app;

-- +goose Down
DROP TABLE journeys;
