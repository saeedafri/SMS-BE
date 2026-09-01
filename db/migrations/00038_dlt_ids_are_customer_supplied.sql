-- +goose Up
-- Relay is a system of record for India DLT identifiers, not an issuer of them.
--
-- 00023 assigned an external_id on approval — 'REG-' || upper(header) || '-0001'
-- — because an approved registration with a blank id read as data loss. That
-- was the right shape for a product whose approval WAS the registration. It is
-- the wrong shape now.
--
-- A DLT identifier is issued by DLT, to the customer, in their own operator
-- portal. We never have the authority to mint one, and a fabricated id is worse
-- than a blank: it lets a tenant submit under a content-template id that does
-- not exist on DLT, which is how a telemarketer registration gets pulled. The
-- customer supplies the real id now, and we store it verbatim.

DROP TRIGGER IF EXISTS registrations_assign_external_id ON registrations;
DROP TRIGGER IF EXISTS sender_ids_assign_external_id ON sender_ids;
DROP FUNCTION IF EXISTS assign_registration_external_id();
DROP FUNCTION IF EXISTS assign_sender_external_id();

-- Clear what the trigger invented.
--
-- Every one of these is necessarily fabricated: no INSERT ever supplied
-- external_id and no request body carried a registrationId until this slice, so
-- the trigger is the only thing that could have written the column. Scoped to
-- the exact pattern it produced, so a real id typed in after this migration
-- could never be caught by a re-run.
UPDATE registrations SET external_id = NULL WHERE external_id LIKE 'REG-%-0001';
UPDATE sender_ids    SET external_id = NULL WHERE external_id LIKE 'REG-%-0001';

-- A template carries its own DLT identity: the content-template id, and the
-- category DLT approved it under.
--
-- dlt_category is deliberately NOT the existing `category` column. That one is
-- Meta's WhatsApp taxonomy (MARKETING / UTILITY / AUTHENTICATION /
-- TRANSACTIONAL); this one is India's (PROMOTIONAL / SERVICE_IMPLICIT /
-- SERVICE_EXPLICIT / TRANSACTIONAL). Both spell TRANSACTIONAL and it means
-- different things — Meta's is ordinary, DLT's is restricted to banking and OTP
-- traffic. Collapsing them into one column would mis-categorise Indian traffic
-- with no error anywhere until DLT complained.
ALTER TABLE templates
    ADD COLUMN external_id  text,
    ADD COLUMN dlt_category text;

-- +goose Down
ALTER TABLE templates DROP COLUMN external_id, DROP COLUMN dlt_category;

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
