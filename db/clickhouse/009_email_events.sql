-- What the email provider said about each message, in order: send, delivery,
-- delay, bounce, complaint, reject, rendering failure, open, click. Keyed by
-- the provider's event id, so a redelivered event collapses onto itself.
CREATE TABLE IF NOT EXISTS email_events
(
    tenant_id       UUID,
    message_id      UUID,
    event_id        String,
    type            LowCardinality(String),
    at              DateTime64(3),
    detail          Nullable(String),
    bounce_type     LowCardinality(Nullable(String)),
    bounce_sub_type Nullable(String),
    bot_likely      Bool DEFAULT false
)
ENGINE = ReplacingMergeTree
ORDER BY (tenant_id, message_id, event_id)
TTL toDateTime(at) + INTERVAL 365 DAY;
