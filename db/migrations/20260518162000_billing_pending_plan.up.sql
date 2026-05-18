ALTER TABLE billing_accounts
    ADD COLUMN IF NOT EXISTS pending_plan_code TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pending_plan_effective_at TIMESTAMPTZ;
