package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// nonNilStorePlaceholder returns a *db.Store that passes SyncInstallation's
// nil check but must never reach WithTx (its pool is nil and would panic).
// Use only in tests whose GitHub stub fails before the DB transaction.
func nonNilStorePlaceholder() *db.Store {
	return &db.Store{Queries: sqlc.New(nil)}
}

// newAppWithGitHubStub returns an App whose http client (used to mint
// installation tokens and to back AppClient/ClientForInstallation)
// transparently redirects every api.github.com request to srv. The App
// carries a freshly-generated RSA key so appJWT signing works without a
// real GitHub-issued private key.
func newAppWithGitHubStub(t *testing.T, srv *httptest.Server) *App {
	t.Helper()
	app := newTestApp(t, "secret")
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse srv url: %v", err)
	}
	app.http = &http.Client{Transport: rewriteTransport{base: base}}
	return app
}

// rewriteTransport sends every request to base (the httptest server)
// regardless of the original host, so go-github clients pinned at
// api.github.com talk to our stub instead.
type rewriteTransport struct {
	base *url.URL
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTripper must not mutate the request it's given; clone first.
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.base.Scheme
	clone.URL.Host = rt.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// tokenMintHandler responds to the CreateInstallationToken endpoint with
// a token that expires an hour out, so freshly minted tokens satisfy the
// safety window.
func tokenMintHandler(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/access_tokens") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, token, exp)
	}
}

func TestInstallationToken_MintsAndCaches(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		tokenMintHandler("ghs_minted")(w, r)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	tok, exp, err := app.InstallationToken(context.Background(), 555, []int64{1})
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if tok != "ghs_minted" {
		t.Errorf("token = %q, want ghs_minted", tok)
	}
	if time.Until(exp) < 50*time.Minute {
		t.Errorf("expiry %v too soon", exp)
	}

	// Second call with the same scope should be served from cache.
	tok2, _, err := app.InstallationToken(context.Background(), 555, []int64{1})
	if err != nil {
		t.Fatalf("second InstallationToken: %v", err)
	}
	if tok2 != "ghs_minted" {
		t.Errorf("cached token = %q, want ghs_minted", tok2)
	}
	if calls != 1 {
		t.Errorf("expected exactly one mint call, got %d", calls)
	}

	// A different scope must re-mint.
	if _, _, err := app.InstallationToken(context.Background(), 555, []int64{2}); err != nil {
		t.Fatalf("scope-change InstallationToken: %v", err)
	}
	if calls != 2 {
		t.Errorf("scope change should re-mint, calls = %d", calls)
	}
}

// TestInstallationToken_NarrowerScopeReMints guards the security-critical
// path: a token cached for a BROAD repo set [1,2] must never be handed to
// a caller asking for a NARROWER set [1], which would silently grant that
// caller access to repo 2. The cache must re-mint instead.
func TestInstallationToken_NarrowerScopeReMints(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		tokenMintHandler("ghs_scoped")(w, r)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	if _, _, err := app.InstallationToken(context.Background(), 99, []int64{1, 2}); err != nil {
		t.Fatalf("broad mint: %v", err)
	}
	if _, _, err := app.InstallationToken(context.Background(), 99, []int64{1}); err != nil {
		t.Fatalf("narrow mint: %v", err)
	}
	if calls != 2 {
		t.Fatalf("narrowing [1,2]->[1] must re-mint, not reuse the broad token; got %d mints", calls)
	}
}

func TestInstallationTokenMinTTL_FloorsAtSafetyWindow(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		tokenMintHandler("ghs_ttl")(w, r)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	// A sub-window minTTL is raised to the safety window; the call mints
	// once and caches.
	tok, _, err := app.InstallationTokenMinTTL(context.Background(), 7, nil, time.Minute)
	if err != nil {
		t.Fatalf("InstallationTokenMinTTL small: %v", err)
	}
	if tok != "ghs_ttl" {
		t.Errorf("token = %q", tok)
	}
	if calls != 1 {
		t.Fatalf("first call should mint once, got %d mints", calls)
	}

	// A large minTTL (longer than the hour the stub grants) must force a
	// re-mint: the cached 1h token can never satisfy a 90m floor. Assert
	// the mint actually happened — without the call counter this test
	// would pass even if the stale token were wrongly served from cache.
	if _, _, err := app.InstallationTokenMinTTL(context.Background(), 7, nil, 90*time.Minute); err != nil {
		t.Fatalf("InstallationTokenMinTTL large: %v", err)
	}
	if calls != 2 {
		t.Fatalf("large minTTL must re-mint past the cached token, got %d mints", calls)
	}
}

