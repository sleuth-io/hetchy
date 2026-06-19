package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db"
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

func TestRunMeterHelpersWriteFlavorResourcesAndFinalizeOutcome(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(45 * time.Minute)
	fake := newBillingFakeDB()
	fake.queryRow["UpsertBillingRunMeterStart"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorPlus))
	fake.queryRow["FinalizeBillingRunMeter"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorPlus))
	queries := sqlc.New(fake)

	if err := upsertRunMeterStart(context.Background(), queries, "run1", "org1", MustFlavor(FlavorPlus), startedAt); err != nil {
		t.Fatalf("upsertRunMeterStart returned error: %v", err)
	}
	startCall := fake.onlyQueryRowCall(t, "UpsertBillingRunMeterStart")
	assertArg(t, startCall.args, 0, "run1")
	assertArg(t, startCall.args, 1, "org1")
	assertArg(t, startCall.args, 2, FlavorPlus)
	assertArg(t, startCall.args, 3, int32(2))
	assertArg(t, startCall.args, 4, int32(4))
	assertArg(t, startCall.args, 5, int32(8))
	assertArg(t, startCall.args, 6, int32(10))
	if got, ok := startCall.args[7].(pgtype.Timestamptz); !ok || !got.Valid || !got.Time.Equal(startedAt) {
		t.Fatalf("started_at arg = %#v, want %v", startCall.args[7], startedAt)
	}

	if err := finalizeRunMeter(context.Background(), queries, "run1", endedAt, 45, 6, "success"); err != nil {
		t.Fatalf("finalizeRunMeter returned error: %v", err)
	}
	finalCall := fake.onlyQueryRowCall(t, "FinalizeBillingRunMeter")
	assertArg(t, finalCall.args, 0, "run1")
	if got, ok := finalCall.args[1].(pgtype.Timestamptz); !ok || !got.Valid || !got.Time.Equal(endedAt) {
		t.Fatalf("ended_at arg = %#v, want %v", finalCall.args[1], endedAt)
	}
	assertArg(t, finalCall.args, 2, int32(45))
	assertArg(t, finalCall.args, 3, int32(6))
	assertArg(t, finalCall.args, 4, "success")
}

func TestAdmitRunTxReservesIncludedThenTopupCredits(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingErrRow{err: pgx.ErrNoRows}
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 10
		a.IncludedCreditsUsed = 8
		a.TopupCredits = 5
	}))
	fake.queryRow["UpdateBillingReservedBalances"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 10
		a.IncludedCreditsUsed = 10
		a.TopupCredits = 2
	}))
	fake.queryRow["InsertBillingCreditReservation"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.ReservedCredits = 5
		r.FromIncludedCredits = 2
		r.FromTopupCredits = 3
		r.Status = ReservationReserved
	}))
	fake.queryRow["UpsertBillingRunMeterStart"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard))

	res, acct, err := admitRunTx(context.Background(), sqlc.New(fake), "org1", "run1", 5, MustFlavor(FlavorStandard), startedAt)
	if err != nil {
		t.Fatalf("admitRunTx returned error: %v", err)
	}
	if res.ReservedCredits != 5 || res.FromIncludedCredits != 2 || res.FromTopupCredits != 3 || res.Status != ReservationReserved {
		t.Fatalf("reservation = %+v, want 5 credits split 2 included / 3 top-up", res)
	}
	if acct.IncludedCreditsUsed != 10 || acct.TopupCredits != 2 {
		t.Fatalf("account after reservation = %+v, want included used 10 and top-up 2", acct)
	}
	reserveCall := fake.onlyQueryRowCall(t, "UpdateBillingReservedBalances")
	assertArg(t, reserveCall.args, 0, "org1")
	assertArg(t, reserveCall.args, 1, int32(2))
	assertArg(t, reserveCall.args, 2, int32(3))
	insertCall := fake.onlyQueryRowCall(t, "InsertBillingCreditReservation")
	assertArg(t, insertCall.args, 2, int32(5))
	assertArg(t, insertCall.args, 3, int32(2))
	assertArg(t, insertCall.args, 4, int32(3))
	assertArg(t, insertCall.args, 5, ReservationReserved)
}

