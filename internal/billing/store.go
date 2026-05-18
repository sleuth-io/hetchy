package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

type Store struct {
	db *db.Store
}

func NewStore(d *db.Store) *Store { return &Store{db: d} }

func (s *Store) Enabled() bool { return s != nil && s.db != nil }

func (s *Store) EnsureAccount(ctx context.Context, orgID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.EnsureBillingAccount(ctx, orgID)
	if err != nil {
		return Account{}, fmt.Errorf("ensure billing account: %w", err)
	}
	if _, err := s.EnsureTopupSettings(ctx, orgID); err != nil {
		return Account{}, err
	}
	return accountFromRow(row), nil
}

func (s *Store) GetAccount(ctx context.Context, orgID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.GetBillingAccount(ctx, orgID)
	if err != nil {
		return Account{}, err
	}
	return accountFromRow(row), nil
}

func (s *Store) FindAccountByStripeCustomer(ctx context.Context, customerID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.GetBillingAccountByStripeCustomer(ctx, customerID)
	if err != nil {
		return Account{}, err
	}
	return accountFromRow(row), nil
}

func (s *Store) UpsertAccountMirror(ctx context.Context, mirror AccountMirror) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	if mirror.PlanCode == "" {
		mirror.PlanCode = PlanFree
	}
	if mirror.Status == "" {
		mirror.Status = mirror.PlanCode
	}
	if mirror.MaxFlavor == "" {
		mirror.MaxFlavor = FlavorStandard
	}
	if mirror.PerRunMaxCredits < 1 {
		mirror.PerRunMaxCredits = 4
	}
	row, err := s.db.Queries.UpsertBillingAccountMirror(ctx, sqlc.UpsertBillingAccountMirrorParams{
		OrgID:                mirror.OrgID,
		StripeCustomerID:     mirror.StripeCustomerID,
		StripeSubscriptionID: mirror.StripeSubscriptionID,
		PlanCode:             mirror.PlanCode,
		Status:               mirror.Status,
		CurrentPeriodStart:   timestamptz(mirror.CurrentPeriodStart),
		CurrentPeriodEnd:     timestamptz(mirror.CurrentPeriodEnd),
		IncludedCredits:      int32(max(mirror.IncludedCredits, 0)),
		MaxFlavor:            mirror.MaxFlavor,
		PerRunMaxCredits:     int32(mirror.PerRunMaxCredits),
		BillingExempt:        mirror.BillingExempt,
		LastPaymentError:     mirror.LastPaymentError,
	})
	if err != nil {
		return Account{}, fmt.Errorf("upsert billing account mirror: %w", err)
	}
	if _, err := s.EnsureTopupSettings(ctx, mirror.OrgID); err != nil {
		return Account{}, err
	}
	return accountFromRow(row), nil
}

func (s *Store) SetStripeCustomer(ctx context.Context, orgID, customerID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	if _, err := s.EnsureAccount(ctx, orgID); err != nil {
		return Account{}, err
	}
	row, err := s.db.Queries.UpdateBillingStripeCustomer(ctx, sqlc.UpdateBillingStripeCustomerParams{
		OrgID:            orgID,
		StripeCustomerID: customerID,
	})
	if err != nil {
		return Account{}, fmt.Errorf("set stripe customer: %w", err)
	}
	return accountFromRow(row), nil
}

func (s *Store) SetPendingPlanChange(ctx context.Context, orgID, planCode string, effectiveAt time.Time) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.SetBillingPendingPlanChange(ctx, sqlc.SetBillingPendingPlanChangeParams{
		OrgID:                  orgID,
		PendingPlanCode:        planCode,
		PendingPlanEffectiveAt: timestamptz(effectiveAt),
	})
	if err != nil {
		return Account{}, fmt.Errorf("set billing pending plan change: %w", err)
	}
	return accountFromRow(row), nil
}

