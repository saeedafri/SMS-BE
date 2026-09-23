-- +goose Up

-- Journeys run. Until now a journey was stored, activated and never read by
-- anything that sends; its funnel was derived from the trigger list and the
-- suppression list, so it reported contacts as having completed a sequence
-- that never sent them a message.

-- When a scheduled journey swept its list. Set once: a scheduled journey
-- enrols everyone on the list at run_at, in one pass, and never again.
ALTER TABLE journeys ADD COLUMN trigger_swept_at timestamptz;

-- One contact's progress through one journey.
--
-- step_index is the step the contact is on. waiting is true while a wait step
-- holds them until next_run_at; a send step is passed through the moment it is
-- reached. state ends 'completed' or 'exited_suppressed' and the row is kept:
-- the funnel's totals are counts over these rows, so a finished contact is
-- still one who enrolled. One row per contact per journey — enrolment is the
-- insert, and the unique key is what stops a list_entry sweep re-enrolling
-- someone already in.
CREATE TABLE journey_enrollments (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    journey_id  uuid        NOT NULL REFERENCES journeys(id) ON DELETE CASCADE,
    contact_id  uuid        NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    step_index  integer     NOT NULL DEFAULT 0 CHECK (step_index >= 0),
    waiting     boolean     NOT NULL DEFAULT false,
    next_run_at timestamptz NOT NULL DEFAULT now(),
    state       text        NOT NULL DEFAULT 'active'
                CHECK (state IN ('active','completed','exited_suppressed')),
    enrolled_at timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (journey_id, contact_id)
);

CREATE INDEX journey_enrollments_due ON journey_enrollments (journey_id, next_run_at)
    WHERE state = 'active';

ALTER TABLE journey_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE journey_enrollments FORCE  ROW LEVEL SECURITY;
CREATE POLICY journey_enrollments_isolation ON journey_enrollments
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON journey_enrollments TO sms_app;

-- Every journey that is 'active' today was activated when activating did
-- nothing. Left active, the first engine cycle after this deploy would enrol
-- its whole list and start sending real messages that nobody decided to send
-- today. Paused instead: activated_at is kept, and resuming — a deliberate
-- click — is what now starts it.
UPDATE journeys SET status = 'paused', updated_at = now() WHERE status = 'active';

-- The engine finds active journeys across tenants through the operator pool,
-- as the campaign scheduler does. Additive, as 00019 established.
CREATE POLICY journeys_operator ON journeys
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY journeys_operator ON journeys;
DROP TABLE journey_enrollments;
ALTER TABLE journeys DROP COLUMN trigger_swept_at;
