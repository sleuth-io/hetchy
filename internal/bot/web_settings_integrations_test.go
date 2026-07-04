package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/githubapp"
)

// fakeInstallationLister is an in-memory githubInstallationLister so the
// Integrations tab view assembly can be exercised without a live Postgres.
type fakeInstallationLister struct {
	installs    []sqlc.GithubAppInstallation
	reposByID   map[int64][]sqlc.GithubRepo
	installsErr error
	reposErr    error
	// reposErrForID scopes a repos-list failure to one installation so the
	// per-installation error path can be exercised on the second row.
	reposErrForID int64

	installsCalled int
	reposCalledFor []int64
}

func (f *fakeInstallationLister) ListGithubInstallationsByOrg(_ context.Context, _ string) ([]sqlc.GithubAppInstallation, error) {
	f.installsCalled++
	if f.installsErr != nil {
		return nil, f.installsErr
	}
	return f.installs, nil
}

func (f *fakeInstallationLister) ListGithubReposByInstallation(_ context.Context, installationID int64) ([]sqlc.GithubRepo, error) {
	f.reposCalledFor = append(f.reposCalledFor, installationID)
	if f.reposErr != nil && (f.reposErrForID == 0 || f.reposErrForID == installationID) {
		return nil, f.reposErr
	}
	return f.reposByID[installationID], nil
}

func TestLoadIntegrationsViewGroupsReposAndDecoratesInstallations(t *testing.T) {
	// A synthetic PAT installation carries a negative id.
	patID := githubapp.PATInstallationID("acme")
	if !githubapp.IsPATInstallation(patID) {
		t.Fatalf("expected synthetic PAT id %d to be a PAT installation", patID)
	}

	lister := &fakeInstallationLister{
		installs: []sqlc.GithubAppInstallation{
			{InstallationID: 42, AccountLogin: "sleuth-io", AccountType: "Organization"},
			{InstallationID: 99, AccountLogin: "dylan", AccountType: "User", SuspendedAt: pgtype.Timestamptz{Valid: true}},
			{InstallationID: patID, AccountLogin: "acme", AccountType: "User"},
		},
		reposByID: map[int64][]sqlc.GithubRepo{
			42: {
				{Owner: "sleuth-io", Name: "hetchy", DefaultBranch: "main", Private: true},
				{Owner: "sleuth-io", Name: "public-repo", DefaultBranch: "trunk", Private: false},
			},
			99:    {{Owner: "dylan", Name: "dotfiles", DefaultBranch: "main"}},
			patID: {{Owner: "acme", Name: "widgets", DefaultBranch: "master"}},
		},
	}

	installs, repos, err := loadIntegrationsView(context.Background(), "org_test", lister)
	if err != nil {
		t.Fatalf("loadIntegrationsView: %v", err)
	}
	if len(installs) != 3 {
		t.Fatalf("installations = %d, want 3: %#v", len(installs), installs)
	}
	// Flattened repo list spans every installation.
	if len(repos) != 4 {
		t.Fatalf("flattened repos = %d, want 4: %#v", len(repos), repos)
	}

	org := installs[0]
	if org.InstallationID != 42 || org.AccountLogin != "sleuth-io" || org.Suspended || org.IsPAT {
		t.Fatalf("org installation = %+v", org)
	}
	if org.ManageURL != "https://github.com/organizations/sleuth-io/settings/installations/42" {
		t.Fatalf("org manage URL = %q", org.ManageURL)
	}
	if len(org.Repos) != 2 || org.Repos[0].Name != "hetchy" || !org.Repos[0].Private {
		t.Fatalf("org repos = %+v", org.Repos)
	}
	if org.Repos[1].DefaultBranch != "trunk" || org.Repos[1].Private {
		t.Fatalf("org repo[1] = %+v", org.Repos[1])
	}

	user := installs[1]
	if !user.Suspended {
		t.Fatal("suspended installation should report Suspended=true")
	}
	if user.ManageURL != "https://github.com/settings/installations/99" {
		t.Fatalf("user manage URL = %q", user.ManageURL)
	}

	pat := installs[2]
	if !pat.IsPAT {
		t.Fatal("synthetic installation should report IsPAT=true")
	}
	if pat.ManageURL != "https://github.com/settings/tokens" {
		t.Fatalf("PAT manage URL = %q", pat.ManageURL)
	}
}

func TestLoadIntegrationsViewEmpty(t *testing.T) {
	lister := &fakeInstallationLister{}
	installs, repos, err := loadIntegrationsView(context.Background(), "org_test", lister)
	if err != nil {
		t.Fatalf("loadIntegrationsView: %v", err)
	}
	if len(installs) != 0 || len(repos) != 0 {
		t.Fatalf("expected empty view, got installs=%#v repos=%#v", installs, repos)
	}
	if len(lister.reposCalledFor) != 0 {
		t.Fatalf("repos lookup should be skipped when there are no installations: %#v", lister.reposCalledFor)
	}
}

func TestLoadIntegrationsViewInstallationsError(t *testing.T) {
	sentinel := errors.New("boom")
	lister := &fakeInstallationLister{installsErr: sentinel}
	_, _, err := loadIntegrationsView(context.Background(), "org_test", lister)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
}

func TestLoadIntegrationsViewReposErrorSurfacesInstallationID(t *testing.T) {
	sentinel := errors.New("repo boom")
	lister := &fakeInstallationLister{
		installs: []sqlc.GithubAppInstallation{
			{InstallationID: 1, AccountLogin: "ok", AccountType: "User"},
			{InstallationID: 2, AccountLogin: "bad", AccountType: "User"},
		},
		reposByID:     map[int64][]sqlc.GithubRepo{1: {{Owner: "ok", Name: "repo"}}},
		reposErr:      sentinel,
		reposErrForID: 2,
	}
	_, _, err := loadIntegrationsView(context.Background(), "org_test", lister)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
	// The wrapped error names the offending installation for operators.
	if got := err.Error(); !strings.Contains(got, "install 2") {
		t.Fatalf("error %q should identify installation 2", got)
	}
}
