package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// fakeBillingService is an in-memory billingService that records calls and
// returns caller-configured results, letting the billing settings handlers and
// view builders exercise their success and error branches without a
// database-backed billing store.
type fakeBillingService struct {
	enabled bool

	overview    billing.Overview
	overviewErr error

	repoSettings    map[string]billing.RepoSetting
	repoSettingsErr error

	setRepoFlavorErr error
	setOwner         string
	setRepo          string
	setFlavor        string

	updateTopupErr error
	updatedTopup   billing.TopupSettings
	updatedCalled  bool
}

func (f *fakeBillingService) Enabled() bool { return f.enabled }

func (f *fakeBillingService) Overview(_ context.Context, _ string) (billing.Overview, error) {
	return f.overview, f.overviewErr
}

func (f *fakeBillingService) ListRepoSettings(_ context.Context, _ string) (map[string]billing.RepoSetting, error) {
	return f.repoSettings, f.repoSettingsErr
}

func (f *fakeBillingService) SetRepoFlavor(_ context.Context, _, owner, repo, flavor string) (billing.RepoSetting, error) {
	f.setOwner, f.setRepo, f.setFlavor = owner, repo, flavor
	if f.setRepoFlavorErr != nil {
		return billing.RepoSetting{}, f.setRepoFlavorErr
	}
	return billing.RepoSetting{GitHubOwner: owner, GitHubRepo: repo, Flavor: flavor}, nil
}

func (f *fakeBillingService) UpdateTopupSettings(_ context.Context, _ string, settings billing.TopupSettings) (billing.TopupSettings, error) {
	f.updatedCalled = true
	f.updatedTopup = settings
	return settings, f.updateTopupErr
}

func adminBillingRequest(method, target, body string) *http.Request {
	req := settingsFormRequest(method, target, body)
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_admin",
		OrgID:  "org_test",
		Role:   "admin",
	}))
}

func memberBillingRequest(method, target, body string) *http.Request {
	req := settingsFormRequest(method, target, body)
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_member",
		OrgID:  "org_test",
		Role:   "member",
	}))
}

func TestBillingSvcReturnsNilWhenUnconfigured(t *testing.T) {
	if svc := (&Bot{}).billingSvc(); svc != nil {
		t.Fatalf("billingSvc() = %v, want nil when billing is unconfigured", svc)
	}
	real := billing.NewService(nil, nil)
	if svc := (&Bot{billing: real}).billingSvc(); svc == nil {
		t.Fatal("billingSvc() = nil, want the configured service")
	}
}

func TestLoadBillingOverviewFreePlanWhenDisabled(t *testing.T) {
	b := &Bot{log: discardLogger()}

	t.Run("nil service falls back to the free plan", func(t *testing.T) {
		view, err := b.loadBillingOverview(t.Context(), nil, "org_test")
		if err != nil {
			t.Fatalf("loadBillingOverview: %v", err)
		}
		if view.PlanCode != "free" || view.Status != "free" {
			t.Fatalf("view = %+v, want free plan", view)
		}
		if view.IncludedCredits != billing.FreeIncludedCredits {
			t.Fatalf("IncludedCredits = %d, want %d", view.IncludedCredits, billing.FreeIncludedCredits)
		}
	})

	t.Run("disabled service falls back to the free plan", func(t *testing.T) {
		view, err := b.loadBillingOverview(t.Context(), &fakeBillingService{enabled: false}, "org_test")
		if err != nil {
			t.Fatalf("loadBillingOverview: %v", err)
		}
		if view.PlanCode != "free" {
			t.Fatalf("PlanCode = %q, want free", view.PlanCode)
		}
	})
}

func TestLoadBillingOverviewPopulatesViewFromService(t *testing.T) {
	b := &Bot{log: discardLogger()}
	started := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	svc := &fakeBillingService{
		enabled: true,
		overview: billing.Overview{
			Account: billing.Account{
				OrgID:                "org_test",
				StripeCustomerID:     "cus_1",
				StripeSubscriptionID: "sub_1",
				PlanCode:             billing.PlanStudio,
				Status:               "active",
				IncludedCredits:      500,
				IncludedCreditsUsed:  120,
				TopupCredits:         40,
				MaxFlavor:            billing.FlavorPlus,
				PerRunMaxCredits:     12,
				LastPaymentError:     "card_declined",
			},
			TopupSettings: billing.TopupSettings{
				AutoTopupEnabled: true,
				MonthlyMaxCents:  5000,
			},
			RecentMeters: []billing.RunMeter{{
				RunID:           "run_1",
				Flavor:          billing.FlavorPlus,
				BillableMinutes: 7,
				CapturedCredits: 3,
				TerminalState:   "succeeded",
				StartedAt:       started,
			}},
		},
	}

	view, err := b.loadBillingOverview(t.Context(), svc, "org_test")
	if err != nil {
		t.Fatalf("loadBillingOverview: %v", err)
	}
	if view.PlanCode != billing.PlanStudio {
		t.Fatalf("PlanCode = %q, want %q", view.PlanCode, billing.PlanStudio)
	}
	if view.IncludedRemaining != 380 {
		t.Fatalf("IncludedRemaining = %d, want 380", view.IncludedRemaining)
	}
	if view.Balance != 420 {
		t.Fatalf("Balance = %d, want 420", view.Balance)
	}
	if !view.HasStripeCustomer || !view.HasSubscription {
		t.Fatalf("stripe flags = %+v, want both true", view)
	}
	if view.LastPaymentError != "card_declined" {
		t.Fatalf("LastPaymentError = %q", view.LastPaymentError)
	}
	if len(view.RecentMeters) != 1 || view.RecentMeters[0].RunID != "run_1" {
		t.Fatalf("RecentMeters = %+v", view.RecentMeters)
	}
	if len(view.PlanOptions) != len(billing.PaidPlans()) {
		t.Fatalf("PlanOptions = %d, want %d", len(view.PlanOptions), len(billing.PaidPlans()))
	}
}

