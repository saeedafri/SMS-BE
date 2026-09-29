-- The exact text a message carried, and the carrier's own delivery report.
--
-- rendered_text is the string handed to the carrier, after substitution. It is
-- written when the message is queued and carried forward by every later
-- version, never re-rendered: a contact's fields and the template can both
-- change after the send. NULL for a message rejected before sending, for a
-- Verify one-time code (see otp), and for every row written before this column.
--
-- otp marks a Verify one-time-code message. Its rendered_text is never stored,
-- and the receipt's `text:` field is blanked out of dlr_raw, so an operator
-- reading a tenant's log cannot read a live code.
--
-- dlr_* is the receipt that SETTLED the message (delivered, failed, expired);
-- a replayed receipt does not overwrite it. dlr_raw is the deliver_sm
-- short_message exactly as the carrier sent it, NULL for a webhook channel.
--
-- These live on the messages row, so they age out with it: the retention worker
-- deletes the row, and the 365-day table TTL is the ceiling.
--
-- Every column here is carried forward by the settle path — this table is a
-- ReplacingMergeTree, and a column a later version omits is erased.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS rendered_text Nullable(String);
ALTER TABLE messages ADD COLUMN IF NOT EXISTS otp Bool DEFAULT false;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dlr_stat LowCardinality(Nullable(String));
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dlr_err Nullable(String);
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dlr_submitted_at Nullable(DateTime64(3));
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dlr_done_at Nullable(DateTime64(3));
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dlr_received_at Nullable(DateTime64(3));
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dlr_raw Nullable(String);
