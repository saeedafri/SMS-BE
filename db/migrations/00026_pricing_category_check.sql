-- +goose Up
-- pricing_rates.category was free text, and the demo seed put 'PROMOTIONAL' in
-- it. That value does not exist: the contract types RateCardRow.category as
-- TemplateCategory, whose members are MARKETING, UTILITY, AUTHENTICATION and
-- TRANSACTIONAL.
--
-- The consequence was not a wrong label on one row. The frontend resolves this
-- field against a fixed registry and throws on a value it does not know, so a
-- single 'PROMOTIONAL' row blanked the entire operator rate card — including
-- the TRANSACTIONAL rows sitting next to it, which were perfectly valid. The
-- API returned 200 throughout.
--
-- This is the fourth time this exact class of bug has been found in this
-- codebase (a carrier name, a route standing, an error class, and now this).
-- The pattern is always the same: a text column, a contract enum, and no
-- constraint joining them. Hence the constraint.
--
-- The empty string is kept as a legal value. It is not a category — it means
-- "this corridor prices per channel, not per category", which is how SMS and
-- RCS are priced, and the existing rows rely on it being part of the primary
-- key rather than NULL.
UPDATE pricing_rates SET category = 'MARKETING' WHERE category = 'PROMOTIONAL';

-- Anything else unrecognised becomes a plain channel-level rate rather than
-- blocking the migration; a wrong category is worse than none.
UPDATE pricing_rates
   SET category = ''
 WHERE category IS NOT NULL
   AND category NOT IN ('', 'MARKETING', 'UTILITY', 'AUTHENTICATION', 'TRANSACTIONAL');

ALTER TABLE pricing_rates
    ADD CONSTRAINT pricing_rates_category_check CHECK (
        category IS NULL
     OR category IN ('', 'MARKETING', 'UTILITY', 'AUTHENTICATION', 'TRANSACTIONAL')
    );

-- +goose Down
ALTER TABLE pricing_rates DROP CONSTRAINT pricing_rates_category_check;
