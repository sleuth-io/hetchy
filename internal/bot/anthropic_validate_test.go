package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestValidateAnthropicCredential covers the four outcomes the
// settings handler branches on: accepted, definitively rejected,
// transient API error, and a malformed call (empty value). Each case
// stubs api.anthropic.com with an httptest.Server so we never touch
// the network.
func TestValidateAnthropicCredential(t *testing.T) {
	t.Run("api key accepted", func(t *testing.T) {
		var gotAuth, gotVer, gotBeta string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/count_tokens" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			gotAuth = r.Header.Get("x-api-key")
			gotVer = r.Header.Get("anthropic-version")
			gotBeta = r.Header.Get("anthropic-beta")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"input_tokens":3}`))
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		if err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "sk-ant-test"); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if gotAuth != "sk-ant-test" {
			t.Errorf("x-api-key header = %q, want %q", gotAuth, "sk-ant-test")
		}
		if gotVer != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", gotVer, "2023-06-01")
		}
		if gotBeta != "" {
			t.Errorf("anthropic-beta should be unset for API key, got %q", gotBeta)
		}
	})

	t.Run("oauth accepted", func(t *testing.T) {
		var gotAuth, gotBeta string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotBeta = r.Header.Get("anthropic-beta")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		if err := validateAnthropicCredential(context.Background(), anthropicCredOAuthToken, "sk-ant-oat01-abc"); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if gotAuth != "Bearer sk-ant-oat01-abc" {
			t.Errorf("Authorization = %q, want Bearer sk-ant-oat01-abc", gotAuth)
		}
		if gotBeta != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta = %q, want oauth-2025-04-20", gotBeta)
		}
	})

	t.Run("401 → invalid credential sentinel", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "sk-ant-bad")
		if !errors.Is(err, errAnthropicInvalidCredential) {
			t.Fatalf("expected errAnthropicInvalidCredential, got %v", err)
		}
	})

	t.Run("403 → invalid credential sentinel", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		err := validateAnthropicCredential(context.Background(), anthropicCredOAuthToken, "sk-ant-oat01-bad")
		if !errors.Is(err, errAnthropicInvalidCredential) {
			t.Fatalf("expected errAnthropicInvalidCredential, got %v", err)
		}
	})

	t.Run("500 → transient error (not the sentinel)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`upstream down`))
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "sk-ant-test")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, errAnthropicInvalidCredential) {
			t.Fatalf("5xx must not map to invalid-credential sentinel; got %v", err)
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("expected status code in error, got %v", err)
		}
	})

	t.Run("empty credential rejected without HTTP call", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("validator should not call Anthropic for an empty credential")
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		if err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "   "); err == nil {
			t.Fatal("expected error for empty credential, got nil")
		}
	})
}

// withAnthropicBase swaps the package var for a test's lifetime and
// restores it on cleanup. Tests must use this rather than mutating the
// var directly so a panic in the test body doesn't leak the override
// into other tests.
func withAnthropicBase(t *testing.T, url string) {
	t.Helper()
	prev := anthropicAPIBase
	anthropicAPIBase = url
	t.Cleanup(func() { anthropicAPIBase = prev })
}
