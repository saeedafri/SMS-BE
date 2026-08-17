-- +goose Up
-- The contract's SenderId carries qualityRating and messagingTier for WhatsApp
-- senders and the senders list renders both, but there was nowhere to store
-- them, so every WhatsApp sender reported null for both and the column read as
-- empty. Meta assigns these per business account: quality is a rolling health
-- score derived from user blocks and reports, tier is the cap on how many
-- distinct people the account may open a conversation with in 24 hours.
--
-- Both are nullable and both stay null for every non-WhatsApp channel — an SMS
-- header has no such rating. A default would be worse than null here: a fresh
-- WhatsApp sender genuinely has no rating until Meta has seen traffic, and
-- reporting "green" for an account nobody has messaged yet is a claim we
-- cannot support.
ALTER TABLE sender_ids
    ADD COLUMN quality_rating text,
    ADD COLUMN messaging_tier integer;

-- Values are constrained rather than free text because the frontend resolves
-- both against fixed registries: an out-of-enum value does not degrade the
-- column, it throws and blanks the whole senders page.
ALTER TABLE sender_ids
    ADD CONSTRAINT sender_ids_quality_rating_check
        CHECK (quality_rating IS NULL OR quality_rating IN ('green', 'yellow', 'red')),
    ADD CONSTRAINT sender_ids_messaging_tier_check
        CHECK (messaging_tier IS NULL OR messaging_tier IN (250, 1000, 10000, 100000));

-- +goose Down
ALTER TABLE sender_ids
    DROP CONSTRAINT sender_ids_quality_rating_check,
    DROP CONSTRAINT sender_ids_messaging_tier_check,
    DROP COLUMN quality_rating,
    DROP COLUMN messaging_tier;
