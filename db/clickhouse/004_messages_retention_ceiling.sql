-- The table TTL is now only the ceiling: the longest retention a tenant can
-- choose (365 days). Each tenant's own message_log_retention_days — 30, 90, 180
-- or 365 — is enforced below that by the retention worker, which deletes the
-- tenant's older rows. A flat 90 here kept a 30-day tenant's data for 90 days
-- and deleted a 365-day tenant's at 90; both broke the setting's promise.
--
-- message_events keeps its flat 30 days; this setting is about the message log.
ALTER TABLE messages MODIFY TTL toDateTime(created_at) + INTERVAL 365 DAY;
