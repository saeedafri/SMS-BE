-- +goose Up
-- SMS is the only channel whose message is just a string. The contract has
-- always said so — Template carries rcsContent, waContent and emailContent
-- alongside `body` — but there was nowhere to put any of them, so an RCS card
-- saved as a plain body, a WhatsApp template lost its buttons, and an email
-- template had no subject line. The templates screen rendered every channel as
-- if it were SMS.
--
-- Stored as jsonb rather than as columns per shape, because each of these is a
-- discriminated union and the set of shapes belongs to the channel's owner, not
-- to us: WhatsApp has text, buttons and list today and will have more next
-- year. A column per variant would mean a migration every time Meta ships a
-- message type, and twenty mostly-null columns after a few of those.
--
-- The trade is that the database cannot check the shape, so the API validates
-- against the contract's discriminator on write. That check lives in Go because
-- that is where the generated union types are, and a CHECK constraint
-- re-implementing them in SQL would be a second copy to keep in step.
ALTER TABLE templates
    ADD COLUMN rcs_content   jsonb,
    ADD COLUMN wa_content    jsonb,
    ADD COLUMN email_content jsonb;

-- Each belongs to exactly one channel. Without this a template can claim to be
-- an SMS with a WhatsApp button payload attached, and whichever renderer looks
-- first wins — the kind of inconsistency that shows up as a wrong message
-- delivered to a customer rather than as an error.
ALTER TABLE templates
    ADD CONSTRAINT templates_content_matches_channel CHECK (
        (rcs_content   IS NULL OR channel = 'RCS')
    AND (wa_content    IS NULL OR channel = 'WHATSAPP')
    AND (email_content IS NULL OR channel = 'EMAIL')
    );

-- +goose Down
ALTER TABLE templates
    DROP CONSTRAINT templates_content_matches_channel,
    DROP COLUMN rcs_content,
    DROP COLUMN wa_content,
    DROP COLUMN email_content;
