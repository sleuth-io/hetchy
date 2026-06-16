package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// fakeStore is a test double for serviceStore. Each method delegates to an
// optional function field so individual tests only need to set the fields they
// exercise; unexpected calls panic to surface accidental invocations early.
type fakeStore struct {
	enabled                     bool
	ensureAccount               func(context.Context, string) (Account, error)
	repoFlavor                  func(context.Context, string, string, string) (Flavor, error)
	admitRun                    func(context.Context, string, string, int, Flavor, time.Time) (Reservation, Account, error)
	autoTopup                   func(context.Context, string, int, func(context.Context, Account, string) (string, error)) (Account, error)
	setLastPaymentError         func(context.Context, string, string) error
	finalizeRun                 func(context.Context, string, string, time.Time) error
	overview                    func(context.Context, string, int32) (Overview, error)
	updateTopupSettings         func(context.Context, string, TopupSettings) (TopupSettings, error)
	listRepoSettings            func(context.Context, string) (map[string]RepoSetting, error)
	setRepoFlavor               func(context.Context, string, string, string, string) (RepoSetting, error)
	setStripeCustomer           func(context.Context, string, string) (Account, error)
	findAccountByStripeCustomer func(context.Context, string) (Account, error)
	upsertAccountMirror         func(context.Context, AccountMirror) (Account, error)
	setBillingExempt            func(context.Context, string, bool) (Account, error)
	listAccountOrgIDs           func(context.Context) ([]string, error)
	listBillingExemptOrgIDs     func(context.Context) ([]string, error)
	setPendingPlanChange        func(context.Context, string, string, time.Time) (Account, error)
	clearPendingPlanChange      func(context.Context, string) (Account, error)
	grantTopupCredits           func(context.Context, string, int) (Account, error)
	grantTopupCreditsOnce       func(context.Context, string, string, string, int) (Account, bool, error)
}

func (f *fakeStore) Enabled() bool { return f.enabled }

func (f *fakeStore) EnsureAccount(ctx context.Context, orgID string) (Account, error) {
	return f.ensureAccount(ctx, orgID)
}
func (f *fakeStore) RepoFlavor(ctx context.Context, orgID, owner, repo string) (Flavor, error) {
	return f.repoFlavor(ctx, orgID, owner, repo)
}
func (f *fakeStore) AdmitRun(ctx context.Context, orgID, runID string, credits int, flavor Flavor, startedAt time.Time) (Reservation, Account, error) {
	return f.admitRun(ctx, orgID, runID, credits, flavor, startedAt)
}
func (f *fakeStore) AutoTopup(ctx context.Context, orgID string, reserveCredits int, purchase func(context.Context, Account, string) (string, error)) (Account, error) {
	return f.autoTopup(ctx, orgID, reserveCredits, purchase)
}
func (f *fakeStore) SetLastPaymentError(ctx context.Context, orgID, msg string) error {
	return f.setLastPaymentError(ctx, orgID, msg)
}
func (f *fakeStore) FinalizeRun(ctx context.Context, runID, terminalState string, endedAt time.Time) error {
	return f.finalizeRun(ctx, runID, terminalState, endedAt)
}
func (f *fakeStore) Overview(ctx context.Context, orgID string, meterLimit int32) (Overview, error) {
	return f.overview(ctx, orgID, meterLimit)
}
func (f *fakeStore) UpdateTopupSettings(ctx context.Context, orgID string, settings TopupSettings) (TopupSettings, error) {
	return f.updateTopupSettings(ctx, orgID, settings)
}
func (f *fakeStore) ListRepoSettings(ctx context.Context, orgID string) (map[string]RepoSetting, error) {
	return f.listRepoSettings(ctx, orgID)
}
func (f *fakeStore) SetRepoFlavor(ctx context.Context, orgID, owner, repo, flavor string) (RepoSetting, error) {
	return f.setRepoFlavor(ctx, orgID, owner, repo, flavor)
}
func (f *fakeStore) SetStripeCustomer(ctx context.Context, orgID, customerID string) (Account, error) {
	return f.setStripeCustomer(ctx, orgID, customerID)
}
func (f *fakeStore) FindAccountByStripeCustomer(ctx context.Context, customerID string) (Account, error) {
	return f.findAccountByStripeCustomer(ctx, customerID)
}
func (f *fakeStore) UpsertAccountMirror(ctx context.Context, mirror AccountMirror) (Account, error) {
	return f.upsertAccountMirror(ctx, mirror)
}
func (f *fakeStore) SetBillingExempt(ctx context.Context, orgID string, exempt bool) (Account, error) {
	return f.setBillingExempt(ctx, orgID, exempt)
}
func (f *fakeStore) ListAccountOrgIDs(ctx context.Context) ([]string, error) {
	return f.listAccountOrgIDs(ctx)
}
func (f *fakeStore) ListBillingExemptOrgIDs(ctx context.Context) ([]string, error) {
	return f.listBillingExemptOrgIDs(ctx)
}
func (f *fakeStore) SetPendingPlanChange(ctx context.Context, orgID, planCode string, effectiveAt time.Time) (Account, error) {
	return f.setPendingPlanChange(ctx, orgID, planCode, effectiveAt)
}
func (f *fakeStore) ClearPendingPlanChange(ctx context.Context, orgID string) (Account, error) {
	return f.clearPendingPlanChange(ctx, orgID)
}
func (f *fakeStore) GrantTopupCredits(ctx context.Context, orgID string, credits int) (Account, error) {
	return f.grantTopupCredits(ctx, orgID, credits)
}
func (f *fakeStore) GrantTopupCreditsOnce(ctx context.Context, eventID, eventType, orgID string, credits int) (Account, bool, error) {
	return f.grantTopupCreditsOnce(ctx, eventID, eventType, orgID, credits)
}

