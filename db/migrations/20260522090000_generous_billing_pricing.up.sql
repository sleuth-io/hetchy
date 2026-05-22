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

UPDATE billing_accounts
SET included_credits = CASE plan_code
        WHEN 'starter' THEN 100
        WHEN 'team' THEN 500
        WHEN 'growth' THEN 1400
        WHEN 'business' THEN 3600
        ELSE included_credits
    END,
    included_credits_used = LEAST(included_credits_used, CASE plan_code
        WHEN 'starter' THEN 100
        WHEN 'team' THEN 500
        WHEN 'growth' THEN 1400
        WHEN 'business' THEN 3600
        ELSE included_credits
    END),
    max_flavor = CASE plan_code
        WHEN 'starter' THEN 'standard'
        WHEN 'team' THEN 'plus'
        WHEN 'growth' THEN 'plus'
        WHEN 'business' THEN 'plus'
        ELSE max_flavor
    END,
    per_run_max_credits = CASE plan_code
        WHEN 'starter' THEN 4
        WHEN 'team' THEN 12
        WHEN 'growth' THEN 24
        WHEN 'business' THEN 48
        ELSE per_run_max_credits
    END,
    updated_at = NOW()
WHERE plan_code IN ('starter', 'team', 'growth', 'business');

UPDATE repo_billing_settings
SET flavor = 'plus',
    updated_at = NOW()
WHERE flavor IN ('pro', 'max', 'enterprise');
