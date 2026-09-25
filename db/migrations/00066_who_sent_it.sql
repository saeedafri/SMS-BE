-- +goose Up

-- Who started a send, recorded when they started it.
--
-- A campaign carried everything about the send except the person behind it:
-- pause, resume and cancel were logged with a name (00042), creating one was
-- not, so "who sent the 9 lakh message campaign" had no answer. A journey had
-- the same gap for the click that actually starts it sending.
--
-- Name and email are copied rather than joined. user_id goes to NULL when a
-- teammate is removed, and the record of what they sent must outlive them —
-- the same reasoning as user_activity.
ALTER TABLE campaigns
    ADD COLUMN created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN created_by_name    text,
    ADD COLUMN created_by_email   text;

ALTER TABLE journeys
    ADD COLUMN created_by_user_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN created_by_name      text,
    ADD COLUMN created_by_email     text,
    -- Whoever most recently switched it on: activate or resume. A journey
    -- sends only while active, so this is the person answerable for what it
    -- is sending now. activated_at stays the FIRST activation.
    ADD COLUMN activated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN activated_by_name    text,
    ADD COLUMN activated_by_email   text;

-- The operator console names the API key behind a programmatic send. Read
-- only: an operator has no business minting or revoking a customer's keys.
CREATE POLICY api_keys_operator_read ON api_keys
    FOR SELECT USING (acting_as_operator());

-- The console's campaign and journey lists name the contact list a send went
-- to and count a journey's enrolments. Both tables had tenant isolation only,
-- so the operator pool read NULL for every list name and zero enrolments.
CREATE POLICY contact_lists_operator_read ON contact_lists
    FOR SELECT USING (acting_as_operator());
CREATE POLICY journey_enrollments_operator_read ON journey_enrollments
    FOR SELECT USING (acting_as_operator());

-- +goose Down
DROP POLICY journey_enrollments_operator_read ON journey_enrollments;
DROP POLICY contact_lists_operator_read ON contact_lists;
DROP POLICY api_keys_operator_read ON api_keys;
ALTER TABLE journeys
    DROP COLUMN created_by_user_id, DROP COLUMN created_by_name,
    DROP COLUMN created_by_email, DROP COLUMN activated_by_user_id,
    DROP COLUMN activated_by_name, DROP COLUMN activated_by_email;
ALTER TABLE campaigns
    DROP COLUMN created_by_user_id, DROP COLUMN created_by_name,
    DROP COLUMN created_by_email;
