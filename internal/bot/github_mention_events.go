package bot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/go-github/v66/github"
)

func (b *Bot) handleIssueCommentEvent(ctx context.Context, body []byte, delivery string) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		Issue struct {
			Number      int    `json:"number"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			HTMLURL     string `json:"html_url"`
			PullRequest *struct {
				URL     string `json:"url"`
				HTMLURL string `json:"html_url"`
			} `json:"pull_request"`
		} `json:"issue"`
		Comment struct {
			ID                int64  `json:"id"`
			Body              string `json:"body"`
			HTMLURL           string `json:"html_url"`
			AuthorAssociation string `json:"author_association"`
			User              struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("issue_comment") {
			b.log.Error("github webhook: parse issue_comment event", "error", err)
		}
		return
	}
	if p.Action != "created" && p.Action != "edited" {
		return
	}
	directive, ok := githubMentionDirective(p.Comment.Body, githubMentionAliases(b.cfg.GitHubAppSlug))
	if !ok {
		return
	}
	owner, repo := webhookRepoSlug(p.Repository.Owner.Login, p.Repository.Name, p.Repository.FullName)
	if p.Installation.ID == 0 || owner == "" || repo == "" || p.Issue.Number <= 0 {
		return
	}
	subjectType := githubMentionSubjectIssue
	subjectURL := p.Issue.HTMLURL
	if p.Issue.PullRequest != nil {
		subjectType = githubMentionSubjectPullRequest
		if p.Issue.PullRequest.HTMLURL != "" {
			subjectURL = p.Issue.PullRequest.HTMLURL
		}
	}
	b.handleGithubMention(ctx, githubMentionEvent{
		DeliveryID:        delivery,
		RequestID:         githubMentionRequestID("github-comment", p.Comment.ID, ""),
		InstallationID:    p.Installation.ID,
		Owner:             owner,
		Repo:              repo,
		SubjectType:       subjectType,
		SubjectNumber:     p.Issue.Number,
		SubjectURL:        subjectURL,
		SubjectTitle:      p.Issue.Title,
		SubjectBody:       p.Issue.Body,
		CommentID:         p.Comment.ID,
		CommentURL:        p.Comment.HTMLURL,
		CommentBody:       p.Comment.Body,
		AuthorLogin:       p.Comment.User.Login,
		AuthorAssociation: p.Comment.AuthorAssociation,
		Directive:         directive,
		Source:            "issue_comment",
	})
}

func (b *Bot) handlePullRequestReviewCommentEvent(ctx context.Context, body []byte, delivery string) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		PullRequest *github.PullRequest `json:"pull_request"`
		Comment     struct {
			ID                int64  `json:"id"`
			Body              string `json:"body"`
			HTMLURL           string `json:"html_url"`
			Path              string `json:"path"`
			DiffHunk          string `json:"diff_hunk"`
			Line              int    `json:"line"`
			OriginalLine      int    `json:"original_line"`
			AuthorAssociation string `json:"author_association"`
			User              struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("pull_request_review_comment") {
			b.log.Error("github webhook: parse pull_request_review_comment event", "error", err)
		}
		return
	}
	if p.Action != "created" && p.Action != "edited" {
		return
	}
	directive, ok := githubMentionDirective(p.Comment.Body, githubMentionAliases(b.cfg.GitHubAppSlug))
	if !ok {
		return
	}
	owner, repo := webhookRepoSlug(p.Repository.Owner.Login, p.Repository.Name, p.Repository.FullName)
	if p.Installation.ID == 0 || owner == "" || repo == "" || p.PullRequest == nil || p.PullRequest.GetNumber() <= 0 {
		return
	}
	prURL := p.PullRequest.GetHTMLURL()
	if prURL == "" {
		prURL = canonicalGitHubPRURL(owner, repo, p.PullRequest.GetNumber())
	}
	b.handleGithubMention(ctx, githubMentionEvent{
		DeliveryID:        delivery,
		RequestID:         githubMentionRequestID("github-review-comment", p.Comment.ID, ""),
		InstallationID:    p.Installation.ID,
		Owner:             owner,
		Repo:              repo,
		SubjectType:       githubMentionSubjectPullRequest,
		SubjectNumber:     p.PullRequest.GetNumber(),
		SubjectURL:        prURL,
		SubjectTitle:      p.PullRequest.GetTitle(),
		SubjectBody:       p.PullRequest.GetBody(),
		PullRequest:       p.PullRequest,
		CommentID:         p.Comment.ID,
		CommentURL:        p.Comment.HTMLURL,
		CommentBody:       p.Comment.Body,
		CommentContext:    githubReviewCommentContext(p.Comment.Path, p.Comment.Line, p.Comment.OriginalLine, p.Comment.DiffHunk),
		AuthorLogin:       p.Comment.User.Login,
		AuthorAssociation: p.Comment.AuthorAssociation,
		Directive:         directive,
		Source:            "pull_request_review_comment",
	})
}

func githubReviewCommentContext(path string, line, originalLine int, diffHunk string) string {
	var b strings.Builder
	if path != "" {
		b.WriteString("Path: ")
		b.WriteString(path)
		b.WriteByte('\n')
	}
	if line > 0 {
		b.WriteString("Line: ")
		b.WriteString(strconv.Itoa(line))
		b.WriteByte('\n')
	} else if originalLine > 0 {
		b.WriteString("Original line: ")
		b.WriteString(strconv.Itoa(originalLine))
		b.WriteByte('\n')
	}
	if hunk := strings.TrimSpace(diffHunk); hunk != "" {
		b.WriteString("Diff hunk:\n")
		b.WriteString(truncate(hunk, 2000))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
