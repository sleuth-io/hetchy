package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

func (s *Store) AdmitRun(ctx context.Context, orgID, runID string, credits int, flavor Flavor, startedAt time.Time) (Reservation, Account, error) {
	if !s.Enabled() {
		return Reservation{}, Account{}, pgx.ErrNoRows
	}
	if credits < 0 {
		credits = 0
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	var reservation Reservation
	var account Account
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		if existing, err := q.GetBillingCreditReservationForUpdate(ctx, runID); err == nil {
			row, err := q.LockBillingAccountForUpdate(ctx, orgID)
			if err != nil {
				return err
			}
			reservation = reservationFromRow(existing)
			account = accountFromRow(row)
			return upsertRunMeterStart(ctx, q, runID, orgID, flavor, startedAt)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		row, err := q.LockBillingAccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		account = accountFromRow(row)
		if account.BillingExempt {
			inserted, err := q.InsertBillingCreditReservation(ctx, sqlc.InsertBillingCreditReservationParams{
				RunID: runID, OrgID: orgID, Status: ReservationComped,
			})
			if err != nil {
				return err
			}
			reservation = reservationFromRow(inserted)
			return upsertRunMeterStart(ctx, q, runID, orgID, flavor, startedAt)
		}
		available := account.Balance()
		if available < credits {
			return InsufficientCreditsError{Needed: credits, Available: available}
		}
		fromIncluded := min(credits, account.IncludedRemaining())
		fromTopup := credits - fromIncluded
		row, err = q.UpdateBillingReservedBalances(ctx, sqlc.UpdateBillingReservedBalancesParams{
			OrgID:               orgID,
			IncludedCreditsUsed: int32(fromIncluded),
			TopupCredits:        int32(fromTopup),
		})
		if err != nil {
			return err
		}
		account = accountFromRow(row)
		inserted, err := q.InsertBillingCreditReservation(ctx, sqlc.InsertBillingCreditReservationParams{
			RunID:               runID,
			OrgID:               orgID,
			ReservedCredits:     int32(credits),
			FromIncludedCredits: int32(fromIncluded),
			FromTopupCredits:    int32(fromTopup),
			Status:              ReservationReserved,
		})
		if err != nil {
			return err
		}
		reservation = reservationFromRow(inserted)
		return upsertRunMeterStart(ctx, q, runID, orgID, flavor, startedAt)
	})
	if err != nil {
		return Reservation{}, Account{}, err
	}
	return reservation, account, nil
}

func upsertRunMeterStart(ctx context.Context, q *sqlc.Queries, runID, orgID string, flavor Flavor, startedAt time.Time) error {
	_, err := q.UpsertBillingRunMeterStart(ctx, sqlc.UpsertBillingRunMeterStartParams{
		RunID:            runID,
		OrgID:            orgID,
		Flavor:           flavor.Code,
		Multiplier:       int32(flavor.Multiplier),
		SandboxVcpu:      int32(flavor.VCPU),
		SandboxMemoryGib: int32(flavor.MemoryGiB),
		SandboxDiskGib:   int32(flavor.DiskGiB),
		StartedAt:        timestamptz(startedAt),
	})
	if err != nil {
		return fmt.Errorf("start run meter: %w", err)
	}
	return nil
}

func (s *Store) FinalizeRun(ctx context.Context, runID, terminalState string, endedAt time.Time) error {
	if !s.Enabled() || runID == "" {
		return nil
	}
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	return s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		meterRow, err := q.GetBillingRunMeterForUpdate(ctx, runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		meter := runMeterFromRow(meterRow)
		if meter.TerminalState != "" {
			return nil
		}
		minutes, credits := BillableCredits(meter.StartedAt, endedAt, meter.Multiplier)
		if terminalState == "" {
			terminalState = "unknown"
		}
		resRow, err := q.GetBillingCreditReservationForUpdate(ctx, runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return finalizeRunMeter(ctx, q, runID, endedAt, minutes, credits, terminalState)
			}
			return err
		}
		res := reservationFromRow(resRow)
		if res.Status == ReservationComped {
			// Record actual credits for visibility in the billing meter, but do not
			// deduct from the account balance — comped orgs are never charged.
			if err := finalizeRunMeter(ctx, q, runID, endedAt, minutes, credits, terminalState); err != nil {
				return err
			}
			_, err := q.UpdateBillingCreditReservationCaptured(ctx, sqlc.UpdateBillingCreditReservationCapturedParams{
				RunID:           runID,
				CapturedCredits: int32(credits),
				ReleasedCredits: 0,
				Status:          ReservationComped,
			})
			return err
		}
		if res.Status != ReservationReserved {
			return finalizeRunMeter(ctx, q, runID, endedAt, minutes, credits, terminalState)
		}
		if _, err := q.LockBillingAccountForUpdate(ctx, res.OrgID); err != nil {
			return err
		}
		if res.ReservedCredits > 0 {
			credits = min(credits, res.ReservedCredits)
		}
		if err := finalizeRunMeter(ctx, q, runID, endedAt, minutes, credits, terminalState); err != nil {
			return err
		}
		releasedIncluded, releasedTopup, extraCredits := captureDeltas(res, credits)
		includedDelta := -releasedIncluded + extraCredits
		topupDelta := releasedTopup
		if _, err := q.UpdateBillingCapturedBalances(ctx, sqlc.UpdateBillingCapturedBalancesParams{
			OrgID:               res.OrgID,
			IncludedCreditsUsed: int32(includedDelta),
			TopupCredits:        int32(topupDelta),
		}); err != nil {
			return err
		}
		released := releasedIncluded + releasedTopup
		status := ReservationCaptured
		if released > 0 {
			status = ReservationReleased
		}
		_, err = q.UpdateBillingCreditReservationCaptured(ctx, sqlc.UpdateBillingCreditReservationCapturedParams{
			RunID:           runID,
			CapturedCredits: int32(credits),
			ReleasedCredits: int32(released),
			Status:          status,
		})
		return err
	})
}

func finalizeRunMeter(ctx context.Context, q *sqlc.Queries, runID string, endedAt time.Time, minutes, credits int, terminalState string) error {
	_, err := q.FinalizeBillingRunMeter(ctx, sqlc.FinalizeBillingRunMeterParams{
		RunID:           runID,
		EndedAt:         timestamptz(endedAt),
		BillableMinutes: int32(minutes),
		CapturedCredits: int32(credits),
		TerminalState:   terminalState,
	})
	return err
}

func captureDeltas(res Reservation, captured int) (releasedIncluded, releasedTopup, extra int) {
	if captured < 0 {
		captured = 0
	}
	capturedIncluded := min(captured, res.FromIncludedCredits)
	remainingCapture := max(captured-capturedIncluded, 0)
	capturedTopup := min(remainingCapture, res.FromTopupCredits)
	releasedIncluded = max(res.FromIncludedCredits-capturedIncluded, 0)
	releasedTopup = max(res.FromTopupCredits-capturedTopup, 0)
	extra = max(captured-res.ReservedCredits, 0)
	return releasedIncluded, releasedTopup, extra
}
