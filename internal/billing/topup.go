package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

type autoTopupPaymentError struct {
	err error
}

func (e autoTopupPaymentError) Error() string {
	return e.err.Error()
}

func (e autoTopupPaymentError) Unwrap() error {
	return e.err
}

type autoTopupAttempt struct {
	account        Account
	idempotencyKey string
	target         int
	topupUnitCents int
	shouldPurchase bool
}

func (s *Store) AutoTopup(ctx context.Context, orgID string, reserveCredits int, purchase func(context.Context, Account, string) (string, error)) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	var account Account
	triggered := false
	target := 0
	for {
		attempt, err := s.prepareAutoTopupAttempt(ctx, orgID, reserveCredits, triggered, target)
		if err != nil {
			return Account{}, err
		}
		account = attempt.account
		if !attempt.shouldPurchase {
			if account.Balance() < reserveCredits {
				return Account{}, InsufficientCreditsError{Needed: reserveCredits, Available: account.Balance()}
			}
			return account, nil
		}
		if purchase == nil {
			return Account{}, ErrAutoTopupNotConfigured
		}
		triggered = true
		target = attempt.target
		invoiceID, err := purchase(ctx, attempt.account, attempt.idempotencyKey)
		if err != nil {
			return Account{}, autoTopupPaymentError{err: err}
		}
		grantKey := strings.TrimSpace(invoiceID)
		if grantKey == "" {
			grantKey = attempt.idempotencyKey
		}
		updated, err := s.completeAutoTopupAttempt(ctx, orgID, grantKey, attempt.topupUnitCents)
		if err != nil {
			return Account{}, err
		}
		if updated.Balance() >= target {
			return updated, nil
		}
	}
}

func (s *Store) prepareAutoTopupAttempt(ctx context.Context, orgID string, reserveCredits int, triggered bool, target int) (autoTopupAttempt, error) {
	var attempt autoTopupAttempt
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		row, err := q.LockBillingAccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		attempt.account = accountFromRow(row)
		settings, err := ensureTopupSettings(ctx, q, orgID)
		if err != nil {
			return err
		}
		settingsRow, err := q.LockBillingTopupSettingsForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		settings = topupSettingsFromRow(settingsRow)
		if !settings.AutoTopupEnabled || (!triggered && attempt.account.Balance() > settings.TriggerThreshold) {
			return nil
		}

		if target <= 0 {
			target = max(settings.TargetBalance, reserveCredits)
		}
		topupUnitCents := topupUnitCentsForPlan(attempt.account.PlanCode)
		if attempt.account.Balance() >= target ||
			(settings.MonthlyMaxCents > 0 && settings.MonthlySpendCentsUsed+topupUnitCents > settings.MonthlyMaxCents) {
			return nil
		}

		attempt.target = target
		attempt.topupUnitCents = topupUnitCents
		attempt.idempotencyKey = autoTopupIdempotencyKey(
			orgID,
			attempt.account.PlanCode,
			settings.MonthlyAnchorMonth,
			settings.MonthlyUnitsUsed+1,
			topupUnitCents,
		)
		attempt.shouldPurchase = true
		return nil
	})
	if err != nil {
		return autoTopupAttempt{}, err
	}
	return attempt, nil
}

func (s *Store) completeAutoTopupAttempt(ctx context.Context, orgID, invoiceID string, topupUnitCents int) (Account, error) {
	var account Account
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		if _, err := q.LockBillingAccountForUpdate(ctx, orgID); err != nil {
			return err
		}
		if _, err := ensureTopupSettings(ctx, q, orgID); err != nil {
			return err
		}
		if _, err := q.LockBillingTopupSettingsForUpdate(ctx, orgID); err != nil {
			return err
		}
		updated, processed, err := grantTopupCreditsForInvoice(ctx, q, strings.TrimSpace(invoiceID), orgID, TopupUnitCredits)
		if err != nil {
			return err
		}
		account = updated
		if processed {
			if _, err := incrementTopupMonthlyUsage(ctx, q, orgID, 1, topupUnitCents); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

func autoTopupIdempotencyKey(orgID, planCode, billingMonth string, nextUnit, topupUnitCents int) string {
	sum := sha256.Sum256([]byte(orgID + "|" + planCode + "|" + billingMonth + "|" + strconv.Itoa(nextUnit) + "|" + strconv.Itoa(topupUnitCents)))
	return "hetchy-auto-topup-" + hex.EncodeToString(sum[:16])
}

func grantTopupCreditsForInvoice(ctx context.Context, q *sqlc.Queries, invoiceID, orgID string, credits int) (Account, bool, error) {
	if invoiceID != "" {
		inserted, err := q.InsertBillingStripeEvent(ctx, sqlc.InsertBillingStripeEventParams{
			EventID:   invoiceID,
			EventType: "invoice.paid",
			OrgID:     orgID,
		})
		if err != nil {
			return Account{}, false, err
		}
		if !inserted {
			row, err := q.GetBillingAccount(ctx, orgID)
			if err != nil {
				return Account{}, false, err
			}
			return accountFromRow(row), false, nil
		}
	}
	row, err := q.GrantBillingTopupCredits(ctx, sqlc.GrantBillingTopupCreditsParams{
		OrgID:        orgID,
		TopupCredits: int32(credits),
	})
	if err != nil {
		return Account{}, false, err
	}
	return accountFromRow(row), true, nil
}

func incrementTopupMonthlyUsage(ctx context.Context, q *sqlc.Queries, orgID string, units, cents int) (TopupSettings, error) {
	month := currentBillingMonth(time.Now())
	row, err := q.IncrementBillingTopupMonthlyUsage(ctx, sqlc.IncrementBillingTopupMonthlyUsageParams{
		OrgID:                 orgID,
		MonthlyUnitsUsed:      int32(max(units, 0)),
		MonthlySpendCentsUsed: int32(max(cents, 0)),
		MonthlyAnchorMonth:    month,
	})
	if err != nil {
		return TopupSettings{}, fmt.Errorf("increment monthly top-up usage: %w", err)
	}
	return topupSettingsFromRow(row), nil
}