func (s *Store) ClearPendingPlanChange(ctx context.Context, orgID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.ClearBillingPendingPlanChange(ctx, orgID)
	if err != nil {
		return Account{}, fmt.Errorf("clear billing pending plan change: %w", err)
	}
	return accountFromRow(row), nil
}

func (s *Store) GrantTopupCredits(ctx context.Context, orgID string, credits int) (Account, error) {
	if !s.Enabled() {
		return Account{}, pgx.ErrNoRows
	}
	if credits <= 0 {
		return s.GetAccount(ctx, orgID)
	}
	if _, err := s.EnsureAccount(ctx, orgID); err != nil {
		return Account{}, err
	}
	row, err := s.db.Queries.GrantBillingTopupCredits(ctx, sqlc.GrantBillingTopupCreditsParams{
		OrgID:        orgID,
		TopupCredits: int32(credits),
	})
	if err != nil {
		return Account{}, fmt.Errorf("grant top-up credits: %w", err)
	}
	return accountFromRow(row), nil
}

func (s *Store) GrantTopupCreditsOnce(ctx context.Context, eventID, eventType, orgID string, credits int) (Account, bool, error) {
	if !s.Enabled() {
		return Account{}, false, pgx.ErrNoRows
	}
	if eventID == "" {
		acct, err := s.GrantTopupCredits(ctx, orgID, credits)
		return acct, true, err
	}
	if credits <= 0 {
		acct, err := s.GetAccount(ctx, orgID)
		return acct, false, err
	}
	var account Account
	processed := false
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		inserted, err := q.InsertBillingStripeEvent(ctx, sqlc.InsertBillingStripeEventParams{
			EventID:   eventID,
			EventType: eventType,
			OrgID:     orgID,
		})
		if err != nil {
			return err
		}
		if !inserted {
			row, err := q.GetBillingAccount(ctx, orgID)
			if err != nil {
				return err
			}
			account = accountFromRow(row)
			return nil
		}
		if _, err := q.EnsureBillingAccount(ctx, orgID); err != nil {
			return err
		}
		row, err := q.GrantBillingTopupCredits(ctx, sqlc.GrantBillingTopupCreditsParams{
			OrgID:        orgID,
			TopupCredits: int32(credits),
		})
		if err != nil {
			return err
		}
		account = accountFromRow(row)
		processed = true
		return nil
	})
	if err != nil {
		return Account{}, false, fmt.Errorf("grant top-up credits once: %w", err)
	}
	return account, processed, nil
}

func (s *Store) SetLastPaymentError(ctx context.Context, orgID, msg string) error {
	if !s.Enabled() {
		return pgx.ErrNoRows
	}
	if _, err := s.EnsureAccount(ctx, orgID); err != nil {
		return err
	}
	return s.db.Queries.SetBillingLastPaymentError(ctx, sqlc.SetBillingLastPaymentErrorParams{
		OrgID:            orgID,
		LastPaymentError: msg,
	})
}