// enabledFakeStore returns a fakeStore with Enabled() == true.
func enabledFakeStore() *fakeStore { return &fakeStore{enabled: true} }

// --- error types ---

func TestInsufficientCreditsError(t *testing.T) {
	e := InsufficientCreditsError{Needed: 10, Available: 3}
	msg := e.Error()
	if !strings.Contains(msg, "10") || !strings.Contains(msg, "3") {
		t.Errorf("InsufficientCreditsError.Error() = %q, want to contain 10 and 3", msg)
	}
}

func TestFlavorNotAllowedError(t *testing.T) {
	e := FlavorNotAllowedError{Flavor: "plus", MaxFlavor: "standard"}
	msg := e.Error()
	if !strings.Contains(msg, "plus") || !strings.Contains(msg, "standard") {
		t.Errorf("FlavorNotAllowedError.Error() = %q, want to mention plus and standard", msg)
	}
}

// --- Service.Enabled ---

func TestServiceEnabledNilService(t *testing.T) {
	var s *Service
	if s.Enabled() {
		t.Error("nil *Service should not be Enabled()")
	}
}

func TestServiceEnabledNilStore(t *testing.T) {
	s := &Service{store: nil}
	if s.Enabled() {
		t.Error("Service with nil store should not be Enabled()")
	}
}

func TestServiceEnabledDisabledStore(t *testing.T) {
	s := &Service{store: &fakeStore{enabled: false}}
	if s.Enabled() {
		t.Error("Service with disabled store should not be Enabled()")
	}
}

func TestServiceEnabledTrue(t *testing.T) {
	s := &Service{store: enabledFakeStore()}
	if !s.Enabled() {
		t.Error("Service with enabled store should be Enabled()")
	}
}

// --- NewService ---

func TestNewServiceNilStore(t *testing.T) {
	svc := NewService(nil, nil)
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	// nil *Store assigned to serviceStore interface is non-nil interface value,
	// but Enabled() still returns false via Store.Enabled() nil-receiver check.
	if svc.Enabled() {
		t.Error("NewService(nil, nil) should not be Enabled()")
	}
}

// --- topupUnitCentsForPlan ---

func TestTopupUnitCentsForPlan(t *testing.T) {
	cases := []struct {
		planCode  string
		wantCents int
	}{
		{PlanBuilder, 2500},
		{PlanStudio, 2200},
		{PlanGrowth, 2000},
		{PlanBusiness, 1600},
		{"unknown-plan", 2200}, // falls back to DefaultPaidPlan (studio)
		{"", 2200},             // empty also falls back to DefaultPaidPlan
	}
	for _, tc := range cases {
		got := topupUnitCentsForPlan(tc.planCode)
		if got != tc.wantCents {
			t.Errorf("topupUnitCentsForPlan(%q) = %d, want %d", tc.planCode, got, tc.wantCents)
		}
	}
}

// --- Service disabled-path delegates ---

