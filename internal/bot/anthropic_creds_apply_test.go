package bot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// TestApplyAnthropicCredsChange pins the mutual-exclusivity contract
// *and* the new return signal that tells the handler which credential
// to validate. Without the return value, the handler can't tell
// whether the user is rotating an API key, pasting an OAuth token, or
// just re-saving an unchanged form — so it can't decide what (if
// anything) to ping Anthropic with.
func TestApplyAnthropicCredsChange(t *testing.T) {
	cases := []struct {
		name      string
		before    orgcfg.Config
		form      url.Values
		wantKey   string
		wantOAuth string
		wantKind  anthropicCredKind
		wantValue string
	}{
		{
			name:      "fresh api key",
			before:    orgcfg.Config{},
			form:      url.Values{"anthropic_api_key": {"sk-ant-new"}},
			wantKey:   "sk-ant-new",
			wantOAuth: "",
			wantKind:  anthropicCredAPIKey,
			wantValue: "sk-ant-new",
		},
		{
			name:      "fresh oauth token clears existing api key",
			before:    orgcfg.Config{AnthropicAPIKey: "sk-ant-old"},
			form:      url.Values{"claude_code_oauth_token": {"sk-ant-oat01-new"}},
			wantKey:   "",
			wantOAuth: "sk-ant-oat01-new",
			wantKind:  anthropicCredOAuthToken,
			wantValue: "sk-ant-oat01-new",
		},
		{
			name:      "no change → empty newValue",
			before:    orgcfg.Config{AnthropicAPIKey: "sk-ant-existing"},
			form:      url.Values{},
			wantKey:   "sk-ant-existing",
			wantOAuth: "",
			wantValue: "",
		},
		{
			name:      "rotation with same value → empty newValue",
			before:    orgcfg.Config{AnthropicAPIKey: "sk-ant-same"},
			form:      url.Values{"anthropic_api_key": {"sk-ant-same"}},
			wantKey:   "sk-ant-same",
			wantOAuth: "",
			wantValue: "",
		},
		{
			name:      "remove action clears credential without flagging new",
			before:    orgcfg.Config{AnthropicAPIKey: "sk-ant-existing"},
			form:      url.Values{"anthropic_api_key_action": {"remove"}},
			wantKey:   "",
			wantOAuth: "",
			wantValue: "",
		},
		{
			name:      "both new in same submit prefers oauth",
			before:    orgcfg.Config{},
			form:      url.Values{"anthropic_api_key": {"sk-ant-new"}, "claude_code_oauth_token": {"sk-ant-oat01-new"}},
			wantKey:   "",
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
			cur := tc.before
			gotKind, gotValue := applyAnthropicCredsChange(req, &cur)
			if cur.AnthropicAPIKey != tc.wantKey {
				t.Errorf("AnthropicAPIKey = %q, want %q", cur.AnthropicAPIKey, tc.wantKey)
			}
			if cur.ClaudeCodeOAuthToken != tc.wantOAuth {
				t.Errorf("ClaudeCodeOAuthToken = %q, want %q", cur.ClaudeCodeOAuthToken, tc.wantOAuth)
			}
			if gotValue != tc.wantValue {
				t.Errorf("newValue = %q, want %q", gotValue, tc.wantValue)
			}
			if gotValue != "" && gotKind != tc.wantKind {
				t.Errorf("newKind = %v, want %v", gotKind, tc.wantKind)
			}
		})
	}
}
