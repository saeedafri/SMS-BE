-- +goose Up

-- The cap as a SHARE of what was asked for, and a per-contact exemption from it.
--
-- 00058 capped a tenant's volume as an absolute number of messages per day.
-- That is the wrong shape for the thing it is actually used for: "send 70% of
-- this campaign" scales with the list, so one setting covers a send of a
-- hundred and a send of a lakh, and the operator does not have to re-derive a
-- ceiling every time a customer's list grows.
--
-- NULL means uncapped and is the default, exactly as send_cap_per_day is. 100
-- is a real value and means "send everything", which is not the same as NULL:
-- it is a cap somebody set deliberately, and an operator lowering it later
-- should see what it was before.
ALTER TABLE tenants
    ADD COLUMN send_cap_percent smallint
        CHECK (send_cap_percent IS NULL OR send_cap_percent BETWEEN 0 AND 100);

-- The contacts a cap may never withhold.
--
-- This is the exemption the daily cap never had: whichever contacts matter
-- enough that a margin control must not reach them — the customer's own staff,
-- the numbers a regulator samples, the accounts under contract. They are
-- chosen before the cut is computed, so a tenant capped to 70% still reaches
-- every one of them.
--
-- A plain boolean rather than a list table. A contact is exempt or it is not,
-- there is nothing else to record about the exemption, and a join table would
-- add a second place for the same fact to live.
ALTER TABLE contacts
    ADD COLUMN always_send boolean NOT NULL DEFAULT false;

-- Partial, because the exempt set is small by construction and the query that
-- reads it only ever wants the true rows.
CREATE INDEX contacts_always_send ON contacts (tenant_id) WHERE always_send;

-- When this contact was last carried by a capped send.
--
-- The cap has to cut somebody, and cutting the SAME somebody every time is how
-- a contact at the bottom of a list never receives anything at all — invisible
-- to the customer, and the thing they eventually complain about. Ordering the
-- candidates by this, oldest first, spreads the coverage over repeated sends:
-- whoever was reached last time goes to the back of the queue this time.
--
-- Nullable and null by default, which sorts FIRST: a contact never sent to is
-- the most deserving candidate, not the least.
ALTER TABLE contacts ADD COLUMN last_capped_send_at timestamptz;

-- The ordering the fan-out pages by when a cap is in force. always_send first
-- so the exempt are never at risk of falling past the cut, then oldest contact
-- first.
CREATE INDEX contacts_capped_order
    ON contacts (tenant_id, always_send DESC, last_capped_send_at ASC NULLS FIRST, id);

-- +goose Down
DROP INDEX contacts_capped_order;
ALTER TABLE contacts DROP COLUMN last_capped_send_at;
DROP INDEX contacts_always_send;
ALTER TABLE contacts DROP COLUMN always_send;
ALTER TABLE tenants DROP COLUMN send_cap_percent;