func TestServiceDisabledDelegates(t *testing.T) {
	var s *Service
	ctx := context.Background()

	if err := s.FinalizeRun(ctx, "run1", "success", time.Now()); err != nil {
		t.Errorf("FinalizeRun on nil Service: %v", err)
	}
	if ov, err := s.Overview(ctx, "org1"); err != nil || ov.Account != (Account{}) {
		t.Errorf("Overview on nil Service: %v %v", ov, err)
	}
	if _, err := s.UpdateTopupSettings(ctx, "org1", TopupSettings{}); err != nil {
		t.Errorf("UpdateTopupSettings: %v", err)
	}
	if m, err := s.ListRepoSettings(ctx, "org1"); err != nil || len(m) != 0 {
		t.Errorf("ListRepoSettings: %v %v", m, err)
	}
	if _, err := s.SetRepoFlavor(ctx, "org1", "owner", "repo", FlavorStandard); err != nil {
		t.Errorf("SetRepoFlavor: %v", err)
	}
	if _, err := s.SetStripeCustomer(ctx, "org1", "cus_1"); err != nil {
		t.Errorf("SetStripeCustomer: %v", err)
	}
	if _, err := s.FindAccountByStripeCustomer(ctx, "cus_1"); err != nil {
		t.Errorf("FindAccountByStripeCustomer: %v", err)
	}
	if _, err := s.UpsertAccountMirror(ctx, AccountMirror{}); err != nil {
		t.Errorf("UpsertAccountMirror: %v", err)
	}
	if _, err := s.SetBillingExempt(ctx, "org1", true); err != nil {
		t.Errorf("SetBillingExempt: %v", err)
	}
	if ids, err := s.ListAccountOrgIDs(ctx); err != nil || ids != nil {
		t.Errorf("ListAccountOrgIDs: %v %v", ids, err)
	}
	if ids, err := s.ListBillingExemptOrgIDs(ctx); err != nil || ids != nil {
		t.Errorf("ListBillingExemptOrgIDs: %v %v", ids, err)
	}
	if _, err := s.SetPendingPlanChange(ctx, "org1", PlanStudio, time.Now()); err != nil {
		t.Errorf("SetPendingPlanChange: %v", err)
	}
	if _, err := s.ClearPendingPlanChange(ctx, "org1"); err != nil {
		t.Errorf("ClearPendingPlanChange: %v", err)
	}
	if _, err := s.GrantTopupCredits(ctx, "org1", 10); err != nil {
		t.Errorf("GrantTopupCredits: %v", err)
	}
	if _, _, err := s.GrantTopupCreditsOnce(ctx, "ev1", "invoice.paid", "org1", 100); err != nil {
		t.Errorf("GrantTopupCreditsOnce: %v", err)
	}
	if err := s.SetLastPaymentError(ctx, "org1", "oops"); err != nil {
		t.Errorf("SetLastPaymentError: %v", err)
	}
}

// --- Service.AdmitRun ---

func TestAdmitRunDisabled(t *testing.T) {
	var s *Service
	adm, err := s.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if err != nil {
		t.Fatalf("AdmitRun on nil Service: %v", err)
	}
	if adm.Flavor.Code != FlavorStandard {
		t.Errorf("disabled AdmitRun returned flavor %q, want %q", adm.Flavor.Code, FlavorStandard)
	}
}

func TestAdmitRunMissingOrgOrRunID(t *testing.T) {
	svc := &Service{store: enabledFakeStore()}
	ctx := context.Background()

	if _, err := svc.AdmitRun(ctx, AdmissionRequest{OrgID: "org1"}); err == nil {
		t.Error("AdmitRun with no RunID should return error")
	}
	if _, err := svc.AdmitRun(ctx, AdmissionRequest{RunID: "run1"}); err == nil {
		t.Error("AdmitRun with no OrgID should return error")
	}
}

func TestAdmitRunEnsureAccountError(t *testing.T) {
	wantErr := errors.New("db error")
	store := enabledFakeStore()
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return Account{}, wantErr }
	svc := &Service{store: store}

	_, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected db error, got %v", err)
	}
}

func TestAdmitRunRepoFlavorError(t *testing.T) {
	wantErr := errors.New("flavor error")
	store := enabledFakeStore()
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) {
		return Account{OrgID: "org1", MaxFlavor: FlavorStandard}, nil
	}
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) { return Flavor{}, wantErr }
	svc := &Service{store: store}

	_, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected flavor error, got %v", err)
	}
}

