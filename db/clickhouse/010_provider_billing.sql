-- Meta's own pricing for a WhatsApp message, copied as it came: billable,
-- model, type, category, as JSON. Information only; the tenant pays Meta.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS provider_billing Nullable(String);
