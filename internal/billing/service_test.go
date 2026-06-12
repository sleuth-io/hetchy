package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Tests for InsufficientCreditsError and FlavorNotAllowedError

func TestInsufficientCreditsError(t *testing.T) {
	err := InsufficientCreditsError{Needed: 10, Available: 3}
	got := err.Error()
	want := "billing: insufficient credits: need 10, have 3"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestFlavorNotAllowedError(t *testing.T) {
	err := FlavorNotAllowedError{Flavor: "plus", MaxFlavor: "standard"}
	got := err.Error()
	want := "billing: flavor \"plus\" exceeds plan cap \"standard\""
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// Tests for Service.Enabled()

func TestServiceEnabledNilService(t *testing.T) {
	var s *Service
	if s.Enabled() {
		t.Error("nil Service should not be enabled")
	}
}

func TestServiceEnabledNilStore(t *testing.T) {
	s := &Service{store: nil}
	if s.Enabled() {
		t.Error("Service with nil store should not be enabled")
	}
}

func TestServiceEnabledNilDB(t *testing.T) {
	store := &Store{db: nil}
	s := &Service{store: store}
	if s.Enabled() {
		t.Error("Service with store with nil db should not be enabled")
	}
}

func TestNewService(t *testing.T) {
	store := &Store{}
	s := NewService(store, nil)
	if s == nil {
		t.Fatal("NewService returned nil")
	}
	if s.store != store {
		t.Error("NewService did not set store")
	}
	if s.topupper != nil {
		t.Error("NewService should have nil topupper when nil passed")
	}
}

// Tests for disabled (nil) Service paths — all return zero values without error

func TestAdmitRunDisabled(t *testing.T) {
	var s *Service
	adm, err := s.AdmitRun(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("AdmitRun on disabled service: unexpected error: %v", err)
	}
	if adm.Flavor.Code != MustFlavor(FlavorStandard).Code {
		t.Errorf("AdmitRun disabled: flavor = %q, want %q", adm.Flavor.Code, FlavorStandard)
	}
}

func TestFinalizeRunDisabled(t *testing.T) {
	var s *Service
	if err := s.FinalizeRun(context.Background(), "run-1", "success", time.Time{}); err != nil {
		t.Fatalf("FinalizeRun on disabled service: unexpected error: %v", err)
	}
}

func TestOverviewDisabled(t *testing.T) {
	var s *Service
	ov, err := s.Overview(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Overview on disabled service: unexpected error: %v", err)
	}
	if ov.Account != (Account{}) || ov.TopupSettings != (TopupSettings{}) || len(ov.RecentMeters) != 0 {
		t.Errorf("Overview disabled: got non-zero value %+v", ov)
	}
}

func TestUpdateTopupSettingsDisabled(t *testing.T) {
	var s *Service
	ts, err := s.UpdateTopupSettings(context.Background(), "org-1", TopupSettings{})
	if err != nil {
		t.Fatalf("UpdateTopupSettings on disabled service: unexpected error: %v", err)
	}
	if ts != (TopupSettings{}) {
		t.Errorf("UpdateTopupSettings disabled: got non-zero value %+v", ts)
	}
}

func TestListRepoSettingsDisabled(t *testing.T) {
	var s *Service
	m, err := s.ListRepoSettings(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ListRepoSettings on disabled service: unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("ListRepoSettings disabled: expected empty map, got %v", m)
	}
}

func TestSetRepoFlavorDisabled(t *testing.T) {
	var s *Service
	rs, err := s.SetRepoFlavor(context.Background(), "org-1", "owner", "repo", FlavorStandard)
	if err != nil {
		t.Fatalf("SetRepoFlavor on disabled service: unexpected error: %v", err)
	}
	if rs.OrgID != "org-1" || rs.GitHubOwner != "owner" || rs.GitHubRepo != "repo" || rs.Flavor != FlavorStandard {
		t.Errorf("SetRepoFlavor disabled: unexpected value %+v", rs)
	}
}

func TestSetStripeCustomerDisabled(t *testing.T) {
	var s *Service
	acc, err := s.SetStripeCustomer(context.Background(), "org-1", "cus_123")
	if err != nil {
		t.Fatalf("SetStripeCustomer on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("SetStripeCustomer disabled: got non-zero value %+v", acc)
	}
}

func TestFindAccountByStripeCustomerDisabled(t *testing.T) {
	var s *Service
	acc, err := s.FindAccountByStripeCustomer(context.Background(), "cus_123")
	if err != nil {
		t.Fatalf("FindAccountByStripeCustomer on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("FindAccountByStripeCustomer disabled: got non-zero value %+v", acc)
	}
}

func TestUpsertAccountMirrorDisabled(t *testing.T) {
	var s *Service
	acc, err := s.UpsertAccountMirror(context.Background(), AccountMirror{})
	if err != nil {
		t.Fatalf("UpsertAccountMirror on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("UpsertAccountMirror disabled: got non-zero value %+v", acc)
	}
}

func TestSetBillingExemptDisabled(t *testing.T) {
	var s *Service
	acc, err := s.SetBillingExempt(context.Background(), "org-1", true)
	if err != nil {
		t.Fatalf("SetBillingExempt on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("SetBillingExempt disabled: got non-zero value %+v", acc)
	}
}

func TestListAccountOrgIDsDisabled(t *testing.T) {
	var s *Service
	ids, err := s.ListAccountOrgIDs(context.Background())
	if err != nil {
		t.Fatalf("ListAccountOrgIDs on disabled service: unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("ListAccountOrgIDs disabled: expected nil, got %v", ids)
	}
}

func TestListBillingExemptOrgIDsDisabled(t *testing.T) {
	var s *Service
	ids, err := s.ListBillingExemptOrgIDs(context.Background())
	if err != nil {
		t.Fatalf("ListBillingExemptOrgIDs on disabled service: unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("ListBillingExemptOrgIDs disabled: expected nil, got %v", ids)
	}
}

func TestSetPendingPlanChangeDisabled(t *testing.T) {
	var s *Service
	acc, err := s.SetPendingPlanChange(context.Background(), "org-1", PlanStudio, time.Time{})
	if err != nil {
		t.Fatalf("SetPendingPlanChange on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("SetPendingPlanChange disabled: got non-zero value %+v", acc)
	}
}

func TestClearPendingPlanChangeDisabled(t *testing.T) {
	var s *Service
	acc, err := s.ClearPendingPlanChange(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ClearPendingPlanChange on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("ClearPendingPlanChange disabled: got non-zero value %+v", acc)
	}
}

func TestGrantTopupCreditsDisabled(t *testing.T) {
	var s *Service
	acc, err := s.GrantTopupCredits(context.Background(), "org-1", 50)
	if err != nil {
		t.Fatalf("GrantTopupCredits on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) {
		t.Errorf("GrantTopupCredits disabled: got non-zero value %+v", acc)
	}
}

func TestGrantTopupCreditsOnceDisabled(t *testing.T) {
	var s *Service
	acc, granted, err := s.GrantTopupCreditsOnce(context.Background(), "evt-1", "stripe.invoice", "org-1", 50)
	if err != nil {
		t.Fatalf("GrantTopupCreditsOnce on disabled service: unexpected error: %v", err)
	}
	if acc != (Account{}) || granted {
		t.Errorf("GrantTopupCreditsOnce disabled: got acc=%+v granted=%v", acc, granted)
	}
}

func TestSetLastPaymentErrorDisabled(t *testing.T) {
	var s *Service
	if err := s.SetLastPaymentError(context.Background(), "org-1", "card declined"); err != nil {
		t.Fatalf("SetLastPaymentError on disabled service: unexpected error: %v", err)
	}
}

// Tests for topupUnitCentsForPlan (private, same package)

func TestTopupUnitCentsForPlan(t *testing.T) {
	starterPlan, _ := PaidPlanByCode(PlanStarter)
	studioPlan, _ := PaidPlanByCode(PlanStudio)
	defaultPlan := DefaultPaidPlan()
	cases := []struct {
		planCode string
		want     int
	}{
		{PlanStarter, starterPlan.TopupUnitUSDCents},
		{PlanStudio, studioPlan.TopupUnitUSDCents},
		{"unknown-plan", defaultPlan.TopupUnitUSDCents},
	}
	for _, tc := range cases {
		t.Run(tc.planCode, func(t *testing.T) {
			got := topupUnitCentsForPlan(tc.planCode)
			if got != tc.want {
				t.Errorf("topupUnitCentsForPlan(%q) = %d, want %d", tc.planCode, got, tc.want)
			}
		})
	}
}

// Tests for autoTopupPaymentError (in topup.go, same package)

func TestAutoTopupPaymentErrorMessage(t *testing.T) {
	inner := errors.New("card declined")
	e := autoTopupPaymentError{err: inner}
	if e.Error() != "card declined" {
		t.Errorf("Error() = %q, want %q", e.Error(), "card declined")
	}
}

func TestAutoTopupPaymentErrorUnwrap(t *testing.T) {
	inner := errors.New("payment failed")
	e := autoTopupPaymentError{err: inner}
	if !errors.Is(e, inner) {
		t.Error("errors.Is should find the wrapped inner error")
	}
}
