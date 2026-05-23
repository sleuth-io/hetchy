UPDATE billing_accounts
SET max_flavor = 'max',
    per_run_max_credits = 6,
    updated_at = NOW()
WHERE plan_code = 'team'
  AND (max_flavor <> 'max' OR per_run_max_credits <> 6);
