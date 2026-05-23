ALTER TABLE billing_accounts
    ALTER COLUMN included_credits SET DEFAULT 25;

UPDATE billing_accounts
SET included_credits = 25,
    included_credits_used = LEAST(included_credits_used, 25),
    updated_at = NOW()
WHERE plan_code = 'free'
  AND included_credits = 10;
