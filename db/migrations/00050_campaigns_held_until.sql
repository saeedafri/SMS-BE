-- +goose Up

-- When a scheduled campaign will actually launch. Set once at scheduling, for a
-- promotional campaign whose send time falls outside its country's promotional
-- hours: the next opening. The scheduler selects by it, so a held campaign is
-- not due and never fills a page ahead of one that is.
ALTER TABLE campaigns ADD COLUMN held_until timestamptz NULL;

-- +goose Down
ALTER TABLE campaigns DROP COLUMN held_until;