func TestAdmitRunTxExistingReservationOnlyRefreshesMeter(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.ReservedCredits = 4
		r.FromIncludedCredits = 4
		r.Status = ReservationReserved
	}))
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["UpsertBillingRunMeterStart"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorPlus))

	res, _, err := admitRunTx(context.Background(), sqlc.New(fake), "org1", "run1", 9, MustFlavor(FlavorPlus), startedAt)
	if err != nil {
		t.Fatalf("admitRunTx returned error: %v", err)
	}
	if res.ReservedCredits != 4 || res.FromIncludedCredits != 4 {
		t.Fatalf("existing reservation changed: %+v", res)
	}
	if fake.queryRowCallCount("UpdateBillingReservedBalances") != 0 || fake.queryRowCallCount("InsertBillingCreditReservation") != 0 {
		t.Fatal("existing reservation must not reserve credits or insert a duplicate reservation")
	}
	if fake.queryRowCallCount("UpsertBillingRunMeterStart") != 1 {
		t.Fatal("existing reservation should still refresh the run meter start")
	}
}

func TestAdmitRunTxCompedOrgRecordsReservationWithoutCharge(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingErrRow{err: pgx.ErrNoRows}
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.BillingExempt = true
	}))
	fake.queryRow["InsertBillingCreditReservation"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.Status = ReservationComped
	}))
	fake.queryRow["UpsertBillingRunMeterStart"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorPlus))

	res, _, err := admitRunTx(context.Background(), sqlc.New(fake), "org1", "run1", 10, MustFlavor(FlavorPlus), startedAt)
	if err != nil {
		t.Fatalf("admitRunTx returned error: %v", err)
	}
	if res.Status != ReservationComped || res.ReservedCredits != 0 {
		t.Fatalf("comped reservation = %+v, want zero reserved credits with comped status", res)
	}
	if fake.queryRowCallCount("UpdateBillingReservedBalances") != 0 {
		t.Fatal("comped orgs must not reserve or deduct credits")
	}
	insertCall := fake.onlyQueryRowCall(t, "InsertBillingCreditReservation")
	assertArg(t, insertCall.args, 2, int32(0))
	assertArg(t, insertCall.args, 3, int32(0))
	assertArg(t, insertCall.args, 4, int32(0))
	assertArg(t, insertCall.args, 5, ReservationComped)
}

func TestAdmitRunTxInsufficientCreditsDoesNotWriteReservation(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingErrRow{err: pgx.ErrNoRows}
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 1
		a.IncludedCreditsUsed = 1
		a.TopupCredits = 0
	}))

	_, _, err := admitRunTx(context.Background(), sqlc.New(fake), "org1", "run1", 2, MustFlavor(FlavorStandard), startedAt)
	var insufficient InsufficientCreditsError
	if !errors.As(err, &insufficient) {
		t.Fatalf("admitRunTx error = %v, want InsufficientCreditsError", err)
	}
	if insufficient.Needed != 2 || insufficient.Available != 0 {
		t.Fatalf("InsufficientCreditsError = %+v, want needed 2 available 0", insufficient)
	}
	if fake.queryRowCallCount("UpdateBillingReservedBalances") != 0 ||
		fake.queryRowCallCount("InsertBillingCreditReservation") != 0 ||
		fake.queryRowCallCount("UpsertBillingRunMeterStart") != 0 {
		t.Fatal("insufficient credits must not reserve, insert a reservation, or start the run meter")
	}
}

