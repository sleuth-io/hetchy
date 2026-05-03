package githubapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
)

// installationTokenSafetyWindow is how long before actual expiry we
// treat a cached installation token as stale. GitHub's tokens last
// one hour; refreshing 5 minutes early avoids handing a token to the
// sandbox that will expire mid-clone or mid-push.
const installationTokenSafetyWindow = 5 * time.Minute

type tokenEntry struct {
	token     string
	expiresAt time.Time
	// repoIDs is the set the cached token was scoped to. We compare
	// requested vs cached scopes on every lookup — narrowing a cached
	// broader token would silently grant the caller more access than
	// they asked for, so we re-mint on any mismatch.
	repoIDs []int64
}

type tokenCache struct {
	mu      sync.Mutex
	entries map[int64]*tokenEntry
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: map[int64]*tokenEntry{}}
}

// InstallationToken returns a usable access token for installationID,
// scoped to repoIDs if non-empty. Pass nil/empty repoIDs to request a
// token with the installation's full repo access (used for sync ops);
// pass a single repo's id when minting a token for the sandbox so a
// compromised sandbox can't reach other repos in the installation.
//
// Cached tokens are reused until they're within the safety window of
// expiry and were minted with the same scope set.
func (a *App) InstallationToken(ctx context.Context, installationID int64, repoIDs []int64) (string, time.Time, error) {
	a.tokens.mu.Lock()
	if e, ok := a.tokens.entries[installationID]; ok {
		if time.Until(e.expiresAt) > installationTokenSafetyWindow && sameInts(e.repoIDs, repoIDs) {
			tok, exp := e.token, e.expiresAt
			a.tokens.mu.Unlock()
			return tok, exp, nil
		}
	}
	a.tokens.mu.Unlock()

	jwtTok, err := a.appJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	cli := github.NewClient(a.http).WithAuthToken(jwtTok)
	opts := &github.InstallationTokenOptions{}
	if len(repoIDs) > 0 {
		opts.RepositoryIDs = repoIDs
	}
	itok, resp, err := cli.Apps.CreateInstallationToken(ctx, installationID, opts)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return "", time.Time{}, fmt.Errorf("create installation token (status %d): %w", status, err)
	}
	if itok.GetToken() == "" || itok.GetExpiresAt().IsZero() {
		return "", time.Time{}, errors.New("create installation token: empty response")
	}

	a.tokens.mu.Lock()
	a.tokens.entries[installationID] = &tokenEntry{
		token:     itok.GetToken(),
		expiresAt: itok.GetExpiresAt().Time,
		repoIDs:   append([]int64(nil), repoIDs...),
	}
	a.tokens.mu.Unlock()
	return itok.GetToken(), itok.GetExpiresAt().Time, nil
}

// InvalidateInstallation drops any cached token for installationID.
// Called by the webhook handler when GitHub tells us the installation
// has been suspended, deleted, or had its repos changed (the cached
// token's repo scope is now stale).
func (a *App) InvalidateInstallation(installationID int64) {
	a.tokens.mu.Lock()
	delete(a.tokens.entries, installationID)
	a.tokens.mu.Unlock()
}

// ClientForInstallation returns a *github.Client authenticated as
// installationID with full repo access. Use for sync operations
// (listing repos, teams, members) where you genuinely need org-wide
// access; for sandbox-bound work, mint a per-repo scoped token via
// InstallationToken instead.
func (a *App) ClientForInstallation(ctx context.Context, installationID int64) (*github.Client, error) {
	tok, _, err := a.InstallationToken(ctx, installationID, nil)
	if err != nil {
		return nil, err
	}
	return github.NewClient(a.http).WithAuthToken(tok), nil
}

// AppClient returns a *github.Client authenticated as the App itself
// (not an installation). Used for the handful of endpoints that
// require an App JWT — listing all installations, fetching an
// installation by id, etc.
func (a *App) AppClient() (*github.Client, error) {
	jwtTok, err := a.appJWT()
	if err != nil {
		return nil, err
	}
	return github.NewClient(a.http).WithAuthToken(jwtTok), nil
}

func sameInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	// Order matters here: a token cached with [42, 43] and one with
	// [43, 42] are scope-equivalent, but normalizing is overkill for
	// our caller pattern (we always pass either nil or a single id).
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
