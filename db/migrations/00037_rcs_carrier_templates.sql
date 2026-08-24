-- +goose Up
-- An RCS template has TWO approvals, not one, and until now Relay only knew
-- about its own.
--
-- Relay approves a template so the compliance team is satisfied. The carrier
-- approves it separately — Airtel's review takes up to 24 hours, Vi's is done
-- by an admin in their portal — and a send quoting a template the carrier has
-- never seen is refused at the gateway with "Template not found for provided
-- templateId". So a template could read APPROVED on our screen and fail every
-- send, with nothing anywhere explaining why.
--
-- These columns hold the carrier's side of that. They are deliberately separate
-- from `status` and `rejection_reason` rather than overloading them: the two
-- approvals can disagree, and a screen has to be able to say WHICH one is
-- blocking. Collapsing them would make an Airtel rejection look like a Relay
-- rejection and send the customer to the wrong team.
ALTER TABLE templates
    ADD COLUMN carrier_vendor           text,
    ADD COLUMN carrier_template_id      text,
    ADD COLUMN carrier_status           text NOT NULL DEFAULT 'not_submitted',
    ADD COLUMN carrier_rejection_reason text,
    ADD COLUMN carrier_submitted_at     timestamptz,
    ADD COLUMN carrier_updated_at       timestamptz;

-- Only RCS goes through a carrier template registry. WhatsApp has its own
-- approval flow with Meta and Email has none, so letting them carry a carrier
-- status would invite a screen to show a field that can never become true.
ALTER TABLE templates
    ADD CONSTRAINT templates_carrier_registration_is_rcs CHECK (
        channel = 'RCS' OR (
            carrier_vendor IS NULL
        AND carrier_template_id IS NULL
        AND carrier_status = 'not_submitted'
        )
    );

ALTER TABLE templates
    ADD CONSTRAINT templates_carrier_status_known CHECK (
        carrier_status IN ('not_submitted', 'pending', 'approved', 'rejected')
    );

-- A carrier template id must be present once the carrier has one, and absent
-- before. Without this a 'pending' row with no id looks identical to one whose
-- id was lost, and the status webhook — which matches on the carrier's id and
-- nothing else — would silently never find it.
ALTER TABLE templates
    ADD CONSTRAINT templates_carrier_id_matches_status CHECK (
        (carrier_status = 'not_submitted' AND carrier_template_id IS NULL)
     OR (carrier_status <> 'not_submitted' AND carrier_template_id IS NOT NULL)
    );

-- The status webhook arrives carrying the CARRIER's template id and no tenant.
-- It is looked up by that id alone, so it needs an index and, more importantly,
-- it must be unique per vendor: two tenants whose templates collided on the
-- carrier's id would each receive the other's approvals.
CREATE UNIQUE INDEX templates_carrier_identity
    ON templates (carrier_vendor, carrier_template_id)
    WHERE carrier_template_id IS NOT NULL;

-- +goose Down
DROP INDEX templates_carrier_identity;
ALTER TABLE templates
    DROP CONSTRAINT templates_carrier_id_matches_status,
    DROP CONSTRAINT templates_carrier_status_known,
    DROP CONSTRAINT templates_carrier_registration_is_rcs,
    DROP COLUMN carrier_vendor,
    DROP COLUMN carrier_template_id,
    DROP COLUMN carrier_status,
    DROP COLUMN carrier_rejection_reason,
    DROP COLUMN carrier_submitted_at,
    DROP COLUMN carrier_updated_at;
