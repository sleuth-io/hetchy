ALTER TABLE billing_topup_settings
    ADD COLUMN IF NOT EXISTS monthly_max_cents INTEGER NOT NULL DEFAULT 0 CHECK (monthly_max_cents >= 0),
    ADD COLUMN IF NOT EXISTS monthly_spend_cents_used INTEGER NOT NULL DEFAULT 0 CHECK (monthly_spend_cents_used >= 0);

UPDATE billing_topup_settings AS settings
SET monthly_max_cents = CASE
    WHEN settings.monthly_max_cents = 0 AND settings.monthly_max_units > 0 THEN settings.monthly_max_units *
    CASE account.plan_code
        WHEN 'starter' THEN 1250
        WHEN 'growth' THEN 650
        WHEN 'business' THEN 450
        ELSE 900
    END
    ELSE settings.monthly_max_cents
    END,
    monthly_spend_cents_used = CASE
    WHEN settings.monthly_spend_cents_used = 0 AND settings.monthly_units_used > 0 THEN settings.monthly_units_used *
    CASE account.plan_code
        WHEN 'starter' THEN 1250
        WHEN 'growth' THEN 650
        WHEN 'business' THEN 450
        ELSE 900
    END
    ELSE settings.monthly_spend_cents_used
    END
FROM billing_accounts AS account
WHERE account.org_id = settings.org_id
  AND ((settings.monthly_max_cents = 0 AND settings.monthly_max_units > 0)
    OR (settings.monthly_spend_cents_used = 0 AND settings.monthly_units_used > 0));
