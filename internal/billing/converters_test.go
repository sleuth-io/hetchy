package billing

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

func makeTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func TestAccountFromRow(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	row := sqlc.BillingAccount{
		OrgID:                  "org1",
		StripeCustomerID:       "cus_1",
		StripeSubscriptionID:   "sub_1",
		PlanCode:               PlanStudio,
		Status:                 "active",
		CurrentPeriodStart:     makeTimestamptz(now),
		CurrentPeriodEnd:       makeTimestamptz(now.Add(30 * 24 * time.Hour)),
		IncludedCredits:        500,
		IncludedCreditsUsed:    100,
		TopupCredits:           200,
		MaxFlavor:              FlavorPlus,
		PerRunMaxCredits:       12,
		BillingExempt:          false,
		LastPaymentError:       "",
		PendingPlanCode:        PlanGrowth,
		PendingPlanEffectiveAt: makeTimestamptz(now.Add(60 * 24 * time.Hour)),
		CreatedAt:              makeTimestamptz(now),
		UpdatedAt:              makeTimestamptz(now),
	}
	got := accountFromRow(row)
	if got.OrgID != "org1" || got.PlanCode != PlanStudio || got.IncludedCredits != 500 {
		t.Errorf("accountFromRow basic fields mismatch: %+v", got)
	}
	if got.IncludedCreditsUsed != 100 || got.TopupCredits != 200 {
		t.Errorf("accountFromRow credit fields mismatch: %+v", got)
	}
	if got.MaxFlavor != FlavorPlus || got.PerRunMaxCredits != 12 {
		t.Errorf("accountFromRow flavor fields mismatch: %+v", got)
	}
	if got.PendingPlanCode != PlanGrowth {
		t.Errorf("accountFromRow pending plan = %q, want %q", got.PendingPlanCode, PlanGrowth)
	}
	if got.CreatedAt.IsZero() {
		t.Error("accountFromRow CreatedAt should be non-zero")
	}
}

func TestTopupSettingsFromRow(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	row := sqlc.BillingTopupSetting{
		OrgID:                 "org1",
		AutoTopupEnabled:      true,
		TriggerThreshold:      10,
		TargetBalance:         100,
		MonthlyMaxUnits:       5,
		MonthlyUnitsUsed:      2,
		MonthlyMaxCents:       10000,
		MonthlySpendCentsUsed: 4400,
		MonthlyAnchorMonth:    "2026-01",
		CreatedAt:             makeTimestamptz(now),
		UpdatedAt:             makeTimestamptz(now),
	}
	got := topupSettingsFromRow(row)
	if got.OrgID != "org1" || !got.AutoTopupEnabled {
		t.Errorf("topupSettingsFromRow basic fields: %+v", got)
	}
	if got.TriggerThreshold != 10 || got.TargetBalance != 100 {
		t.Errorf("topupSettingsFromRow threshold/target: %+v", got)
	}
	if got.MonthlyMaxUnits != 5 || got.MonthlyUnitsUsed != 2 {
		t.Errorf("topupSettingsFromRow monthly units: %+v", got)
	}
	if got.MonthlyMaxCents != 10000 || got.MonthlySpendCentsUsed != 4400 {
		t.Errorf("topupSettingsFromRow monthly cents: %+v", got)
	}
	if got.MonthlyAnchorMonth != "2026-01" {
		t.Errorf("topupSettingsFromRow anchor month = %q", got.MonthlyAnchorMonth)
	}
}

func TestReservationFromRow(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	row := sqlc.BillingCreditReservation{
		RunID:               "run1",
		OrgID:               "org1",
		ReservedCredits:     8,
		FromIncludedCredits: 4,
		FromTopupCredits:    4,
		CapturedCredits:     6,
		ReleasedCredits:     2,
		Status:              ReservationReleased,
		CreatedAt:           makeTimestamptz(now),
		UpdatedAt:           makeTimestamptz(now),
	}
	got := reservationFromRow(row)
	if got.RunID != "run1" || got.OrgID != "org1" {
		t.Errorf("reservationFromRow IDs: %+v", got)
	}
	if got.ReservedCredits != 8 || got.FromIncludedCredits != 4 || got.FromTopupCredits != 4 {
		t.Errorf("reservationFromRow credit splits: %+v", got)
	}
	if got.CapturedCredits != 6 || got.ReleasedCredits != 2 {
		t.Errorf("reservationFromRow captured/released: %+v", got)
	}
	if got.Status != ReservationReleased {
		t.Errorf("reservationFromRow status = %q, want %q", got.Status, ReservationReleased)
	}
}

