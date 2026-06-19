package githubapp

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// PAT (personal access token) connections reuse the GitHub App's
// installation/repo cache tables so every query that joins
// github_app_installations → github_repos works unchanged. A PAT
// connection is represented as a synthetic installation whose
// installation_id is a stable negative value derived from the org id —
// real GitHub installation ids are always positive, so the sign is the
// discriminator.

// patTokenTTL is the lifetime we report for a PAT handed to a sandbox.
// PATs don't expire on our clock (classic PATs may never expire;
// fine-grained ones expire server-side on a schedule we can't read), so
// this is just a sane horizon for callers that plan refreshes around
// the returned expiry.
const patTokenTTL = 24 * time.Hour

// maxPATRepoPages caps repo discovery for a PAT at 10 pages of 100.
// Classic PATs on accounts with broad org membership can see thousands
// of repos; past this cap we keep what we have and mark the sync
// truncated rather than hammering the API.
const maxPATRepoPages = 10

// ErrPATUnauthorized reports that GitHub rejected the token itself
// (revoked, expired, or mistyped) as opposed to a transient API error.
var ErrPATUnauthorized = errors.New("githubapp: github rejected the personal access token")

// ErrPATOrgConflict reports that the synthetic installation id derived
// for this org is already bound to a different org — astronomically
// unlikely (it requires a 63-bit hash collision) but surfaced clearly
// rather than silently rebinding the other org's repos.
var ErrPATOrgConflict = errors.New("githubapp: synthetic installation id already bound to another org")

// PATInstallationID derives the stable synthetic installation id for an
// org's PAT connection: the FNV-1a hash of the org id, forced negative.
func PATInstallationID(orgID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(orgID))
	id := int64(h.Sum64() >> 1) // drop the top bit so negation can't overflow
	if id == 0 {
		id = 1
	}
	return -id
}

// IsPATInstallation reports whether installationID names a synthetic
// PAT-backed installation rather than a real GitHub App installation.
func IsPATInstallation(installationID int64) bool {
	return installationID < 0
}

// PATLookup resolves the plaintext PAT for a synthetic installation.
// The bot wires this to an org-config lookup keyed off the
// installation row's org_id.
type PATLookup func(ctx context.Context, installationID int64) (string, error)

// TokenSource is the seam between "give me a GitHub credential for this
// installation" callers and however that credential is produced — App
// installation minting or a stored PAT. *App implements it; Source
// implements it for mixed App+PAT deployments.
type TokenSource interface {
	InstallationToken(ctx context.Context, installationID int64, repoIDs []int64) (string, time.Time, error)
	InstallationTokenMinTTL(ctx context.Context, installationID int64, repoIDs []int64, minTTL time.Duration) (string, time.Time, error)
	ClientForInstallation(ctx context.Context, installationID int64) (*github.Client, error)
	InvalidateInstallation(installationID int64)
}

var _ TokenSource = (*App)(nil)
var _ TokenSource = (*Source)(nil)

// Source routes token requests by installation id sign: negative ids
// resolve through LookupPAT, positive ids through the App. App may be
// nil (PAT-only deployment, e.g. self-hosted without a GitHub App) —
// positive-id requests then fail with a clear error.
type Source struct {
	App       *App
	LookupPAT PATLookup
	// HTTP backs PAT-authenticated clients; nil means http.DefaultClient.
	HTTP *http.Client
}

func (s *Source) pat(ctx context.Context, installationID int64) (string, time.Time, error) {
	if s.LookupPAT == nil {
		return "", time.Time{}, errors.New("githubapp: no PAT lookup configured")
	}
	tok, err := s.LookupPAT(ctx, installationID)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("lookup PAT for installation %d: %w", installationID, err)
	}
	if tok == "" {
		return "", time.Time{}, fmt.Errorf("no PAT stored for installation %d", installationID)
	}
	return tok, time.Now().Add(patTokenTTL), nil
}

func (s *Source) app() (*App, error) {
	if s.App == nil {
		return nil, errors.New("githubapp: github app not configured for this environment")
	}
	return s.App, nil
}

// InstallationToken implements TokenSource. PAT tokens ignore repoIDs:
// a PAT's scope is fixed at creation time on GitHub's side, so there is
// no per-repo narrowing to do.
func (s *Source) InstallationToken(ctx context.Context, installationID int64, repoIDs []int64) (string, time.Time, error) {
	if IsPATInstallation(installationID) {
		return s.pat(ctx, installationID)
	}
	app, err := s.app()
	if err != nil {
		return "", time.Time{}, err
	}
	return app.InstallationToken(ctx, installationID, repoIDs)
}

