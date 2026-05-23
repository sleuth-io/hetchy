ALTER TABLE billing_accounts
    ALTER COLUMN included_credits SET DEFAULT 10;

UPDATE billing_accounts
SET included_credits = 10,
    -- Clamp used credits to the restored limit; excess usage during the
    -- 25-credit window is forgiven on rollback.
    included_credits_used = LEAST(included_credits_used, 10),
    updated_at = NOW()
WHERE plan_code = 'free'
  AND included_credits = 25;
