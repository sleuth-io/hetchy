package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// --- Store.GetAccount ---

func TestStoreGetAccountReturnsAccountOnSuccess(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.PlanCode = PlanStudio
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.GetAccount(context.Background(), "org1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.OrgID != "org1" || got.PlanCode != PlanStudio {
		t.Fatalf("GetAccount = %+v, want org1 studio", got)
	}
}

func TestStoreGetAccountPropagatesError(t *testing.T) {
	wantErr := errors.New("db error")
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccount"] = billingErrRow{err: wantErr}

	store := newBillingStoreForFake(fake)
	_, err := store.GetAccount(context.Background(), "org1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetAccount error = %v, want %v", err, wantErr)
	}
}

// --- Store.FindAccountByStripeCustomer ---

func TestStoreFindAccountByStripeCustomerReturnsAccount(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccountByStripeCustomer"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.StripeCustomerID = "cus_abc"
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.FindAccountByStripeCustomer(context.Background(), "cus_abc")
	if err != nil {
		t.Fatalf("FindAccountByStripeCustomer: %v", err)
	}
	if got.OrgID != "org1" || got.StripeCustomerID != "cus_abc" {
		t.Fatalf("FindAccountByStripeCustomer = %+v, want org1 with stripe cus_abc", got)
	}
	call := fake.onlyQueryRowCall(t, "GetBillingAccountByStripeCustomer")
	assertArg(t, call.args, 0, "cus_abc")
}

func TestStoreFindAccountByStripeCustomerPropagatesError(t *testing.T) {
	wantErr := errors.New("no such customer")
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccountByStripeCustomer"] = billingErrRow{err: wantErr}

	store := newBillingStoreForFake(fake)
	_, err := store.FindAccountByStripeCustomer(context.Background(), "cus_missing")
	if !errors.Is(err, wantErr) {
		t.Fatalf("FindAccountByStripeCustomer error = %v, want %v", err, wantErr)
	}
}

// --- Store.SetBillingExempt ---

func TestStoreSetBillingExemptUpdatesExemptStatus(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["SetBillingExempt"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.BillingExempt = true
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.SetBillingExempt(context.Background(), "org1", true)
	if err != nil {
		t.Fatalf("SetBillingExempt: %v", err)
	}
	if !got.BillingExempt || got.OrgID != "org1" {
		t.Fatalf("SetBillingExempt = %+v, want exempt org1", got)
	}
	call := fake.onlyQueryRowCall(t, "SetBillingExempt")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, true)
}

func TestStoreSetBillingExemptPropagatesError(t *testing.T) {
	wantErr := errors.New("update failed")
	fake := newBillingFakeDB()
	fake.queryRow["SetBillingExempt"] = billingErrRow{err: wantErr}

	store := newBillingStoreForFake(fake)
	_, err := store.SetBillingExempt(context.Background(), "org1", false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("SetBillingExempt error = %v, want %v", err, wantErr)
	}
}

// --- Store.ListAccountOrgIDs ---

func TestStoreListAccountOrgIDsReturnsOrgList(t *testing.T) {
	fake := newBillingFakeDB()
	fake.query["ListBillingAccountOrgIDs"] = &billingRows{rows: []billingScanRow{
		{values: []any{"org1"}},
		{values: []any{"org2"}},
	}}

	store := newBillingStoreForFake(fake)
	got, err := store.ListAccountOrgIDs(context.Background())
	if err != nil {
		t.Fatalf("ListAccountOrgIDs: %v", err)
	}
	if len(got) != 2 || got[0] != "org1" || got[1] != "org2" {
		t.Fatalf("ListAccountOrgIDs = %v, want [org1 org2]", got)
	}
}

// --- Store.ListBillingExemptOrgIDs ---

func TestStoreListBillingExemptOrgIDsReturnsExemptList(t *testing.T) {
	fake := newBillingFakeDB()
	fake.query["ListBillingExemptOrgIDs"] = &billingRows{rows: []billingScanRow{
		{values: []any{"org_vip"}},
	}}

	store := newBillingStoreForFake(fake)
	got, err := store.ListBillingExemptOrgIDs(context.Background())
	if err != nil {
		t.Fatalf("ListBillingExemptOrgIDs: %v", err)
	}
	if len(got) != 1 || got[0] != "org_vip" {
		t.Fatalf("ListBillingExemptOrgIDs = %v, want [org_vip]", got)
	}
}

// --- Store.SetStripeCustomer ---

func TestStoreSetStripeCustomerEnsuresAccountAndSetsCustomer(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["UpdateBillingStripeCustomer"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.StripeCustomerID = "cus_new"
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.SetStripeCustomer(context.Background(), "org1", "cus_new")
	if err != nil {
		t.Fatalf("SetStripeCustomer: %v", err)
	}
	if got.StripeCustomerID != "cus_new" || got.OrgID != "org1" {
		t.Fatalf("SetStripeCustomer = %+v, want org1 with cus_new", got)
	}
	if fake.queryRowCallCount("EnsureBillingAccount") != 1 {
		t.Fatal("SetStripeCustomer should ensure the account exists first")
	}
	call := fake.onlyQueryRowCall(t, "UpdateBillingStripeCustomer")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, "cus_new")
}

func TestStoreSetStripeCustomerPropagatesEnsureAccountError(t *testing.T) {
	wantErr := errors.New("ensure failed")
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingErrRow{err: wantErr}

	store := newBillingStoreForFake(fake)
	_, err := store.SetStripeCustomer(context.Background(), "org1", "cus_new")
	if !errors.Is(err, wantErr) {
		t.Fatalf("SetStripeCustomer error = %v, want %v", err, wantErr)
	}
	if fake.queryRowCallCount("UpdateBillingStripeCustomer") != 0 {
		t.Fatal("stripe customer must not be set if account ensure fails")
	}
}

