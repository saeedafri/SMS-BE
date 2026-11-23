-- +goose Up

-- A WhatsApp sender connected through Embedded Signup: the tenant's own
-- number and Meta account. The business token and the registration PIN are
-- encrypted and never returned. What Meta last said about payment, billing
-- currency, display name and limit is kept for waSetup, which is derived
-- from these facts on every read.
ALTER TABLE sender_ids
    ADD COLUMN wa_phone_number_id       text,
    ADD COLUMN wa_business_id           text,
    ADD COLUMN wa_token_sealed          text,
    ADD COLUMN wa_token_status          text CHECK (wa_token_status IN ('ok', 'expiring', 'invalid')),
    ADD COLUMN wa_pin_sealed            text,
    ADD COLUMN wa_subscribed            boolean NOT NULL DEFAULT false,
    ADD COLUMN wa_registered            boolean NOT NULL DEFAULT false,
    ADD COLUMN wa_registration_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN wa_registration_since    timestamptz,
    ADD COLUMN wa_payment_method        text CHECK (wa_payment_method IN
                                            ('none', 'prepaid_upi', 'card', 'credit_line', 'unknown')),
    ADD COLUMN wa_billing_currency      text,
    ADD COLUMN wa_display_name_status   text CHECK (wa_display_name_status IN
                                            ('none', 'pending_review', 'approved', 'declined')),
    ADD COLUMN wa_messaging_limit       text CHECK (wa_messaging_limit IN
                                            ('TIER_250', 'TIER_2K', 'TIER_10K', 'TIER_100K', 'TIER_UNLIMITED')),
    ADD COLUMN wa_restricted            boolean,
    ADD COLUMN wa_restricted_until      timestamptz,
    ADD COLUMN wa_disconnected          boolean NOT NULL DEFAULT false;

-- One number belongs to one tenant.
CREATE UNIQUE INDEX sender_ids_wa_phone_number ON sender_ids (wa_phone_number_id)
    WHERE wa_phone_number_id IS NOT NULL;

-- Every number Relay onboarded, for the rolling seven-day cap Meta places on
-- a Tech Provider. A table of its own, so deleting a sender cannot lower it.
CREATE TABLE wa_onboardings (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    onboarded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX wa_onboardings_recent ON wa_onboardings (onboarded_at);
ALTER TABLE wa_onboardings ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_onboardings FORCE  ROW LEVEL SECURITY;
CREATE POLICY wa_onboardings_isolation ON wa_onboardings
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY wa_onboardings_operator ON wa_onboardings
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());
GRANT SELECT, INSERT ON wa_onboardings TO sms_app;

-- A WhatsApp template's definition on Meta's component model, and Meta's
-- standing for it, which is what decides whether it can be sent.
ALTER TABLE templates ADD COLUMN wa_definition jsonb, ADD COLUMN wa_state jsonb;

-- +goose Down
ALTER TABLE templates DROP COLUMN wa_state, DROP COLUMN wa_definition;
DROP TABLE wa_onboardings;
DROP INDEX sender_ids_wa_phone_number;
ALTER TABLE sender_ids DROP COLUMN wa_disconnected, DROP COLUMN wa_restricted_until,
    DROP COLUMN wa_restricted, DROP COLUMN wa_messaging_limit, DROP COLUMN wa_display_name_status,
    DROP COLUMN wa_billing_currency, DROP COLUMN wa_payment_method, DROP COLUMN wa_registration_since,
    DROP COLUMN wa_registration_attempts, DROP COLUMN wa_registered, DROP COLUMN wa_subscribed,
    DROP COLUMN wa_pin_sealed, DROP COLUMN wa_token_status, DROP COLUMN wa_token_sealed,
    DROP COLUMN wa_business_id, DROP COLUMN wa_phone_number_id;