func TestAdmitRunBillingExempt(t *testing.T) {
	store := enabledFakeStore()
	account := Account{OrgID: "org1", MaxFlavor: FlavorPlus, BillingExempt: true}
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return account, nil }
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) {
		return MustFlavor(FlavorStandard), nil
	}
	admittedRes := Reservation{RunID: "run1", OrgID: "org1", Status: ReservationComped}
	store.admitRun = func(_ context.Context, _, _ string, credits int, _ Flavor, _ time.Time) (Reservation, Account, error) {
		if credits != 0 {
			return Reservation{}, Account{}, errors.New("billing-exempt should reserve 0 credits")
		}
		return admittedRes, account, nil
	}
	svc := &Service{store: store}

	adm, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if err != nil {
		t.Fatalf("AdmitRun billing-exempt: %v", err)
	}
	if !adm.Comped {
		t.Error("expected Comped = true for billing-exempt account")
	}
}

func TestAdmitRunFlavorNotAllowed(t *testing.T) {
	store := enabledFakeStore()
	account := Account{OrgID: "org1", MaxFlavor: FlavorStandard, IncludedCredits: 100}
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return account, nil }
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) {
		return MustFlavor(FlavorPlus), nil
	}
	svc := &Service{store: store}

	_, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	var flavErr FlavorNotAllowedError
	if !errors.As(err, &flavErr) {
		t.Errorf("expected FlavorNotAllowedError, got %v", err)
	}
}

func TestAdmitRunSufficientCredits(t *testing.T) {
	store := enabledFakeStore()
	account := Account{OrgID: "org1", MaxFlavor: FlavorStandard, IncludedCredits: 100, PerRunMaxCredits: 4}
	admitted := Account{OrgID: "org1", MaxFlavor: FlavorStandard, IncludedCredits: 100, IncludedCreditsUsed: 4}
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return account, nil }
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) {
		return MustFlavor(FlavorStandard), nil
	}
	store.admitRun = func(_ context.Context, _, _ string, credits int, _ Flavor, _ time.Time) (Reservation, Account, error) {
		return Reservation{RunID: "run1", ReservedCredits: credits}, admitted, nil
	}
	svc := &Service{store: store}

	adm, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if err != nil {
		t.Fatalf("AdmitRun with sufficient credits: %v", err)
	}
	if adm.Comped {
		t.Error("non-exempt account should not be Comped")
	}
	if adm.ReservedCredits <= 0 {
		t.Errorf("expected non-zero ReservedCredits, got %d", adm.ReservedCredits)
	}
}

func TestAdmitRunInsufficientCreditsNoTopupper(t *testing.T) {
	store := enabledFakeStore()
	// account has 0 balance, needs at least 1 credit
	account := Account{OrgID: "org1", MaxFlavor: FlavorStandard, IncludedCredits: 0}
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return account, nil }
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) {
		return MustFlavor(FlavorStandard), nil
	}
	errInsufficient := InsufficientCreditsError{Needed: 1, Available: 0}
	store.autoTopup = func(_ context.Context, _ string, _ int, _ func(context.Context, Account, string) (string, error)) (Account, error) {
		return Account{}, errInsufficient
	}
	svc := &Service{store: store, topupper: nil}

	_, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if err == nil {
		t.Fatal("expected error for insufficient credits")
	}
	var insuffErr InsufficientCreditsError
	if !errors.As(err, &insuffErr) {
		t.Errorf("expected InsufficientCreditsError, got %T: %v", err, err)
	}
}

func TestAdmitRunAutoTopupPaymentError(t *testing.T) {
	paymentErr := errors.New("card declined")
	store := enabledFakeStore()
	account := Account{OrgID: "org1", MaxFlavor: FlavorStandard}
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return account, nil }
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) {
		return MustFlavor(FlavorStandard), nil
	}
	store.autoTopup = func(_ context.Context, _ string, _ int, _ func(context.Context, Account, string) (string, error)) (Account, error) {
		return Account{}, autoTopupPaymentError{err: paymentErr}
	}

	var capturedErrOrgID string
	store.setLastPaymentError = func(_ context.Context, orgID string, _ string) error {
		capturedErrOrgID = orgID
		return nil
	}

	tp := &fakeTopupper{purchaseTopupUnit: func(_ context.Context, _ Account, _ string) (string, error) {
		return "", paymentErr
	}}
	svc := &Service{store: store, topupper: tp}

	_, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if err == nil {
		t.Fatal("expected payment error to propagate")
	}
	if !errors.Is(err, paymentErr) {
		t.Errorf("expected wrapped paymentErr, got %v", err)
	}
	if capturedErrOrgID != "org1" {
		t.Errorf("SetLastPaymentError called with orgID %q, want %q", capturedErrOrgID, "org1")
	}
}

