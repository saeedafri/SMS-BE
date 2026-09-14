-- +goose Up

-- The SMPP protocol values a connection overrides: TLV tags, TON/NPI,
-- registered_delivery and the telemarketer chain, from its operator's interface
-- document. Overrides only. Effective values are the platform defaults under
-- these, computed on read, so changing a default changes every connection that
-- did not override it — which is what a default means.
ALTER TABLE connections ADD COLUMN protocol jsonb NOT NULL DEFAULT '{}';
ALTER TABLE connections ADD CONSTRAINT connections_protocol_keys CHECK (
    jsonb_typeof(protocol) = 'object'
    AND (protocol - ARRAY['dltEntityTlv', 'dltTemplateTlv', 'dltChainTlv',
                          'sourceTon', 'sourceNpi', 'destTon', 'destNpi',
                          'registeredDelivery', 'dltTelemarketerChain']) = '{}'::jsonb
);

-- +goose Down
ALTER TABLE connections DROP CONSTRAINT connections_protocol_keys;
ALTER TABLE connections DROP COLUMN protocol;
