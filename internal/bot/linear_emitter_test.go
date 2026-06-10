package bot

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/linear"
)

// fakeLinearAPI records every call the emitter and webhook handler
// make against the Linear API.
type fakeLinearAPI struct {
	mu sync.Mutex

	identity    linear.Identity
	identityErr error

	activities []recordedActivity
	urls       [][]linear.ExternalURL
	started    []string

	activityErr error
}

type recordedActivity struct {
	sessionID string
	content   linear.ActivityContent
	ephemeral bool
}

func (f *fakeLinearAPI) Identity(context.Context) (linear.Identity, error) {
	return f.identity, f.identityErr
}

func (f *fakeLinearAPI) CreateActivity(_ context.Context, sessionID string, content linear.ActivityContent, ephemeral bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activities = append(f.activities, recordedActivity{sessionID: sessionID, content: content, ephemeral: ephemeral})
	return f.activityErr
}

func (f *fakeLinearAPI) AddExternalURLs(_ context.Context, _ string, urls []linear.ExternalURL) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, urls)
	return nil
}

func (f *fakeLinearAPI) MoveIssueToStarted(_ context.Context, issueID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, issueID)
	return nil
}

func (f *fakeLinearAPI) snapshotActivities() []recordedActivity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedActivity(nil), f.activities...)
}

func newTestLinearEmitter(cli *fakeLinearAPI) *linearEmitter {
	e := newLinearEmitter(discardLogger(), cli, "sess-1", "https://app.example/?session=t1", "fix the login bug")
	// Disable throttling so every refresh posts deterministically.
	e.minInterval = -1
	return e
}

func TestLinearEmitterToolUseStreamsEphemeralThoughts(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	id := e.Start(blocks.KindToolUse, "Reading internal/bot/web.go", nil)
	e.Done(id, "")

	acts := cli.snapshotActivities()
	if len(acts) == 0 {
		t.Fatal("no activities posted")
	}
	for _, a := range acts {
		if !a.ephemeral || a.content.Type != "thought" {
			t.Fatalf("activity = %+v, want ephemeral thought", a)
		}
		if a.sessionID != "sess-1" {
			t.Fatalf("sessionID = %q", a.sessionID)
		}
	}
	last := acts[len(acts)-1]
	if !strings.Contains(last.content.Body, "Read 1") {
		t.Fatalf("final progress body = %q, want Read counter", last.content.Body)
	}
}

func TestLinearEmitterNotifyQuestionBecomesElicitation(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	e.Notify("Which repository?", "Reply with `owner/name`.")

	acts := cli.snapshotActivities()
	if len(acts) != 1 {
		t.Fatalf("activities = %d, want 1", len(acts))
	}
	a := acts[0]
	if a.content.Type != "elicitation" || a.ephemeral {
		t.Fatalf("activity = %+v, want durable elicitation", a)
	}
	if !strings.Contains(a.content.Body, "Which repository?") {
		t.Fatalf("body = %q", a.content.Body)
	}
}

func TestLinearEmitterNotifyStatusBecomesThought(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	e.Notify("Starting", "Spinning up a sandbox.")

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "thought" || acts[0].ephemeral {
		t.Fatalf("activities = %+v, want one durable thought", acts)
	}
}

func TestLinearEmitterResultPostsResponseAndPRLink(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	e.Result("Done", "Opened https://github.com/acme/site/pull/42 for review.")

	acts := cli.snapshotActivities()
	if len(acts) != 1 {
		t.Fatalf("activities = %d, want 1", len(acts))
	}
	a := acts[0]
	if a.content.Type != "response" || a.ephemeral {
		t.Fatalf("activity = %+v, want durable response", a)
	}
	if !strings.Contains(a.content.Body, "fix the login bug") {
		t.Fatalf("body missing request recap: %q", a.content.Body)
	}
	if !strings.Contains(a.content.Body, "https://app.example/?session=t1") {
		t.Fatalf("body missing run link: %q", a.content.Body)
	}
	if len(cli.urls) != 1 || cli.urls[0][0].URL != "https://github.com/acme/site/pull/42" {
		t.Fatalf("external urls = %+v, want PR link", cli.urls)
	}
	if cli.urls[0][0].Label != "Pull request" {
		t.Fatalf("label = %q", cli.urls[0][0].Label)
	}
}

func TestLinearEmitterErrorPostsErrorActivity(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	e.Error("Run failed", "sandbox exploded")

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "error" {
		t.Fatalf("activities = %+v, want one error", acts)
	}
	if !strings.Contains(acts[0].content.Body, "sandbox exploded") {
		t.Fatalf("body = %q", acts[0].content.Body)
	}
}

func TestLinearEmitterTerminalBlocksViaStartDone(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	id := e.Start(blocks.KindResult, "Done", nil)
	e.Append(id, "PR ready: https://github.com/acme/site/pull/7")
	e.Done(id, "")

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "response" {
		t.Fatalf("activities = %+v, want one response", acts)
	}
	if !strings.Contains(acts[0].content.Body, "pull/7") {
		t.Fatalf("body = %q", acts[0].content.Body)
	}
}

func TestLinearEmitterFailOnResultBlockPostsError(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	id := e.Start(blocks.KindResult, "Done", nil)
	e.Fail(id, "publish failed")

	acts := cli.snapshotActivities()
	if len(acts) != 1 || acts[0].content.Type != "error" {
		t.Fatalf("activities = %+v, want one error", acts)
	}
	if !strings.Contains(acts[0].content.Body, "publish failed") {
		t.Fatalf("body = %q", acts[0].content.Body)
	}
}

func TestLinearEmitterNonTerminalFailureStaysQuiet(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	id := e.Start(blocks.KindToolUse, "Running go test", nil)
	e.Fail(id, "exit 1")

	for _, a := range cli.snapshotActivities() {
		if !a.ephemeral {
			t.Fatalf("non-terminal failure posted durable activity: %+v", a)
		}
	}
}

func TestLinearEmitterNoEphemeralAfterTermination(t *testing.T) {
	cli := &fakeLinearAPI{}
	e := newTestLinearEmitter(cli)

	e.Result("Done", "all good")
	before := len(cli.snapshotActivities())

	id := e.Start(blocks.KindToolUse, "Reading file", nil)
	e.Done(id, "")

	if got := len(cli.snapshotActivities()); got != before {
		t.Fatalf("activities grew from %d to %d after termination", before, got)
	}
}

func TestIsLinearElicitation(t *testing.T) {
	cases := map[string]bool{
		"Which repository?": true,
		"Try again":         true,
		"Starting":          false,
		"Sandbox ready":     false,
	}
	for title, want := range cases {
		if got := isLinearElicitation(title); got != want {
			t.Errorf("isLinearElicitation(%q) = %v, want %v", title, got, want)
		}
	}
}

func TestJoinTitleBody(t *testing.T) {
	if got := joinTitleBody("Done", "body"); got != "**Done**\n\nbody" {
		t.Fatalf("got %q", got)
	}
	if got := joinTitleBody("Done", ""); got != "**Done**" {
		t.Fatalf("got %q", got)
	}
	if got := joinTitleBody("", "body"); got != "body" {
		t.Fatalf("got %q", got)
	}
	if got := joinTitleBody("", ""); got != "" {
		t.Fatalf("got %q", got)
	}
}