func TestFinalizeRunTxReservedRunReleasesUnusedCredits(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(45 * time.Minute)
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingRunMeterForUpdate"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard, func(m *sqlc.BillingRunMeter) {
		m.TerminalState = ""
	}))
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.ReservedCredits = 5
		r.FromIncludedCredits = 2
		r.FromTopupCredits = 3
		r.Status = ReservationReserved
	}))
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["FinalizeBillingRunMeter"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard))
	fake.queryRow["UpdateBillingCapturedBalances"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["UpdateBillingCreditReservationCaptured"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.ReservedCredits = 5
		r.FromIncludedCredits = 2
		r.FromTopupCredits = 3
		r.CapturedCredits = 3
		r.ReleasedCredits = 2
		r.Status = ReservationReleased
	}))

	if err := finalizeRunTx(context.Background(), sqlc.New(fake), "run1", "success", endedAt); err != nil {
		t.Fatalf("finalizeRunTx returned error: %v", err)
	}
	finalCall := fake.onlyQueryRowCall(t, "FinalizeBillingRunMeter")
	assertArg(t, finalCall.args, 2, int32(45))
	assertArg(t, finalCall.args, 3, int32(3))
	assertArg(t, finalCall.args, 4, "success")
	balanceCall := fake.onlyQueryRowCall(t, "UpdateBillingCapturedBalances")
	assertArg(t, balanceCall.args, 0, "org1")
	assertArg(t, balanceCall.args, 1, int32(0))
	assertArg(t, balanceCall.args, 2, int32(2))
	capturedCall := fake.onlyQueryRowCall(t, "UpdateBillingCreditReservationCaptured")
	assertArg(t, capturedCall.args, 1, int32(3))
	assertArg(t, capturedCall.args, 2, int32(2))
	assertArg(t, capturedCall.args, 3, ReservationReleased)
}

func TestFinalizeRunTxCompedRunRecordsUsageWithoutBalanceUpdate(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(45 * time.Minute)
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingRunMeterForUpdate"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard, func(m *sqlc.BillingRunMeter) {
		m.TerminalState = ""
	}))
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.Status = ReservationComped
	}))
	fake.queryRow["FinalizeBillingRunMeter"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard))
	fake.queryRow["UpdateBillingCreditReservationCaptured"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.CapturedCredits = 3
		r.Status = ReservationComped
	}))

	if err := finalizeRunTx(context.Background(), sqlc.New(fake), "run1", "", endedAt); err != nil {
		t.Fatalf("finalizeRunTx returned error: %v", err)
	}
	finalCall := fake.onlyQueryRowCall(t, "FinalizeBillingRunMeter")
	assertArg(t, finalCall.args, 3, int32(3))
	assertArg(t, finalCall.args, 4, "unknown")
	capturedCall := fake.onlyQueryRowCall(t, "UpdateBillingCreditReservationCaptured")
	assertArg(t, capturedCall.args, 1, int32(3))
	assertArg(t, capturedCall.args, 2, int32(0))
	assertArg(t, capturedCall.args, 3, ReservationComped)
	if fake.queryRowCallCount("UpdateBillingCapturedBalances") != 0 || fake.queryRowCallCount("LockBillingAccountForUpdate") != 0 {
		t.Fatal("comped finalization must not lock or update account balances")
	}
}

func TestFinalizeRunTxAlreadyTerminalDoesNotDoubleCapture(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingRunMeterForUpdate"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard, func(m *sqlc.BillingRunMeter) {
		m.TerminalState = "success"
	}))

	if err := finalizeRunTx(context.Background(), sqlc.New(fake), "run1", "failed", time.Now()); err != nil {
		t.Fatalf("finalizeRunTx returned error: %v", err)
	}
	if fake.queryRowCallCount("FinalizeBillingRunMeter") != 0 ||
		fake.queryRowCallCount("UpdateBillingCreditReservationCaptured") != 0 ||
		fake.queryRowCallCount("UpdateBillingCapturedBalances") != 0 {
		t.Fatal("terminal meters must not be finalized or captured again")
	}
}

