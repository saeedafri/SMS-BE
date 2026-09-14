-- +goose Up
-- The signing secret, encrypted with the server key, so events can be signed
-- with the secret the customer was shown. Only its hash was stored before, and
-- events were signed with the 14-character display prefix instead, which no
-- customer can verify. Existing endpoints stay NULL: their secret cannot be
-- recovered, so their deliveries are skipped until the endpoint is recreated.
ALTER TABLE webhook_endpoints ADD COLUMN signing_secret_sealed text;

-- +goose Down
ALTER TABLE webhook_endpoints DROP COLUMN signing_secret_sealed;