func TestLoadBillingOverviewCompedAccountDisplaysBusiness(t *testing.T) {
	b := &Bot{log: discardLogger()}
	svc := &fakeBillingService{
		enabled: true,
		overview: billing.Overview{
			Account: billing.Account{OrgID: "org_test", PlanCode: billing.PlanFree, BillingExempt: true},
		},
	}
	view, err := b.loadBillingOverview(t.Context(), svc, "org_test")
	if err != nil {
		t.Fatalf("loadBillingOverview: %v", err)
	}
	if !view.BillingExempt {
		t.Fatal("BillingExempt = false, want true")
	}
	if view.CurrentPlanLabel != "Business" && view.CurrentPlanLabel != "Comped" {
		t.Fatalf("CurrentPlanLabel = %q, want Business/Comped", view.CurrentPlanLabel)
	}
	if view.CanTopup {
		t.Fatal("CanTopup = true, comped orgs may not top up")
	}
}

func TestLoadBillingOverviewPropagatesOverviewError(t *testing.T) {
	b := &Bot{log: discardLogger()}
	svc := &fakeBillingService{enabled: true, overviewErr: errors.New("db down")}
	if _, err := b.loadBillingOverview(t.Context(), svc, "org_test"); err == nil {
		t.Fatal("expected error from Overview to propagate")
	}
}

func TestRepoBillingViewData(t *testing.T) {
	b := &Bot{log: discardLogger()}

	t.Run("nil service returns standard-only defaults", func(t *testing.T) {
		settings, allowed := b.repoBillingViewData(t.Context(), nil, "org_test")
		if len(settings) != 0 {
			t.Fatalf("settings = %+v, want empty", settings)
		}
		if len(allowed) != 1 || allowed[0].Code != billing.FlavorStandard {
			t.Fatalf("allowed = %+v, want standard only", allowed)
		}
	})

	t.Run("enabled service returns allowed flavors and repo rows", func(t *testing.T) {
		svc := &fakeBillingService{
			enabled: true,
			overview: billing.Overview{
				AllowedFlavors: billing.AllowedFlavors(billing.FlavorPlus),
			},
			repoSettings: map[string]billing.RepoSetting{
				"acme/widgets": {GitHubOwner: "acme", GitHubRepo: "widgets", Flavor: billing.FlavorPlus},
			},
		}
		settings, allowed := b.repoBillingViewData(t.Context(), svc, "org_test")
		if len(settings) != 1 {
			t.Fatalf("settings = %+v, want one row", settings)
		}
		if len(allowed) < 2 {
			t.Fatalf("allowed = %+v, want plus tier", allowed)
		}
	})

	t.Run("comped org is granted the enterprise flavor set", func(t *testing.T) {
		svc := &fakeBillingService{
			enabled: true,
			overview: billing.Overview{
				AllowedFlavors: billing.AllowedFlavors(billing.FlavorStandard),
				Account:        billing.Account{BillingExempt: true},
			},
		}
		_, allowed := b.repoBillingViewData(t.Context(), svc, "org_test")
		want := billing.AllowedFlavors(billing.FlavorEnterprise)
		if len(allowed) != len(want) {
			t.Fatalf("allowed = %d flavors, want %d for comped org", len(allowed), len(want))
		}
	})
}

func okRepoLookup(_ context.Context, _, owner, name string) (sqlc.GithubRepo, error) {
	return sqlc.GithubRepo{Owner: owner, Name: name}, nil
}

