package bot

import (
	"context"
	"strings"
	"sync"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

type githubMentionEmitter struct {
	log    logger
	client *github.Client
	owner  string
	repo   string
	number int
	runURL string
	once   sync.Once
}

type logger interface {
	Warn(msg string, args ...any)
}

func newGithubMentionEmitter(log logger, client *github.Client, owner, repo string, number int, runURL string) *githubMentionEmitter {
	return &githubMentionEmitter{log: log, client: client, owner: owner, repo: repo, number: number, runURL: runURL}
}

func (e *githubMentionEmitter) Start(blocks.Kind, string, map[string]any) string { return "" }
func (e *githubMentionEmitter) Append(string, string)                            {}
func (e *githubMentionEmitter) Done(string, string)                              {}
func (e *githubMentionEmitter) Fail(string, string)                              {}
func (e *githubMentionEmitter) Notify(string, string)                            {}

func (e *githubMentionEmitter) Result(title, body string) {
	e.post("Hetchy finished.\n\n" + strings.TrimSpace(body))
}

func (e *githubMentionEmitter) Error(title, body string) {
	msg := "Hetchy hit an error."
	if title = strings.TrimSpace(title); title != "" {
		msg += "\n\n" + title
	}
	if body = strings.TrimSpace(body); body != "" {
		msg += "\n\n" + body
	}
	e.post(msg)
}

func (e *githubMentionEmitter) post(body string) {
	e.once.Do(func() {
		if e.runURL != "" {
			body += "\n\nProgress: " + e.runURL
		}
		ctx, cancel := context.WithTimeout(context.Background(), githubMentionAckDeadline)
		defer cancel()
		_, _, err := e.client.Issues.CreateComment(ctx, e.owner, e.repo, e.number, &github.IssueComment{Body: github.String(truncateGitHubComment(body))})
		if err != nil && e.log != nil {
			e.log.Warn("github mention: terminal comment failed", "repo", e.owner+"/"+e.repo, "subject", e.number, "error", err)
		}
	})
}
