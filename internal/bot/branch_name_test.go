package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestSanitizeBranchSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean kebab", "add-dark-mode-toggle", "add-dark-mode-toggle"},
		{"trim whitespace", "  add-dark-mode  ", "add-dark-mode"},
		{"strips backticks", "`fix-login`", "fix-login"},
		{"strips quotes", `"upgrade-node-20"`, "upgrade-node-20"},
		{"strips feature prefix", "feature/fix-button", "fix-button"},
		{"strips branch name prefix", "Branch name: fix-typo", "fix-typo"},
		{"lowercases", "Fix-Login-Redirect", "fix-login-redirect"},
		{"converts spaces to hyphens", "fix login redirect", "fix-login-redirect"},
		{"converts underscores", "fix_login_redirect", "fix-login-redirect"},
		{"drops punctuation", "fix login, redirect!", "fix-login-redirect"},
		{"collapses runs of hyphens", "fix---login", "fix-login"},
		{"trims leading hyphens", "---fix-login", "fix-login"},
		{"trims trailing hyphens", "fix-login---", "fix-login"},
		{"drops leading digit", "42-fix-login", "fix-login"},
		{"drops leading digits and hyphens", "42-fix", "fix"},
		{"strips leading group of digit-then-hyphen-then-digit", "2025-rewrite", "rewrite"},
		{"strips longer digit-hyphen prefix", "1-2-fix-login", "fix-login"},
		{"truncates over max len", strings.Repeat("a", branchSlugMaxLen+10), strings.Repeat("a", branchSlugMaxLen)},
		{"truncate then trim trailing hyphen", strings.Repeat("a", branchSlugMaxLen-1) + "-extra", strings.Repeat("a", branchSlugMaxLen-1)},
		{"empty input", "", ""},
		{"whitespace only", "   ", ""},
		{"non-ascii dropped", "café-déjà", "caf-dj"},
		{"slashes dropped", "fix/login/bug", "fixloginbug"},
		{"colons dropped", "fix:login:bug", "fixloginbug"},
		{"single digit only -> empty", "9", ""},
		{"only punctuation -> empty", "!!!---???", ""},
		{"newlines as hyphens", "fix\nlogin\nbug", "fix-login-bug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeBranchSlug(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeBranchSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRandomBranchSuffix_FormatAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{6}$`)
	seen := map[string]struct{}{}
	for i := range 64 {
		s := randomBranchSuffix()
		if !re.MatchString(s) {
			t.Fatalf("suffix %q is not 6 lowercase hex chars", s)
		}
		if _, dup := seen[s]; dup {
			// Collisions in 24 bits across 64 samples are vanishingly
			// improbable. A repeat means we forgot to seed CSPRNG.
			t.Fatalf("duplicate suffix %q at iteration %d", s, i)
		}
		seen[s] = struct{}{}
	}
}

func TestResolveAnthropicCred(t *testing.T) {
	cases := []struct {
		name      string
		oc        orgcfg.Config
		wantKind  anthropicCredKind
		wantValue string
		wantOK    bool
	}{
		{
			name:      "oauth wins over api key",
			oc:        orgcfg.Config{AnthropicAPIKey: "sk-ant-key", ClaudeCodeOAuthToken: "sk-ant-oat01-abc"},
			wantKind:  anthropicCredOAuthToken,
			wantValue: "sk-ant-oat01-abc",
			wantOK:    true,
		},
		{
			name:      "api key only",
			oc:        orgcfg.Config{AnthropicAPIKey: "sk-ant-key"},
			wantKind:  anthropicCredAPIKey,
			wantValue: "sk-ant-key",
			wantOK:    true,
		},
		{
			name:      "trims whitespace",
			oc:        orgcfg.Config{AnthropicAPIKey: "  sk-ant-key  "},
			wantKind:  anthropicCredAPIKey,
			wantValue: "sk-ant-key",
			wantOK:    true,
		},
		{
			name:   "no credentials",
			oc:     orgcfg.Config{},
			wantOK: false,
		},
		{
			name:   "whitespace credentials are empty",
			oc:     orgcfg.Config{AnthropicAPIKey: "   ", ClaudeCodeOAuthToken: "\t"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotK, gotV, gotOK := resolveAnthropicCred(tc.oc)
			if gotOK != tc.wantOK || gotV != tc.wantValue || (tc.wantOK && gotK != tc.wantKind) {
				t.Fatalf("resolveAnthropicCred = (%d,%q,%v), want (%d,%q,%v)",
					gotK, gotV, gotOK, tc.wantKind, tc.wantValue, tc.wantOK)
			}
		})
	}
}

