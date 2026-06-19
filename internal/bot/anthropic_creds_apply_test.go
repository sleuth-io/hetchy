package bot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestApplyAnthropicCredsChange(t *testing.T) {
	cases := []struct {
		name      string
		before    orgcfg.Config
		form      url.Values
		wantAPI   string
		wantOAuth string
		wantKind  anthropicCredKind
		wantValue string
	}{
		{
			name:      "fresh api key",
			form:      url.Values{"anthropic_api_key": {"sk-ant-new"}},
			wantAPI:   "sk-ant-new",
			wantKind:  anthropicCredAPIKey,
			wantValue: "sk-ant-new",
		},
		{
			name:      "fresh oauth token clears existing api key",
			before:    orgcfg.Config{AnthropicAPIKey: "sk-ant-old"},
			form:      url.Values{"claude_code_oauth_token": {"sk-ant-oat01-new"}},
			wantOAuth: "sk-ant-oat01-new",
			wantKind:  anthropicCredOAuthToken,
			wantValue: "sk-ant-oat01-new",
		},
		{
			name:    "no change does not request validation",
			before:  orgcfg.Config{AnthropicAPIKey: "sk-ant-existing"},
			form:    url.Values{},
			wantAPI: "sk-ant-existing",
		},
		{
			name:    "repasting same value does not request validation",
			before:  orgcfg.Config{AnthropicAPIKey: "sk-ant-same"},
			form:    url.Values{"anthropic_api_key": {"sk-ant-same"}},
			wantAPI: "sk-ant-same",
		},
		{
			name:   "remove clears without requesting validation",
			before: orgcfg.Config{AnthropicAPIKey: "sk-ant-existing"},
			form:   url.Values{"anthropic_api_key_action": {"remove"}},
		},
		{
			name:      "both new in same submit prefers oauth",
			form:      url.Values{"anthropic_api_key": {"sk-ant-new"}, "claude_code_oauth_token": {"sk-ant-oat01-new"}},
			wantOAuth: "sk-ant-oat01-new",
			wantKind:  anthropicCredOAuthToken,
			wantValue: "sk-ant-oat01-new",
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
			gotKind, gotValue := applyAnthropicCredsChange(req, &current)
			if current.AnthropicAPIKey != tc.wantAPI || current.ClaudeCodeOAuthToken != tc.wantOAuth {
				t.Fatalf("creds = api:%q oauth:%q, want api:%q oauth:%q",
					current.AnthropicAPIKey, current.ClaudeCodeOAuthToken, tc.wantAPI, tc.wantOAuth)
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
