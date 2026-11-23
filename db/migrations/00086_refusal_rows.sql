-- +goose Up

-- Every refusal code has a dictionary row, the ones email and WhatsApp add
-- included. Channel-neutral, like the rest of our own refusals.
INSERT INTO failure_reasons (id, channel, source, match_kind, match_value, layer, code, title,
    fix, fix_kind, retryable) VALUES
('fr-0148', NULL, 'gate', 'exact', 'sending_paused', 'provider', 'sending_paused',
 'Email sending is paused for this account',
 'Contact support to resume email sending.', 'support', false),
('fr-0149', NULL, 'gate', 'exact', 'variables_missing', 'ours', 'variables_missing',
 'The template needs a value for every variable',
 'Give each of the template''s variables a value and send again.', 'templates', false),
('fr-0150', NULL, 'gate', 'exact', 'messaging_limit_reached', 'provider', 'messaging_limit_reached',
 'This WhatsApp number has reached its daily limit of new conversations',
 'Send to fewer people today, or wait for the limit to rise.', 'limits', true);

-- +goose Down
DELETE FROM failure_reasons WHERE id IN ('fr-0148', 'fr-0149', 'fr-0150');
