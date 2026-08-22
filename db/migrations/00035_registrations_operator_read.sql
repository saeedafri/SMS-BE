-- +goose Up

-- The operator console could not see compliance registrations at all.
--
-- 00019 opened tenants, sender_ids, templates, support and sessions to the
-- operator path, and later migrations added user_activity and dns_records. The
-- registrations table was never included, so `acting_as_operator()` bought
-- nothing there and the tenant_isolation policy refused every row.
--
-- That is not a cosmetic gap. Compliance approval is what unblocks a tenant's
-- ability to send in a country, so a submitted registration sat in
-- pending_review with no screen able to list it and no endpoint able to decide
-- it. The customer saw "in review" indefinitely.
--
-- FOR ALL rather than FOR SELECT, matching sender_ids and templates: the
-- operator does not merely read the queue, they approve and reject from it, and
-- a read-only policy would list the row and then fail the UPDATE.
CREATE POLICY registrations_operator_read ON registrations
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY IF EXISTS registrations_operator_read ON registrations;