func TestApplyAnthropicAuth(t *testing.T) {
	t.Run("api key sets x-api-key", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://example/", nil)
		if !applyAnthropicAuth(req, anthropicCredAPIKey, "sk-ant-key") {
			t.Fatal("applyAnthropicAuth returned false for valid api-key kind")
		}
		if got := req.Header.Get("x-api-key"); got != "sk-ant-key" {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := req.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization should be empty for api-key path, got %q", got)
		}
		if got := req.Header.Get("anthropic-beta"); got != "" {
			t.Fatalf("anthropic-beta should be empty for api-key path, got %q", got)
		}
	})

	t.Run("oauth sets bearer and beta header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://example/", nil)
		if !applyAnthropicAuth(req, anthropicCredOAuthToken, "sk-ant-oat01-abc") {
			t.Fatal("applyAnthropicAuth returned false for valid oauth kind")
		}
		if got := req.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-abc" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Fatalf("anthropic-beta = %q", got)
		}
		if got := req.Header.Get("x-api-key"); got != "" {
			t.Fatalf("x-api-key should be empty for oauth path, got %q", got)
		}
	})

	t.Run("unknown kind returns false and sets nothing", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://example/", nil)
		if applyAnthropicAuth(req, anthropicCredKind(99), "value") {
			t.Fatal("applyAnthropicAuth returned true for unknown kind")
		}
		for _, h := range []string{"x-api-key", "Authorization", "anthropic-beta"} {
			if got := req.Header.Get(h); got != "" {
				t.Fatalf("header %s should be empty for unknown kind, got %q", h, got)
			}
		}
	})
}

// TestGenerateBranchSlug_HappyPath stubs the Anthropic API to return a
// suggested slug and verifies the generator sanitises it correctly.
func TestGenerateBranchSlug_HappyPath(t *testing.T) {
	var gotModel, gotKey, gotUserMsg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Fatalf("request = %s %s, want POST /v1/messages", r.Method, r.URL.Path)
		}
		gotKey = r.Header.Get("x-api-key")
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotModel = body.Model
		if len(body.Messages) > 0 {
			gotUserMsg = body.Messages[0].Content
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"add-dark-mode-toggle"}]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant-key"}, "Add a dark mode toggle to the navbar")
	if got != "add-dark-mode-toggle" {
		t.Fatalf("generateBranchSlug = %q, want add-dark-mode-toggle", got)
	}
	if gotKey != "sk-ant-key" {
		t.Fatalf("x-api-key = %q", gotKey)
	}
	if gotModel != branchNameModel {
		t.Fatalf("model = %q, want %q", gotModel, branchNameModel)
	}
	if gotUserMsg != "Add a dark mode toggle to the navbar" {
		t.Fatalf("user message = %q", gotUserMsg)
	}
}

func TestGenerateBranchSlug_FallsBackOnEmptyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("LLM should not be called for an empty request")
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "   ")
	if got != branchSlugFallback {
		t.Fatalf("generateBranchSlug for empty request = %q, want %q", got, branchSlugFallback)
	}
}

func TestGenerateBranchSlug_FallsBackOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "Ship the new login flow")
	if got != branchSlugFallback {
		t.Fatalf("generateBranchSlug on 500 = %q, want %q", got, branchSlugFallback)
	}
}

func TestGenerateBranchSlug_FallsBackOnMissingCreds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("LLM should not be called without credentials")
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{}, "Ship the new login flow")
	if got != branchSlugFallback {
		t.Fatalf("generateBranchSlug without creds = %q, want %q", got, branchSlugFallback)
	}
}

func TestGenerateBranchSlug_FallsBackOnUnsanitisableOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"!!! ??? ###"}]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "Refactor router")
	if got != branchSlugFallback {
		t.Fatalf("generateBranchSlug for junk output = %q, want %q", got, branchSlugFallback)
	}
}

func TestBranchNameFor_ShapeAndSuffix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"my-useful-branchname"}]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	branch := b.branchNameFor(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "make a useful branch")

	wantPrefix := "feature/my-useful-branchname-"
	if !strings.HasPrefix(branch, wantPrefix) {
		t.Fatalf("branchNameFor = %q, want prefix %q", branch, wantPrefix)
	}
	suffix := strings.TrimPrefix(branch, wantPrefix)
	if !regexp.MustCompile(`^[0-9a-f]{6}$`).MatchString(suffix) {
		t.Fatalf("suffix %q is not 6 lowercase hex chars", suffix)
	}
}

func TestBranchNameFor_FallsBackToSF(t *testing.T) {
	// No API key, no test server — branchNameFor must short-circuit
	// before hitting the network.
	b := &Bot{log: discardLogger()}
	branch := b.branchNameFor(context.Background(), orgcfg.Config{}, "ship anything")

	wantPrefix := "feature/sf-"
	if !strings.HasPrefix(branch, wantPrefix) {
		t.Fatalf("branchNameFor fallback = %q, want prefix %q", branch, wantPrefix)
	}
	suffix := strings.TrimPrefix(branch, wantPrefix)
	if !regexp.MustCompile(`^[0-9a-f]{6}$`).MatchString(suffix) {
		t.Fatalf("fallback suffix %q is not 6 lowercase hex chars", suffix)
	}
}

