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
	return ensureTopupSettings(ctx, s.db.Queries, orgID)
}

func ensureTopupSettings(ctx context.Context, q *sqlc.Queries, orgID string) (TopupSettings, error) {
	row, err := q.EnsureBillingTopupSettings(ctx, orgID)
	if err != nil {
		return TopupSettings{}, fmt.Errorf("ensure top-up settings: %w", err)
	}
	settings := topupSettingsFromRow(row)
	month := currentBillingMonth(time.Now())
	if settings.MonthlyAnchorMonth != month {
		row, err = q.ResetBillingTopupMonthlyUsage(ctx, sqlc.ResetBillingTopupMonthlyUsageParams{
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
	if _, err := s.EnsureTopupSettings(ctx, orgID); err != nil {
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
