-- +goose Up
-- A registration that reaches `approved` has, in the real world, just been
-- given an identifier by the registry — that number is what the customer
-- quotes in a support call and what an auditor asks for. Nothing was assigning
-- it, so the compliance screen rendered "Registration ID" with an empty value
-- next to an approved badge, which reads as data loss.
--
-- This is a trigger rather than a line in the approval handler because there is
-- more than one way to approve: the operator console approves, and the dev
-- hooks advance a registration directly for the browser suite. Putting the rule
-- in one handler leaves the other minting nothing, and the next approval path
-- someone adds inherits the same omission. At the table, every writer is
-- covered by construction.
--
-- The id is assigned once and never rewritten: re-approving something that was
-- already approved must not hand the customer a different number for the same
-- registration.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assign_registration_external_id()
RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'approved' AND NEW.external_id IS NULL THEN
        NEW.external_id := 'REG-' || upper(NEW.object_key) || '-0001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER registrations_assign_external_id
    BEFORE INSERT OR UPDATE ON registrations
    FOR EACH ROW EXECUTE FUNCTION assign_registration_external_id();

-- The same applies to a sender header: an approved DLT header carries the
-- registry's id for that header, and the senders list has a column for it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assign_sender_external_id()
RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'approved' AND NEW.external_id IS NULL THEN
        NEW.external_id := 'REG-' || upper(NEW.header) || '-0001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER sender_ids_assign_external_id
    BEFORE INSERT OR UPDATE ON sender_ids
    FOR EACH ROW EXECUTE FUNCTION assign_sender_external_id();

-- Backfill anything already sitting approved without an id, so existing rows
-- match what every new approval will now produce.
UPDATE registrations SET external_id = 'REG-' || upper(object_key) || '-0001'
 WHERE status = 'approved' AND external_id IS NULL;
UPDATE sender_ids SET external_id = 'REG-' || upper(header) || '-0001'
 WHERE status = 'approved' AND external_id IS NULL;

-- +goose Down
DROP TRIGGER registrations_assign_external_id ON registrations;
DROP TRIGGER sender_ids_assign_external_id ON sender_ids;
DROP FUNCTION assign_registration_external_id();
DROP FUNCTION assign_sender_external_id();
