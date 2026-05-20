UPDATE billing_accounts
SET max_flavor = 'pro',
    per_run_max_credits = 3,
    updated_at = NOW()
WHERE plan_code = 'team'
  AND max_flavor = 'max'
  AND per_run_max_credits = 6;
