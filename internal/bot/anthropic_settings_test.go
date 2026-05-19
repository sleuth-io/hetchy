package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestSettingsHandlerRejectsInvalidAnthropicCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid x-api-key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	store := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "sx-old"}}
	b := newBypassOrgBot(t, "admin")
	b.orgs = store
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler)))

	rec := httptest.NewRecorder()
	req := settingsFormRequest(http.MethodPost, "/settings/org?tab=integrations", "sx_key=sx-new&anthropic_api_key=sk-ant-bad")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q, want 302", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/org?tab=integrations&error=anthropic_api_key_invalid" {
		t.Fatalf("redirect = %q", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(store.upserts))
	}
}

func TestErrorMessage(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"unknown":                      "",
		"anthropic_api_key_invalid":    "Anthropic rejected that API key.",
		"anthropic_api_key_unverified": "Couldn't reach Anthropic to verify that API key.",
		"anthropic_oauth_invalid":      "Anthropic rejected that subscription token.",
		"anthropic_oauth_unverified":   "Couldn't reach Anthropic to verify that subscription token.",
		"openai_api_key_invalid":       "OpenAI rejected that API key.",
		"openai_api_key_unverified":    "Couldn't reach OpenAI to verify that API key.",
		"openai_oauth_invalid":         "That Codex subscription auth is not usable.",
		"openai_oauth_unverified":      "Couldn't verify that Codex subscription auth.",
	}
	for in, wantSubstr := range cases {
		got := errorMessage(in)
		if wantSubstr == "" {
			if got != "" {
				t.Fatalf("errorMessage(%q) = %q, want empty", in, got)
			}
			continue
		}
		if !strings.Contains(got, wantSubstr) {
			t.Fatalf("errorMessage(%q) = %q, want substring %q", in, got, wantSubstr)
		}
	}
}