func TestAdmitRunAutoTopupSuccess(t *testing.T) {
	store := enabledFakeStore()
	// Start with empty balance, topup brings it to 100
	accountBefore := Account{OrgID: "org1", MaxFlavor: FlavorStandard, IncludedCredits: 0}
	accountAfter := Account{OrgID: "org1", MaxFlavor: FlavorStandard, IncludedCredits: 100}
	store.ensureAccount = func(_ context.Context, _ string) (Account, error) { return accountBefore, nil }
	store.repoFlavor = func(_ context.Context, _, _, _ string) (Flavor, error) {
		return MustFlavor(FlavorStandard), nil
	}
	store.autoTopup = func(_ context.Context, _ string, _ int, _ func(context.Context, Account, string) (string, error)) (Account, error) {
		return accountAfter, nil
	}
	store.admitRun = func(_ context.Context, _, _ string, credits int, _ Flavor, _ time.Time) (Reservation, Account, error) {
		return Reservation{RunID: "run1", ReservedCredits: credits}, accountAfter, nil
	}
	tp := &fakeTopupper{purchaseTopupUnit: func(_ context.Context, _ Account, _ string) (string, error) {
		return "inv_123", nil
	}}
	svc := &Service{store: store, topupper: tp}

	adm, err := svc.AdmitRun(context.Background(), AdmissionRequest{OrgID: "org1", RunID: "run1"})
	if err != nil {
		t.Fatalf("AdmitRun after topup: %v", err)
	}
	if adm.ReservedCredits <= 0 {
		t.Errorf("expected reserved credits after topup, got %d", adm.ReservedCredits)
	}
}

// fakeTopupper is a minimal AutoTopupper for testing.
type fakeTopupper struct {
	purchaseTopupUnit func(context.Context, Account, string) (string, error)
}

func (f *fakeTopupper) PurchaseTopupUnit(ctx context.Context, a Account, key string) (string, error) {
	return f.purchaseTopupUnit(ctx, a, key)
}

// --- Service.FinalizeRun ---

func TestFinalizeRunDisabled(t *testing.T) {
	var s *Service
	if err := s.FinalizeRun(context.Background(), "run1", "success", time.Now()); err != nil {
		t.Errorf("FinalizeRun disabled: %v", err)
	}
}

func TestFinalizeRunEnabled(t *testing.T) {
	store := enabledFakeStore()
	called := false
	store.finalizeRun = func(_ context.Context, runID, state string, _ time.Time) error {
		called = true
		if runID != "run1" || state != "success" {
			return errors.New("unexpected args")
		}
		return nil
	}
	svc := &Service{store: store}

	if err := svc.FinalizeRun(context.Background(), "run1", "success", time.Now()); err != nil {
		t.Fatalf("FinalizeRun: %v", err)
	}
	if !called {
		t.Error("store.FinalizeRun was not called")
	}
}

// newDelegateFakeStore builds a fakeStore wired with stubs for all delegate methods.
func newDelegateFakeStore(wantAccount Account, wantTopup TopupSettings, wantRepoSetting RepoSetting) *fakeStore {
	store := enabledFakeStore()
	store.overview = func(_ context.Context, orgID string, _ int32) (Overview, error) {
		if orgID != "org1" {
			return Overview{}, errors.New("unexpected orgID")
		}
		return Overview{Account: wantAccount}, nil
	}
	store.updateTopupSettings = func(_ context.Context, _ string, _ TopupSettings) (TopupSettings, error) {
		return wantTopup, nil
	}
	store.listRepoSettings = func(_ context.Context, _ string) (map[string]RepoSetting, error) {
		return map[string]RepoSetting{"owner/repo": {OrgID: "org1"}}, nil
	}
	store.setRepoFlavor = func(_ context.Context, _, _, _, _ string) (RepoSetting, error) { return wantRepoSetting, nil }
	store.setStripeCustomer = func(_ context.Context, _, _ string) (Account, error) { return wantAccount, nil }
	store.findAccountByStripeCustomer = func(_ context.Context, _ string) (Account, error) { return wantAccount, nil }
	store.upsertAccountMirror = func(_ context.Context, _ AccountMirror) (Account, error) { return wantAccount, nil }
	store.setBillingExempt = func(_ context.Context, _ string, _ bool) (Account, error) { return wantAccount, nil }
	store.listAccountOrgIDs = func(_ context.Context) ([]string, error) { return []string{"org1"}, nil }
	store.listBillingExemptOrgIDs = func(_ context.Context) ([]string, error) { return []string{"org1"}, nil }
	store.setPendingPlanChange = func(_ context.Context, _, _ string, _ time.Time) (Account, error) { return wantAccount, nil }
	store.clearPendingPlanChange = func(_ context.Context, _ string) (Account, error) { return wantAccount, nil }
	store.grantTopupCredits = func(_ context.Context, _ string, _ int) (Account, error) { return wantAccount, nil }
	store.grantTopupCreditsOnce = func(_ context.Context, _, _, _ string, _ int) (Account, bool, error) {
		return wantAccount, true, nil
	}
	store.setLastPaymentError = func(_ context.Context, _, _ string) error { return nil }
	return store
}

