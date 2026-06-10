package bot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/linear"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// fakeLinearSessionStore is an in-memory linearSessionStore.
type fakeLinearSessionStore struct {
	mu        sync.Mutex
	rows      map[string]sqlc.LinearAgentSession
	insertErr error
	deleteErr error
}

func newFakeLinearSessionStore() *fakeLinearSessionStore {
	return &fakeLinearSessionStore{rows: map[string]sqlc.LinearAgentSession{}}
}

func (f *fakeLinearSessionStore) Get(_ context.Context, sessionID string) (sqlc.LinearAgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[sessionID]
	if !ok {
		return sqlc.LinearAgentSession{}, errors.New("no rows")
	}
	return row, nil
}

func (f *fakeLinearSessionStore) Insert(_ context.Context, arg sqlc.InsertLinearAgentSessionParams) (sqlc.LinearAgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return sqlc.LinearAgentSession{}, f.insertErr
	}
	// Keep-first on conflict, like the SQL.
	if existing, ok := f.rows[arg.AgentSessionID]; ok {
		return existing, nil
	}
	row := sqlc.LinearAgentSession{
		AgentSessionID:  arg.AgentSessionID,
		OrgID:           arg.OrgID,
		ThreadID:        arg.ThreadID,
		IssueID:         arg.IssueID,
		IssueIdentifier: arg.IssueIdentifier,
		IssueUrl:        arg.IssueUrl,
	}
	f.rows[arg.AgentSessionID] = row
	return row, nil
}

