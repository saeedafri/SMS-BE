-- +goose Up

-- A campaign's own hours of silence, {from, to, zone}; null means none. A launch
-- inside them is held until they end (held_until), and a resume inside them
-- is refused.
ALTER TABLE campaigns ADD COLUMN quiet_hours jsonb;

-- +goose Down
ALTER TABLE campaigns DROP COLUMN quiet_hours;
