-- +goose Up
-- Priority is per CARRIER within a corridor, not per corridor.
--
-- The console groups routes by {country, channel, carrier} and its Move up /
-- Move down buttons reorder inside that group — "Jio Direct" above "Jio via
-- Aggregator A" is a choice between two ways of reaching Jio, and it says
-- nothing about where Airtel sits. Both Airtel and Jio are legitimately
-- priority 1: they are the first choice for reaching their own network.
--
-- The old UNIQUE (country, channel, priority) made that unrepresentable — a
-- second carrier in the same corridor could not also be priority 1, so the
-- fixture had to fake a total ordering across carriers that the product does
-- not actually have.
ALTER TABLE routes DROP CONSTRAINT routes_country_channel_priority_key;
ALTER TABLE routes ADD CONSTRAINT routes_corridor_priority_key
    UNIQUE (country, channel, carrier, priority);

-- +goose Down
ALTER TABLE routes DROP CONSTRAINT routes_corridor_priority_key;
ALTER TABLE routes ADD CONSTRAINT routes_country_channel_priority_key
    UNIQUE (country, channel, priority);
