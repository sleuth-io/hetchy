package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrAutoTopupNotConfigured = errors.New("billing: auto top-up is not configured")

type InsufficientCreditsError struct {
	Needed    int
	Available int
}

func (e InsufficientCreditsError) Error() string {
	return fmt.Sprintf("billing: insufficient credits: need %d, have %d", e.Needed, e.Available)
}

type FlavorNotAllowedError struct {
	Flavor    string
	MaxFlavor string
}

func (e FlavorNotAllowedError) Error() string {
	return fmt.Sprintf("billing: flavor %q exceeds plan cap %q", e.Flavor, e.MaxFlavor)
}

type AutoTopupper interface {
	PurchaseTopupUnit(context.Context, Account, string) (string, error)
}

// serviceStore is the subset of Store methods used by Service. Extracted as an
// interface so tests can inject a fake without a real database.
type serviceStore interface {
	Enabled() bool
	EnsureAccount(ctx context.Context, orgID string) (Account, error)
	RepoFlavor(ctx context.Context, orgID, owner, repo string) (Flavor, error)
	AdmitRun(ctx context.Context, orgID, runID string, credits int, flavor Flavor, startedAt time.Time) (Reservation, Account, error)
	AutoTopup(ctx context.Context, orgID string, reserveCredits int, purchase func(context.Context, Account, string) (string, error)) (Account, error)
	SetLastPaymentError(ctx context.Context, orgID, msg string) error
	FinalizeRun(ctx context.Context, runID, terminalState string, endedAt time.Time) error
	Overview(ctx context.Context, orgID string, meterLimit int32) (Overview, error)
	UpdateTopupSettings(ctx context.Context, orgID string, settings TopupSettings) (TopupSettings, error)
	ListRepoSettings(ctx context.Context, orgID string) (map[string]RepoSetting, error)
	SetRepoFlavor(ctx context.Context, orgID, owner, repo, flavor string) (RepoSetting, error)
	SetStripeCustomer(ctx context.Context, orgID, customerID string) (Account, error)
	FindAccountByStripeCustomer(ctx context.Context, customerID string) (Account, error)
	UpsertAccountMirror(ctx context.Context, mirror AccountMirror) (Account, error)
	SetBillingExempt(ctx context.Context, orgID string, exempt bool) (Account, error)
	ListAccountOrgIDs(ctx context.Context) ([]string, error)
	ListBillingExemptOrgIDs(ctx context.Context) ([]string, error)
	SetPendingPlanChange(ctx context.Context, orgID, planCode string, effectiveAt time.Time) (Account, error)
	ClearPendingPlanChange(ctx context.Context, orgID string) (Account, error)
	GrantTopupCredits(ctx context.Context, orgID string, credits int) (Account, error)
	GrantTopupCreditsOnce(ctx context.Context, eventID, eventType, orgID string, credits int) (Account, bool, error)
}

type Service struct {
	store    serviceStore
	topupper AutoTopupper
}

func NewService(store *Store, topupper AutoTopupper) *Service {
	return &Service{store: store, topupper: topupper}
}

func (s *Service) Enabled() bool {
	return s != nil && s.store != nil && s.store.Enabled()
}

func (s *Service) AdmitRun(ctx context.Context, req AdmissionRequest) (Admission, error) {
	if !s.Enabled() {
		return Admission{Flavor: MustFlavor(FlavorStandard)}, nil
	}
	if req.RunID == "" || req.OrgID == "" {
		return Admission{}, errors.New("billing: org_id and run_id required")
	}
	account, err := s.store.EnsureAccount(ctx, req.OrgID)
	if err != nil {
		return Admission{}, err
	}
	flavor, err := s.store.RepoFlavor(ctx, req.OrgID, req.GitHubOwner, req.GitHubRepo)
	if err != nil {
		return Admission{}, err
	}
	if account.BillingExempt {
		if _, _, err := s.store.AdmitRun(ctx, req.OrgID, req.RunID, 0, flavor, req.StartedAt); err != nil {
			return Admission{}, err
		}
		return Admission{
			Account:          account,
			Flavor:           flavor,
			Comped:           true,
			AvailableCredits: account.Balance(),
		}, nil
	}
	if !FlavorAllowed(flavor.Code, account.MaxFlavor) {
		return Admission{}, FlavorNotAllowedError{Flavor: flavor.Code, MaxFlavor: account.MaxFlavor}
	}
	reserveCredits := max(account.PerRunMaxCredits, flavor.Multiplier, 1)
	if account.Balance() < reserveCredits {
		account, err = s.maybeAutoTopup(ctx, account, reserveCredits)
		if err != nil {
			return Admission{}, err
		}
	}
	res, account, err := s.store.AdmitRun(ctx, req.OrgID, req.RunID, reserveCredits, flavor, req.StartedAt)
	if err != nil {
		return Admission{}, err
	}
	return Admission{
		Account:          account,
		Flavor:           flavor,
		ReservedCredits:  res.ReservedCredits,
		AvailableCredits: account.Balance(),
	}, nil
}

