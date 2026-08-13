-- ClickHouse schema for the data plane.
--
-- This is where the volume lives. At the stated 2–3 crore/day, Postgres would
-- need ~900 GB/day for these rows; here the same year is ~11 TB. That ratio is
-- the entire reason for a second database — see docs/RESEARCH_AND_SCALE.md §2.
--
-- Applied by `make clickhouse-migrate`. ClickHouse has no transactional DDL, so
-- every statement is IF NOT EXISTS and safe to re-run.

-- One row per message, collapsed to the latest state.
--
-- ReplacingMergeTree keyed on (tenant_id, id) with `version` as the tiebreak:
-- a message is written once at queue time and again on every status change,
-- and the engine keeps the highest version. Reads must say FINAL (or aggregate)
-- because merges are asynchronous — the alternative, mutating rows in place, is
-- something ClickHouse is deliberately bad at.
CREATE TABLE IF NOT EXISTS messages
(
    tenant_id        UUID,
    id               UUID,
    campaign_id      Nullable(UUID),
    campaign_name    Nullable(String),
    journey_id       Nullable(UUID),
    journey_name     Nullable(String),
    conversation_id  Nullable(UUID),
    channel          LowCardinality(String),
    country          LowCardinality(String),
    sender_header    String,
    template_id      Nullable(UUID),
    msisdn           String,
    email            Nullable(String),
    status           LowCardinality(String),
    delivered_channel Nullable(String),
    error_code       Nullable(String),
    error_class      Nullable(String),
    fraud_flag       LowCardinality(String) DEFAULT 'none',
    segments         UInt8,
    cost_minor       Int64,
    currency         LowCardinality(String),
    route_id         Nullable(String),
    carrier_ref      Nullable(String),
    created_at       DateTime64(3),
    sent_at          Nullable(DateTime64(3)),
    delivered_at     Nullable(DateTime64(3)),
    updated_at       DateTime64(3),
    version          UInt64
)
ENGINE = ReplacingMergeTree(version)
-- Daily partitions: ageing out a day is a DROP PARTITION, not a DELETE
-- scanning hundreds of millions of rows. At this volume that distinction is
-- the difference between retention being a config value and a migration.
PARTITION BY toDate(created_at)
ORDER BY (tenant_id, created_at, id)
TTL toDateTime(created_at) + INTERVAL 90 DAY;

-- Append-only transition log. Every state change lands here, so "why did this
-- message end up failed" is answerable without inference.
CREATE TABLE IF NOT EXISTS message_events
(
    tenant_id  UUID,
    message_id UUID,
    from_state LowCardinality(String),
    to_state   LowCardinality(String),
    error_code Nullable(String),
    detail     String,
    occurred_at DateTime64(3)
)
ENGINE = MergeTree
PARTITION BY toDate(occurred_at)
ORDER BY (tenant_id, message_id, occurred_at)
TTL toDateTime(occurred_at) + INTERVAL 30 DAY;

-- Rollups are permanent. Raw rows age out under the TTLs above, but a tenant's
-- delivery history must not — so every analytics read comes from here, and the
-- dashboard stays complete forever on a few megabytes.
CREATE TABLE IF NOT EXISTS message_rollup_hourly
(
    tenant_id   UUID,
    hour        DateTime,
    channel     LowCardinality(String),
    country     LowCardinality(String),
    status      LowCardinality(String),
    message_count UInt64,
    segment_count UInt64,
    cost_minor  Int64,
    currency    LowCardinality(String)
)
ENGINE = SummingMergeTree((message_count, segment_count, cost_minor))
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, channel, country, status, currency);

-- Populated by the ingest path rather than a materialised view, so the rollup
-- is written in the same code path that writes the message and cannot silently
-- drift from it.
