UPDATE billing_accounts
SET included_credits = CASE plan_code
        WHEN 'starter' THEN 50
        WHEN 'team' THEN 300
        WHEN 'growth' THEN 1000
        WHEN 'business' THEN 4000
        ELSE included_credits
    END,
    included_credits_used = LEAST(included_credits_used, CASE plan_code
        WHEN 'starter' THEN 50
        WHEN 'team' THEN 300
        WHEN 'growth' THEN 1000
        WHEN 'business' THEN 4000
        ELSE included_credits
    END),
    max_flavor = CASE plan_code
        WHEN 'starter' THEN 'standard'
        WHEN 'team' THEN 'max'
        WHEN 'growth' THEN 'max'
        WHEN 'business' THEN 'max'
        ELSE max_flavor
    END,
    per_run_max_credits = CASE plan_code
        WHEN 'starter' THEN 1
        WHEN 'team' THEN 6
        WHEN 'growth' THEN 6
        WHEN 'business' THEN 6
        ELSE per_run_max_credits
    END,
    updated_at = NOW()
WHERE plan_code IN ('starter', 'team', 'growth', 'business');

UPDATE repo_billing_settings
SET flavor = 'pro',
    updated_at = NOW()
WHERE flavor = 'plus';

UPDATE billing_accounts
SET max_flavor = 'max',
    updated_at = NOW()
WHERE max_flavor = 'plus';

ALTER TABLE billing_accounts
    DROP CONSTRAINT IF EXISTS billing_accounts_max_flavor_check;

ALTER TABLE billing_accounts
    ADD CONSTRAINT billing_accounts_max_flavor_check
    CHECK (max_flavor IN ('standard', 'pro', 'max', 'enterprise'));

ALTER TABLE repo_billing_settings
    DROP CONSTRAINT IF EXISTS repo_billing_settings_flavor_check;

ALTER TABLE repo_billing_settings
    ADD CONSTRAINT repo_billing_settings_flavor_check
    CHECK (flavor IN ('standard', 'pro', 'max', 'enterprise'));