func (s *Service) maybeAutoTopup(ctx context.Context, account Account, reserveCredits int) (Account, error) {
	var purchase func(context.Context, Account, string) (string, error)
	if s.topupper != nil {
		purchase = s.topupper.PurchaseTopupUnit
	}
	account, err := s.store.AutoTopup(ctx, account.OrgID, reserveCredits, purchase)
	if err != nil {
		var paymentErr autoTopupPaymentError
		if errors.As(err, &paymentErr) {
			_ = s.store.SetLastPaymentError(ctx, account.OrgID, paymentErr.err.Error())
		}
		return Account{}, err
	}
	return account, nil
}

func topupUnitCentsForPlan(planCode string) int {
	plan, ok := PaidPlanByCode(planCode)
	if !ok {
		plan = DefaultPaidPlan()
	}
	return max(plan.TopupUnitUSDCents, 0)
}

func (s *Service) FinalizeRun(ctx context.Context, runID, terminalState string, endedAt time.Time) error {
	if !s.Enabled() {
		return nil
	}
	return s.store.FinalizeRun(ctx, runID, terminalState, endedAt)
}

func (s *Service) Overview(ctx context.Context, orgID string) (Overview, error) {
	if !s.Enabled() {
		return Overview{}, nil
	}
	return s.store.Overview(ctx, orgID, 10)
}

func (s *Service) UpdateTopupSettings(ctx context.Context, orgID string, settings TopupSettings) (TopupSettings, error) {
	if !s.Enabled() {
		return TopupSettings{}, nil
	}
	return s.store.UpdateTopupSettings(ctx, orgID, settings)
}

func (s *Service) ListRepoSettings(ctx context.Context, orgID string) (map[string]RepoSetting, error) {
	if !s.Enabled() {
		return map[string]RepoSetting{}, nil
	}
	return s.store.ListRepoSettings(ctx, orgID)
}

func (s *Service) SetRepoFlavor(ctx context.Context, orgID, owner, repo, flavor string) (RepoSetting, error) {
	if !s.Enabled() {
		return RepoSetting{OrgID: orgID, GitHubOwner: owner, GitHubRepo: repo, Flavor: FlavorStandard}, nil
	}
	return s.store.SetRepoFlavor(ctx, orgID, owner, repo, flavor)
}

func (s *Service) SetStripeCustomer(ctx context.Context, orgID, customerID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.SetStripeCustomer(ctx, orgID, customerID)
}

func (s *Service) FindAccountByStripeCustomer(ctx context.Context, customerID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.FindAccountByStripeCustomer(ctx, customerID)
}

func (s *Service) UpsertAccountMirror(ctx context.Context, mirror AccountMirror) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.UpsertAccountMirror(ctx, mirror)
}

func (s *Service) SetBillingExempt(ctx context.Context, orgID string, exempt bool) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.SetBillingExempt(ctx, orgID, exempt)
}

func (s *Service) ListAccountOrgIDs(ctx context.Context) ([]string, error) {
	if !s.Enabled() {
		return nil, nil
	}
	return s.store.ListAccountOrgIDs(ctx)
}

func (s *Service) ListBillingExemptOrgIDs(ctx context.Context) ([]string, error) {
	if !s.Enabled() {
		return nil, nil
	}
	return s.store.ListBillingExemptOrgIDs(ctx)
}

func (s *Service) SetPendingPlanChange(ctx context.Context, orgID, planCode string, effectiveAt time.Time) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.SetPendingPlanChange(ctx, orgID, planCode, effectiveAt)
}

func (s *Service) ClearPendingPlanChange(ctx context.Context, orgID string) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.ClearPendingPlanChange(ctx, orgID)
}

func (s *Service) GrantTopupCredits(ctx context.Context, orgID string, credits int) (Account, error) {
	if !s.Enabled() {
		return Account{}, nil
	}
	return s.store.GrantTopupCredits(ctx, orgID, credits)
}

func (s *Service) GrantTopupCreditsOnce(ctx context.Context, eventID, eventType, orgID string, credits int) (Account, bool, error) {
	if !s.Enabled() {
		return Account{}, false, nil
	}
	return s.store.GrantTopupCreditsOnce(ctx, eventID, eventType, orgID, credits)
}

func (s *Service) SetLastPaymentError(ctx context.Context, orgID, msg string) error {
	if !s.Enabled() {
		return nil
	}
	return s.store.SetLastPaymentError(ctx, orgID, msg)
}