func TestGrantTopupCreditsOnceTxPreservesEventIdempotency(t *testing.T) {
	t.Run("duplicate event returns current account", func(t *testing.T) {
		fake := newBillingFakeDB()
		fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{false}}
		fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
			a.TopupCredits = 25
		}))

		got, processed, err := grantTopupCreditsOnceTx(context.Background(), sqlc.New(fake), "evt_1", "invoice.paid", "org1", 100)
		if err != nil {
			t.Fatalf("grantTopupCreditsOnceTx returned error: %v", err)
		}
		if processed {
			t.Fatal("processed = true, want false for duplicate event")
		}
		if got.TopupCredits != 25 {
			t.Fatalf("TopupCredits = %d, want existing balance 25", got.TopupCredits)
		}
		if fake.queryRowCallCount("EnsureBillingAccount") != 0 || fake.queryRowCallCount("GrantBillingTopupCredits") != 0 {
			t.Fatal("duplicate event must not ensure account or grant credits")
		}
	})

	t.Run("new event ensures account then grants credits", func(t *testing.T) {
		fake := newBillingFakeDB()
		fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{true}}
		fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
		fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
			a.TopupCredits = 110
		}))

		got, processed, err := grantTopupCreditsOnceTx(context.Background(), sqlc.New(fake), "evt_2", "invoice.paid", "org1", 100)
		if err != nil {
			t.Fatalf("grantTopupCreditsOnceTx returned error: %v", err)
		}
		if !processed {
			t.Fatal("processed = false, want true for new event")
		}
		if got.TopupCredits != 110 {
			t.Fatalf("TopupCredits = %d, want 110", got.TopupCredits)
		}
		insertCall := fake.onlyQueryRowCall(t, "InsertBillingStripeEvent")
		assertArg(t, insertCall.args, 0, "evt_2")
		assertArg(t, insertCall.args, 1, "invoice.paid")
		assertArg(t, insertCall.args, 2, "org1")
		grantCall := fake.onlyQueryRowCall(t, "GrantBillingTopupCredits")
		assertArg(t, grantCall.args, 0, "org1")
		assertArg(t, grantCall.args, 1, int32(100))
	})
}

func TestGrantTopupCreditsForInvoicePreservesStripeIdempotency(t *testing.T) {
	t.Run("already processed invoice returns current account without granting", func(t *testing.T) {
		fake := newBillingFakeDB()
		fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{false}}
		fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
			a.TopupCredits = 25
		}))

		got, processed, err := grantTopupCreditsForInvoice(context.Background(), sqlc.New(fake), "in_1", "org1", 100)
		if err != nil {
			t.Fatalf("grantTopupCreditsForInvoice returned error: %v", err)
		}
		if processed {
			t.Fatal("processed = true, want false for duplicate invoice")
		}
		if got.TopupCredits != 25 {
			t.Fatalf("TopupCredits = %d, want existing balance 25", got.TopupCredits)
		}
		if fake.queryRowCallCount("GrantBillingTopupCredits") != 0 {
			t.Fatal("duplicate invoice should not grant credits again")
		}
		insertCall := fake.onlyQueryRowCall(t, "InsertBillingStripeEvent")
		assertArg(t, insertCall.args, 0, "in_1")
		assertArg(t, insertCall.args, 1, "invoice.paid")
		assertArg(t, insertCall.args, 2, "org1")
	})

	t.Run("new invoice records event then grants credits", func(t *testing.T) {
		fake := newBillingFakeDB()
		fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{true}}
		fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
			a.TopupCredits = 125
		}))

		got, processed, err := grantTopupCreditsForInvoice(context.Background(), sqlc.New(fake), "in_2", "org1", 100)
		if err != nil {
			t.Fatalf("grantTopupCreditsForInvoice returned error: %v", err)
		}
		if !processed {
			t.Fatal("processed = false, want true for new invoice")
		}
		if got.TopupCredits != 125 {
			t.Fatalf("TopupCredits = %d, want 125", got.TopupCredits)
		}
		grantCall := fake.onlyQueryRowCall(t, "GrantBillingTopupCredits")
		assertArg(t, grantCall.args, 0, "org1")
		assertArg(t, grantCall.args, 1, int32(100))
	})

	t.Run("blank invoice falls back to direct grant", func(t *testing.T) {
		fake := newBillingFakeDB()
		fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
			a.TopupCredits = 125
		}))

		_, processed, err := grantTopupCreditsForInvoice(context.Background(), sqlc.New(fake), "", "org1", 100)
		if err != nil {
			t.Fatalf("grantTopupCreditsForInvoice returned error: %v", err)
		}
		if !processed {
			t.Fatal("processed = false, want true for direct grant")
		}
		if fake.queryRowCallCount("InsertBillingStripeEvent") != 0 {
			t.Fatal("blank invoice id should not insert a Stripe event")
		}
	})
}

