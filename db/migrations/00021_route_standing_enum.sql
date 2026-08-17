-- +goose Up

-- compliance_standing was invented as good/watch/blocked. The contract's
-- ComplianceStanding enum is registered/grey — two values, meaning "this route
-- is registered with the regulator" or "it is a grey route". The console
-- resolves the value against that enum, so any other string renders nothing.
--
-- Found the same way as the carrier-id mismatch: the API happily returned a
-- value no client could interpret.
ALTER TABLE routes DROP CONSTRAINT routes_compliance_standing_check;
UPDATE routes SET compliance_standing = CASE compliance_standing
    WHEN 'good' THEN 'registered'
    ELSE 'grey'
END;
ALTER TABLE routes ALTER COLUMN compliance_standing SET DEFAULT 'registered';
ALTER TABLE routes ADD CONSTRAINT routes_compliance_standing_check
    CHECK (compliance_standing IN ('registered', 'grey'));

-- +goose Down
ALTER TABLE routes DROP CONSTRAINT routes_compliance_standing_check;
UPDATE routes SET compliance_standing = CASE compliance_standing
    WHEN 'registered' THEN 'good'
    ELSE 'watch'
END;
ALTER TABLE routes ALTER COLUMN compliance_standing SET DEFAULT 'good';
ALTER TABLE routes ADD CONSTRAINT routes_compliance_standing_check
    CHECK (compliance_standing IN ('good', 'watch', 'blocked'));
