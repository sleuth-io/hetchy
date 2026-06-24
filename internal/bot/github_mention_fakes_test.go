package bot

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func webhookMentionThreadRow(threadID string) webhookRow {
	ts := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return webhookMentionThreadRowWithTimes(threadID, ts.Time, ts.Time)
}

func webhookMentionThreadRowWithTimes(threadID string, created, updated time.Time) webhookRow {
	return webhookRow{values: []any{
		"org1",
		"acme",
		"repo",
		githubMentionSubjectIssue,
		int32(7),
		threadID,
		pgtype.Timestamptz{Time: created, Valid: true},
		pgtype.Timestamptz{Time: updated, Valid: true},
	}}
}

func captureGitHubIssueComments(t *testing.T) (*http.Client, *[]string) {
	t.Helper()
	var bodies []string
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/issues/") || !strings.HasSuffix(r.URL.Path, "/comments") {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode comment payload: %v", err)
		}
		bodies = append(bodies, payload.Body)
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       io.NopCloser(strings.NewReader(`{"id":1}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	})}, &bodies
}
