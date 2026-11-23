-- +goose Up

-- The approved-links list (cta_whitelist) is one registration per host, so
-- "one registration per tenant, country and object" now holds for every other
-- object only. The list itself holds each kind and value once.
ALTER TABLE registrations DROP CONSTRAINT registrations_tenant_id_country_object_key_key;
CREATE UNIQUE INDEX registrations_one_per_object ON registrations (tenant_id, country, object_key)
    WHERE object_key <> 'cta_whitelist';
CREATE UNIQUE INDEX registrations_one_per_link ON registrations
    (tenant_id, country, (fields ->> 'type'), lower(fields ->> 'value'))
    WHERE object_key = 'cta_whitelist';

-- +goose Down
DROP INDEX registrations_one_per_link;
DROP INDEX registrations_one_per_object;
ALTER TABLE registrations ADD CONSTRAINT registrations_tenant_id_country_object_key_key
    UNIQUE (tenant_id, country, object_key);
