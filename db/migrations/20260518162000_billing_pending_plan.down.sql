ALTER TABLE billing_accounts
    DROP COLUMN IF EXISTS pending_plan_effective_at,
    DROP COLUMN IF EXISTS pending_plan_code;
