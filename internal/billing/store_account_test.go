package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

func TestStoreUpsertAccountMirrorAppliesSafeDefaults(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["UpsertBillingAccountMirror"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.PlanCode = PlanFree
		a.Status = PlanFree
		a.IncludedCredits = 0
		a.MaxFlavor = FlavorStandard
		a.PerRunMaxCredits = 4
		a.BillingExempt = true
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.UpsertAccountMirror(context.Background(), AccountMirror{
		OrgID:            "org1",
		IncludedCredits:  -50,
		PerRunMaxCredits: -1,
		BillingExempt:    true,
	})
	if err != nil {
		t.Fatalf("UpsertAccountMirror returned error: %v", err)
	}
	if got.PlanCode != PlanFree || got.Status != PlanFree {
		t.Fatalf("plan/status = %q/%q, want free/free", got.PlanCode, got.Status)
	}
	if got.IncludedCredits != 0 || got.PerRunMaxCredits != 4 || got.MaxFlavor != FlavorStandard {
		t.Fatalf("normalized account = %+v", got)
	}
	call := fake.onlyQueryRowCall(t, "UpsertBillingAccountMirror")
	assertArg(t, call.args, 3, PlanFree)
	assertArg(t, call.args, 4, PlanFree)
	assertArg(t, call.args, 7, int32(0))
	assertArg(t, call.args, 8, FlavorStandard)
	assertArg(t, call.args, 9, int32(4))
	assertArg(t, call.args, 10, true)
}

func TestStoreGrantTopupCreditsSkipsNonPositiveGrants(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.TopupCredits = 25
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.GrantTopupCredits(context.Background(), "org1", 0)
	if err != nil {
		t.Fatalf("GrantTopupCredits returned error: %v", err)
	}
	if got.TopupCredits != 25 {
		t.Fatalf("TopupCredits = %d, want existing balance 25", got.TopupCredits)
	}
	if fake.queryRowCallCount("GrantBillingTopupCredits") != 0 {
		t.Fatal("non-positive grant should not write top-up credits")
	}
	if fake.queryRowCallCount("EnsureBillingAccount") != 0 {
		t.Fatal("non-positive grant should not create a missing account")
	}
}

func TestStoreGrantTopupCreditsEnsuresAccountAndWritesGrant(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.TopupCredits = 125
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.GrantTopupCredits(context.Background(), "org1", 100)
	if err != nil {
		t.Fatalf("GrantTopupCredits returned error: %v", err)
	}
	if got.TopupCredits != 125 {
		t.Fatalf("TopupCredits = %d, want 125", got.TopupCredits)
	}
	if fake.queryRowCallCount("EnsureBillingAccount") != 1 {
		t.Fatal("positive grants should ensure the account exists before writing")
	}
	call := fake.onlyQueryRowCall(t, "GrantBillingTopupCredits")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, int32(100))
}

func TestStoreEnsureTopupSettingsResetsStaleBillingMonth(t *testing.T) {
	currentMonth := currentBillingMonth(time.Now())
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingTopupSettings"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.MonthlyUnitsUsed = 3
		s.MonthlySpendCentsUsed = 4500
		s.MonthlyAnchorMonth = "2000-01"
	}))
	fake.queryRow["ResetBillingTopupMonthlyUsage"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.MonthlyUnitsUsed = 0
		s.MonthlySpendCentsUsed = 0
		s.MonthlyAnchorMonth = currentMonth
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.EnsureTopupSettings(context.Background(), "org1")
	if err != nil {
		t.Fatalf("EnsureTopupSettings returned error: %v", err)
	}
	if got.MonthlyAnchorMonth != currentMonth || got.MonthlyUnitsUsed != 0 || got.MonthlySpendCentsUsed != 0 {
		t.Fatalf("settings after stale-month reset = %+v", got)
	}
	call := fake.onlyQueryRowCall(t, "ResetBillingTopupMonthlyUsage")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, currentMonth)
}

