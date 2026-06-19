package bot

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestSXVaultSettingsHandlerExistingAndDelete(t *testing.T) {
	sx := &fakeSXManager{}
	b := newBypassOrgBot(t, "admin")
	b.sx = sx
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.sxVaultSettingsHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/sx-vault", "mode=existing&git_vault_repo=acme/vault")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("existing status = %d body=%q", rec.Code, rec.Body.String())
	}
	if sx.configuredRepo != "acme/vault" {
		t.Fatalf("configured repo = %q", sx.configuredRepo)
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=integrations&saved=sx_git_vault_saved" {
		t.Fatalf("existing redirect = %q", got)
	}

	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/sx-vault/delete", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("delete status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !sx.deletedGitVault {
		t.Fatal("DeleteGitVault was not called")
	}
}

func TestSXVaultSettingsHandlerClearsSkillsNewToken(t *testing.T) {
	sx := &fakeSXManager{}
	orgs := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "sx-old"}}
	b := newBypassOrgBot(t, "admin")
	b.sx = sx
	b.orgs = orgs
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.sxVaultSettingsHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org/sx-vault", "mode=existing&git_vault_repo=acme/vault")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if sx.configuredRepo != "acme/vault" {
		t.Fatalf("configured repo = %q", sx.configuredRepo)
	}
	if len(orgs.upserts) != 1 {
		t.Fatalf("org upserts = %d, want 1", len(orgs.upserts))
	}
	if orgs.upserts[0].SXKey != "" {
		t.Fatalf("SXKey after git vault save = %q, want cleared", orgs.upserts[0].SXKey)
	}
}

func TestSXVaultSettingsHandlerValidation(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.sx = &fakeSXManager{}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.sxVaultSettingsHandler)))

	cases := []struct {
		name string
		form string
		want int
	}{
		{name: "missing existing repo", form: "mode=existing", want: http.StatusBadRequest},
		{name: "create mode disabled", form: "mode=create&installation_id=42&new_repo_name=team-vault", want: http.StatusBadRequest},
		{name: "unknown mode", form: "mode=bogus", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := settingsFormRequest(http.MethodPost, "/settings/org/sx-vault", tc.form)
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/sx-vault", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", rec.Code)
	}

	member := newBypassOrgBot(t, "member")
	member.sx = &fakeSXManager{}
	memberHandler := member.auth.Middleware(member.auth.RequireOrg(http.HandlerFunc(member.sxVaultSettingsHandler)))
	rec = httptest.NewRecorder()
	req = settingsFormRequest(http.MethodPost, "/settings/org/sx-vault", "mode=existing&git_vault_repo=acme/vault")
	memberHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member status = %d", rec.Code)
	}
}