// --- Service enabled delegate methods (split to stay within complexity limits) ---

func TestServiceDelegatesOverviewAndSettings(t *testing.T) {
	ctx := context.Background()
	wantAccount := Account{OrgID: "org1", StripeCustomerID: "cus_1"}
	wantTopup := TopupSettings{OrgID: "org1", TargetBalance: 50}
	wantRepoSetting := RepoSetting{OrgID: "org1", GitHubOwner: "owner", GitHubRepo: "repo", Flavor: FlavorPlus}
	svc := &Service{store: newDelegateFakeStore(wantAccount, wantTopup, wantRepoSetting)}

	ov, err := svc.Overview(ctx, "org1")
	if err != nil || ov.Account != wantAccount {
		t.Errorf("Overview: %v %v", ov, err)
	}
	ts, err := svc.UpdateTopupSettings(ctx, "org1", TopupSettings{})
	if err != nil || ts != wantTopup {
		t.Errorf("UpdateTopupSettings: %v %v", ts, err)
	}
	rs, err := svc.ListRepoSettings(ctx, "org1")
	if err != nil || len(rs) != 1 {
		t.Errorf("ListRepoSettings: %v %v", rs, err)
	}
	rsetting, err := svc.SetRepoFlavor(ctx, "org1", "owner", "repo", FlavorPlus)
	if err != nil || rsetting != wantRepoSetting {
		t.Errorf("SetRepoFlavor: %v %v", rsetting, err)
	}
}

func TestServiceDelegatesAccountMethods(t *testing.T) {
	ctx := context.Background()
	wantAccount := Account{OrgID: "org1", StripeCustomerID: "cus_1"}
	wantTopup := TopupSettings{}
	wantRepoSetting := RepoSetting{}
	svc := &Service{store: newDelegateFakeStore(wantAccount, wantTopup, wantRepoSetting)}

	acct, err := svc.SetStripeCustomer(ctx, "org1", "cus_1")
	if err != nil || acct != wantAccount {
		t.Errorf("SetStripeCustomer: %v %v", acct, err)
	}
	acct, err = svc.FindAccountByStripeCustomer(ctx, "cus_1")
	if err != nil || acct != wantAccount {
		t.Errorf("FindAccountByStripeCustomer: %v %v", acct, err)
	}
	acct, err = svc.UpsertAccountMirror(ctx, AccountMirror{})
	if err != nil || acct != wantAccount {
		t.Errorf("UpsertAccountMirror: %v %v", acct, err)
	}
	acct, err = svc.SetBillingExempt(ctx, "org1", true)
	if err != nil || acct != wantAccount {
		t.Errorf("SetBillingExempt: %v %v", acct, err)
	}
}

