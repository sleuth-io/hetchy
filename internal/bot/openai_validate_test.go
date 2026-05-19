package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateOpenAICredential(t *testing.T) {
	t.Run("api key accepted", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
				t.Fatalf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
			}
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		defer srv.Close()
		withOpenAIBase(t, srv.URL)

		if err := validateOpenAICredential(context.Background(), openaiCredAPIKey, " sk-test "); err != nil {
			t.Fatalf("validateOpenAICredential: %v", err)
		}
		if gotAuth != "Bearer sk-test" {
			t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
		}
	})

	t.Run("oauth accepted without platform request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("validator called OpenAI Platform for Codex token")
		}))
		defer srv.Close()
		withOpenAIBase(t, srv.URL)

		token := testJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
		if err := validateOpenAICredential(context.Background(), openaiCredOAuthToken, token); err != nil {
			t.Fatalf("validateOpenAICredential: %v", err)
		}
	})

	t.Run("401 is invalid credential", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad key", http.StatusUnauthorized)
		}))
		defer srv.Close()
		withOpenAIBase(t, srv.URL)

		err := validateOpenAICredential(context.Background(), openaiCredAPIKey, "sk-bad")
		if !errors.Is(err, errOpenAIInvalidCredential) {
			t.Fatalf("error = %v, want errOpenAIInvalidCredential", err)
		}
	})

	t.Run("403 is invalid credential", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}))
		defer srv.Close()
		withOpenAIBase(t, srv.URL)

		err := validateOpenAICredential(context.Background(), openaiCredAPIKey, "sk-forbidden")
		if !errors.Is(err, errOpenAIInvalidCredential) {
			t.Fatalf("error = %v, want errOpenAIInvalidCredential", err)
		}
	})

	t.Run("oauth rejects malformed token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("validator called OpenAI Platform for malformed Codex token")
		}))
		defer srv.Close()
		withOpenAIBase(t, srv.URL)

		err := validateOpenAICredential(context.Background(), openaiCredOAuthToken, "not-a-jwt")
		if !errors.Is(err, errOpenAIInvalidCredential) {
			t.Fatalf("error = %v, want errOpenAIInvalidCredential", err)
		}
	})

	t.Run("oauth rejects expired token", func(t *testing.T) {
		token := testJWT(t, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})
		err := validateOpenAICredential(context.Background(), openaiCredOAuthToken, token)
		if !errors.Is(err, errOpenAIInvalidCredential) {
			t.Fatalf("error = %v, want errOpenAIInvalidCredential", err)
		}
	})

	t.Run("500 is transient", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "upstream down", http.StatusInternalServerError)
		}))
		defer srv.Close()
		withOpenAIBase(t, srv.URL)

		err := validateOpenAICredential(context.Background(), openaiCredAPIKey, "sk-test")
		if err == nil {
			t.Fatal("error = nil, want transient error")
		}
		if errors.Is(err, errOpenAIInvalidCredential) {
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
		withOpenAIBase(t, srv.URL)

		if err := validateOpenAICredential(context.Background(), openaiCredAPIKey, "   "); err == nil {
			t.Fatal("error = nil, want empty credential error")
		}
	})
}

func withOpenAIBase(t *testing.T, url string) {
	t.Helper()
	prev := openaiAPIBaseRef.Load()
	override := url
	openaiAPIBaseRef.Store(&override)
	t.Cleanup(func() { openaiAPIBaseRef.Store(prev) })
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt part: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return encode(map[string]string{"alg": "none", "typ": "JWT"}) + "." + encode(claims) + ".sig"
}
