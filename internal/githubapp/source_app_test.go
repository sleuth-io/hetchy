package githubapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

// TestSourceAppPath exercises the positive-installation-id branch of every
// Source method by wiring a Source to an App whose GitHub calls are stubbed.
func TestSourceAppPath(t *testing.T) {
	srv := httptest.NewServer(tokenMintHandler("ghs_src"))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)
	src := &Source{App: app}

	tok, _, err := src.InstallationToken(context.Background(), 100, nil)
	if err != nil || tok != "ghs_src" {
		t.Fatalf("InstallationToken = %q, %v", tok, err)
	}

	tok, _, err = src.InstallationTokenMinTTL(context.Background(), 100, nil, 30*time.Second)
	if err != nil || tok != "ghs_src" {
		t.Fatalf("InstallationTokenMinTTL = %q, %v", tok, err)
	}

	cli, err := src.ClientForInstallation(context.Background(), 100)
	if err != nil || cli == nil {
		t.Fatalf("ClientForInstallation = %v, %v", cli, err)
	}

	// App-backed installations are cached, so Invalidate must reach the App.
	src.InvalidateInstallation(100)
	if _, ok := app.tokens.entries[100]; ok {
		t.Error("InvalidateInstallation did not drop the App's cached token")
	}
}

func TestSourceInstallationTokenMinTTL_NoApp(t *testing.T) {
	src := &Source{LookupPAT: func(context.Context, int64) (string, error) { return "x", nil }}
	if _, _, err := src.InstallationTokenMinTTL(context.Background(), 5, nil, time.Minute); err == nil {
		t.Error("positive id without an App must error")
	}
}

func TestSyncPAT_EmptyToken(t *testing.T) {
	src := &Source{}
	if _, err := src.SyncPAT(context.Background(), nonNilStorePlaceholder(), "org", ""); err == nil {
		t.Error("empty token must error")
	}
}

func TestSyncPAT_Unauthorized(t *testing.T) {
	// Token verification (Users.Get) returns 401, mapping to ErrPATUnauthorized
	// before any DB transaction.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	src := &Source{HTTP: &http.Client{Transport: rewriteTransport{base: mustParse(t, srv.URL)}}}

	if _, err := src.SyncPAT(context.Background(), nonNilStorePlaceholder(), "org", "ghp_bad"); !errors.Is(err, ErrPATUnauthorized) {
		t.Errorf("err = %v, want ErrPATUnauthorized", err)
	}
}

func TestSyncPAT_VerifyOtherError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	src := &Source{HTTP: &http.Client{Transport: rewriteTransport{base: mustParse(t, srv.URL)}}}

	if _, err := src.SyncPAT(context.Background(), nonNilStorePlaceholder(), "org", "ghp_x"); err == nil {
		t.Fatal("expected verify error")
	} else if !strings.Contains(err.Error(), "verify token") {
		t.Errorf("err = %v, want verify-token wrap", err)
	}
}

func TestSyncPAT_ListReposError(t *testing.T) {
	// Verification succeeds, repo listing fails — still before WithTx.
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octocat","id":1,"type":"User"}`))
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	src := &Source{HTTP: &http.Client{Transport: rewriteTransport{base: mustParse(t, srv.URL)}}}

	if _, err := src.SyncPAT(context.Background(), nonNilStorePlaceholder(), "org", "ghp_x"); err == nil {
		t.Fatal("expected list-repos error")
	} else if !strings.Contains(err.Error(), "list repos") {
		t.Errorf("err = %v, want list-repos wrap", err)
	}
}