func TestIncrementTopupMonthlyUsageClampsNegativeValues(t *testing.T) {
	currentMonth := currentBillingMonth(time.Now())
	fake := newBillingFakeDB()
	fake.queryRow["IncrementBillingTopupMonthlyUsage"] = billingTopupSettingRow(testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.MonthlyUnitsUsed = 0
		s.MonthlySpendCentsUsed = 0
		s.MonthlyAnchorMonth = currentMonth
	}))

	got, err := incrementTopupMonthlyUsage(context.Background(), sqlc.New(fake), "org1", -2, -500)
	if err != nil {
		t.Fatalf("incrementTopupMonthlyUsage returned error: %v", err)
	}
	if got.MonthlyUnitsUsed != 0 || got.MonthlySpendCentsUsed != 0 || got.MonthlyAnchorMonth != currentMonth {
		t.Fatalf("settings after increment = %+v", got)
	}
	call := fake.onlyQueryRowCall(t, "IncrementBillingTopupMonthlyUsage")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, int32(0))
	assertArg(t, call.args, 2, int32(0))
	assertArg(t, call.args, 3, currentMonth)
}

func newBillingFakeDB() *billingFakeDB {
	return &billingFakeDB{
		queryRow: map[string]pgx.Row{},
		query:    map[string]pgx.Rows{},
		exec:     map[string]billingExecResult{},
	}
}

func newBillingStoreForFake(fake *billingFakeDB) *Store {
	return NewStore(&db.Store{Queries: sqlc.New(fake)})
}

type billingFakeDB struct {
	queryRow map[string]pgx.Row
	query    map[string]pgx.Rows
	exec     map[string]billingExecResult

	queryRowCalls []billingDBCall
	queryCalls    []billingDBCall
	execCalls     []billingDBCall
}

type billingDBCall struct {
	sql  string
	args []any
}

type billingExecResult struct {
	rows int64
	err  error
}

func (f *billingFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, billingDBCall{sql: sql, args: args})
	for frag, res := range f.exec {
		if strings.Contains(sql, frag) {
			if res.err != nil {
				return pgconn.CommandTag{}, res.err
			}
			return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", res.rows)), nil
		}
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected exec: %s", sql)
}

func (f *billingFakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalls = append(f.queryCalls, billingDBCall{sql: sql, args: args})
	for frag, rows := range f.query {
		if strings.Contains(sql, frag) {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("unexpected query: %s", sql)
}

func (f *billingFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queryRowCalls = append(f.queryRowCalls, billingDBCall{sql: sql, args: args})
	for frag, row := range f.queryRow {
		if strings.Contains(sql, frag) {
			return row
		}
	}
	return billingErrRow{err: fmt.Errorf("unexpected query row: %s", sql)}
}

func (f *billingFakeDB) queryRowCallCount(fragment string) int {
	count := 0
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *billingFakeDB) execCallCount(fragment string) int {
	count := 0
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *billingFakeDB) onlyQueryRowCall(t *testing.T, fragment string) billingDBCall {
	t.Helper()
	var matches []billingDBCall
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query row calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *billingFakeDB) onlyQueryCall(t *testing.T, fragment string) billingDBCall {
	t.Helper()
	var matches []billingDBCall
	for _, call := range f.queryCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func assertArg(t *testing.T, args []any, idx int, want any) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	if got := args[idx]; got != want {
		t.Fatalf("arg[%d] = %#v (%T), want %#v (%T)", idx, got, got, want, want)
	}
}

type billingErrRow struct {
	err error
}

func (r billingErrRow) Scan(...any) error {
	return r.err
}

type billingScanRow struct {
	values []any
}

func (r billingScanRow) Scan(dest ...any) error {
	return billingAssignScan(dest, r.values)
}

type billingRows struct {
	rows   []billingScanRow
	idx    int
	closed bool
}

func (r *billingRows) Close()                                       { r.closed = true }
func (r *billingRows) Err() error                                   { return nil }
func (r *billingRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *billingRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *billingRows) Values() ([]any, error)                       { return nil, nil }
func (r *billingRows) RawValues() [][]byte                          { return nil }
func (r *billingRows) Conn() *pgx.Conn                              { return nil }

func (r *billingRows) Next() bool {
	if r.idx >= len(r.rows) {
		r.Close()
		return false
	}
	r.idx++
	return true
}

func (r *billingRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("scan called before Next or after EOF")
	}
	return r.rows[r.idx-1].Scan(dest...)
}

func billingAssignScan(dest, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		if err := billingAssignScanValue(dest[i], values[i]); err != nil {
			return fmt.Errorf("scan dest %d: %w", i, err)
		}
	}
	return nil
}