func (f *fakeLinearSessionStore) ListByIssue(_ context.Context, orgID, issueID string) ([]sqlc.LinearAgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sqlc.LinearAgentSession
	for _, row := range f.rows {
		if row.OrgID == orgID && row.IssueID == issueID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeLinearSessionStore) DeleteBefore(_ context.Context, cutoff time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	var deleted int64
	for id, row := range f.rows {
		if row.CreatedAt.Valid && row.CreatedAt.Time.Before(cutoff) {
			delete(f.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

func createdEvent(sessionID, issueID string) linear.AgentSessionEvent {
	return linear.AgentSessionEvent{
		WebhookEnvelope: linear.WebhookEnvelope{
			Type:           linear.WebhookTypeAgentSession,
			Action:         linear.AgentSessionActionCreated,
			OrganizationID: "ws-1",
		},
		AgentSession: linear.AgentSession{
			ID: sessionID,
			Issue: &linear.SessionIssue{
				ID: issueID, Identifier: "ENG-1", Title: "Fix login",
				URL: "https://linear.app/acme/issue/ENG-1",
			},
		},
		PromptContext: "Fix the login bug",
	}
}

func TestResolveLinearThreadFreshSession(t *testing.T) {
	sessions := newFakeLinearSessionStore()
	b := &Bot{
		log:            discardLogger(),
		convs:          &fakeConversationStore{getErr: convstore.ErrNotFound},
		linearSessions: sessions,
	}
	threadID, requestID, fresh, ok := b.resolveLinearThread("org-1", createdEvent("sess-1", "iss-1"))
	if !ok {
		t.Fatal("resolve failed")
	}
	if threadID != "linear-sess-1" || !fresh {
		t.Fatalf("threadID=%q fresh=%v, want fresh linear-sess-1", threadID, fresh)
	}
	if requestID != "linear-sess-1" {
		t.Fatalf("requestID = %q", requestID)
	}
	row, err := sessions.Get(context.Background(), "sess-1")
	if err != nil || row.ThreadID != "linear-sess-1" || row.IssueID != "iss-1" {
		t.Fatalf("stored row = %+v err=%v", row, err)
	}
}

func TestResolveLinearThreadResumesOpenPR(t *testing.T) {
	sessions := newFakeLinearSessionStore()
	_, _ = sessions.Insert(context.Background(), sqlc.InsertLinearAgentSessionParams{
		AgentSessionID: "sess-old", OrgID: "org-1", ThreadID: "linear-sess-old", IssueID: "iss-1",
	})
	// The prior session's conversation has an open PR.
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID: "org-1", ThreadID: "linear-sess-old", PRURL: "https://github.com/acme/site/pull/9",
	}}
	b := &Bot{log: discardLogger(), convs: convs, linearSessions: sessions}

	threadID, _, fresh, ok := b.resolveLinearThread("org-1", createdEvent("sess-new", "iss-1"))
	if !ok {
		t.Fatal("resolve failed")
	}
	if threadID != "linear-sess-old" || fresh {
		t.Fatalf("threadID=%q fresh=%v, want resumed linear-sess-old", threadID, fresh)
	}
	// The new session maps onto the resumed thread.
	row, err := sessions.Get(context.Background(), "sess-new")
	if err != nil || row.ThreadID != "linear-sess-old" {
		t.Fatalf("new session row = %+v err=%v", row, err)
	}
}

func TestResolveLinearThreadIgnoresMergedPR(t *testing.T) {
	sessions := newFakeLinearSessionStore()
	_, _ = sessions.Insert(context.Background(), sqlc.InsertLinearAgentSessionParams{
		AgentSessionID: "sess-old", OrgID: "org-1", ThreadID: "linear-sess-old", IssueID: "iss-1",
	})
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID: "org-1", ThreadID: "linear-sess-old",
		PRURL: "https://github.com/acme/site/pull/9", PRMerged: true,
	}}
	b := &Bot{log: discardLogger(), convs: convs, linearSessions: sessions}

	threadID, _, fresh, ok := b.resolveLinearThread("org-1", createdEvent("sess-new", "iss-1"))
	if !ok {
		t.Fatal("resolve failed")
	}
	if threadID != "linear-sess-new" || !fresh {
		t.Fatalf("threadID=%q fresh=%v, want fresh thread (PR merged)", threadID, fresh)
	}
}

func TestResolveLinearThreadPromptedReusesMapping(t *testing.T) {
	sessions := newFakeLinearSessionStore()
	_, _ = sessions.Insert(context.Background(), sqlc.InsertLinearAgentSessionParams{
		AgentSessionID: "sess-1", OrgID: "org-1", ThreadID: "linear-sess-1", IssueID: "iss-1",
	})
	b := &Bot{
		log:            discardLogger(),
		convs:          &fakeConversationStore{getErr: convstore.ErrNotFound},
		linearSessions: sessions,
	}
	ev := createdEvent("sess-1", "iss-1")
	ev.Action = linear.AgentSessionActionPrompted
	ev.AgentActivity = &linear.AgentActivity{ID: "act-7", Content: linear.ActivityContent{Type: "prompt", Body: "also fix signup"}}

	threadID, requestID, fresh, ok := b.resolveLinearThread("org-1", ev)
	if !ok {
		t.Fatal("resolve failed")
	}
	if threadID != "linear-sess-1" || fresh {
		t.Fatalf("threadID=%q fresh=%v, want existing mapping", threadID, fresh)
	}
	if requestID != "linear-act-7" {
		t.Fatalf("requestID = %q, want per-activity id", requestID)
	}
}

func TestHandleLinearAgentSessionEventAcksAndRuns(t *testing.T) {
	cli := &fakeLinearAPI{}
	orgs := &fakeOrgStore{getByLinearConfig: orgcfg.Config{
		OrgID:             "org-1",
		LinearAccessToken: "lin-token",
		LinearWorkspaceID: "ws-1",
		// No Anthropic credentials: HandleRequest fails fast with a
		// missing-credentials error, which the emitter must surface as
		// an error activity on the session.
	}}
	sessions := newFakeLinearSessionStore()
	b := &Bot{
		log:               discardLogger(),
		cfg:               Config{Env: "dev", WebPort: "8080"},
		orgs:              orgs,
		convs:             &fakeConversationStore{getErr: convstore.ErrNotFound},
		linearSessions:    sessions,
		live:              newLiveRegistry(),
		newLinearClientFn: func(string) linearAPI { return cli },
	}

	b.handleLinearAgentSessionEvent(createdEvent("sess-1", "iss-1"))

	// The run itself executes on a detached goroutine; wait for its
	// terminal activity (missing credentials → error) to arrive.
	waitForLinearActivity(t, cli, func(a recordedActivity) bool { return a.content.Type == "error" })

	acts := cli.snapshotActivities()
	if acts[0].content.Type != "thought" || acts[0].ephemeral {
		t.Fatalf("first activity = %+v, want durable ack thought", acts[0])
	}
	if len(cli.urls) == 0 || cli.urls[0][0].Label != "Hetchy run" {
		t.Fatalf("external urls = %+v, want Hetchy run link", cli.urls)
	}
	if _, err := sessions.Get(context.Background(), "sess-1"); err != nil {
		t.Fatalf("session mapping not stored: %v", err)
	}
	// MoveIssueToStarted also runs on a detached goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for {
		cli.mu.Lock()
		moved := len(cli.started) > 0
		cli.mu.Unlock()
		if moved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("issue was never moved to started")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForLinearActivity polls until an activity matching want has been
// recorded, failing the test after a deadline.
func waitForLinearActivity(t *testing.T, cli *fakeLinearAPI, want func(recordedActivity) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if slices.ContainsFunc(cli.snapshotActivities(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected activity never arrived; have %+v", cli.snapshotActivities())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCleanupLinearSessionsDeletesExpired(t *testing.T) {
	sessions := newFakeLinearSessionStore()
	old := sqlc.LinearAgentSession{AgentSessionID: "sess-old", OrgID: "org-1", ThreadID: "t-old"}
	old.CreatedAt.Time = time.Now().Add(-linearSessionRetention - time.Hour)
	old.CreatedAt.Valid = true
	recent := sqlc.LinearAgentSession{AgentSessionID: "sess-new", OrgID: "org-1", ThreadID: "t-new"}
	recent.CreatedAt.Time = time.Now()
	recent.CreatedAt.Valid = true
	sessions.rows["sess-old"] = old
	sessions.rows["sess-new"] = recent

	b := &Bot{log: discardLogger(), linearSessions: sessions}
	b.cleanupLinearSessions(context.Background())

	if _, err := sessions.Get(context.Background(), "sess-old"); err == nil {
		t.Fatal("expired session survived cleanup")
	}
	if _, err := sessions.Get(context.Background(), "sess-new"); err != nil {
		t.Fatal("recent session was deleted")
	}
}

func TestCleanupLinearSessionsToleratesError(t *testing.T) {
	sessions := newFakeLinearSessionStore()
	sessions.deleteErr = errors.New("db down")
	b := &Bot{log: discardLogger(), linearSessions: sessions}
	// Must not panic; failure is logged and the next sweep retries.
	b.cleanupLinearSessions(context.Background())
}

func TestExtractLinearRepoMention(t *testing.T) {
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		{"Fix the bug in https://github.com/acme/site please", "acme/site", true},
		{"See github.com/acme/site.git for the code", "acme/site", true},
		// Bare owner/name and file paths must NOT match — prompt
		// context is full of them.
		{"Update internal/bot to handle ENG-42/login", "", false},
		{"plain text with no repo", "", false},
	}
	for _, tc := range cases {
		got, ok := extractLinearRepoMention(tc.text)
		if got != tc.want || ok != tc.ok {
			t.Errorf("extractLinearRepoMention(%q) = (%q, %v), want (%q, %v)", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHandleLinearAgentSessionEventNoOrg(t *testing.T) {
	cli := &fakeLinearAPI{}
	b := &Bot{
		log:               discardLogger(),
		cfg:               Config{Env: "dev", WebPort: "8080"},
		orgs:              &fakeOrgStore{getByLinearErr: orgcfg.ErrNotFound},
		newLinearClientFn: func(string) linearAPI { return cli },
	}
	b.handleLinearAgentSessionEvent(createdEvent("sess-1", "iss-1"))
	if got := len(cli.snapshotActivities()); got != 0 {
		t.Fatalf("activities = %d, want none when org unknown", got)
	}
}

func TestHandleLinearAgentSessionEventEmptyPromptAsksForText(t *testing.T) {
	cli := &fakeLinearAPI{}
	orgs := &fakeOrgStore{getByLinearConfig: orgcfg.Config{
		OrgID: "org-1", LinearAccessToken: "lin-token",
	}}
	b := &Bot{
		log:               discardLogger(),
		cfg:               Config{Env: "dev", WebPort: "8080"},
		orgs:              orgs,
		convs:             &fakeConversationStore{getErr: convstore.ErrNotFound},
		linearSessions:    newFakeLinearSessionStore(),
		live:              newLiveRegistry(),
		newLinearClientFn: func(string) linearAPI { return cli },
	}
	ev := createdEvent("sess-1", "iss-1")
	ev.PromptContext = ""
	ev.AgentSession.Issue = &linear.SessionIssue{ID: "iss-1"}
	b.handleLinearAgentSessionEvent(ev)

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "elicitation" {
		t.Fatalf("activities = %+v, want a single elicitation", acts)
	}
}

func TestHandleLinearAgentSessionEventInFlightRejectsBeforeAck(t *testing.T) {
	cli := &fakeLinearAPI{}
	orgs := &fakeOrgStore{getByLinearConfig: orgcfg.Config{
		OrgID: "org-1", LinearAccessToken: "lin-token",
	}}
	live := newLiveRegistry()
	// Occupy the thread's live-run slot before the event arrives.
	prior, registered := live.RegisterIfAbsent(context.Background(), "org-1", "linear-sess-1")
	if !registered {
		t.Fatal("could not pre-register live run")
	}
	defer live.Done("org-1", "linear-sess-1", prior)

	b := &Bot{
		log:               discardLogger(),
		cfg:               Config{Env: "dev", WebPort: "8080"},
		orgs:              orgs,
		convs:             &fakeConversationStore{getErr: convstore.ErrNotFound},
		linearSessions:    newFakeLinearSessionStore(),
		live:              live,
		newLinearClientFn: func(string) linearAPI { return cli },
	}
	b.handleLinearAgentSessionEvent(createdEvent("sess-1", "iss-1"))

	// Exactly one activity: the rejection. No optimistic "On it" ack
	// that the rejection would immediately contradict.
	acts := cli.snapshotActivities()
	if len(acts) != 1 {
		t.Fatalf("activities = %+v, want exactly one rejection", acts)
	}
	if acts[0].content.Type != "elicitation" || !strings.Contains(acts[0].content.Body, "already in flight") {
		t.Fatalf("activity = %+v, want already-in-flight elicitation", acts[0])
	}
}

func TestLinearDirectiveStripsMention(t *testing.T) {
	ev := createdEvent("sess-1", "iss-1")
	ev.AgentSession.Comment = &struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	}{ID: "c1", Body: "@hetchy please fix this in the repo."}
	if got := linearDirective(ev); got != "please fix this in the repo." {
		t.Fatalf("directive = %q", got)
	}
}

func TestLinearPromptTextCreated(t *testing.T) {
	ev := createdEvent("sess-1", "iss-1")
	ev.PromptContext = `<issue identifier="ENG-1">raw xml blob</issue>`
	ev.AgentSession.Issue.Description = "The scrollbar should be hidden."
	ev.AgentSession.Comment = &struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	}{ID: "c1", Body: "@hetchy please fix this with the frontend bot."}

	got := linearPromptText(ev, linearDirective(ev))
	for _, want := range []string{
		"Linear issue ENG-1: Fix login",
		"The scrollbar should be hidden.",
		"Issue link: https://linear.app/acme/issue/ENG-1",
		"Request from the Linear thread:\nplease fix this with the frontend bot.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<issue") {
		t.Fatalf("prompt leaked raw promptContext markup:\n%s", got)
	}
}

func TestLinearPromptTextPromptedUsesDirectiveOnly(t *testing.T) {
	ev := createdEvent("sess-1", "iss-1")
	ev.Action = linear.AgentSessionActionPrompted
	ev.AgentActivity = &linear.AgentActivity{ID: "act-1", Content: linear.ActivityContent{Type: "prompt", Body: "also fix signup"}}
	got := linearPromptText(ev, linearDirective(ev))
	if got != "also fix signup" {
		t.Fatalf("prompt = %q, want the follow-up text only", got)
	}
}

func TestLinearPromptTextFallsBackToPromptContext(t *testing.T) {
	ev := linear.AgentSessionEvent{
		WebhookEnvelope: linear.WebhookEnvelope{Action: linear.AgentSessionActionCreated},
		PromptContext:   "raw context",
	}
	if got := linearPromptText(ev, ""); got != "raw context" {
		t.Fatalf("prompt = %q, want promptContext fallback", got)
	}
}

func TestExtractLinearAgent(t *testing.T) {
	b := &Bot{log: discardLogger(), agents: agents.NewStore(nil)}
	cases := []struct {
		text string
		want string
	}{
		{"please fix this in https://github.com/a/b with the Frontend Bot.", "alice"},
		{"please fix this using Alice. Thanks a lot", "alice"},
		{"please fix this with the new login flow", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := b.extractLinearAgent(context.Background(), "org", tc.text); got != tc.want {
			t.Errorf("extractLinearAgent(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestHandleLinearStopRequestNoLiveRunPostsConfirmation(t *testing.T) {
	cli := &fakeLinearAPI{}
	sessions := newFakeLinearSessionStore()
	_, _ = sessions.Insert(context.Background(), sqlc.InsertLinearAgentSessionParams{
		AgentSessionID: "sess-1", OrgID: "org-1", ThreadID: "linear-sess-1",
	})
	b := &Bot{log: discardLogger(), linearSessions: sessions, live: newLiveRegistry()}

	b.handleLinearStopRequest(orgcfg.Config{OrgID: "org-1"}, cli, "sess-1")

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "response" {
		t.Fatalf("activities = %+v, want one response confirmation", acts)
	}
	if !strings.Contains(acts[0].content.Body, "Stopped") {
		t.Fatalf("body = %q", acts[0].content.Body)
	}
}

func TestHandleLinearStopRequestCancelsLiveRun(t *testing.T) {
	cli := &fakeLinearAPI{}
	sessions := newFakeLinearSessionStore()
	_, _ = sessions.Insert(context.Background(), sqlc.InsertLinearAgentSessionParams{
		AgentSessionID: "sess-1", OrgID: "org-1", ThreadID: "linear-sess-1",
	})
	b := &Bot{log: discardLogger(), linearSessions: sessions, live: newLiveRegistry()}
	run, registered := b.live.RegisterIfAbsent(context.Background(), "org-1", "linear-sess-1")
	if !registered {
		t.Fatal("could not register live run")
	}
	defer b.live.Done("org-1", "linear-sess-1", run)

	b.handleLinearStopRequest(orgcfg.Config{OrgID: "org-1"}, cli, "sess-1")

	if run.Context().Err() == nil {
		t.Fatal("live run context was not cancelled")
	}
	// The cancelled run's own emitter posts the terminal response; the
	// stop handler must not double-confirm.
	if got := len(cli.snapshotActivities()); got != 0 {
		t.Fatalf("activities = %d, want 0 direct posts when a live run was cancelled", got)
	}
}

func TestHandleLinearAgentSessionEventStopSignal(t *testing.T) {
	cli := &fakeLinearAPI{}
	orgs := &fakeOrgStore{getByLinearConfig: orgcfg.Config{
		OrgID: "org-1", LinearAccessToken: "lin-token",
	}}
	b := &Bot{
		log:               discardLogger(),
		cfg:               Config{Env: "dev", WebPort: "8080"},
		orgs:              orgs,
		linearSessions:    newFakeLinearSessionStore(),
		live:              newLiveRegistry(),
		newLinearClientFn: func(string) linearAPI { return cli },
	}
	ev := createdEvent("sess-1", "iss-1")
	ev.Action = linear.AgentSessionActionPrompted
	ev.AgentActivity = &linear.AgentActivity{ID: "act-1", Signal: linear.SignalStop}

	b.handleLinearAgentSessionEvent(ev)

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "response" {
		t.Fatalf("activities = %+v, want one stop confirmation response", acts)
	}
}