// --- Store.SetPendingPlanChange ---

func TestStoreSetPendingPlanChangeSetsValues(t *testing.T) {
	effectiveAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	fake := newBillingFakeDB()
	fake.queryRow["SetBillingPendingPlanChange"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.PendingPlanCode = PlanBusiness
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.SetPendingPlanChange(context.Background(), "org1", PlanBusiness, effectiveAt)
	if err != nil {
		t.Fatalf("SetPendingPlanChange: %v", err)
	}
	if got.PendingPlanCode != PlanBusiness || got.OrgID != "org1" {
		t.Fatalf("SetPendingPlanChange = %+v, want org1 with pending business plan", got)
	}
	call := fake.onlyQueryRowCall(t, "SetBillingPendingPlanChange")
	assertArg(t, call.args, 0, "org1")
	assertArg(t, call.args, 1, PlanBusiness)
}

func TestStoreSetPendingPlanChangePropagatesError(t *testing.T) {
	wantErr := errors.New("set pending plan failed")
	fake := newBillingFakeDB()
	fake.queryRow["SetBillingPendingPlanChange"] = billingErrRow{err: wantErr}

	store := newBillingStoreForFake(fake)
	_, err := store.SetPendingPlanChange(context.Background(), "org1", PlanGrowth, time.Now())
	if !errors.Is(err, wantErr) {
		t.Fatalf("SetPendingPlanChange error = %v, want %v", err, wantErr)
	}
}

// --- Store.ClearPendingPlanChange ---

func TestStoreClearPendingPlanChangeRemovesPendingValues(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["ClearBillingPendingPlanChange"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.PendingPlanCode = ""
	}))

	store := newBillingStoreForFake(fake)
	got, err := store.ClearPendingPlanChange(context.Background(), "org1")
	if err != nil {
		t.Fatalf("ClearPendingPlanChange: %v", err)
	}
	if got.PendingPlanCode != "" || got.OrgID != "org1" {
		t.Fatalf("ClearPendingPlanChange = %+v, want org1 with no pending plan", got)
	}
	call := fake.onlyQueryRowCall(t, "ClearBillingPendingPlanChange")
	assertArg(t, call.args, 0, "org1")
}

func TestStoreClearPendingPlanChangePropagatesError(t *testing.T) {
	wantErr := errors.New("clear failed")
	fake := newBillingFakeDB()
	fake.queryRow["ClearBillingPendingPlanChange"] = billingErrRow{err: wantErr}

	store := newBillingStoreForFake(fake)
	_, err := store.ClearPendingPlanChange(context.Background(), "org1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ClearPendingPlanChange error = %v, want %v", err, wantErr)
	}
}

// --- Store.SetLastPaymentError ---

func TestStoreSetLastPaymentErrorWritesError(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.exec["SetBillingLastPaymentError"] = billingExecResult{rows: 1}

	store := newBillingStoreForFake(fake)
	if err := store.SetLastPaymentError(context.Background(), "org1", "card declined"); err != nil {
		t.Fatalf("SetLastPaymentError: %v", err)
	}
	if fake.queryRowCallCount("EnsureBillingAccount") != 1 {
		t.Fatal("SetLastPaymentError should ensure the account exists first")
	}
	if fake.execCallCount("SetBillingLastPaymentError") != 1 {
		t.Fatal("SetLastPaymentError should execute the update query")
	}
}

// --- Store.GrantTopupCreditsOnce ---

func TestStoreGrantTopupCreditsOnceWithEmptyEventIDDelegatesGrant(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.TopupCredits = 200
	}))

	store := newBillingStoreForFake(fake)
	// empty eventID means: grant unconditionally (no dedup needed)
	got, processed, err := store.GrantTopupCreditsOnce(context.Background(), "", "invoice.paid", "org1", 100)
	if err != nil {
		t.Fatalf("GrantTopupCreditsOnce (empty eventID): %v", err)
	}
	if !processed {
		t.Error("empty eventID grant should set processed = true")
	}
	if got.TopupCredits != 200 {
		t.Fatalf("TopupCredits = %d, want 200", got.TopupCredits)
	}
}

func TestStoreGrantTopupCreditsOnceWithNonPositiveCreditsReturnsCurrentAccount(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.TopupCredits = 50
	}))

	store := newBillingStoreForFake(fake)
	// non-positive credits with a non-empty eventID: return current account without granting
	got, processed, err := store.GrantTopupCreditsOnce(context.Background(), "evt_exists", "invoice.paid", "org1", 0)
	if err != nil {
		t.Fatalf("GrantTopupCreditsOnce (credits=0): %v", err)
	}
	if processed {
		t.Error("non-positive credits grant should not set processed = true")
	}
	if got.TopupCredits != 50 {
		t.Fatalf("TopupCredits = %d, want existing balance 50", got.TopupCredits)
	}
	if fake.queryRowCallCount("InsertBillingStripeEvent") != 0 {
		t.Fatal("non-positive credits must not insert a Stripe event")
	}
}

// --- Store.FinalizeRun (enabled store + empty runID is a no-op) ---

func TestStoreFinalizeRunWithEnabledStoreAndEmptyRunIDReturnsNil(t *testing.T) {
	store := newBillingStoreForFake(newBillingFakeDB())
	if err := store.FinalizeRun(context.Background(), "", "success", time.Now()); err != nil {
		t.Errorf("FinalizeRun with empty runID on enabled store: %v", err)
	}
}