func TestStoreEnsureTopupSettingsKeepsCurrentBillingMonth(t *testing.T) {
	currentMonth := currentBillingMonth(time.Now())
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingTopupSettings"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.MonthlyUnitsUsed = 2
		s.MonthlySpendCentsUsed = 3000
		s.MonthlyAnchorMonth = currentMonth
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.EnsureTopupSettings(context.Background(), "org1")
	if err != nil {
		t.Fatalf("EnsureTopupSettings returned error: %v", err)
	}
	if got.MonthlyUnitsUsed != 2 || got.MonthlySpendCentsUsed != 3000 {
		t.Fatalf("current-month settings were unexpectedly changed: %+v", got)
	}
	if fake.queryRowCallCount("ResetBillingTopupMonthlyUsage") != 0 {
		t.Fatal("current-month settings should not be reset")
	}
}

func TestStoreUpdateTopupSettingsClampsNegativeLimits(t *testing.T) {
	currentMonth := currentBillingMonth(time.Now())
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["EnsureBillingTopupSettings"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.MonthlyAnchorMonth = currentMonth
	}))
	fake.queryRow["UpdateBillingTopupSettings"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.AutoTopupEnabled = true
		s.TriggerThreshold = 0
		s.TargetBalance = 0
		s.MonthlyMaxUnits = 0
		s.MonthlyMaxCents = 0
		s.MonthlyAnchorMonth = currentMonth
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.UpdateTopupSettings(context.Background(), "org1", TopupSettings{
		AutoTopupEnabled: true,
		TriggerThreshold: -5,
		TargetBalance:    -10,
		MonthlyMaxUnits:  -2,
		MonthlyMaxCents:  -500,
	})
	if err != nil {
		t.Fatalf("UpdateTopupSettings returned error: %v", err)
	}
	if !got.AutoTopupEnabled || got.TriggerThreshold != 0 || got.TargetBalance != 0 || got.MonthlyMaxUnits != 0 || got.MonthlyMaxCents != 0 {
		t.Fatalf("clamped settings = %+v", got)
	}
	call := fake.onlyQueryRowCall(t, "UpdateBillingTopupSettings")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, true)
	assertArg(t, call.args, 2, int32(0))
	assertArg(t, call.args, 3, int32(0))
	assertArg(t, call.args, 4, int32(0))
	assertArg(t, call.args, 5, int32(0))
}

func TestStoreRepoFlavorDefaultsMissingSettingToStandard(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetRepoBillingSetting"] = billingErrRow{err: pgx.ErrNoRows}

	store := newBillingStoreForFake(fake)
	got, err := store.RepoFlavor(context.Background(), "org1", "owner", "repo")
	if err != nil {
		t.Fatalf("RepoFlavor returned error: %v", err)
	}
	if got.Code != FlavorStandard {
		t.Fatalf("RepoFlavor = %q, want standard", got.Code)
	}
}

func TestStoreRepoFlavorParsesStoredFlavor(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetRepoBillingSetting"] = billingRepoSettingRow(testRepoSetting("org1", "owner", "repo", FlavorPlus))

	store := newBillingStoreForFake(fake)
	got, err := store.RepoFlavor(context.Background(), "org1", "owner", "repo")
	if err != nil {
		t.Fatalf("RepoFlavor returned error: %v", err)
	}
	if got.Code != FlavorPlus {
		t.Fatalf("RepoFlavor = %q, want plus", got.Code)
	}
}

func TestStoreListRepoSettingsKeysSettingsByOwnerRepo(t *testing.T) {
	fake := newBillingFakeDB()
	fake.query["ListRepoBillingSettingsByOrg"] = &billingRows{rows: []billingScanRow{
		billingRepoSettingRow(testRepoSetting("org1", "acme", "api", FlavorPlus)),
		billingRepoSettingRow(testRepoSetting("org1", "acme", "web", FlavorStandard)),
	}}

	store := newBillingStoreForFake(fake)
	got, err := store.ListRepoSettings(context.Background(), "org1")
	if err != nil {
		t.Fatalf("ListRepoSettings returned error: %v", err)
	}
	if got["acme/api"].Flavor != FlavorPlus {
		t.Fatalf("acme/api setting = %+v, want plus", got["acme/api"])
	}
	if got["acme/web"].Flavor != FlavorStandard {
		t.Fatalf("acme/web setting = %+v, want standard", got["acme/web"])
	}
}

