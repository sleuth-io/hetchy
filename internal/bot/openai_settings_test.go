package bot

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestSettingsHandlerRejectsInvalidOpenAICredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid api key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	withOpenAIBase(t, srv.URL)

	store := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}
	b := newBypassOrgBot(t, "admin")
	b.orgs = store
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org?tab=integrations", "openai_api_key=sk-bad")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q, want 302", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=integrations&error=openai_api_key_invalid" {
		t.Fatalf("redirect = %q", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(store.upserts))
	}
}