func (s *Store) EnsureTopupSettings(ctx context.Context, orgID string) (TopupSettings, error) {
	if !s.Enabled() {
		return TopupSettings{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.EnsureBillingTopupSettings(ctx, orgID)
	if err != nil {
		return TopupSettings{}, fmt.Errorf("ensure top-up settings: %w", err)
	}
	settings := topupSettingsFromRow(row)
	month := currentBillingMonth(time.Now())
	if settings.MonthlyAnchorMonth != month {
		row, err = s.db.Queries.ResetBillingTopupMonthlyUsage(ctx, sqlc.ResetBillingTopupMonthlyUsageParams{
			OrgID:              orgID,
			MonthlyAnchorMonth: month,
		})
		if err != nil {
			return TopupSettings{}, fmt.Errorf("reset top-up monthly usage: %w", err)
		}
		settings = topupSettingsFromRow(row)
	}
	return settings, nil
}

func (s *Store) UpdateTopupSettings(ctx context.Context, orgID string, settings TopupSettings) (TopupSettings, error) {
	if !s.Enabled() {
		return TopupSettings{}, pgx.ErrNoRows
	}
	if _, err := s.EnsureAccount(ctx, orgID); err != nil {
		return TopupSettings{}, err
	}
	row, err := s.db.Queries.UpdateBillingTopupSettings(ctx, sqlc.UpdateBillingTopupSettingsParams{
		OrgID:            orgID,
		AutoTopupEnabled: settings.AutoTopupEnabled,
		TriggerThreshold: int32(max(settings.TriggerThreshold, 0)),
		TargetBalance:    int32(max(settings.TargetBalance, 0)),
		MonthlyMaxUnits:  int32(max(settings.MonthlyMaxUnits, 0)),
		MonthlyMaxCents:  int32(max(settings.MonthlyMaxCents, 0)),
	})
	if err != nil {
		return TopupSettings{}, fmt.Errorf("update top-up settings: %w", err)
	}
	return topupSettingsFromRow(row), nil
}

func (s *Store) IncrementTopupMonthlyUsage(ctx context.Context, orgID string, units, cents int) (TopupSettings, error) {
	if !s.Enabled() {
		return TopupSettings{}, pgx.ErrNoRows
	}
	if units <= 0 && cents <= 0 {
		return s.EnsureTopupSettings(ctx, orgID)
	}
	month := currentBillingMonth(time.Now())
	row, err := s.db.Queries.IncrementBillingTopupMonthlyUsage(ctx, sqlc.IncrementBillingTopupMonthlyUsageParams{
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

func (s *Store) ReserveCredits(ctx context.Context, orgID, runID string, credits int) (Reservation, Account, error) {
	if !s.Enabled() {
		return Reservation{}, Account{}, pgx.ErrNoRows
	}
	if credits < 0 {
		credits = 0
	}
	if _, err := s.EnsureAccount(ctx, orgID); err != nil {
		return Reservation{}, Account{}, err
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
			return nil
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
			return nil
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
		return nil
	})
	if err != nil {
		return Reservation{}, Account{}, err
	}
	return reservation, account, nil
}

func (s *Store) StartRunMeter(ctx context.Context, runID, orgID string, flavor Flavor, startedAt time.Time) (RunMeter, error) {
	if !s.Enabled() {
		return RunMeter{}, pgx.ErrNoRows
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	row, err := s.db.Queries.UpsertBillingRunMeterStart(ctx, sqlc.UpsertBillingRunMeterStartParams{
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
		return RunMeter{}, fmt.Errorf("start run meter: %w", err)
	}
	return runMeterFromRow(row), nil
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
		if _, err := q.FinalizeBillingRunMeter(ctx, sqlc.FinalizeBillingRunMeterParams{
			RunID:           runID,
			EndedAt:         timestamptz(endedAt),
			BillableMinutes: int32(minutes),
			CapturedCredits: int32(credits),
			TerminalState:   terminalState,
		}); err != nil {
			return err
		}
		resRow, err := q.GetBillingCreditReservationForUpdate(ctx, runID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		res := reservationFromRow(resRow)
		if res.Status == ReservationComped {
			_, err := q.UpdateBillingCreditReservationCaptured(ctx, sqlc.UpdateBillingCreditReservationCapturedParams{
				RunID:           runID,
				CapturedCredits: 0,
				ReleasedCredits: 0,
				Status:          ReservationComped,
			})
			return err
		}
		if res.Status != ReservationReserved {
			return nil
		}
		releasedIncluded, releasedTopup, extraCredits := captureDeltas(res, credits)
		if _, err := q.LockBillingAccountForUpdate(ctx, res.OrgID); err != nil {
			return err
		}
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

func (s *Store) Overview(ctx context.Context, orgID string, meterLimit int32) (Overview, error) {
	acct, err := s.EnsureAccount(ctx, orgID)
	if err != nil {
		return Overview{}, err
	}
	settings, err := s.EnsureTopupSettings(ctx, orgID)
	if err != nil {
		return Overview{}, err
	}
	if meterLimit <= 0 {
		meterLimit = 10
	}
	rows, err := s.db.Queries.ListBillingRunMetersByOrg(ctx, sqlc.ListBillingRunMetersByOrgParams{
		OrgID: orgID,
		Limit: meterLimit,
	})
	if err != nil {
		return Overview{}, fmt.Errorf("list billing run meters: %w", err)
	}
	meters := make([]RunMeter, 0, len(rows))
	for _, row := range rows {
		meters = append(meters, runMeterFromRow(row))
	}
	return Overview{
		Account:        acct,
		TopupSettings:  settings,
		RecentMeters:   meters,
		AllowedFlavors: AllowedFlavors(acct.MaxFlavor),
	}, nil
}

func (s *Store) RepoFlavor(ctx context.Context, orgID, owner, repo string) (Flavor, error) {
	if !s.Enabled() {
		return MustFlavor(FlavorStandard), nil
	}
	row, err := s.db.Queries.GetRepoBillingSetting(ctx, sqlc.GetRepoBillingSettingParams{
		OrgID: orgID, GithubOwner: owner, GithubRepo: repo,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MustFlavor(FlavorStandard), nil
		}
		return Flavor{}, err
	}
	return ParseFlavor(row.Flavor)
}

func (s *Store) ListRepoSettings(ctx context.Context, orgID string) (map[string]RepoSetting, error) {
	if !s.Enabled() {
		return map[string]RepoSetting{}, nil
	}
	rows, err := s.db.Queries.ListRepoBillingSettingsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list repo billing settings: %w", err)
	}
	out := make(map[string]RepoSetting, len(rows))
	for _, row := range rows {
		setting := repoSettingFromRow(row)
		out[setting.GitHubOwner+"/"+setting.GitHubRepo] = setting
	}
	return out, nil
}

func (s *Store) SetRepoFlavor(ctx context.Context, orgID, owner, repo, flavorCode string) (RepoSetting, error) {
	if !s.Enabled() {
		return RepoSetting{}, pgx.ErrNoRows
	}
	acct, err := s.EnsureAccount(ctx, orgID)
	if err != nil {
		return RepoSetting{}, err
	}
	flavor, err := ParseFlavor(flavorCode)
	if err != nil {
		return RepoSetting{}, err
	}
	if !FlavorAllowed(flavor.Code, acct.MaxFlavor) {
		return RepoSetting{}, FlavorNotAllowedError{Flavor: flavor.Code, MaxFlavor: acct.MaxFlavor}
	}
	if flavor.Code == FlavorStandard {
		if err := s.db.Queries.DeleteRepoBillingSetting(ctx, sqlc.DeleteRepoBillingSettingParams{
			OrgID: orgID, GithubOwner: owner, GithubRepo: repo,
		}); err != nil {
			return RepoSetting{}, fmt.Errorf("delete standard repo billing setting: %w", err)
		}
		return RepoSetting{OrgID: orgID, GitHubOwner: owner, GitHubRepo: repo, Flavor: FlavorStandard}, nil
	}
	row, err := s.db.Queries.UpsertRepoBillingSetting(ctx, sqlc.UpsertRepoBillingSettingParams{
		OrgID: orgID, GithubOwner: owner, GithubRepo: repo, Flavor: flavor.Code,
	})
	if err != nil {
		return RepoSetting{}, fmt.Errorf("set repo billing flavor: %w", err)
	}
	return repoSettingFromRow(row), nil
}

func accountFromRow(row sqlc.BillingAccount) Account {
	return Account{
		OrgID:                  row.OrgID,
		StripeCustomerID:       row.StripeCustomerID,
		StripeSubscriptionID:   row.StripeSubscriptionID,
		PlanCode:               row.PlanCode,
		Status:                 row.Status,
		CurrentPeriodStart:     pgTime(row.CurrentPeriodStart),
		CurrentPeriodEnd:       pgTime(row.CurrentPeriodEnd),
		IncludedCredits:        int(row.IncludedCredits),
		IncludedCreditsUsed:    int(row.IncludedCreditsUsed),
		TopupCredits:           int(row.TopupCredits),
		MaxFlavor:              row.MaxFlavor,
		PerRunMaxCredits:       int(row.PerRunMaxCredits),
		BillingExempt:          row.BillingExempt,
		LastPaymentError:       row.LastPaymentError,
		PendingPlanCode:        row.PendingPlanCode,
		PendingPlanEffectiveAt: pgTime(row.PendingPlanEffectiveAt),
		CreatedAt:              pgTime(row.CreatedAt),
		UpdatedAt:              pgTime(row.UpdatedAt),
	}
}

func topupSettingsFromRow(row sqlc.BillingTopupSetting) TopupSettings {
	return TopupSettings{
		OrgID:                 row.OrgID,
		AutoTopupEnabled:      row.AutoTopupEnabled,
		TriggerThreshold:      int(row.TriggerThreshold),
		TargetBalance:         int(row.TargetBalance),
		MonthlyMaxUnits:       int(row.MonthlyMaxUnits),
		MonthlyUnitsUsed:      int(row.MonthlyUnitsUsed),
		MonthlyMaxCents:       int(row.MonthlyMaxCents),
		MonthlySpendCentsUsed: int(row.MonthlySpendCentsUsed),
		MonthlyAnchorMonth:    row.MonthlyAnchorMonth,
		CreatedAt:             pgTime(row.CreatedAt),
		UpdatedAt:             pgTime(row.UpdatedAt),
	}
}

func reservationFromRow(row sqlc.BillingCreditReservation) Reservation {
	return Reservation{
		RunID:               row.RunID,
		OrgID:               row.OrgID,
		ReservedCredits:     int(row.ReservedCredits),
		FromIncludedCredits: int(row.FromIncludedCredits),
		FromTopupCredits:    int(row.FromTopupCredits),
		CapturedCredits:     int(row.CapturedCredits),
		ReleasedCredits:     int(row.ReleasedCredits),
		Status:              row.Status,
		CreatedAt:           pgTime(row.CreatedAt),
		UpdatedAt:           pgTime(row.UpdatedAt),
	}
}

func runMeterFromRow(row sqlc.BillingRunMeter) RunMeter {
	return RunMeter{
		RunID:            row.RunID,
		OrgID:            row.OrgID,
		Flavor:           row.Flavor,
		Multiplier:       int(row.Multiplier),
		SandboxVCPU:      int(row.SandboxVcpu),
		SandboxMemoryGiB: int(row.SandboxMemoryGib),
		SandboxDiskGiB:   int(row.SandboxDiskGib),
		StartedAt:        pgTime(row.StartedAt),
		EndedAt:          pgTime(row.EndedAt),
		BillableMinutes:  int(row.BillableMinutes),
		CapturedCredits:  int(row.CapturedCredits),
		TerminalState:    row.TerminalState,
		CreatedAt:        pgTime(row.CreatedAt),
		UpdatedAt:        pgTime(row.UpdatedAt),
	}
}

func repoSettingFromRow(row sqlc.RepoBillingSetting) RepoSetting {
	return RepoSetting{
		OrgID:       row.OrgID,
		GitHubOwner: row.GithubOwner,
		GitHubRepo:  row.GithubRepo,
		Flavor:      row.Flavor,
		CreatedAt:   pgTime(row.CreatedAt),
		UpdatedAt:   pgTime(row.UpdatedAt),
	}
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func pgTime(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

func currentBillingMonth(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format("2006-01")
}