func billingAssignScanValue(dest, value any) error {
	switch d := dest.(type) {
	case *string:
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("got %T, want string", value)
		}
		*d = v
	case *bool:
		v, ok := value.(bool)
		if !ok {
			return fmt.Errorf("got %T, want bool", value)
		}
		*d = v
	case *int32:
		v, ok := value.(int32)
		if !ok {
			return fmt.Errorf("got %T, want int32", value)
		}
		*d = v
	case *pgtype.Timestamptz:
		v, ok := value.(pgtype.Timestamptz)
		if !ok {
			return fmt.Errorf("got %T, want pgtype.Timestamptz", value)
		}
		*d = v
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}

func testBillingAccount(orgID string, mutate func(*sqlc.BillingAccount)) sqlc.BillingAccount {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	account := sqlc.BillingAccount{
		OrgID:                  orgID,
		PlanCode:               PlanStudio,
		Status:                 "active",
		CurrentPeriodStart:     makeTimestamptz(now.Add(-24 * time.Hour)),
		CurrentPeriodEnd:       makeTimestamptz(now.Add(30 * 24 * time.Hour)),
		IncludedCredits:        500,
		IncludedCreditsUsed:    50,
		TopupCredits:           10,
		MaxFlavor:              FlavorPlus,
		PerRunMaxCredits:       12,
		CreatedAt:              makeTimestamptz(now),
		UpdatedAt:              makeTimestamptz(now),
		PendingPlanEffectiveAt: pgtype.Timestamptz{},
	}
	if mutate != nil {
		mutate(&account)
	}
	return account
}

func billingAccountRow(account sqlc.BillingAccount) billingScanRow {
	return billingScanRow{values: []any{
		account.OrgID,
		account.StripeCustomerID,
		account.StripeSubscriptionID,
		account.PlanCode,
		account.Status,
		account.CurrentPeriodStart,
		account.CurrentPeriodEnd,
		account.IncludedCredits,
		account.IncludedCreditsUsed,
		account.TopupCredits,
		account.MaxFlavor,
		account.PerRunMaxCredits,
		account.BillingExempt,
		account.LastPaymentError,
		account.CreatedAt,
		account.UpdatedAt,
		account.PendingPlanCode,
		account.PendingPlanEffectiveAt,
	}}
}

func testTopupSettings(orgID string, mutate func(*sqlc.BillingTopupSetting)) sqlc.BillingTopupSetting {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	settings := sqlc.BillingTopupSetting{
		OrgID:                 orgID,
		AutoTopupEnabled:      true,
		TriggerThreshold:      20,
		TargetBalance:         100,
		MonthlyMaxUnits:       5,
		MonthlyUnitsUsed:      1,
		MonthlyAnchorMonth:    currentBillingMonth(time.Now()),
		CreatedAt:             makeTimestamptz(now),
		UpdatedAt:             makeTimestamptz(now),
		MonthlyMaxCents:       10000,
		MonthlySpendCentsUsed: 1500,
	}
	if mutate != nil {
		mutate(&settings)
	}
	return settings
}

