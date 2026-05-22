ALTER TABLE billing_accounts
    DROP CONSTRAINT IF EXISTS billing_accounts_max_flavor_check;

ALTER TABLE billing_accounts
    ADD CONSTRAINT billing_accounts_max_flavor_check
    CHECK (max_flavor IN ('standard', 'plus', 'pro', 'max', 'enterprise'));

ALTER TABLE repo_billing_settings
    DROP CONSTRAINT IF EXISTS repo_billing_settings_flavor_check;

ALTER TABLE repo_billing_settings
    ADD CONSTRAINT repo_billing_settings_flavor_check
    CHECK (flavor IN ('standard', 'plus', 'pro', 'max', 'enterprise'));
