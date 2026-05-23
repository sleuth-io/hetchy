ALTER TABLE billing_topup_settings
    DROP COLUMN IF EXISTS monthly_spend_cents_used,
    DROP COLUMN IF EXISTS monthly_max_cents;
