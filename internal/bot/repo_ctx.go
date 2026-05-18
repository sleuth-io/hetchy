package bot

import "time"

// repoCtx carries the resolved per-request repository details into the
// sandbox: the slug "owner/name", the default branch the agent should
// branch off, and a freshly-minted GitHub App installation token. The
// token is scoped to a single repo (RepoID is set when minting upstream)
// so a compromised sandbox can only push to the one repo it's working
// on, not the whole installation.
type repoCtx struct {
	Slug         string
	BaseBranch   string
	GitHubToken  string
	InstallID    int64
	RepoID       int64
	CacheMounted bool
	TokenExpires time.Time
}
