package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

const (
	githubPRStateOpen   = "open"
	githubPRStateClosed = "closed"

	prStateBackfillDefaultLimit  = 1000
	prStateBackfillMaxLimit      = 10000
	prStateBackfillProgressEvery = 25
)

// PRStateBackfillResult reports what the explicit GitHub backfill command did.
type PRStateBackfillResult struct {
	Scanned int
	Updated int
	Skipped int
	Failed  int
}

func (b *Bot) refreshConversationPRState(ctx context.Context, orgID, threadID, rawURL string) error {
	return b.refreshConversationPRStateForRepo(ctx, orgID, threadID, rawURL, "", "")
}

func (b *Bot) refreshConversationPRStateBestEffort(ctx context.Context, orgID, threadID, rawURL string) {
	if b == nil {
		return
	}
	if err := b.refreshConversationPRState(ctx, orgID, threadID, rawURL); err != nil && b.log != nil {
		b.log.Warn("refresh conversation PR state failed",
			"org", orgID, "thread", threadID, "pr_url", rawURL, "error", err)
	}
}

func (b *Bot) refreshConversationPRStateForRepo(ctx context.Context, orgID, threadID, rawURL, ownerHint, repoHint string) error {
	if b == nil || b.store == nil || b.app == nil || strings.TrimSpace(rawURL) == "" {
		return nil
	}
	parsed, err := parseGitHubPRURL(rawURL)
	if err != nil {
		return err
	}
	owner := firstNonEmpty(ownerHint, parsed.Owner)
	repo := firstNonEmpty(repoHint, parsed.Repo)
	repoRow, err := b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  repo,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("github repo not installed for org %s: %s/%s", orgID, owner, repo)
		}
		return fmt.Errorf("resolve github repo for PR state: %w", err)
	}
	token, _, err := b.app.InstallationToken(ctx, repoRow.InstallationID, []int64{repoRow.RepoID})
	if err != nil {
		return fmt.Errorf("mint github token for PR state: %w", err)
	}
	pr, err := lookupGitHubPullRequest(ctx, token, parsed.Owner, parsed.Repo, parsed.Number)
	if err != nil {
		return fmt.Errorf("lookup github PR state: %w", err)
	}
	if pr == nil {
		return errors.New("lookup github PR state: empty GitHub response")
	}
	return b.saveConversationPRState(ctx, orgID, threadID, pr)
}

func (b *Bot) saveConversationPRState(ctx context.Context, orgID, threadID string, pr *github.PullRequest) error {
	if b == nil || b.store == nil || pr == nil {
		return nil
	}
	state := conversationPRStateFromGitHub(pr)
	return b.store.Queries.SaveConversationPRState(ctx, sqlc.SaveConversationPRStateParams{
		OrgID:      orgID,
		ThreadID:   threadID,
		PrState:    state.State,
		PrMerged:   state.Merged,
		PrMergedAt: pgTimestamptz(state.MergedAt),
		PrClosedAt: pgTimestamptz(state.ClosedAt),
	})
}

func (b *Bot) saveConversationPRStateByURL(ctx context.Context, orgID, owner, repo string, number int, rawURL string, pr *github.PullRequest) (int64, error) {
	if b == nil || b.store == nil || pr == nil || number <= 0 {
		return 0, nil
	}
	if rawURL = strings.TrimSpace(rawURL); rawURL == "" {
		rawURL = canonicalGitHubPRURL(owner, repo, number)
	}
	state := conversationPRStateFromGitHub(pr)
	return b.store.Queries.SaveConversationPRStateByURL(ctx, sqlc.SaveConversationPRStateByURLParams{
		OrgID:       orgID,
		GithubOwner: owner,
		GithubRepo:  repo,
		PrUrl:       rawURL,
		PrNumber:    int32(number),
		PrState:     state.State,
		PrMerged:    state.Merged,
		PrMergedAt:  pgTimestamptz(state.MergedAt),
		PrClosedAt:  pgTimestamptz(state.ClosedAt),
	})
}

