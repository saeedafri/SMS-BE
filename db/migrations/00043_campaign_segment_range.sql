-- +goose Up

-- A campaign body carrying {{variables}} becomes as many different messages as
-- it has recipients, and SMS bills per segment per message. One integer cannot
-- describe that, and the integer we stored was the worst available answer: the
-- count of the body AS WRITTEN, in which "{{first_name}}" is fourteen
-- characters that never reach a handset and whose braces are GSM 03.38
-- extension characters charged at two septets each.
--
-- Renamed rather than added alongside, so nothing can read the old name and get
-- a number that means something else now. The rename is metadata only.
ALTER TABLE campaigns RENAME COLUMN segments_per_message TO segments_per_message_min;

ALTER TABLE campaigns
    ADD COLUMN segments_per_message_max integer NOT NULL DEFAULT 1;

-- Existing rows get max = min rather than an invented spread. The stored value
-- was the written-token count, which is not a bound in either direction, so any
-- range derived from it now would be a guess dressed as a measurement. What
-- these campaigns actually cost is in the message log, per message, and that
-- has always been right — see the note in the estimate path: nothing ever
-- billed from this column.
UPDATE campaigns SET segments_per_message_max = segments_per_message_min;

-- +goose Down
ALTER TABLE campaigns DROP COLUMN segments_per_message_max;
ALTER TABLE campaigns RENAME COLUMN segments_per_message_min TO segments_per_message;
