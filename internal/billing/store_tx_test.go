package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// fakeTxRunner runs the WithTx callback against an in-memory queries stub so the
// transactional Store wrappers can be exercised without a live pgxpool. When err
// is set it short-circuits, standing in for a Begin/Commit failure.
type fakeTxRunner struct {
	queries *sqlc.Queries
	err     error
}

func (r fakeTxRunner) WithTx(_ context.Context, fn func(*sqlc.Queries) error) error {
	if r.err != nil {
		return r.err
	}
	return fn(r.queries)
}

// newBillingStoreWithTx wires a fake-backed store whose WithTx runs callbacks
// against the same fake DB, so transaction wrappers route through the fake.
func newBillingStoreWithTx(fake *billingFakeDB) *Store {
	s := newBillingStoreForFake(fake)
	s.tx = fakeTxRunner{queries: sqlc.New(fake)}
	return s
}

func TestAdmitRunNotEnabledReturnsNoRows(t *testing.T) {
	store := &Store{}
	_, _, err := store.AdmitRun(context.Background(), "org1", "run1", 5, MustFlavor(FlavorStandard), time.Now())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("AdmitRun on disabled store err = %v, want pgx.ErrNoRows", err)
	}
}

func TestAdmitRunNormalizesInputsAndCommits(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingCreditReservationForUpdate"] = billingErrRow{err: pgx.ErrNoRows}
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 10
		a.IncludedCreditsUsed = 8
		a.TopupCredits = 5
	}))
	fake.queryRow["UpdateBillingReservedBalances"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["InsertBillingCreditReservation"] = billingReservationRow(testReservation("run1", "org1", func(r *sqlc.BillingCreditReservation) {
		r.Status = ReservationReserved
	}))
	fake.queryRow["UpsertBillingRunMeterStart"] = billingRunMeterRow(testRunMeter("run1", "org1", FlavorStandard))
	store := newBillingStoreWithTx(fake)

	// Negative credits normalize to 0 and a zero startedAt falls back to now.
	res, acct, err := store.AdmitRun(context.Background(), "org1", "run1", -5, MustFlavor(FlavorStandard), time.Time{})
	if err != nil {
		t.Fatalf("AdmitRun returned error: %v", err)
	}
	if res.Status != ReservationReserved {
		t.Fatalf("reservation status = %q, want %q", res.Status, ReservationReserved)
	}
	if acct.OrgID != "org1" {
		t.Fatalf("account org = %q, want org1", acct.OrgID)
	}
	fake.onlyQueryRowCall(t, "UpsertBillingRunMeterStart")
	reserveCall := fake.onlyQueryRowCall(t, "InsertBillingCreditReservation")
	assertArg(t, reserveCall.args, 2, int32(0))
}

func TestAdmitRunPropagatesTxError(t *testing.T) {
	fake := newBillingFakeDB()
	store := newBillingStoreForFake(fake)
	store.tx = fakeTxRunner{err: errors.New("begin boom")}
	_, _, err := store.AdmitRun(context.Background(), "org1", "run1", 5, MustFlavor(FlavorStandard), time.Now())
	if err == nil || err.Error() != "begin boom" {
		t.Fatalf("AdmitRun err = %v, want begin boom", err)
	}
}

func TestFinalizeRunNoopPaths(t *testing.T) {
	if err := (&Store{}).FinalizeRun(context.Background(), "run1", "success", time.Now()); err != nil {
		t.Fatalf("FinalizeRun on disabled store err = %v, want nil", err)
	}
	fake := newBillingFakeDB()
	store := newBillingStoreWithTx(fake)
	if err := store.FinalizeRun(context.Background(), "", "success", time.Now()); err != nil {
		t.Fatalf("FinalizeRun with empty runID err = %v, want nil", err)
	}
	if len(fake.queryRowCalls) != 0 {
		t.Fatalf("empty runID issued %d queries, want 0", len(fake.queryRowCalls))
	}
}

func TestFinalizeRunCommitsWhenMeterMissing(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingRunMeterForUpdate"] = billingErrRow{err: pgx.ErrNoRows}
	store := newBillingStoreWithTx(fake)
	// Zero endedAt falls back to now; a missing meter is a clean no-op inside the tx.
	if err := store.FinalizeRun(context.Background(), "run1", "success", time.Time{}); err != nil {
		t.Fatalf("FinalizeRun returned error: %v", err)
	}
	fake.onlyQueryRowCall(t, "GetBillingRunMeterForUpdate")
}