// InstallationTokenMinTTL implements TokenSource. PATs satisfy any
// requested TTL (see patTokenTTL).
func (s *Source) InstallationTokenMinTTL(ctx context.Context, installationID int64, repoIDs []int64, minTTL time.Duration) (string, time.Time, error) {
	if IsPATInstallation(installationID) {
		return s.pat(ctx, installationID)
	}
	app, err := s.app()
	if err != nil {
		return "", time.Time{}, err
	}
	return app.InstallationTokenMinTTL(ctx, installationID, repoIDs, minTTL)
}

// ClientForInstallation implements TokenSource.
func (s *Source) ClientForInstallation(ctx context.Context, installationID int64) (*github.Client, error) {
	if IsPATInstallation(installationID) {
		tok, _, err := s.pat(ctx, installationID)
		if err != nil {
			return nil, err
		}
		return github.NewClient(s.HTTP).WithAuthToken(tok), nil
	}
	app, err := s.app()
	if err != nil {
		return nil, err
	}
	return app.ClientForInstallation(ctx, installationID)
}

// InvalidateInstallation implements TokenSource. PATs aren't cached
// server-side, so only App installations have anything to drop.
func (s *Source) InvalidateInstallation(installationID int64) {
	if IsPATInstallation(installationID) || s.App == nil {
		return
	}
	s.App.InvalidateInstallation(installationID)
}

// SyncPAT validates the token against GitHub, then refreshes the org's
// synthetic installation row and its cached repos — the PAT counterpart
// of App.SyncInstallation. Teams are not synced: a PAT has no
// installation-style org binding, and team data only powers App-mode
// features.
func (s *Source) SyncPAT(ctx context.Context, store *db.Store, orgID, pat string) (SyncResult, error) {
	if store == nil {
		return SyncResult{}, errors.New("githubapp: SyncPAT requires a non-nil db store")
	}
	if pat == "" {
		return SyncResult{}, errors.New("githubapp: SyncPAT requires a non-empty token")
	}
	installationID := PATInstallationID(orgID)
	cli := github.NewClient(s.HTTP).WithAuthToken(pat)

	user, resp, err := cli.Users.Get(ctx, "")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return SyncResult{}, ErrPATUnauthorized
		}
		return SyncResult{}, fmt.Errorf("verify token: %w", err)
	}

	repos, truncated, err := listPATRepos(ctx, cli)
	if err != nil {
		return SyncResult{}, fmt.Errorf("list repos: %w", err)
	}

	result := SyncResult{InstallationID: installationID, Repos: len(repos), Truncated: truncated}
	err = store.WithTx(ctx, func(q *sqlc.Queries) error {
		if _, err := q.UpsertGithubInstallation(ctx, sqlc.UpsertGithubInstallationParams{
			InstallationID: installationID,
			OrgID:          orgID,
			AccountLogin:   user.GetLogin(),
			AccountType:    user.GetType(),
			AccountID:      user.GetID(),
			SuspendedAt:    pgtype.Timestamptz{},
		}); err != nil {
			// The upsert's cross-org guard yields zero rows when the
			// synthetic id is somehow already bound elsewhere.
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPATOrgConflict
			}
			return fmt.Errorf("upsert PAT installation: %w", err)
		}
		keepRepoIDs := make([]int64, 0, len(repos))
		for _, r := range repos {
			if err := q.UpsertGithubRepo(ctx, sqlc.UpsertGithubRepoParams{
				InstallationID: installationID,
				RepoID:         r.GetID(),
				Owner:          r.GetOwner().GetLogin(),
				Name:           r.GetName(),
				DefaultBranch:  r.GetDefaultBranch(),
				Private:        r.GetPrivate(),
			}); err != nil {
				return fmt.Errorf("upsert repo %s: %w", r.GetFullName(), err)
			}
			keepRepoIDs = append(keepRepoIDs, r.GetID())
		}
		if err := q.DeleteGithubReposByInstallationExcept(ctx, sqlc.DeleteGithubReposByInstallationExceptParams{
			InstallationID: installationID,
			Column2:        keepRepoIDs,
		}); err != nil {
			return fmt.Errorf("prune repos: %w", err)
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// listPATRepos pages through every repo the token can push to, up to
// maxPATRepoPages. Pull-only repos are skipped — the agent's whole job
// is branch + PR, which needs write access.
func listPATRepos(ctx context.Context, cli *github.Client) (repos []*github.Repository, truncated bool, err error) {
	opts := &github.RepositoryListByAuthenticatedUserOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for page := 0; ; page++ {
		if page >= maxPATRepoPages {
			return repos, true, nil
		}
		batch, resp, err := cli.Repositories.ListByAuthenticatedUser(ctx, opts)
		if err != nil {
			return nil, false, err
		}
		for _, r := range batch {
			if r.GetPermissions()["push"] {
				repos = append(repos, r)
			}
		}
		if resp.NextPage == 0 {
			return repos, false, nil
		}
		opts.Page = resp.NextPage
	}
}
