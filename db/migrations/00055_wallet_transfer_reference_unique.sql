-- +goose Up

-- A bank transfer is credited to a tenant once. The reference (the bank's UTR)
-- is carried in the description, and both the operator console and the
-- operator-admin CLI refuse a reference already booked. Checking first and
-- inserting second lets two operators pressing Credit at the same moment both
-- pass the check; this index makes the second insert fail instead.
CREATE UNIQUE INDEX wallet_ledger_transfer_reference
    ON wallet_ledger (tenant_id, description)
    WHERE entry_type = 'topup' AND description LIKE 'Bank transfer %';

-- +goose Down
DROP INDEX wallet_ledger_transfer_reference;
