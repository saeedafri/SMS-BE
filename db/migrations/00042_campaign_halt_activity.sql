-- +goose Up

-- Pause, resume and cancel are customer-initiated state changes on a billable
-- object, so they belong in the same activity log as the other three —
-- inviting a teammate, minting a key, revoking a session.
--
-- Creating a campaign is not recorded today, which is its own gap; these are
-- recorded because a halt is the action someone asks about afterwards. "Who
-- stopped the 900,000-recipient send, and when" is a question with a real
-- answer only if it was written down at the time.
ALTER TABLE user_activity DROP CONSTRAINT user_activity_event_type_check;
ALTER TABLE user_activity ADD CONSTRAINT user_activity_event_type_check
    CHECK (event_type IN (
        'login','mfa.enroll','mfa.disable','api_key.create',
        'api_key.revoke','api_key.rotate','session.revoke',
        'team.invite','team.role_change','sso.config_change',
        'campaign.pause','campaign.resume','campaign.cancel'));

-- +goose Down
DELETE FROM user_activity
 WHERE event_type IN ('campaign.pause','campaign.resume','campaign.cancel');
ALTER TABLE user_activity DROP CONSTRAINT user_activity_event_type_check;
ALTER TABLE user_activity ADD CONSTRAINT user_activity_event_type_check
    CHECK (event_type IN (
        'login','mfa.enroll','mfa.disable','api_key.create',
        'api_key.revoke','api_key.rotate','session.revoke',
        'team.invite','team.role_change','sso.config_change'));