type conversationPRState struct {
	State    string
	Merged   bool
	MergedAt time.Time
	ClosedAt time.Time
}

func conversationPRStateFromGitHub(pr *github.PullRequest) conversationPRState {
	if pr == nil {
		return conversationPRState{}
	}
	state := strings.ToLower(strings.TrimSpace(pr.GetState()))
	switch state {
	case githubPRStateOpen, githubPRStateClosed:
	default:
		state = ""
	}
	mergedAt := pr.GetMergedAt().Time
	closedAt := pr.GetClosedAt().Time
	merged := pr.GetMerged() || !mergedAt.IsZero()
	return conversationPRState{
		State:    state,
		Merged:   merged,
		MergedAt: mergedAt,
		ClosedAt: closedAt,
	}
}

func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func canonicalGitHubPRURL(owner, repo string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, number)
}

// BackfillConversationPRStates refreshes stored PR state for conversations that
// already have a PR URL. It is intentionally a command path, not a migration:
// it uses the GitHub API and can be rerun safely in batches.
func (b *Bot) BackfillConversationPRStates(ctx context.Context, limit int, force bool) (PRStateBackfillResult, error) {
	if b == nil || b.store == nil {
		return PRStateBackfillResult{}, errors.New("database is not configured")
	}
	if b.app == nil {
		return PRStateBackfillResult{}, errors.New("github app is not configured")
	}
	if limit <= 0 {
		limit = prStateBackfillDefaultLimit
	}
	if limit > prStateBackfillMaxLimit {
		limit = prStateBackfillMaxLimit
	}
	if b.log != nil {
		b.log.Info("backfill conversation PR states starting", "limit", limit, "force", force)
	}
	rows, err := b.store.Queries.ListConversationPRStateBackfillCandidates(ctx, sqlc.ListConversationPRStateBackfillCandidatesParams{
		Force: force,
		Lim:   int32(limit),
	})
	if err != nil {
		return PRStateBackfillResult{}, fmt.Errorf("list PR state backfill candidates: %w", err)
	}
	total := len(rows)
	if b.log != nil {
		b.log.Info("backfill conversation PR states candidates loaded", "total", total)
	}
	var result PRStateBackfillResult
	for _, row := range rows {
		result.Scanned++
		if b.log != nil {
			b.log.Debug("backfill conversation PR state candidate",
				"index", result.Scanned, "total", total, "org", row.OrgID, "thread", row.ThreadID,
				"repo", row.GithubOwner+"/"+row.GithubRepo, "pr_url", row.PrUrl)
		}
		if strings.TrimSpace(row.PrUrl) == "" {
			result.Skipped++
			b.logPRStateBackfillProgress(result, total)
			continue
		}
		if err := b.refreshConversationPRStateForRepo(ctx, row.OrgID, row.ThreadID, row.PrUrl, row.GithubOwner, row.GithubRepo); err != nil {
			result.Failed++
			if b.log != nil {
				b.log.Warn("backfill conversation PR state failed",
					"org", row.OrgID, "thread", row.ThreadID, "pr_url", row.PrUrl, "error", err)
			}
			b.logPRStateBackfillProgress(result, total)
			continue
		}
		result.Updated++
		b.logPRStateBackfillProgress(result, total)
	}
	if b.log != nil {
		b.log.Info("backfill conversation PR states complete",
			"scanned", result.Scanned, "updated", result.Updated, "skipped", result.Skipped, "failed", result.Failed)
	}
	return result, nil
}

func (b *Bot) logPRStateBackfillProgress(result PRStateBackfillResult, total int) {
	if b == nil || b.log == nil || result.Scanned == 0 {
		return
	}
	if result.Scanned%prStateBackfillProgressEvery != 0 && result.Scanned != total {
		return
	}
	b.log.Info("backfill conversation PR states progress",
		"scanned", result.Scanned, "total", total, "remaining", total-result.Scanned,
		"updated", result.Updated, "skipped", result.Skipped, "failed", result.Failed)
}
