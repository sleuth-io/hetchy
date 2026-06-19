package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

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
