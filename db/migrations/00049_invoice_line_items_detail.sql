-- +goose Up

-- Invoice lines carry what they bill as data, and reconcile.
--
-- Lines were written with the channel and country folded into the description
-- ("SMS messages (IN)"), and the read path served that description as the
-- line's channel, which the invoice page could not look up. The channel and
-- country are now their own columns.
ALTER TABLE invoice_line_items ADD COLUMN channel text NULL;
ALTER TABLE invoice_line_items ADD COLUMN country text NULL;

-- Every line reconciles, enforced by the database rather than by the one
-- function that happens to write lines today. Production held 13 lines and
-- none failed this when it was added (14 September 2026).
ALTER TABLE invoice_line_items
    ADD CONSTRAINT invoice_line_reconciles CHECK (quantity * unit_minor = amount_minor);

-- The lines written before this in the old description form.
UPDATE invoice_line_items
   SET channel = substring(description from '^(SMS|RCS|WHATSAPP|EMAIL|VOICE) messages'),
       country = substring(description from '\(([A-Z]{2})\)$'),
       description = regexp_replace(description, ' \([A-Z]{2}\)$', '')
 WHERE description ~ '^(SMS|RCS|WHATSAPP|EMAIL|VOICE) messages \([A-Z]{2}\)$';

-- +goose Down
ALTER TABLE invoice_line_items DROP CONSTRAINT invoice_line_reconciles;
ALTER TABLE invoice_line_items DROP COLUMN country;
ALTER TABLE invoice_line_items DROP COLUMN channel;
