package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/auth"
)

// TestSwitchOrgHandler_POSTSwitchErrorReusesFetchedOrgs proves the POST error
// path renders the picker from the slice already fetched for the IDOR check
// rather than calling listUserOrgs a second time. listUserOrgsFn is wired to
// fail on any call after the first, so a second fetch would surface here.
func TestSwitchOrgHandler_POSTSwitchErrorReusesFetchedOrgs(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	calls := 0
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		calls++
		if calls > 1 {
			t.Errorf("listUserOrgs called %d times; error path must reuse the IDOR-check slice", calls)
		}
		return []auth.UserOrg{
			{OrgID: "org_test", Name: "Acme", Current: true},
			{OrgID: "org_other", Name: "Beta"},
		}, nil
	}
	b.switchOrgFn = func(http.ResponseWriter, *http.Request, string) error {
		return errors.New("refresh token rejected")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered picker)", rec.Code)
	}
	if calls != 1 {
		t.Fatalf("listUserOrgs called %d times, want 1", calls)
	}
	if !strings.Contains(rec.Body.String(), "Could not switch organization. Please try again.") {
		t.Fatalf("expected switch error in body, got %s", rec.Body.String())
	}
}

// TestSwitchOrgHandler_POSTSwitchErrorShowsErrorDespiteMembershipDrop guards the
// correctness gap the medium review comment flagged: if the user's membership
// count drops to <= 1 between the IDOR check and the re-render, re-fetching
// would silently redirect to "/" and swallow the switch error. Reusing the
// already-fetched slice keeps the error visible. The seam returns 2 orgs first
// then 1 to simulate the concurrent change; the handler must still render 200
// with the error rather than 302 -> /.
func TestSwitchOrgHandler_POSTSwitchErrorShowsErrorDespiteMembershipDrop(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	calls := 0
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		calls++
		if calls == 1 {
			return []auth.UserOrg{
				{OrgID: "org_test", Name: "Acme", Current: true},
				{OrgID: "org_other", Name: "Beta"},
			}, nil
		}
		// Simulate the user being removed from the other org mid-flow.
		return []auth.UserOrg{{OrgID: "org_test", Name: "Acme", Current: true}}, nil
	}
	b.switchOrgFn = func(http.ResponseWriter, *http.Request, string) error {
		return errors.New("refresh token rejected")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error must render, not silent redirect)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Could not switch organization. Please try again.") {
		t.Fatalf("expected switch error in body, got %s", rec.Body.String())
	}
}
