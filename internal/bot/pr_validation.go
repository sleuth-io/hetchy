package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v66/github"
)

type parsedPRURL struct {
	Owner  string
	Repo   string
	Number int
}

var errReportedPRNotVerified = errors.New("reported PR URL was not verified")

var lookupGitHubPullRequest = func(ctx context.Context, token, owner, repo string, number int) (*github.PullRequest, error) {
	cli := github.NewClient(&http.Client{Timeout: 15 * time.Second}).WithAuthToken(token)
	pr, _, err := cli.PullRequests.Get(ctx, owner, repo, number)
	return pr, err
}

func (b *Bot) validateReportedPR(ctx context.Context, repo repoCtx, expectedBranch, expectedBase, rawURL string) (string, error) {
	parsed, err := parseGitHubPRURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errReportedPRNotVerified, err)
	}
	if !sameGitHubSlug(parsed.Owner+"/"+parsed.Repo, repo.Slug) {
		return "", fmt.Errorf("%w: reported PR URL %q is for %s/%s, expected %s", errReportedPRNotVerified, rawURL, parsed.Owner, parsed.Repo, repo.Slug)
	}

	token, err := b.githubTokenForPRValidation(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errReportedPRNotVerified, err)
	}
	pr, err := lookupGitHubPullRequest(ctx, token, parsed.Owner, parsed.Repo, parsed.Number)
	if err != nil {
		return "", fmt.Errorf("%w: verify reported PR %q: %w", errReportedPRNotVerified, rawURL, err)
	}
	if pr == nil {
		return "", fmt.Errorf("%w: verify reported PR %q: empty GitHub response", errReportedPRNotVerified, rawURL)
	}

	headRepo := ""
	if pr.GetHead() != nil && pr.GetHead().GetRepo() != nil {
		headRepo = pr.GetHead().GetRepo().GetFullName()
	}
	headBranch := ""
	if pr.GetHead() != nil {
		headBranch = pr.GetHead().GetRef()
	}
	if !sameGitHubSlug(headRepo, repo.Slug) || headBranch != expectedBranch {
		return "", fmt.Errorf("%w: reported PR %q has head %s/%s, expected %s/%s", errReportedPRNotVerified, rawURL, headRepo, headBranch, repo.Slug, expectedBranch)
	}

	if expectedBase != "" {
		baseBranch := ""
		if pr.GetBase() != nil {
			baseBranch = pr.GetBase().GetRef()
		}
		if baseBranch != expectedBase {
			return "", fmt.Errorf("%w: reported PR %q targets base %q, expected %q", errReportedPRNotVerified, rawURL, baseBranch, expectedBase)
		}
	}

	if pr.GetHTMLURL() != "" {
		return pr.GetHTMLURL(), nil
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo.Slug, parsed.Number), nil
}

func (b *Bot) githubTokenForPRValidation(ctx context.Context, repo repoCtx) (string, error) {
	if b != nil && b.app != nil && repo.InstallID != 0 && repo.RepoID != 0 {
		token, _, err := b.app.InstallationToken(ctx, repo.InstallID, []int64{repo.RepoID})
		if err == nil {
			return token, nil
		}
		if repo.GitHubToken == "" {
			return "", fmt.Errorf("refresh GitHub token for PR validation: %w", err)
		}
		if b.log != nil {
			b.log.Warn("refresh GitHub token for PR validation failed; falling back to existing token",
				"repo", repo.Slug, "error", err)
		}
	}
	if repo.GitHubToken == "" {
		return "", errors.New("missing GitHub token for PR validation")
	}
	return repo.GitHubToken, nil
}

func parseGitHubPRURL(raw string) (parsedPRURL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return parsedPRURL{}, fmt.Errorf("parse reported PR URL %q: %w", raw, err)
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return parsedPRURL{}, fmt.Errorf("reported PR URL %q is not an https://github.com pull request URL", raw)
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return parsedPRURL{}, fmt.Errorf("reported PR URL %q is not a GitHub pull request URL", raw)
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return parsedPRURL{}, fmt.Errorf("parse reported PR owner %q: %w", parts[0], err)
	}
	repo, err := url.PathUnescape(parts[1])
	if err != nil {
		return parsedPRURL{}, fmt.Errorf("parse reported PR repo %q: %w", parts[1], err)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return parsedPRURL{}, fmt.Errorf("reported PR URL %q has invalid pull request number", raw)
	}
	return parsedPRURL{Owner: owner, Repo: repo, Number: number}, nil
}

func sameGitHubSlug(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