func billingTopupSettingRow(settings sqlc.BillingTopupSetting) billingScanRow {
	return billingScanRow{values: []any{
		settings.OrgID,
		settings.AutoTopupEnabled,
		settings.TriggerThreshold,
		settings.TargetBalance,
		settings.MonthlyMaxUnits,
		settings.MonthlyUnitsUsed,
		settings.MonthlyAnchorMonth,
		settings.CreatedAt,
		settings.UpdatedAt,
		settings.MonthlyMaxCents,
		settings.MonthlySpendCentsUsed,
	}}
}

func testRepoSetting(orgID, owner, repo, flavor string) sqlc.RepoBillingSetting {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return sqlc.RepoBillingSetting{
		OrgID:       orgID,
		GithubOwner: owner,
		GithubRepo:  repo,
		Flavor:      flavor,
		CreatedAt:   makeTimestamptz(now),
		UpdatedAt:   makeTimestamptz(now),
	}
}

func billingRepoSettingRow(setting sqlc.RepoBillingSetting) billingScanRow {
	return billingScanRow{values: []any{
		setting.OrgID,
		setting.GithubOwner,
		setting.GithubRepo,
		setting.Flavor,
		setting.CreatedAt,
		setting.UpdatedAt,
	}}
}

func testReservation(runID, orgID string, mutate func(*sqlc.BillingCreditReservation)) sqlc.BillingCreditReservation {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reservation := sqlc.BillingCreditReservation{
		RunID:     runID,
		OrgID:     orgID,
		Status:    ReservationReserved,
		CreatedAt: makeTimestamptz(now),
		UpdatedAt: makeTimestamptz(now),
	}
	if mutate != nil {
		mutate(&reservation)
	}
	return reservation
}

func billingReservationRow(reservation sqlc.BillingCreditReservation) billingScanRow {
	return billingScanRow{values: []any{
		reservation.RunID,
		reservation.OrgID,
		reservation.ReservedCredits,
		reservation.FromIncludedCredits,
		reservation.FromTopupCredits,
		reservation.CapturedCredits,
		reservation.ReleasedCredits,
		reservation.Status,
		reservation.CreatedAt,
		reservation.UpdatedAt,
	}}
}

func testRunMeter(runID, orgID, flavor string, mutate ...func(*sqlc.BillingRunMeter)) sqlc.BillingRunMeter {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	meter := sqlc.BillingRunMeter{
		RunID:            runID,
		OrgID:            orgID,
		Flavor:           flavor,
		Multiplier:       int32(MustFlavor(flavor).Multiplier),
		SandboxVcpu:      int32(MustFlavor(flavor).VCPU),
		SandboxMemoryGib: int32(MustFlavor(flavor).MemoryGiB),
		SandboxDiskGib:   int32(MustFlavor(flavor).DiskGiB),
		StartedAt:        makeTimestamptz(start),
		EndedAt:          makeTimestamptz(start.Add(30 * time.Minute)),
		BillableMinutes:  30,
		CapturedCredits:  2,
		TerminalState:    "success",
		CreatedAt:        makeTimestamptz(start),
		UpdatedAt:        makeTimestamptz(start),
	}
	for _, fn := range mutate {
		if fn != nil {
			fn(&meter)
		}
	}
	return meter
}

func billingRunMeterRow(meter sqlc.BillingRunMeter) billingScanRow {
	return billingScanRow{values: []any{
		meter.RunID,
		meter.OrgID,
		meter.Flavor,
		meter.Multiplier,
		meter.SandboxVcpu,
		meter.SandboxMemoryGib,
		meter.SandboxDiskGib,
		meter.StartedAt,
		meter.EndedAt,
		meter.BillableMinutes,
		meter.CapturedCredits,
		meter.TerminalState,
		meter.CreatedAt,
		meter.UpdatedAt,
	}}
}