func TestRunMeterFromRow(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	row := sqlc.BillingRunMeter{
		RunID:            "run1",
		OrgID:            "org1",
		Flavor:           FlavorPlus,
		Multiplier:       2,
		SandboxVcpu:      4,
		SandboxMemoryGib: 8,
		SandboxDiskGib:   10,
		StartedAt:        makeTimestamptz(now),
		EndedAt:          makeTimestamptz(now.Add(30 * time.Minute)),
		BillableMinutes:  30,
		CapturedCredits:  4,
		TerminalState:    "success",
		CreatedAt:        makeTimestamptz(now),
		UpdatedAt:        makeTimestamptz(now),
	}
	got := runMeterFromRow(row)
	if got.RunID != "run1" || got.OrgID != "org1" || got.Flavor != FlavorPlus {
		t.Errorf("runMeterFromRow basic: %+v", got)
	}
	if got.Multiplier != 2 || got.SandboxVCPU != 4 || got.SandboxMemoryGiB != 8 || got.SandboxDiskGiB != 10 {
		t.Errorf("runMeterFromRow hardware: %+v", got)
	}
	if got.BillableMinutes != 30 || got.CapturedCredits != 4 {
		t.Errorf("runMeterFromRow billing: %+v", got)
	}
	if got.TerminalState != "success" {
		t.Errorf("runMeterFromRow terminal state = %q", got.TerminalState)
	}
}

func TestRepoSettingFromRow(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	// Known flavor — ParseFlavor succeeds and normalizes
	row := sqlc.RepoBillingSetting{
		OrgID:       "org1",
		GithubOwner: "owner",
		GithubRepo:  "repo",
		Flavor:      FlavorPlus,
		CreatedAt:   makeTimestamptz(now),
		UpdatedAt:   makeTimestamptz(now),
	}
	got := repoSettingFromRow(row)
	if got.OrgID != "org1" || got.GitHubOwner != "owner" || got.GitHubRepo != "repo" {
		t.Errorf("repoSettingFromRow IDs: %+v", got)
	}
	if got.Flavor != FlavorPlus {
		t.Errorf("repoSettingFromRow flavor = %q, want %q", got.Flavor, FlavorPlus)
	}

	// Legacy flavor code (pro) normalizes to plus via ParseFlavor
	rowLegacy := sqlc.RepoBillingSetting{
		OrgID: "org1", GithubOwner: "owner", GithubRepo: "repo",
		Flavor: FlavorPro, CreatedAt: makeTimestamptz(now), UpdatedAt: makeTimestamptz(now),
	}
	gotLegacy := repoSettingFromRow(rowLegacy)
	if gotLegacy.Flavor != FlavorPlus {
		t.Errorf("repoSettingFromRow legacy pro flavor = %q, want %q", gotLegacy.Flavor, FlavorPlus)
	}

	// Unknown flavor falls back to original string
	rowUnknown := sqlc.RepoBillingSetting{
		OrgID: "org1", GithubOwner: "owner", GithubRepo: "repo",
		Flavor: "unknown-flavor", CreatedAt: makeTimestamptz(now), UpdatedAt: makeTimestamptz(now),
	}
	gotUnknown := repoSettingFromRow(rowUnknown)
	if gotUnknown.Flavor != "unknown-flavor" {
		t.Errorf("repoSettingFromRow unknown flavor = %q, want original string", gotUnknown.Flavor)
	}
}

// TestStoreRepoFlavorDisabled verifies that RepoFlavor returns standard with no error when disabled.
func TestStoreRepoFlavorDisabled(t *testing.T) {
	s := NewStore(nil)
	f, err := s.RepoFlavor(context.Background(), "org1", "owner", "repo")
	if err != nil {
		t.Fatalf("RepoFlavor disabled: unexpected error %v", err)
	}
	if f.Code != FlavorStandard {
		t.Errorf("RepoFlavor disabled = %q, want %q", f.Code, FlavorStandard)
	}
}

// TestFlavorAllowedUnknownFlavor verifies FlavorAllowed returns false for unknown flavor codes.
func TestFlavorAllowedUnknownFlavor(t *testing.T) {
	if FlavorAllowed("completely-unknown", FlavorPlus) {
		t.Error("FlavorAllowed with unknown code should return false")
	}
}
