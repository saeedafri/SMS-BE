-- Who sent a message, and when the recipient read it.
--
-- sent_by_kind is 'user' (a dashboard session) or 'api_key'; empty for a
-- campaign or journey message, whose author is on the campaign or journey row
-- in Postgres, and for everything written before this column existed.
-- sent_by_id is that user's or key's id. The name is resolved when read, so a
-- row stays a UUID wide rather than carrying a string per message.
--
-- read_at is set by an RCS read receipt. It is not a state: a read message is
-- still `delivered`, and the state machine stays the one place that decides
-- what can follow what.
--
-- Every column here is carried forward by the settle path — this table is a
-- ReplacingMergeTree, and a column a later version omits is erased.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS sent_by_kind LowCardinality(String) DEFAULT '';
ALTER TABLE messages ADD COLUMN IF NOT EXISTS sent_by_id Nullable(UUID);
ALTER TABLE messages ADD COLUMN IF NOT EXISTS read_at Nullable(DateTime64(3));
