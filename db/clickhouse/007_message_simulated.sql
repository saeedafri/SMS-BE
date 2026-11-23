-- simulated marks a message accepted on a channel in sandbox mode: recorded,
-- never dispatched to a carrier, never billed. Its cost_minor is always 0.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS simulated Bool DEFAULT false;
