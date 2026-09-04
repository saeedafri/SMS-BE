-- +goose Up

-- A campaign had no brake. Nine hundred thousand recipients with a wrong link,
-- a wrong list or a wrong price ran to completion and the only available action
-- was to watch.
--
-- Three columns and two new statuses. paused_at and cancelled_at are separate
-- rather than one "halted_at" because both can be set at once — cancelling a
-- paused campaign is the normal way someone who hit the brake decides not to
-- continue — and because the campaign's effective stop time is then the EARLIER
-- of the two. Collapsing them into one column loses exactly the fact that makes
-- that decidable.
ALTER TABLE campaigns
    ADD COLUMN paused_at    timestamptz,
    ADD COLUMN cancelled_at timestamptz,
    -- Where the fan-out reached. Fan-out pages the contact list with a keyset
    -- cursor, so resuming from this is resuming from the exact recipient the
    -- pause stopped at: nobody is sent twice and nobody is skipped. NULL means
    -- "from the beginning", which is what a campaign that has never dispatched
    -- should do.
    ADD COLUMN dispatch_cursor text;

ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('scheduled','queued','sending','paused','sent','failed','cancelled'));

-- The timestamps must agree with the status they describe. A campaign reading
-- 'paused' with no paused_at cannot compute its own held time, and one reading
-- 'sent' with a cancelled_at is two contradictory claims in one row.
ALTER TABLE campaigns ADD CONSTRAINT campaigns_halt_timestamps_match_status CHECK (
    (status <> 'paused'    OR paused_at    IS NOT NULL) AND
    (status <> 'cancelled' OR cancelled_at IS NOT NULL) AND
    (cancelled_at IS NULL OR status = 'cancelled')
);

-- +goose Down
ALTER TABLE campaigns DROP CONSTRAINT campaigns_halt_timestamps_match_status;
ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('scheduled','queued','sending','sent','failed'));
ALTER TABLE campaigns
    DROP COLUMN dispatch_cursor,
    DROP COLUMN cancelled_at,
    DROP COLUMN paused_at;
