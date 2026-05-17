package bot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestApplyOpenAICredsChange(t *testing.T) {
	cases := []struct {
		name      string
		before    orgcfg.Config
		form      url.Values
		wantAPI   string
		wantOAuth string
		wantKind  openaiCredKind
		wantValue string
	}{
		{
			name:      "fresh api key",
			form:      url.Values{"openai_api_key": {"sk-new"}},
			wantAPI:   "sk-new",
			wantKind:  openaiCredAPIKey,
			wantValue: "sk-new",
		},
		{
			name:      "fresh oauth token clears existing api key",
			before:    orgcfg.Config{OpenAIAPIKey: "sk-old"},
			form:      url.Values{"openai_codex_oauth_token": {"ey-new"}},
			wantOAuth: "ey-new",
			wantKind:  openaiCredOAuthToken,
			wantValue: "ey-new",
		},
		{
			name:    "no change does not request validation",
			before:  orgcfg.Config{OpenAIAPIKey: "sk-existing"},
			form:    url.Values{},
			wantAPI: "sk-existing",
		},
		{
			name:    "repasting same value does not request validation",
			before:  orgcfg.Config{OpenAIAPIKey: "sk-same"},
			form:    url.Values{"openai_api_key": {"sk-same"}},
			wantAPI: "sk-same",
		},
		{
			name:   "remove clears without requesting validation",
			before: orgcfg.Config{OpenAIAPIKey: "sk-existing"},
			form:   url.Values{"openai_api_key_action": {"remove"}},
		},
		{
			name:      "both new in same submit prefers oauth",
			form:      url.Values{"openai_api_key": {"sk-new"}, "openai_codex_oauth_token": {"ey-new"}},
			wantOAuth: "ey-new",
			wantKind:  openaiCredOAuthToken,
			wantValue: "ey-new",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/settings/org", strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := req.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			current := tc.before
			gotKind, gotValue := applyOpenAICredsChange(req, &current)
			if current.OpenAIAPIKey != tc.wantAPI || current.OpenAICodexOAuthToken != tc.wantOAuth {
				t.Fatalf("creds = api:%q oauth:%q, want api:%q oauth:%q",
					current.OpenAIAPIKey, current.OpenAICodexOAuthToken, tc.wantAPI, tc.wantOAuth)
			}
			if gotValue != tc.wantValue {
				t.Fatalf("new value = %q, want %q", gotValue, tc.wantValue)
			}
			if gotValue != "" && gotKind != tc.wantKind {
				t.Fatalf("new kind = %d, want %d", gotKind, tc.wantKind)
			}
		})
	}
}
