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

type prStateCandidate struct {
	OrgID       string
	ThreadID    string
	GithubOwner string
	GithubRepo  string
	PRURL       string
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
	if b == nil || b.store == nil || b.githubTokenSource() == nil || strings.TrimSpace(rawURL) == "" {
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
	token, _, err := b.githubTokenSource().InstallationToken(ctx, repoRow.InstallationID, []int64{repoRow.RepoID})
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
	if b.githubTokenSource() == nil {
		return PRStateBackfillResult{}, errors.New("github is not configured")
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
	return b.refreshPRStateCandidates(ctx, rowsToPRStateCandidates(rows), "backfill")
}

func (b *Bot) PollPATConversationPRStates(ctx context.Context, limit int, staleAfter time.Duration) (PRStateBackfillResult, error) {
	if b == nil || b.store == nil {
		return PRStateBackfillResult{}, errors.New("database is not configured")
	}
	if b.githubTokenSource() == nil {
		return PRStateBackfillResult{}, errors.New("github is not configured")
	}
	if limit <= 0 {
		limit = defaultPRStatePollLimit
	}
	if limit > prStateBackfillMaxLimit {
		limit = prStateBackfillMaxLimit
	}
	if staleAfter <= 0 {
		staleAfter = time.Duration(defaultPRStatePollIntervalSeconds) * time.Second
	}
	checkedBefore := time.Now().Add(-staleAfter)
	if b.log != nil {
		b.log.Info("poll PAT conversation PR states starting",
			"limit", limit, "stale_after", staleAfter, "checked_before", checkedBefore.UTC().Format(time.RFC3339))
	}
	rows, err := b.store.Queries.ListPATConversationPRStatePollCandidates(ctx, sqlc.ListPATConversationPRStatePollCandidatesParams{
		CheckedBefore: pgTimestamptz(checkedBefore),
		Lim:           int32(limit),
	})
	if err != nil {
		return PRStateBackfillResult{}, fmt.Errorf("list PAT PR state poll candidates: %w", err)
	}
	if b.log != nil {
		b.log.Info("poll PAT conversation PR states candidates loaded", "total", len(rows))
	}
	return b.refreshPRStateCandidates(ctx, patRowsToPRStateCandidates(rows), "poll PAT")
}

func rowsToPRStateCandidates(rows []sqlc.ListConversationPRStateBackfillCandidatesRow) []prStateCandidate {
	candidates := make([]prStateCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, prStateCandidate{
			OrgID:       row.OrgID,
			ThreadID:    row.ThreadID,
			GithubOwner: row.GithubOwner,
			GithubRepo:  row.GithubRepo,
			PRURL:       row.PrUrl,
		})
	}
	return candidates
}

func patRowsToPRStateCandidates(rows []sqlc.ListPATConversationPRStatePollCandidatesRow) []prStateCandidate {
	candidates := make([]prStateCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, prStateCandidate{
			OrgID:       row.OrgID,
			ThreadID:    row.ThreadID,
			GithubOwner: row.GithubOwner,
			GithubRepo:  row.GithubRepo,
			PRURL:       row.PrUrl,
		})
	}
	return candidates
}

func (b *Bot) refreshPRStateCandidates(ctx context.Context, rows []prStateCandidate, label string) (PRStateBackfillResult, error) {
	var result PRStateBackfillResult
	total := len(rows)
	for _, row := range rows {
		result.Scanned++
		if b.log != nil {
			b.log.Debug(label+" conversation PR state candidate",
				"index", result.Scanned, "total", total, "org", row.OrgID, "thread", row.ThreadID,
				"repo", row.GithubOwner+"/"+row.GithubRepo, "pr_url", row.PRURL)
		}
		if strings.TrimSpace(row.PRURL) == "" {
			result.Skipped++
			b.logPRStateProgress(label, result, total)
			continue
		}
		if err := b.refreshConversationPRStateForRepo(ctx, row.OrgID, row.ThreadID, row.PRURL, row.GithubOwner, row.GithubRepo); err != nil {
			result.Failed++
			if b.log != nil {
				b.log.Warn(label+" conversation PR state failed",
					"org", row.OrgID, "thread", row.ThreadID, "pr_url", row.PRURL, "error", err)
			}
			b.logPRStateProgress(label, result, total)
			continue
		}
		result.Updated++
		b.logPRStateProgress(label, result, total)
	}
	if b.log != nil {
		b.log.Info(label+" conversation PR states complete",
			"scanned", result.Scanned, "updated", result.Updated, "skipped", result.Skipped, "failed", result.Failed)
	}
	return result, nil
}

func (b *Bot) logPRStateProgress(label string, result PRStateBackfillResult, total int) {
	if b == nil || b.log == nil || result.Scanned == 0 {
		return
	}
	if result.Scanned%prStateBackfillProgressEvery != 0 && result.Scanned != total {
		return
	}
	b.log.Info(label+" conversation PR states progress",
		"scanned", result.Scanned, "total", total, "remaining", total-result.Scanned,
		"updated", result.Updated, "skipped", result.Skipped, "failed", result.Failed)
}
