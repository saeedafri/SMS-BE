-- +goose Up
-- A sender's registry id was built by pasting the header straight into it, so a
-- WhatsApp sender headed "Acme Retail" got `REG-ACME RETAIL-0001`.
--
-- Two things wrong with that. A registry identifier with a space in the middle
-- is not something any real registry issues — these are opaque tokens meant to
-- be quoted over the phone and pasted into a form. And on screen it meant the
-- sender's name appeared twice in the same row, once as the header and once
-- buried inside its own id, so anything looking for the header found two of it.
--
-- Now stripped to alphanumerics: `REG-ACMERETAIL-0001`. Registrations already
-- did the right thing by accident — their object_key is snake_case and has no
-- spaces to begin with — but the same normalisation is applied there so the two
-- cannot drift.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assign_sender_external_id()
RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'approved' AND NEW.external_id IS NULL THEN
        NEW.external_id := 'REG-' ||
            upper(regexp_replace(NEW.header, '[^A-Za-z0-9]', '', 'g')) || '-0001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assign_registration_external_id()
RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'approved' AND NEW.external_id IS NULL THEN
        NEW.external_id := 'REG-' ||
            upper(regexp_replace(NEW.object_key, '[^A-Za-z0-9_]', '', 'g')) || '-0001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Rewrite the ids already issued in the old shape. Only those: an id that came
-- from anywhere else is left exactly as it is.
UPDATE sender_ids
   SET external_id = 'REG-' ||
       upper(regexp_replace(header, '[^A-Za-z0-9]', '', 'g')) || '-0001'
 WHERE external_id = 'REG-' || upper(header) || '-0001'
   AND external_id <> 'REG-' ||
       upper(regexp_replace(header, '[^A-Za-z0-9]', '', 'g')) || '-0001';

-- +goose Down
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
