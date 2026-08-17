-- +goose Up
-- The operator console could not change a price.
--
-- pricing_rates was granted SELECT only, so every rate edit in the console ran,
-- returned an error the handler turned into a 500 or was swallowed on the way
-- back, and the number on screen snapped back to the old one on the next load.
-- The rate card looked editable and was not.
--
-- The operator pool connects as the same sms_app role — the separation between
-- tenant and operator traffic is the app.operator session flag and the
-- authenticated operator session in front of it, not a second database user —
-- so write access has to be granted to that role. This matches what 00018
-- already does for the other operator-owned tables (routes, rate_overrides,
-- operator_audit_log).
--
-- No DELETE: a default rate is amended, never removed. A corridor with no rate
-- at all cannot be quoted, so deleting one would break sending on that corridor
-- rather than making it free. Tenant-specific overrides are the deletable ones,
-- and they live in rate_overrides.
GRANT INSERT, UPDATE ON pricing_rates TO sms_app;

-- +goose Down
REVOKE INSERT, UPDATE ON pricing_rates FROM sms_app;
