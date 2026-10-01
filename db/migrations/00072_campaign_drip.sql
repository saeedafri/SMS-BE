-- +goose Up

-- Drip sending: send a campaign in instalments instead of all at once.
--
-- drip_batch_size recipients go out, then the campaign parks its cursor and
-- reschedules itself drip_interval_minutes ahead, and the ordinary campaign
-- scheduler picks it up and sends the next instalment. Both columns are set or
-- neither is: a batch size with no interval would never resume.
ALTER TABLE campaigns
    ADD COLUMN drip_batch_size        integer CHECK (drip_batch_size BETWEEN 1 AND 100000),
    ADD COLUMN drip_interval_minutes  integer CHECK (drip_interval_minutes BETWEEN 1 AND 1440),
    ADD CONSTRAINT campaigns_drip_pair CHECK (
        (drip_batch_size IS NULL) = (drip_interval_minutes IS NULL));

-- +goose Down
ALTER TABLE campaigns DROP CONSTRAINT campaigns_drip_pair,
    DROP COLUMN drip_interval_minutes, DROP COLUMN drip_batch_size;
