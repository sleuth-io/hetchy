package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateAnthropicCredential(t *testing.T) {
	t.Run("api key accepted", func(t *testing.T) {
		var gotKey, gotVersion, gotBeta, gotModel string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/count_tokens" {
				t.Fatalf("request = %s %s, want POST /v1/messages/count_tokens", r.Method, r.URL.Path)
			}
			gotKey = r.Header.Get("x-api-key")
			gotVersion = r.Header.Get("anthropic-version")
			gotBeta = r.Header.Get("anthropic-beta")
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			gotModel = body.Model
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"input_tokens":3}`))
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		if err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, " sk-ant-test "); err != nil {
			t.Fatalf("validateAnthropicCredential: %v", err)
		}
		if gotKey != "sk-ant-test" {
			t.Fatalf("x-api-key = %q, want sk-ant-test", gotKey)
		}
		if gotVersion != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", gotVersion)
		}
		if gotBeta != "" {
			t.Fatalf("anthropic-beta = %q, want empty", gotBeta)
		}
		if gotModel != anthropicValidateModel {
			t.Fatalf("model = %q, want %q", gotModel, anthropicValidateModel)
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
			t.Fatalf("validateAnthropicCredential: %v", err)
		}
		if gotAuth != "Bearer sk-ant-oat01-abc" {
			t.Fatalf("Authorization = %q, want Bearer sk-ant-oat01-abc", gotAuth)
		}
		if gotBeta != "oauth-2025-04-20" {
			t.Fatalf("anthropic-beta = %q, want oauth-2025-04-20", gotBeta)
		}
	})

	t.Run("401 is invalid credential", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad key", http.StatusUnauthorized)
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "sk-ant-bad")
		if !errors.Is(err, errAnthropicInvalidCredential) {
			t.Fatalf("error = %v, want errAnthropicInvalidCredential", err)
		}
	})

	t.Run("403 is invalid credential", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		err := validateAnthropicCredential(context.Background(), anthropicCredOAuthToken, "sk-ant-oat01-bad")
		if !errors.Is(err, errAnthropicInvalidCredential) {
			t.Fatalf("error = %v, want errAnthropicInvalidCredential", err)
		}
	})

	t.Run("500 is transient", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "upstream down", http.StatusInternalServerError)
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "sk-ant-test")
		if err == nil {
			t.Fatal("error = nil, want transient error")
		}
		if errors.Is(err, errAnthropicInvalidCredential) {
			t.Fatalf("5xx mapped to invalid credential: %v", err)
		}
		if !strings.Contains(err.Error(), "500") {
			t.Fatalf("error = %v, want status code", err)
		}
	})

	t.Run("empty credential skips http", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("validator called server for empty credential")
		}))
		defer srv.Close()
		withAnthropicBase(t, srv.URL)

		if err := validateAnthropicCredential(context.Background(), anthropicCredAPIKey, "   "); err == nil {
			t.Fatal("error = nil, want empty credential error")
		}
	})
}

func withAnthropicBase(t *testing.T, url string) {
	t.Helper()
	prev := anthropicAPIBase
	anthropicAPIBase = url
	t.Cleanup(func() { anthropicAPIBase = prev })
}