func TestServiceDelegatesCreditsAndPlanMethods(t *testing.T) {
	ctx := context.Background()
	wantAccount := Account{OrgID: "org1", StripeCustomerID: "cus_1"}
	svc := &Service{store: newDelegateFakeStore(wantAccount, TopupSettings{}, RepoSetting{})}

	orgIDs, err := svc.ListAccountOrgIDs(ctx)
	if err != nil || len(orgIDs) != 1 {
		t.Errorf("ListAccountOrgIDs: %v %v", orgIDs, err)
	}
	exemptIDs, err := svc.ListBillingExemptOrgIDs(ctx)
	if err != nil || len(exemptIDs) != 1 {
		t.Errorf("ListBillingExemptOrgIDs: %v %v", exemptIDs, err)
	}
	acct, err := svc.SetPendingPlanChange(ctx, "org1", PlanStudio, time.Now())
	if err != nil || acct != wantAccount {
		t.Errorf("SetPendingPlanChange: %v %v", acct, err)
	}
	acct, err = svc.ClearPendingPlanChange(ctx, "org1")
	if err != nil || acct != wantAccount {
		t.Errorf("ClearPendingPlanChange: %v %v", acct, err)
	}
	acct, err = svc.GrantTopupCredits(ctx, "org1", 50)
	if err != nil || acct != wantAccount {
		t.Errorf("GrantTopupCredits: %v %v", acct, err)
	}
	acct, granted, err := svc.GrantTopupCreditsOnce(ctx, "ev1", "invoice.paid", "org1", 100)
	if err != nil || acct != wantAccount || !granted {
		t.Errorf("GrantTopupCreditsOnce: %v %v %v", acct, granted, err)
	}
	if err := svc.SetLastPaymentError(ctx, "org1", "err"); err != nil {
		t.Errorf("SetLastPaymentError: %v", err)
	}
}

// --- Store.Enabled and NewStore ---

func TestStoreEnabledNil(t *testing.T) {
	var s *Store
	if s.Enabled() {
		t.Error("nil *Store should not be Enabled()")
	}
}

func TestStoreEnabledNilDB(t *testing.T) {
	s := NewStore(nil)
	if s.Enabled() {
		t.Error("Store with nil db should not be Enabled()")
	}
}

// --- Store method early-returns when not enabled ---

func TestStoreDisabledMethodsReturnEarlyErrors(t *testing.T) {
	s := NewStore(nil)
	ctx := context.Background()

	if _, err := s.EnsureAccount(ctx, "org1"); err == nil {
		t.Error("EnsureAccount on disabled store should return error")
	}
	if _, err := s.GetAccount(ctx, "org1"); err == nil {
		t.Error("GetAccount on disabled store should return error")
	}
	if _, err := s.FindAccountByStripeCustomer(ctx, "cus_1"); err == nil {
		t.Error("FindAccountByStripeCustomer on disabled store should return error")
	}
	if _, err := s.UpsertAccountMirror(ctx, AccountMirror{}); err == nil {
		t.Error("UpsertAccountMirror on disabled store should return error")
	}
	if _, err := s.SetBillingExempt(ctx, "org1", true); err == nil {
		t.Error("SetBillingExempt on disabled store should return error")
	}
	if _, err := s.ListAccountOrgIDs(ctx); err == nil {
		t.Error("ListAccountOrgIDs on disabled store should return error")
	}
	if _, err := s.ListBillingExemptOrgIDs(ctx); err == nil {
		t.Error("ListBillingExemptOrgIDs on disabled store should return error")
	}
	if _, err := s.SetStripeCustomer(ctx, "org1", "cus_1"); err == nil {
		t.Error("SetStripeCustomer on disabled store should return error")
	}
	if _, err := s.SetPendingPlanChange(ctx, "org1", PlanStudio, time.Now()); err == nil {
		t.Error("SetPendingPlanChange on disabled store should return error")
	}
	if _, err := s.ClearPendingPlanChange(ctx, "org1"); err == nil {
		t.Error("ClearPendingPlanChange on disabled store should return error")
	}
	if _, err := s.GrantTopupCredits(ctx, "org1", 10); err == nil {
		t.Error("GrantTopupCredits on disabled store should return error")
	}
	if _, _, err := s.GrantTopupCreditsOnce(ctx, "ev1", "type", "org1", 10); err == nil {
		t.Error("GrantTopupCreditsOnce on disabled store should return error")
	}
	if err := s.SetLastPaymentError(ctx, "org1", "msg"); err == nil {
		t.Error("SetLastPaymentError on disabled store should return error")
	}
	if _, err := s.EnsureTopupSettings(ctx, "org1"); err == nil {
		t.Error("EnsureTopupSettings on disabled store should return error")
	}
	if _, err := s.UpdateTopupSettings(ctx, "org1", TopupSettings{}); err == nil {
		t.Error("UpdateTopupSettings on disabled store should return error")
	}
	// ListRepoSettings returns empty map (not error) when disabled
	if rs, err := s.ListRepoSettings(ctx, "org1"); err != nil || len(rs) != 0 {
		t.Errorf("ListRepoSettings on disabled store: %v %v", rs, err)
	}
	if _, err := s.SetRepoFlavor(ctx, "org1", "owner", "repo", FlavorStandard); err == nil {
		t.Error("SetRepoFlavor on disabled store should return error")
	}
	if _, _, err := s.AdmitRun(ctx, "org1", "run1", 4, MustFlavor(FlavorStandard), time.Now()); err == nil {
		t.Error("AdmitRun on disabled store should return error")
	}
	if _, err := s.AutoTopup(ctx, "org1", 4, nil); err == nil {
		t.Error("AutoTopup on disabled store should return error")
	}
}