func TestStoreSetRepoFlavorDeletesExplicitStandardSetting(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.exec["DeleteRepoBillingSetting"] = billingExecResult{rows: 1}

	store := newBillingStoreForFake(fake)
	got, err := store.SetRepoFlavor(context.Background(), "org1", "owner", "repo", FlavorStandard)
	if err != nil {
		t.Fatalf("SetRepoFlavor returned error: %v", err)
	}
	if got.Flavor != FlavorStandard || got.GitHubOwner != "owner" || got.GitHubRepo != "repo" {
		t.Fatalf("standard setting result = %+v", got)
	}
	if fake.execCallCount("DeleteRepoBillingSetting") != 1 {
		t.Fatal("standard repo flavor should delete the override row")
	}
	if fake.queryRowCallCount("UpsertRepoBillingSetting") != 0 {
		t.Fatal("standard repo flavor should not write an override row")
	}
}

func TestStoreSetRepoFlavorRejectsFlavorAboveAccountCap(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.MaxFlavor = FlavorStandard
		a.BillingExempt = false
	}))

	store := newBillingStoreForFake(fake)
	_, err := store.SetRepoFlavor(context.Background(), "org1", "owner", "repo", FlavorPlus)
	var flavorErr FlavorNotAllowedError
	if !errors.As(err, &flavorErr) {
		t.Fatalf("SetRepoFlavor error = %v, want FlavorNotAllowedError", err)
	}
	if flavorErr.Flavor != FlavorPlus || flavorErr.MaxFlavor != FlavorStandard {
		t.Fatalf("FlavorNotAllowedError = %+v", flavorErr)
	}
	if fake.queryRowCallCount("UpsertRepoBillingSetting") != 0 {
		t.Fatal("disallowed repo flavor should not be persisted")
	}
}

func TestStoreSetRepoFlavorAllowsExemptOrgAboveAccountCap(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.MaxFlavor = FlavorStandard
		a.BillingExempt = true
	}))
	fake.queryRow["UpsertRepoBillingSetting"] = billingRepoSettingRow(testRepoSetting("org1", "owner", "repo", FlavorPlus))

	store := newBillingStoreForFake(fake)
	got, err := store.SetRepoFlavor(context.Background(), "org1", "owner", "repo", FlavorPlus)
	if err != nil {
		t.Fatalf("SetRepoFlavor returned error: %v", err)
	}
	if got.Flavor != FlavorPlus {
		t.Fatalf("exempt org repo flavor = %q, want plus", got.Flavor)
	}
	call := fake.onlyQueryRowCall(t, "UpsertRepoBillingSetting")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, "owner")
	assertArg(t, call.args, 2, "repo")
	assertArg(t, call.args, 3, FlavorPlus)
}

func TestStoreOverviewUsesDefaultMeterLimitAndAllowedFlavors(t *testing.T) {
	currentMonth := currentBillingMonth(time.Now())
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.MaxFlavor = FlavorPlus
	}))
	fake.queryRow["EnsureBillingTopupSettings"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.MonthlyAnchorMonth = currentMonth
	}))
	fake.query["ListBillingRunMetersByOrg"] = &billingRows{rows: []billingScanRow{
		billingRunMeterRow(testRunMeter("run1", "org1", FlavorPlus)),
		billingRunMeterRow(testRunMeter("run2", "org1", FlavorStandard)),
	}}

	store := newBillingStoreForFake(fake)
	got, err := store.Overview(context.Background(), "org1", 0)
	if err != nil {
		t.Fatalf("Overview returned error: %v", err)
	}
	if got.Account.OrgID != "org1" || got.TopupSettings.OrgID != "org1" {
		t.Fatalf("Overview org fields = %+v", got)
	}
	if len(got.RecentMeters) != 2 {
		t.Fatalf("RecentMeters length = %d, want 2", len(got.RecentMeters))
	}
	if len(got.AllowedFlavors) != 2 || got.AllowedFlavors[0].Code != FlavorStandard || got.AllowedFlavors[1].Code != FlavorPlus {
		t.Fatalf("AllowedFlavors = %+v", got.AllowedFlavors)
	}
	call := fake.onlyQueryCall(t, "ListBillingRunMetersByOrg")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, int32(10))
}
