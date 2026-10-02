-- +goose Up

-- The public redirect has no session, so it reads and writes across tenants by
-- code alone. It first used the migration role, which production deliberately
-- does not hold in the API process, so every live redirect answered 503. It
-- uses the operator pool instead, as the inbound-SMS attribution does, which
-- needs these policies. Additive, as 00019 established.
CREATE POLICY short_links_operator ON short_links
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());
CREATE POLICY link_clicks_operator ON link_clicks
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY link_clicks_operator ON link_clicks;
DROP POLICY short_links_operator ON short_links;
