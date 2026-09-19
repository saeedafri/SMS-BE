-- +goose Up

-- Which of a list's columns feeds which template slot.
--
-- Keys are slot names as a TEMPLATE spells them ({{firstName}} -> firstName);
-- values are fields keys as the CUSTOMER'S FILE spells them ("First Name").
--
-- It belongs to the list rather than to a campaign because it is a fact about
-- that spreadsheet — customer_name holds the name whatever is being sent — and
-- because a campaign is sent by handing us a list id: the fan-out walks that
-- list and has nowhere campaign-shaped to read a mapping from.
ALTER TABLE contact_lists ADD COLUMN variable_mapping jsonb NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(variable_mapping) = 'object');

-- Who said these people agreed, and when.
--
-- Recording consent for a whole list is the one click in the product that makes
-- a legal claim about thousands of people, and it sits inside a send flow where
-- it is easy to click quickly. When a complaint arrives the first question is
-- "who said they agreed?" — a record that says only that somebody ticked a box
-- answers nothing, so the exact sentence shown on screen is stored with it.
--
-- Append-only: a consent record that can be edited afterwards is not evidence.
CREATE TABLE contact_consent_records (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    list_id      uuid        NOT NULL REFERENCES contact_lists(id) ON DELETE CASCADE,
    channel      text        NOT NULL,
    state        text        NOT NULL,
    -- Whether contacts who had already answered were left alone. Stored because
    -- it changes what the record CLAIMS: "everyone on the list now says yes" is
    -- a different assertion from "everyone who had not answered now says yes".
    only_unknown boolean     NOT NULL,
    -- The exact sentence the person agreed to, verbatim.
    declaration  text        NOT NULL CHECK (length(btrim(declaration)) > 0),
    -- How many contacts actually changed state, so the record says what it did
    -- rather than what it was asked to do.
    updated      integer     NOT NULL,
    recorded_by  text        NOT NULL,
    recorded_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX contact_consent_records_list
    ON contact_consent_records (tenant_id, list_id, recorded_at DESC);

-- +goose StatementBegin
CREATE FUNCTION reject_consent_record_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'contact_consent_records is append-only (attempted %)', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER contact_consent_records_append_only
    BEFORE UPDATE OR DELETE ON contact_consent_records
    FOR EACH ROW EXECUTE FUNCTION reject_consent_record_mutation();

ALTER TABLE contact_consent_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE contact_consent_records FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON contact_consent_records
    USING (tenant_id = current_tenant_id());
CREATE POLICY tenant_insert ON contact_consent_records
    FOR INSERT WITH CHECK (tenant_id = current_tenant_id());

-- No UPDATE or DELETE grant: the trigger is the enforcement and withholding the
-- privilege is the second lock on the same door.
GRANT SELECT, INSERT ON contact_consent_records TO sms_app;

-- Searching a contact by any of the customer's own column names reads the whole
-- jsonb, so it needs an index that covers keys we do not know in advance.
CREATE INDEX contacts_fields ON contacts USING gin (fields jsonb_path_ops);

-- +goose Down
DROP INDEX contacts_fields;
DROP TRIGGER contact_consent_records_append_only ON contact_consent_records;
DROP TABLE contact_consent_records;
DROP FUNCTION reject_consent_record_mutation();
ALTER TABLE contact_lists DROP COLUMN variable_mapping;
