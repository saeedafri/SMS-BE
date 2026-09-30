-- +goose Up

-- Trustsignal (sold as Sigmo): an aggregator reaching every Indian network
-- through one account. See connector.TrustsignalRCS.
ALTER TABLE rcs_connections DROP CONSTRAINT rcs_connections_vendor_check;
ALTER TABLE rcs_connections ADD CONSTRAINT rcs_connections_vendor_check
    CHECK (vendor IN ('airtel', 'vi', 'jio', 'google', 'trustsignal'));

-- +goose Down
ALTER TABLE rcs_connections DROP CONSTRAINT rcs_connections_vendor_check;
ALTER TABLE rcs_connections ADD CONSTRAINT rcs_connections_vendor_check
    CHECK (vendor IN ('airtel', 'vi', 'jio', 'google'));