func TestInstallationToken_MintError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"bad creds"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	if _, _, err := app.InstallationToken(context.Background(), 9, nil); err == nil {
		t.Fatal("expected error from a 401 mint response")
	} else if !strings.Contains(err.Error(), "create installation token") {
		t.Errorf("error = %v, want create-installation-token wrap", err)
	}
}

func TestInstallationToken_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	if _, _, err := app.InstallationToken(context.Background(), 11, nil); err == nil {
		t.Fatal("expected error for empty token response")
	} else if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("error = %v, want empty-response", err)
	}
}

func TestInvalidateInstallation_DropsCachedToken(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		tokenMintHandler("ghs_inv")(w, r)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	if _, _, err := app.InstallationToken(context.Background(), 33, nil); err != nil {
		t.Fatalf("mint: %v", err)
	}
	app.InvalidateInstallation(33)
	if _, _, err := app.InstallationToken(context.Background(), 33, nil); err != nil {
		t.Fatalf("re-mint: %v", err)
	}
	if calls != 2 {
		t.Errorf("invalidation should force a re-mint, calls = %d", calls)
	}
}

func TestClientForInstallation_MintsToken(t *testing.T) {
	srv := httptest.NewServer(tokenMintHandler("ghs_cli"))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	cli, err := app.ClientForInstallation(context.Background(), 77)
	if err != nil {
		t.Fatalf("ClientForInstallation: %v", err)
	}
	if cli == nil {
		t.Fatal("nil client")
	}
}

func TestClientForInstallation_PropagatesMintError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	if _, err := app.ClientForInstallation(context.Background(), 88); err == nil {
		t.Fatal("expected mint error to propagate")
	}
}

func TestAppClient_ReturnsClient(t *testing.T) {
	app := newTestApp(t, "secret")
	cli, err := app.AppClient()
	if err != nil {
		t.Fatalf("AppClient: %v", err)
	}
	if cli == nil {
		t.Fatal("nil client")
	}
}

// --- sync.go GitHub-side helpers (exercised directly with a stubbed client) ---

func TestListInstallationRepos_Paginates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"total_count":3,"repositories":[
				{"id":3,"name":"c","owner":{"login":"acme"}}
			]}`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/installation/repositories?page=2>; rel="next"`, r.Host))
		fmt.Fprint(w, `{"total_count":3,"repositories":[
			{"id":1,"name":"a","owner":{"login":"acme"}},
			{"id":2,"name":"b","owner":{"login":"acme"}}
		]}`)
	})
	cli := newPATTestClient(t, mux)

	repos, err := listInstallationRepos(context.Background(), cli)
	if err != nil {
		t.Fatalf("listInstallationRepos: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("got %d repos across two pages, want 3", len(repos))
	}
	if repos[0].GetID() != 1 || repos[2].GetID() != 3 {
		t.Errorf("unexpected order: %d..%d", repos[0].GetID(), repos[2].GetID())
	}
}

