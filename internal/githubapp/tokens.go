package githubapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
	"golang.org/x/sync/singleflight"
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
	// flight coalesces concurrent mint requests for the same
	// (installation, scope) so a burst of webhook/sandbox launches for
	// the same org doesn't multiply our GitHub API spend.
	flight singleflight.Group
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
	if tok, exp, ok := a.cachedToken(installationID, repoIDs); ok {
		return tok, exp, nil
	}

	// Coalesce concurrent mint requests so a burst of inbound work for
	// the same installation only spends one GitHub API call. The key
	// includes the scope set so a sandbox token (single repo) and a
	// sync token (full access) for the same install don't share a flight.
	key := mintKey(installationID, repoIDs)
	v, err, _ := a.tokens.flight.Do(key, func() (any, error) {
		// Re-check the cache: the inflight goroutine may have just
		// populated it for the same key while we were waiting on Do.
		if tok, exp, ok := a.cachedToken(installationID, repoIDs); ok {
			return mintResult{token: tok, expiresAt: exp}, nil
		}
		jwtTok, err := a.appJWT()
		if err != nil {
			return mintResult{}, err
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
			return mintResult{}, fmt.Errorf("create installation token (status %d): %w", status, err)
		}
		if itok.GetToken() == "" || itok.GetExpiresAt().IsZero() {
			return mintResult{}, errors.New("create installation token: empty response")
		}
		a.tokens.mu.Lock()
		a.tokens.entries[installationID] = &tokenEntry{
			token:     itok.GetToken(),
			expiresAt: itok.GetExpiresAt().Time,
			repoIDs:   append([]int64(nil), repoIDs...),
		}
		a.tokens.mu.Unlock()
		return mintResult{token: itok.GetToken(), expiresAt: itok.GetExpiresAt().Time}, nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	r := v.(mintResult)
	return r.token, r.expiresAt, nil
}

type mintResult struct {
	token     string
	expiresAt time.Time
}

// cachedToken returns the cached token for installationID if one exists
// that's still inside the safety window and was minted with a matching
// scope set. The bool reports whether the returned token is usable.
func (a *App) cachedToken(installationID int64, repoIDs []int64) (string, time.Time, bool) {
	a.tokens.mu.Lock()
	defer a.tokens.mu.Unlock()
	e, ok := a.tokens.entries[installationID]
	if !ok {
		return "", time.Time{}, false
	}
	if time.Until(e.expiresAt) <= installationTokenSafetyWindow {
		return "", time.Time{}, false
	}
	if !sameInts(e.repoIDs, repoIDs) {
		return "", time.Time{}, false
	}
	return e.token, e.expiresAt, true
}

// mintKey is the singleflight key for a given (installation, scope)
// pair. Scope IDs are sorted so callers passing the same set in
// different orders share a flight.
func mintKey(installationID int64, repoIDs []int64) string {
	if len(repoIDs) == 0 {
		return strconv.FormatInt(installationID, 10) + ":all"
	}
	sorted := slices.Clone(repoIDs)
	slices.Sort(sorted)
	var b []byte
	b = strconv.AppendInt(b, installationID, 10)
	b = append(b, ':')
	for i, id := range sorted {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, id, 10)
	}
	return string(b)
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

// sameInts reports whether a and b contain the same elements,
// disregarding order. We sort copies so callers don't have to commit
// to a stable ordering — passing the same scope list as `[42, 43]`
// or `[43, 42]` should both hit the same cached token.
func sameInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	aa := slices.Clone(a)
	bb := slices.Clone(b)
	slices.Sort(aa)
	slices.Sort(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
