package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestWebhookRepoSlugUsesFullNameFallback(t *testing.T) {
	owner, repo := webhookRepoSlug("", " ", " sleuth-io/hetchy ")
	if owner != "sleuth-io" || repo != "hetchy" {
		t.Fatalf("webhookRepoSlug fallback = %q/%q, want sleuth-io/hetchy", owner, repo)
	}

	owner, repo = webhookRepoSlug("octo", "repo", "ignored/full-name")
	if owner != "octo" || repo != "repo" {
		t.Fatalf("webhookRepoSlug explicit = %q/%q, want octo/repo", owner, repo)
	}
}

func TestHandlePullRequestEventSavesCanonicalPRState(t *testing.T) {
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(42, "org1")
	fake.exec["SaveConversationPRStateByURL"] = webhookExecResult{rows: 3}

	b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
	b.handlePullRequestEvent(context.Background(), []byte(`{
		"action": "closed",
		"installation": {"id": 42},
		"repository": {"full_name": "sleuth-io/hetchy"},
		"pull_request": {"number": 12, "state": "closed", "merged": true}
	}`))

	call := fake.onlyExecCall(t, "SaveConversationPRStateByURL")
	assertWebhookArg(t, call.args, 0, "closed")
	assertWebhookArg(t, call.args, 1, true)
	assertWebhookArg(t, call.args, 4, "org1")
	assertWebhookArg(t, call.args, 5, "sleuth-io")
	assertWebhookArg(t, call.args, 6, "hetchy")
	assertWebhookArg(t, call.args, 7, "https://github.com/sleuth-io/hetchy/pull/12")
	assertWebhookArg(t, call.args, 8, int32(12))
}