// --- Pure helper functions in store.go ---

func TestCurrentBillingMonth(t *testing.T) {
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "2026-01"},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "2026-12"},
		{time.Time{}, ""}, // zero time is special-cased — will call time.Now() internally
	}
	for _, tc := range cases {
		got := currentBillingMonth(tc.t)
		if tc.t.IsZero() {
			// Just check format — not the exact month value since it calls time.Now()
			if len(got) != 7 || got[4] != '-' {
				t.Errorf("currentBillingMonth(zero) = %q, want YYYY-MM format", got)
			}
		} else if got != tc.want {
			t.Errorf("currentBillingMonth(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestTimestamptz(t *testing.T) {
	zero := timestamptz(time.Time{})
	if zero.Valid {
		t.Error("timestamptz(zero) should not be valid")
	}

	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	got := timestamptz(ts)
	if !got.Valid {
		t.Error("timestamptz(non-zero) should be valid")
	}
	if !got.Time.Equal(ts) {
		t.Errorf("timestamptz time mismatch: got %v, want %v", got.Time, ts)
	}
}

func TestPgTime(t *testing.T) {
	invalid := pgTime(pgtype.Timestamptz{Valid: false})
	if !invalid.IsZero() {
		t.Errorf("pgTime(invalid) = %v, want zero time", invalid)
	}

	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	got := pgTime(pgtype.Timestamptz{Time: ts, Valid: true})
	if !got.Equal(ts) {
		t.Errorf("pgTime(valid) = %v, want %v", got, ts)
	}
}

// --- ParseFlavor edge cases ---

func TestParseFlavourEdgeCases(t *testing.T) {
	// Empty string normalises to standard
	f, err := ParseFlavor("")
	if err != nil || f.Code != FlavorStandard {
		t.Errorf("ParseFlavor('') = %v %v, want standard", f, err)
	}

	// Whitespace-only also normalises to standard
	f, err = ParseFlavor("  ")
	if err != nil || f.Code != FlavorStandard {
		t.Errorf("ParseFlavor('  ') = %v %v, want standard", f, err)
	}

	// Unknown code returns ErrUnknownFlavor
	_, err = ParseFlavor("bogus")
	if !errors.Is(err, ErrUnknownFlavor) {
		t.Errorf("ParseFlavor('bogus') error = %v, want ErrUnknownFlavor", err)
	}

	// MustFlavor on error falls back to standard
	f = MustFlavor("bogus")
	if f.Code != FlavorStandard {
		t.Errorf("MustFlavor('bogus') = %q, want standard", f.Code)
	}

	// Legacy pro/max/enterprise map to plus
	for _, code := range []string{FlavorPro, FlavorMax, FlavorEnterprise} {
		f, err := ParseFlavor(code)
		if err != nil || f.Code != FlavorPlus {
			t.Errorf("ParseFlavor(%q) = %v %v, want plus", code, f, err)
		}
	}
}

// --- BillableCredits edge cases ---

func TestBillableCreditsMultiplierBelowOne(t *testing.T) {
	start := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	mins, creds := BillableCredits(start, start.Add(15*time.Minute), 0)
	if mins != 15 || creds != 1 {
		t.Errorf("BillableCredits with multiplier 0 = (%d, %d), want (15, 1)", mins, creds)
	}
	// end before start
	mins2, creds2 := BillableCredits(start.Add(time.Minute), start, 1)
	if mins2 != 0 || creds2 != 0 {
		t.Errorf("BillableCredits end<start = (%d, %d), want (0, 0)", mins2, creds2)
	}
}

// --- autoTopupPaymentError ---

func TestAutoTopupPaymentError(t *testing.T) {
	inner := errors.New("stripe declined")
	e := autoTopupPaymentError{err: inner}

	if e.Error() != inner.Error() {
		t.Errorf("Error() = %q, want %q", e.Error(), inner.Error())
	}
	if !errors.Is(e, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}
