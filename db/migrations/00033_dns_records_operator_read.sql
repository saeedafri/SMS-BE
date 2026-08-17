-- +goose Up
-- The operator console could not see a sender's DNS records.
--
-- sender_dns_records was created with only the tenant-isolation policy, so the
-- approvals queue — which runs on the operator pool — read zero rows for every
-- email sender. The review dialog then told the operator "0 of 0 records are
-- verified" for a domain that might have all three published and passing.
--
-- That is worse than a blank screen. The whole point of the DNS check is to
-- stop a sender being approved for a domain the customer does not control, and
-- an operator who is told the evidence does not exist has two bad options:
-- refuse a legitimate customer, or approve without the proof. The guard was
-- doing its job, on data it was never allowed to read.
--
-- Same additive, flag-gated mechanism as every other operator widening in
-- 00019. SELECT only: the console reads this evidence, the tenant's own
-- verification flow writes it.
CREATE POLICY sender_dns_records_operator_read ON sender_dns_records
    FOR SELECT USING (acting_as_operator());

-- +goose Down
DROP POLICY sender_dns_records_operator_read ON sender_dns_records;