func TestFinalizeRunPropagatesTxError(t *testing.T) {
	fake := newBillingFakeDB()
	store := newBillingStoreForFake(fake)
	store.tx = fakeTxRunner{err: errors.New("commit boom")}
	err := store.FinalizeRun(context.Background(), "run1", "success", time.Now())
	if err == nil || err.Error() != "commit boom" {
		t.Fatalf("FinalizeRun err = %v, want commit boom", err)
	}
}

func TestGrantTopupCreditsOnceNotEnabled(t *testing.T) {
	_, _, err := (&Store{}).GrantTopupCreditsOnce(context.Background(), "evt", "invoice.paid", "org1", 100)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GrantTopupCreditsOnce on disabled store err = %v, want pgx.ErrNoRows", err)
	}
}

func TestGrantTopupCreditsOnceEmptyEventGrantsDirectly(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.TopupCredits = 110
	}))
	store := newBillingStoreForFake(fake)
	acct, processed, err := store.GrantTopupCreditsOnce(context.Background(), "", "invoice.paid", "org1", 100)
	if err != nil {
		t.Fatalf("GrantTopupCreditsOnce returned error: %v", err)
	}
	if !processed {
		t.Fatalf("processed = false, want true for empty event id")
	}
	if acct.TopupCredits != 110 {
		t.Fatalf("topup credits = %d, want 110", acct.TopupCredits)
	}
}

func TestGrantTopupCreditsOnceNonPositiveReadsAccount(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	store := newBillingStoreForFake(fake)
	acct, processed, err := store.GrantTopupCreditsOnce(context.Background(), "evt", "invoice.paid", "org1", 0)
	if err != nil {
		t.Fatalf("GrantTopupCreditsOnce returned error: %v", err)
	}
	if processed {
		t.Fatalf("processed = true, want false for non-positive credits")
	}
	if acct.OrgID != "org1" {
		t.Fatalf("account org = %q, want org1", acct.OrgID)
	}
}

func TestGrantTopupCreditsOnceCommitsNewEvent(t *testing.T) {
	fake := newBillingFakeDB()
	fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{true}}
	fake.queryRow["EnsureBillingAccount"] = billingAccountRow(testBillingAccount("org1", nil))
	fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.TopupCredits = 110
	}))
	store := newBillingStoreWithTx(fake)
	acct, processed, err := store.GrantTopupCreditsOnce(context.Background(), "evt_1", "invoice.paid", "org1", 100)
	if err != nil {
		t.Fatalf("GrantTopupCreditsOnce returned error: %v", err)
	}
	if !processed {
		t.Fatalf("processed = false, want true for a freshly inserted event")
	}
	if acct.TopupCredits != 110 {
		t.Fatalf("topup credits = %d, want 110", acct.TopupCredits)
	}
}

func TestGrantTopupCreditsOnceWrapsTxError(t *testing.T) {
	fake := newBillingFakeDB()
	store := newBillingStoreForFake(fake)
	store.tx = fakeTxRunner{err: errors.New("tx boom")}
	_, _, err := store.GrantTopupCreditsOnce(context.Background(), "evt_1", "invoice.paid", "org1", 100)
	if err == nil || !errors.Is(err, store.tx.(fakeTxRunner).err) {
		t.Fatalf("GrantTopupCreditsOnce err = %v, want wrapped tx boom", err)
	}
}

func TestAutoTopupNotEnabled(t *testing.T) {
	_, err := (&Store{}).AutoTopup(context.Background(), "org1", 5, nil)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("AutoTopup on disabled store err = %v, want pgx.ErrNoRows", err)
	}
}

// autoTopupFake builds a fake whose locked account/settings rows let
// prepareAutoTopupAttempt decide whether a purchase is required.
func autoTopupFake(autoEnabled bool, balanceCredits int) *billingFakeDB {
	fake := newBillingFakeDB()
	account := testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 0
		a.IncludedCreditsUsed = 0
		a.TopupCredits = int32(balanceCredits)
	})
	settings := testTopupSettings("org1", func(s *sqlc.BillingTopupSetting) {
		s.AutoTopupEnabled = autoEnabled
		s.TriggerThreshold = 20
		s.TargetBalance = 100
		s.MonthlyMaxCents = 0 // disable the monthly spend gate for these cases
	})
	fake.queryRow["LockBillingAccountForUpdate"] = billingAccountRow(account)
	fake.queryRow["EnsureBillingTopupSettings"] = billingTopupSettingRow(settings)
	fake.queryRow["LockBillingTopupSettingsForUpdate"] = billingTopupSettingRow(settings)
	return fake
}

func TestAutoTopupNoPurchaseSufficientBalance(t *testing.T) {
	fake := autoTopupFake(false, 100)
	store := newBillingStoreWithTx(fake)
	acct, err := store.AutoTopup(context.Background(), "org1", 5, nil)
	if err != nil {
		t.Fatalf("AutoTopup returned error: %v", err)
	}
	if acct.Balance() != 100 {
		t.Fatalf("balance = %d, want 100", acct.Balance())
	}
}

