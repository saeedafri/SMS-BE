-- fallback_reason says why a campaign message went by its fallback leg instead
-- of its primary, when that reason is one the timeline names:
-- rich_content_unsupported (the handset could not render the RCS content).
ALTER TABLE messages ADD COLUMN IF NOT EXISTS fallback_reason LowCardinality(String) DEFAULT '';