func TestRepoFlavorSettings(t *testing.T) {
	t.Run("saves the flavor and redirects", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true}
		b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
		b.repoFlavorSettings(rec, req, svc)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/settings/org?tab=repositories&saved=repo_flavor_saved" {
			t.Fatalf("redirect = %q", got)
		}
		if svc.setOwner != "acme" || svc.setRepo != "widgets" || svc.setFlavor != "plus" {
			t.Fatalf("SetRepoFlavor args = %q/%q/%q", svc.setOwner, svc.setRepo, svc.setFlavor)
		}
	})

	t.Run("non-admin is forbidden", func(t *testing.T) {
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := memberBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
		b.repoFlavorSettings(rec, req, &fakeBillingService{enabled: true})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("wrong method is rejected", func(t *testing.T) {
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodGet, "/settings/org/repositories/flavor", "")
		b.repoFlavorSettings(rec, req, &fakeBillingService{enabled: true})
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("missing fields are a bad request", func(t *testing.T) {
		b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&flavor=plus")
		b.repoFlavorSettings(rec, req, &fakeBillingService{enabled: true})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("unresolvable repo surfaces a repo error", func(t *testing.T) {
		b := &Bot{log: discardLogger(), lookupRepoFn: func(context.Context, string, string, string) (sqlc.GithubRepo, error) {
			return sqlc.GithubRepo{}, errors.New("boom")
		}}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
		b.repoFlavorSettings(rec, req, &fakeBillingService{enabled: true})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("billing not configured returns 500", func(t *testing.T) {
		b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
		b.repoFlavorSettings(rec, req, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("unknown flavor is a bad request", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, setRepoFlavorErr: billing.ErrUnknownFlavor}
		b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=bogus")
		b.repoFlavorSettings(rec, req, svc)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("not-allowed flavor is a bad request", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, setRepoFlavorErr: billing.FlavorNotAllowedError{Flavor: billing.FlavorPlus, MaxFlavor: billing.FlavorStandard}}
		b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
		b.repoFlavorSettings(rec, req, svc)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("generic save failure returns 500", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, setRepoFlavorErr: errors.New("db down")}
		b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
		b.repoFlavorSettings(rec, req, svc)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}

func TestRepoFlavorSettingsHandlerWiring(t *testing.T) {
	// The wrapper resolves b.billingSvc() and delegates; with billing
	// unconfigured the inner body reaches the "not configured" branch,
	// which is enough to prove the handler is wired to the seam.
	b := &Bot{log: discardLogger(), lookupRepoFn: okRepoLookup}
	rec := httptest.NewRecorder()
	req := adminBillingRequest(http.MethodPost, "/settings/org/repositories/flavor", "owner=acme&name=widgets&flavor=plus")
	b.repoFlavorSettingsHandler(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (billing not configured)", rec.Code)
	}
}

func TestBillingTopupSettings(t *testing.T) {
	paidOverview := func() billing.Overview {
		return billing.Overview{Account: billing.Account{PlanCode: billing.PlanStudio, PerRunMaxCredits: 12}}
	}

	t.Run("saves settings and redirects", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, overview: paidOverview()}
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/billing/topup-settings", "auto_topup_enabled=1&monthly_max_spend=%2450")
		b.billingTopupSettings(rec, req, svc)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/settings/org?tab=billing&saved=billing_saved" {
			t.Fatalf("redirect = %q", got)
		}
		if !svc.updatedCalled || !svc.updatedTopup.AutoTopupEnabled {
			t.Fatalf("update settings = %+v", svc.updatedTopup)
		}
		if svc.updatedTopup.MonthlyMaxCents != 5000 {
			t.Fatalf("MonthlyMaxCents = %d, want 5000", svc.updatedTopup.MonthlyMaxCents)
		}
	})

	t.Run("non-admin is forbidden", func(t *testing.T) {
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := memberBillingRequest(http.MethodPost, "/settings/org/billing/topup-settings", "auto_topup_enabled=1")
		b.billingTopupSettings(rec, req, &fakeBillingService{enabled: true, overview: paidOverview()})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("wrong method is rejected", func(t *testing.T) {
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodGet, "/settings/org/billing/topup-settings", "")
		b.billingTopupSettings(rec, req, &fakeBillingService{enabled: true})
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("billing not configured returns 500", func(t *testing.T) {
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/billing/topup-settings", "auto_topup_enabled=1")
		b.billingTopupSettings(rec, req, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("overview error returns 500", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, overviewErr: errors.New("db down")}
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/billing/topup-settings", "auto_topup_enabled=1")
		b.billingTopupSettings(rec, req, svc)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("free plan may not enable top-ups", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, overview: billing.Overview{Account: billing.Account{PlanCode: billing.PlanFree}}}
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/billing/topup-settings", "auto_topup_enabled=1")
		b.billingTopupSettings(rec, req, svc)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("update failure returns 500", func(t *testing.T) {
		svc := &fakeBillingService{enabled: true, overview: paidOverview(), updateTopupErr: errors.New("db down")}
		b := &Bot{log: discardLogger()}
		rec := httptest.NewRecorder()
		req := adminBillingRequest(http.MethodPost, "/settings/org/billing/topup-settings", "auto_topup_enabled=1")
		b.billingTopupSettings(rec, req, svc)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}
