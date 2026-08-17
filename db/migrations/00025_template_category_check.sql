-- +goose Up
-- templates.category was free text. The contract declares it as an enum of
-- four values and the frontend resolves it against a fixed registry, so a fifth
-- value does not degrade one cell — it throws and blanks the templates page.
--
-- This has already happened three times on other columns (a carrier name, a
-- route standing, an error class). Each returned 200, and each looked like a
-- broken page rather than a bad value. The constraint is the cheap half of the
-- fix; scripts/enum-check.py is the other half.
--
-- Rows are normalised before the constraint is added, so an existing seed with
-- a lowercase or unknown category becomes NULL — "no category recorded" —
-- rather than blocking the migration. NULL is a legitimate state here: SMS and
-- Voice templates have no category at all.
UPDATE templates
   SET category = upper(category)
 WHERE category IS NOT NULL;

UPDATE templates
   SET category = NULL
 WHERE category IS NOT NULL
   AND category NOT IN ('MARKETING', 'UTILITY', 'AUTHENTICATION', 'TRANSACTIONAL');

ALTER TABLE templates
    ADD CONSTRAINT templates_category_check CHECK (
        category IS NULL
     OR category IN ('MARKETING', 'UTILITY', 'AUTHENTICATION', 'TRANSACTIONAL')
    );

-- +goose Down
ALTER TABLE templates DROP CONSTRAINT templates_category_check;
