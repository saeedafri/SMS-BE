-- +goose Up

-- A free-text note on what a campaign or journey is for. Nullable with no
-- default: NULL is "none was given", and the API stores an empty string as NULL
-- too, so there is exactly one way to say it.
ALTER TABLE campaigns ADD COLUMN description text;
ALTER TABLE journeys  ADD COLUMN description text;

-- +goose Down
ALTER TABLE journeys  DROP COLUMN description;
ALTER TABLE campaigns DROP COLUMN description;
