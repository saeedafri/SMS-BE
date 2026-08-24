-- A data-skipping index on carrier_ref, because the delivery webhook looks a
-- message up by it and nothing else.
--
-- Airtel's callbacks contain no field we control: they quote the
-- messageRequestId issued at submit, so the carrier reference is the only key
-- back to a Relay message — and to the tenant whose wallet is holding money
-- against it. The table is ORDER BY (tenant_id, created_at, id), so that lookup
-- reads every part with no index at all: fine on a laptop, a full scan of the
-- warehouse once per delivery report in production, and there is one delivery
-- report per message.
--
-- A bloom filter rather than a min-max index because carrier references are
-- opaque high-cardinality strings with no ordering relationship to the sort
-- key: a range index over them would skip nothing. GRANULARITY 4 covers 32k
-- rows per index granule, which keeps the index small while still discarding
-- almost every part for a single-reference lookup.
ALTER TABLE messages
    ADD INDEX IF NOT EXISTS messages_carrier_ref carrier_ref
    TYPE bloom_filter(0.01) GRANULARITY 4;
