-- +goose Up
-- Operator SMPP binds, and the corridor ladder that uses them.
--
-- Textify is a registered telemarketer holding binds with all four Indian
-- operators, test and live for each. Until now a "route" was a commercial
-- corridor row with no host, port, system id or password anywhere in it — it
-- described where traffic should go and carried nothing that could send it.

CREATE TABLE connections (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    label                     text    NOT NULL,
    carrier                   text    NOT NULL,
    environment               text    NOT NULL CHECK (environment IN ('live', 'test')),
    host                      text    NOT NULL,
    port                      integer NOT NULL CHECK (port > 0 AND port <= 65535),
    system_id                 text    NOT NULL,
    system_type               text,
    bind_type                 text    NOT NULL
                              CHECK (bind_type IN ('transmitter', 'receiver', 'transceiver')),

    -- Encrypted, never hashed: we have to present the plaintext to the operator
    -- on every bind, so a hash would make the column useless. The key lives in
    -- the environment, not here — otherwise the ciphertext and the means to read
    -- it sit in the same dump.
    password_encrypted        text,
    password_set_at           timestamptz,

    -- Per bind, never global. Each operator contracts a different ceiling and
    -- exceeding it is how a bind gets dropped.
    max_tps                   integer NOT NULL CHECK (max_tps > 0),
    window_size               integer NOT NULL DEFAULT 10  CHECK (window_size > 0),
    enquire_link_seconds      integer NOT NULL DEFAULT 30  CHECK (enquire_link_seconds > 0),
    reconnect_backoff_seconds integer NOT NULL DEFAULT 5   CHECK (reconnect_backoff_seconds > 0),

    -- Always created disabled. A bind that went live the moment it was typed
    -- would put customer traffic on an untested path.
    status                    text    NOT NULL DEFAULT 'disabled'
                              CHECK (status IN ('active', 'disabled')),

    -- Last known bind state, reported by us. The console polls this rather than
    -- holding a socket open.
    health_status             text    NOT NULL DEFAULT 'unbound'
                              CHECK (health_status IN ('bound', 'unbound', 'error')),
    last_bound_at             timestamptz,
    last_error                text,

    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),

    -- One operator will not issue the same system_id twice on the same host and
    -- environment; two rows claiming to be the same bind is a configuration
    -- mistake worth catching here.
    UNIQUE (carrier, environment, host, system_id)
);

-- Connections are platform configuration, never tenant data: no tenant_id, and
-- deliberately outside the row-level security every tenant table carries. The
-- boundary is that only an authenticated operator reaches the handler.

-- The app role reads and writes connections through the operator pool. Granted
-- explicitly, like every other table here: nothing in this schema relies on
-- default privileges.
GRANT SELECT, INSERT, UPDATE, DELETE ON connections TO sms_app;

-- A corridor points at the bind that carries it. ON DELETE RESTRICT, not
-- CASCADE: deleting a connection that routes still reference must fail loudly so
-- the operator repoints them deliberately, rather than silently unwiring live
-- corridors.
ALTER TABLE routes
    ADD COLUMN connection_id uuid REFERENCES connections(id) ON DELETE RESTRICT;

-- Priority regroups from {country, channel, carrier} to {country, channel}.
--
-- 00030 made priority per-carrier because the product then had no cross-carrier
-- ordering: Airtel and Jio were each legitimately "first" for their own network.
-- With four binds and priority-with-failover that is exactly the ordering the
-- product now needs — the ladder has to say try Airtel, then Jio, then Vi, then
-- BSNL, and a per-carrier priority cannot express it.
--
-- Dropped BEFORE renumbering: several rows in a corridor currently share
-- priority 1, so the target constraint cannot hold until the data is fixed.
ALTER TABLE routes DROP CONSTRAINT routes_corridor_priority_key;

-- Cheapest first, and a registered corridor ahead of a grey one on a tie.
-- Carrier and id only break remaining ties, so the result is deterministic
-- rather than dependent on physical row order. The console reorders from here.
WITH ranked AS (
    SELECT id, row_number() OVER (
        PARTITION BY country, channel
        ORDER BY cost_per_segment_minor ASC,
                 (compliance_standing = 'registered') DESC,
                 carrier ASC,
                 id ASC
    ) AS new_priority
    FROM routes
)
UPDATE routes SET priority = ranked.new_priority
FROM ranked WHERE routes.id = ranked.id;

ALTER TABLE routes ADD CONSTRAINT routes_country_channel_priority_key
    UNIQUE (country, channel, priority);

-- +goose Down
ALTER TABLE routes DROP CONSTRAINT routes_country_channel_priority_key;
ALTER TABLE routes ADD CONSTRAINT routes_corridor_priority_key
    UNIQUE (country, channel, carrier, priority);
ALTER TABLE routes DROP COLUMN connection_id;
DROP TABLE connections;
