-- +goose Up

-- The retention worker reads every tenant's message_log_retention_days in one
-- query through the operator pool. Additive, as 00019 established: open only
-- while app.operator is set.
CREATE POLICY tenant_settings_operator ON tenant_settings
    FOR ALL USING (acting_as_operator()) WITH CHECK (acting_as_operator());

-- +goose Down
DROP POLICY tenant_settings_operator ON tenant_settings;