func TestBranchNameFor_RespectsTestSeam(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		branchNameFn: func(_ context.Context, _ orgcfg.Config, userRequest string) string {
			if userRequest != "make a thing" {
				t.Fatalf("seam got userRequest = %q", userRequest)
			}
			return "feature/predetermined-aaaaaa"
		},
	}
	got := b.branchNameFor(context.Background(), orgcfg.Config{}, "make a thing")
	if got != "feature/predetermined-aaaaaa" {
		t.Fatalf("branchNameFor = %q, want feature/predetermined-aaaaaa", got)
	}
}

// TestGenerateBranchSlug_OAuthHeader verifies the OAuth code path
// sets the Bearer authorization header and the oauth beta flag, since
// claudeAuthEnv-style precedence is duplicated in anthropicAuthHeader.
func TestGenerateBranchSlug_OAuthHeader(t *testing.T) {
	var gotAuth, gotBeta, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotAPIKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"fix-login-redirect"}]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(),
		orgcfg.Config{ClaudeCodeOAuthToken: "sk-ant-oat01-abc"},
		"Fix the login redirect")
	if got != "fix-login-redirect" {
		t.Fatalf("generateBranchSlug = %q, want fix-login-redirect", got)
	}
	if gotAuth != "Bearer sk-ant-oat01-abc" {
		t.Fatalf("Authorization = %q, want Bearer sk-ant-oat01-abc", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q, want oauth-2025-04-20", gotBeta)
	}
	if gotAPIKey != "" {
		t.Fatalf("x-api-key = %q, want empty when using OAuth", gotAPIKey)
	}
}

// TestGenerateBranchSlug_MultipleContentBlocks ensures the parser
// concatenates several text blocks rather than picking only the first.
func TestGenerateBranchSlug_MultipleContentBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[
			{"type":"text","text":"add-"},
			{"type":"text","text":"dark-mode-"},
			{"type":"text","text":"toggle"}
		]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "Add dark mode toggle")
	if got != "add-dark-mode-toggle" {
		t.Fatalf("generateBranchSlug = %q, want add-dark-mode-toggle", got)
	}
}

// TestGenerateBranchSlug_NonTextContentIgnored ensures content blocks
// whose type isn't "text" (e.g. tool_use, thinking) are skipped.
func TestGenerateBranchSlug_NonTextContentIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[
			{"type":"thinking","text":"ignore-this"},
			{"type":"text","text":"clean-slug"}
		]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "anything")
	if got != "clean-slug" {
		t.Fatalf("generateBranchSlug = %q, want clean-slug", got)
	}
}

// TestGenerateBranchSlug_MalformedJSONFallsBack covers a 200 OK with
// an unparseable response body (e.g. partial proxy response, HTML
// error page returned at 200). We must fall back, not crash.
func TestGenerateBranchSlug_MalformedJSONFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "Ship it")
	if got != branchSlugFallback {
		t.Fatalf("generateBranchSlug on malformed JSON = %q, want %q", got, branchSlugFallback)
	}
}

// TestGenerateBranchSlug_ContextCancelFallsBack ensures an already-
// cancelled parent context short-circuits to the fallback instead of
// dialing the network. This matters because branchNameFor is called
// from the chat handler which can be cancelled by /chat/cancel.
func TestGenerateBranchSlug_ContextCancelFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("LLM should not be called with a cancelled context")
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(ctx, orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "Ship it")
	if got != branchSlugFallback {
		t.Fatalf("generateBranchSlug under cancelled ctx = %q, want %q", got, branchSlugFallback)
	}
}

// TestGenerateBranchSlug_ChattyPrefaceTrimmedOrFallback verifies that
// when the model adds prose around the slug (against instructions),
// the sanitiser either trims it (if the model added a recognised
// preface like "Branch name:") or, failing that, produces a still-
// safe — if uglier — slug that nonetheless passes git ref-name rules.
func TestGenerateBranchSlug_ChattyPrefaceTrimmedOrFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Branch name: fix-typo"}]}`))
	}))
	defer srv.Close()
	withAnthropicBase(t, srv.URL)

	b := &Bot{log: discardLogger()}
	got := b.generateBranchSlug(context.Background(), orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "Fix the typo")
	if got != "fix-typo" {
		t.Fatalf("generateBranchSlug stripped 'Branch name:' = %q, want fix-typo", got)
	}
}