func TestAutoTopupNoPurchaseInsufficientBalance(t *testing.T) {
	fake := autoTopupFake(false, 3)
	store := newBillingStoreWithTx(fake)
	_, err := store.AutoTopup(context.Background(), "org1", 5, nil)
	var insufficient InsufficientCreditsError
	if !errors.As(err, &insufficient) {
		t.Fatalf("AutoTopup err = %v, want InsufficientCreditsError", err)
	}
	if insufficient.Needed != 5 || insufficient.Available != 3 {
		t.Fatalf("InsufficientCreditsError = %+v, want needed 5 / available 3", insufficient)
	}
}

func TestAutoTopupPurchaseRequiredButNotConfigured(t *testing.T) {
	fake := autoTopupFake(true, 10)
	store := newBillingStoreWithTx(fake)
	_, err := store.AutoTopup(context.Background(), "org1", 5, nil)
	if !errors.Is(err, ErrAutoTopupNotConfigured) {
		t.Fatalf("AutoTopup err = %v, want ErrAutoTopupNotConfigured", err)
	}
}

func TestAutoTopupPurchaseFailureWrapsPaymentError(t *testing.T) {
	fake := autoTopupFake(true, 10)
	store := newBillingStoreWithTx(fake)
	purchaseErr := errors.New("card declined")
	purchase := func(_ context.Context, _ Account, _ string) (string, error) {
		return "", purchaseErr
	}
	_, err := store.AutoTopup(context.Background(), "org1", 5, purchase)
	var payErr autoTopupPaymentError
	if !errors.As(err, &payErr) {
		t.Fatalf("AutoTopup err = %v, want autoTopupPaymentError", err)
	}
	if !errors.Is(err, purchaseErr) {
		t.Fatalf("AutoTopup err does not wrap purchase error: %v", err)
	}
}

func TestAutoTopupPurchaseSucceedsReachesTarget(t *testing.T) {
	fake := autoTopupFake(true, 10)
	// completeAutoTopupAttempt records the Stripe event, grants credits, and bumps usage.
	fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{true}}
	fake.queryRow["GrantBillingTopupCredits"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 0
		a.IncludedCreditsUsed = 0
		a.TopupCredits = 150
	}))
	fake.queryRow["IncrementBillingTopupMonthlyUsage"] = billingTopupSettingRow(testTopupSettings("org1", nil))
	store := newBillingStoreWithTx(fake)

	var gotIdempotencyKey string
	purchase := func(_ context.Context, _ Account, key string) (string, error) {
		gotIdempotencyKey = key
		return "in_test_123", nil
	}
	acct, err := store.AutoTopup(context.Background(), "org1", 5, purchase)
	if err != nil {
		t.Fatalf("AutoTopup returned error: %v", err)
	}
	if acct.Balance() != 150 {
		t.Fatalf("balance = %d, want 150", acct.Balance())
	}
	if gotIdempotencyKey == "" {
		t.Fatalf("purchase was called without an idempotency key")
	}
	eventCall := fake.onlyQueryRowCall(t, "InsertBillingStripeEvent")
	assertArg(t, eventCall.args, 0, "in_test_123")
}

func TestAutoTopupPurchaseAlreadyRecordedSkipsUsage(t *testing.T) {
	fake := autoTopupFake(true, 10)
	// A duplicate invoice: the Stripe event row already exists, so credits are not
	// re-granted and monthly usage is left untouched. The current balance already
	// clears the target, so the loop returns on the first pass.
	fake.queryRow["InsertBillingStripeEvent"] = billingScanRow{values: []any{false}}
	fake.queryRow["GetBillingAccount"] = billingAccountRow(testBillingAccount("org1", func(a *sqlc.BillingAccount) {
		a.IncludedCredits = 0
		a.IncludedCreditsUsed = 0
		a.TopupCredits = 120
	}))
	store := newBillingStoreWithTx(fake)
	purchase := func(_ context.Context, _ Account, _ string) (string, error) {
		return "in_dup_456", nil
	}
	acct, err := store.AutoTopup(context.Background(), "org1", 5, purchase)
	if err != nil {
		t.Fatalf("AutoTopup returned error: %v", err)
	}
	if acct.Balance() != 120 {
		t.Fatalf("balance = %d, want 120", acct.Balance())
	}
	if fake.queryRowCallCount("IncrementBillingTopupMonthlyUsage") != 0 {
		t.Fatalf("monthly usage was incremented for an already-recorded invoice")
	}
}
