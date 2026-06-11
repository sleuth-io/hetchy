package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/v66/github"
)

func TestPATInstallationID(t *testing.T) {
	a := PATInstallationID("org_alpha")
	b := PATInstallationID("org_beta")
	if a >= 0 || b >= 0 {
		t.Fatalf("synthetic ids must be negative: got %d, %d", a, b)
	}
	if a == b {
		t.Fatalf("distinct orgs must derive distinct ids: both %d", a)
	}
	if again := PATInstallationID("org_alpha"); again != a {
		t.Errorf("id not stable: %d vs %d", a, again)
	}
	if PATInstallationID("") >= 0 {
		t.Errorf("empty org id must still derive a negative id")
	}
}

func TestIsPATInstallation(t *testing.T) {
	if !IsPATInstallation(-42) {
		t.Errorf("negative ids are PAT installations")
	}
	if IsPATInstallation(42) || IsPATInstallation(0) {
		t.Errorf("non-negative ids are not PAT installations")
	}
}

func TestSourcePATPath(t *testing.T) {
	src := &Source{
		LookupPAT: func(_ context.Context, installationID int64) (string, error) {
			if installationID != -7 {
				return "", fmt.Errorf("unexpected id %d", installationID)
			}
			return "ghp_test", nil
		},
	}
	ctx := context.Background()

	tok, exp, err := src.InstallationToken(ctx, -7, []int64{1, 2})
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if tok != "ghp_test" {
		t.Errorf("token = %q, want ghp_test", tok)
	}
	if time.Until(exp) < time.Hour {
		t.Errorf("PAT expiry %v should be comfortably in the future", exp)
	}

	tok, _, err = src.InstallationTokenMinTTL(ctx, -7, nil, 55*time.Minute)
	if err != nil || tok != "ghp_test" {
		t.Errorf("InstallationTokenMinTTL = %q, %v", tok, err)
	}

	cli, err := src.ClientForInstallation(ctx, -7)
	if err != nil || cli == nil {
		t.Errorf("ClientForInstallation = %v, %v", cli, err)
	}

	// No-op, but must not panic with a nil App.
	src.InvalidateInstallation(-7)
	src.InvalidateInstallation(7)
}

func TestSourceAppPathWithoutApp(t *testing.T) {
	src := &Source{LookupPAT: func(context.Context, int64) (string, error) { return "x", nil }}
	if _, _, err := src.InstallationToken(context.Background(), 42, nil); err == nil {
		t.Errorf("positive installation id without an App must error")
	}
	if _, err := src.ClientForInstallation(context.Background(), 42); err == nil {
		t.Errorf("positive installation id without an App must error")
	}
}

func TestSourcePATErrors(t *testing.T) {
	src := &Source{}
	if _, _, err := src.InstallationToken(context.Background(), -1, nil); err == nil {
		t.Errorf("missing LookupPAT must error")
	}
	src.LookupPAT = func(context.Context, int64) (string, error) { return "", nil }
	if _, _, err := src.InstallationToken(context.Background(), -1, nil); err == nil {
		t.Errorf("empty stored PAT must error")
	}
	wantErr := errors.New("boom")
	src.LookupPAT = func(context.Context, int64) (string, error) { return "", wantErr }
	if _, _, err := src.InstallationToken(context.Background(), -1, nil); !errors.Is(err, wantErr) {
		t.Errorf("lookup error not propagated: %v", err)
	}
}

func TestSyncPATInputValidation(t *testing.T) {
	src := &Source{}
	if _, err := src.SyncPAT(context.Background(), nil, "org", "tok"); err == nil {
		t.Errorf("nil store must error")
	}
}

// newPATTestClient points a go-github client at a local httptest server.
func newPATTestClient(t *testing.T, handler http.Handler) *github.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cli := github.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	cli.BaseURL = base
	return cli
}

func TestListPATRepos_FiltersAndPaginates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[
				{"id": 3, "name": "writable-2", "owner": {"login": "acme"}, "default_branch": "main", "permissions": {"push": true}}
			]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/user/repos?page=2>; rel="next"`, r.Host))
		fmt.Fprint(w, `[
			{"id": 1, "name": "writable-1", "owner": {"login": "acme"}, "default_branch": "main", "permissions": {"push": true}},
			{"id": 2, "name": "read-only", "owner": {"login": "acme"}, "default_branch": "main", "permissions": {"push": false}}
		]`)
	})
	cli := newPATTestClient(t, mux)

	repos, truncated, err := listPATRepos(context.Background(), cli)
	if err != nil {
		t.Fatalf("listPATRepos: %v", err)
	}
	if truncated {
		t.Errorf("two pages should not report truncation")
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2 (read-only filtered out)", len(repos))
	}
	if repos[0].GetID() != 1 || repos[1].GetID() != 3 {
		t.Errorf("unexpected repo ids: %d, %d", repos[0].GetID(), repos[1].GetID())
	}
}

func TestListPATRepos_Truncates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		w.Header().Set("Content-Type", "application/json")
		// Always claim another page exists so the cap has to kick in.
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/user/repos?page=%s9>; rel="next"`, r.Host, page))
		fmt.Fprintf(w, `[{"id": %d0, "name": "r%s", "owner": {"login": "acme"}, "permissions": {"push": true}}]`, len(page), page)
	})
	cli := newPATTestClient(t, mux)

	repos, truncated, err := listPATRepos(context.Background(), cli)
	if err != nil {
		t.Fatalf("listPATRepos: %v", err)
	}
	if !truncated {
		t.Errorf("endless paging must report truncation")
	}
	if len(repos) != maxPATRepoPages {
		t.Errorf("got %d repos, want %d (one per page up to the cap)", len(repos), maxPATRepoPages)
	}
}