func TestListInstallationRepos_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	cli := newPATTestClient(t, mux)
	if _, err := listInstallationRepos(context.Background(), cli); err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestListOrgTeams_Paginates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/teams", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":20,"slug":"platform","name":"Platform"}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/orgs/acme/teams?page=2>; rel="next"`, r.Host))
		fmt.Fprint(w, `[{"id":10,"slug":"core","name":"Core"}]`)
	})
	cli := newPATTestClient(t, mux)

	teams, err := listOrgTeams(context.Background(), cli, "acme")
	if err != nil {
		t.Fatalf("listOrgTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("got %d teams, want 2", len(teams))
	}
	if teams[0].GetSlug() != "core" || teams[1].GetSlug() != "platform" {
		t.Errorf("unexpected slugs: %s, %s", teams[0].GetSlug(), teams[1].GetSlug())
	}
}

func TestListOrgTeams_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/teams", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	cli := newPATTestClient(t, mux)
	if _, err := listOrgTeams(context.Background(), cli, "acme"); err == nil {
		t.Fatal("expected error from 502 response")
	}
}

func TestListTeamMembers_Paginates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/teams/core/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2,"login":"bob"}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/orgs/acme/teams/core/members?page=2>; rel="next"`, r.Host))
		fmt.Fprint(w, `[{"id":1,"login":"alice"}]`)
	})
	cli := newPATTestClient(t, mux)

	members, err := listTeamMembers(context.Background(), cli, "acme", "core")
	if err != nil {
		t.Fatalf("listTeamMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	if members[0].GetLogin() != "alice" || members[1].GetLogin() != "bob" {
		t.Errorf("unexpected logins: %s, %s", members[0].GetLogin(), members[1].GetLogin())
	}
}

func TestListTeamMembers_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/teams/core/members", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	cli := newPATTestClient(t, mux)
	if _, err := listTeamMembers(context.Background(), cli, "acme", "core"); err == nil {
		t.Fatal("expected error from 500 response")
	}
}

// --- SyncInstallation error paths reachable without a live DB ---

func TestSyncInstallation_NilStore(t *testing.T) {
	app := newTestApp(t, "secret")
	if _, err := app.SyncInstallation(context.Background(), nil, 1); err == nil {
		t.Fatal("nil store must error")
	}
}

func TestSyncInstallation_ListReposError(t *testing.T) {
	// Token mint succeeds, but the repo listing 500s, so SyncInstallation
	// fails before reaching any DB transaction.
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/", tokenMintHandler("ghs_sync")) // /access_tokens suffix
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	store := nonNilStorePlaceholder()
	if _, err := app.SyncInstallation(context.Background(), store, 1234); err == nil {
		t.Fatal("expected list-repos error")
	} else if !strings.Contains(err.Error(), "list repos") {
		t.Errorf("error = %v, want list-repos wrap", err)
	}
}

func TestSyncInstallation_GetInstallationError(t *testing.T) {
	// Repos list fine; GetInstallation 500s.
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			tokenMintHandler("ghs_sync")(w, r)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_count":0,"repositories":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	store := nonNilStorePlaceholder()
	if _, err := app.SyncInstallation(context.Background(), store, 4242); err == nil {
		t.Fatal("expected get-installation error")
	} else if !strings.Contains(err.Error(), "get installation") {
		t.Errorf("error = %v, want get-installation wrap", err)
	}
}

func TestSyncInstallation_OrgTeamsError(t *testing.T) {
	// Drive the Organization branch: token + repos + GetInstallation all
	// succeed and report an Organization account, but listing teams 500s.
	// This errors before WithTx, exercising the org-team fetch path.
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			tokenMintHandler("ghs_sync")(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":4242,"account":{"login":"acme","type":"Organization"}}`)
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_count":0,"repositories":[]}`)
	})
	mux.HandleFunc("/orgs/acme/teams", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	store := nonNilStorePlaceholder()
	if _, err := app.SyncInstallation(context.Background(), store, 4242); err == nil {
		t.Fatal("expected list-teams error")
	} else if !strings.Contains(err.Error(), "list teams") {
		t.Errorf("error = %v, want list-teams wrap", err)
	}
}

func TestSyncInstallation_TeamMembersError(t *testing.T) {
	// Org branch, teams list OK, but member listing for a team 500s —
	// covers the per-team members loop and its error wrap, still before WithTx.
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			tokenMintHandler("ghs_sync")(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":4242,"account":{"login":"acme","type":"Organization"}}`)
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_count":0,"repositories":[]}`)
	})
	mux.HandleFunc("/orgs/acme/teams", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":10,"slug":"core","name":"Core"}]`)
	})
	mux.HandleFunc("/orgs/acme/teams/core/members", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	app := newAppWithGitHubStub(t, srv)

	store := nonNilStorePlaceholder()
	if _, err := app.SyncInstallation(context.Background(), store, 4242); err == nil {
		t.Fatal("expected list-members error")
	} else if !strings.Contains(err.Error(), "list members") {
		t.Errorf("error = %v, want list-members wrap", err)
	}
}
