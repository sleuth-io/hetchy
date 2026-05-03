-- Re-add slack_team_id, dropped in 20260501233125. We need it back so the
-- HTTP webhook transport can map an inbound Slack event (which carries a
-- team_id at the payload root) to the right org's bot token. Socket Mode
-- doesn't need this lookup because each socket already knows its org,
-- but a distributable Slack app delivers all installs' events to one URL.
ALTER TABLE org_configs ADD COLUMN slack_team_id TEXT;

-- One org per Slack workspace. NULLs are allowed (orgs that haven't
-- connected Slack yet); the partial index lets us enforce uniqueness only
-- where the value is set.
CREATE UNIQUE INDEX org_configs_slack_team_id_idx
    ON org_configs (slack_team_id)
    WHERE slack_team_id IS NOT NULL;