func TestHandlePullRequestEventSkipsMalformedPayloadsBeforeDB(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "missing installation", body: `{"repository":{"full_name":"sleuth-io/hetchy"},"pull_request":{"number":12}}`},
		{name: "missing pull request", body: `{"installation":{"id":42},"repository":{"full_name":"sleuth-io/hetchy"}}`},
		{name: "missing repository slug", body: `{"installation":{"id":42},"repository":{},"pull_request":{"number":12}}`},
		{name: "zero pr number", body: `{"installation":{"id":42},"repository":{"full_name":"sleuth-io/hetchy"},"pull_request":{"number":0}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newWebhookFakeDB()
			b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
			b.handlePullRequestEvent(context.Background(), []byte(tt.body))
			if len(fake.queryRowCalls) != 0 || len(fake.execCalls) != 0 {
				t.Fatalf("malformed payload touched DB: query rows=%d execs=%d", len(fake.queryRowCalls), len(fake.execCalls))
			}
		})
	}
}

func TestWebhookEventActionGuardsSkipDB(t *testing.T) {
	tests := []struct {
		name   string
		handle func(*Bot)
	}{
		{
			name: "pull request review edited without mention skips DB",
			handle: func(b *Bot) {
				b.handlePullRequestReviewEvent(context.Background(), []byte(`{
					"action": "edited",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"},
					"pull_request": {"number": 12}
				}`), "")
			},
		},
		{
			name: "check run only reacts to completed",
			handle: func(b *Bot) {
				b.handleCheckRunEvent(context.Background(), []byte(`{
					"action": "created",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"},
					"check_run": {"head_sha": "abc123"}
				}`))
			},
		},
		{
			name: "check suite only reacts to completed",
			handle: func(b *Bot) {
				b.handleCheckSuiteEvent(context.Background(), []byte(`{
					"action": "rerequested",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"},
					"check_suite": {"head_sha": "abc123"}
				}`))
			},
		},
		{
			name: "status requires sha",
			handle: func(b *Bot) {
				b.handleStatusEvent(context.Background(), []byte(`{
					"sha": " ",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"}
				}`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newWebhookFakeDB()
			b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
			tt.handle(b)
			if len(fake.queryRowCalls) != 0 || len(fake.queryCalls) != 0 || len(fake.execCalls) != 0 {
				t.Fatalf("guarded event touched DB: query rows=%d queries=%d execs=%d", len(fake.queryRowCalls), len(fake.queryCalls), len(fake.execCalls))
			}
		})
	}
}

func TestHandleCheckAndStatusEventsResolveInstallation(t *testing.T) {
	tests := []struct {
		name   string
		handle func(*Bot)
	}{
		{
			name: "pull request review submitted",
			handle: func(b *Bot) {
				b.handlePullRequestReviewEvent(context.Background(), []byte(`{
					"action": "submitted",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"},
					"pull_request": {"number": 12}
				}`), "")
			},
		},
		{
			name: "check run completed with pull request",
			handle: func(b *Bot) {
				b.handleCheckRunEvent(context.Background(), []byte(`{
					"action": "completed",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"},
					"check_run": {"pull_requests": [{"number": 12}]}
				}`))
			},
		},
		{
			name: "check suite completed with pull request",
			handle: func(b *Bot) {
				b.handleCheckSuiteEvent(context.Background(), []byte(`{
					"action": "completed",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"},
					"check_suite": {"pull_requests": [{"number": 12}]}
				}`))
			},
		},
		{
			name: "status with sha",
			handle: func(b *Bot) {
				b.handleStatusEvent(context.Background(), []byte(`{
					"sha": "abc123",
					"installation": {"id": 42},
					"repository": {"full_name": "sleuth-io/hetchy"}
				}`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newWebhookFakeDB()
			fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(42, "org1")
			b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
			tt.handle(b)
			call := fake.onlyQueryRowCall(t, "GetGithubInstallation")
			assertWebhookArg(t, call.args, 0, int64(42))
		})
	}
}

func TestHandleInstallationEventDeletedRemovesRecordedInstallation(t *testing.T) {
	fake := newWebhookFakeDB()
	fake.exec["DeleteGithubInstallation"] = webhookExecResult{rows: 1}
	b := &Bot{
		log:   discardLogger(),
		app:   freshGithubAppForTest(t, "wh-secret"),
		store: &db.Store{Queries: sqlc.New(fake)},
	}

	b.handleInstallationEvent(context.Background(), []byte(`{
		"action": "deleted",
		"installation": {"id": 42}
	}`))

	call := fake.onlyExecCall(t, "DeleteGithubInstallation")
	assertWebhookArg(t, call.args, 0, int64(42))
}

type webhookFakeDB struct {
	queryRow map[string]pgx.Row
	query    map[string]pgx.Rows
	exec     map[string]webhookExecResult

	queryRowCalls []webhookDBCall
	queryCalls    []webhookDBCall
	execCalls     []webhookDBCall
}

type webhookDBCall struct {
	sql  string
	args []any
}

type webhookExecResult struct {
	rows int64
	err  error
}

func newWebhookFakeDB() *webhookFakeDB {
	return &webhookFakeDB{
		queryRow: map[string]pgx.Row{},
		query:    map[string]pgx.Rows{},
		exec:     map[string]webhookExecResult{},
	}
}

func (f *webhookFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, webhookDBCall{sql: sql, args: args})
	for fragment, result := range f.exec {
		if strings.Contains(sql, fragment) {
			if result.err != nil {
				return pgconn.CommandTag{}, result.err
			}
			return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", result.rows)), nil
		}
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected exec: %s", sql)
}

func (f *webhookFakeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalls = append(f.queryCalls, webhookDBCall{sql: sql, args: args})
	for fragment, rows := range f.query {
		if strings.Contains(sql, fragment) {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("unexpected query: %s", sql)
}

func (f *webhookFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queryRowCalls = append(f.queryRowCalls, webhookDBCall{sql: sql, args: args})
	for fragment, row := range f.queryRow {
		if strings.Contains(sql, fragment) {
			return row
		}
	}
	return webhookRow{err: fmt.Errorf("unexpected query row: %s", sql)}
}

func (f *webhookFakeDB) onlyQueryRowCall(t *testing.T, fragment string) webhookDBCall {
	t.Helper()
	var matches []webhookDBCall
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query row calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *webhookFakeDB) onlyExecCall(t *testing.T, fragment string) webhookDBCall {
	t.Helper()
	var matches []webhookDBCall
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("exec calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

type webhookRow struct {
	err    error
	values []any
}

func (r webhookRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(r.values))
	}
	for i := range dest {
		if err := assignWebhookScanValue(dest[i], r.values[i]); err != nil {
			return fmt.Errorf("scan dest %d: %w", i, err)
		}
	}
	return nil
}

func assignWebhookScanValue(dest, value any) error {
	switch d := dest.(type) {
	case *int64:
		v, ok := value.(int64)
		if !ok {
			return fmt.Errorf("got %T, want int64", value)
		}
		*d = v
	case *int32:
		v, ok := value.(int32)
		if !ok {
			return fmt.Errorf("got %T, want int32", value)
		}
		*d = v
	case *string:
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("got %T, want string", value)
		}
		*d = v
	case *pgtype.Timestamptz:
		v, ok := value.(pgtype.Timestamptz)
		if !ok {
			return fmt.Errorf("got %T, want pgtype.Timestamptz", value)
		}
		*d = v
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}

func webhookInstallationRow(installationID int64, orgID string) webhookRow {
	return webhookRow{values: []any{
		installationID,
		orgID,
		"sleuth-io",
		"Organization",
		int64(99),
		pgtype.Timestamptz{},
		pgtype.Timestamptz{},
		pgtype.Timestamptz{},
	}}
}

func assertWebhookArg(t *testing.T, args []any, idx int, want any) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	if got := args[idx]; got != want {
		t.Fatalf("arg[%d] = %#v (%T), want %#v (%T)", idx, got, got, want, want)
	}
}
