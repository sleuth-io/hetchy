package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// fakeSlackCall is one captured request to the fake slack server.
// Method is "postMessage" (chat.postMessage) or "updateMessage" (chat.update); TS
// is the message timestamp returned for posts and the message ts the
// caller is editing for updates.
type fakeSlackCall struct {
	Method string
	Text   string
	TS     string
}

// fakeSlackServer is an httptest server that pretends to be Slack's
// Web API for chat.postMessage and chat.update. We point a real
// slack.Client at it via OptionAPIURL — that way the emitter under
// test exercises exactly the same code paths it would in production
// without us mocking the slack-go surface ourselves.
type fakeSlackServer struct {
	mu     sync.Mutex
	calls  []fakeSlackCall
	nextTS int
	server *httptest.Server
}

func newFakeSlackServer(t *testing.T) *fakeSlackServer {
	t.Helper()
	f := &fakeSlackServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nextTS++
		ts := fmt.Sprintf("%d.000000", f.nextTS)
		f.calls = append(f.calls, fakeSlackCall{Method: "postMessage", Text: r.FormValue("text"), TS: ts})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"ts":      ts,
			"channel": r.FormValue("channel"),
		})
	})
	mux.HandleFunc("/chat.update", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, fakeSlackCall{Method: "updateMessage", Text: r.FormValue("text"), TS: r.FormValue("ts")})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"ts":      r.FormValue("ts"),
			"channel": r.FormValue("channel"),
		})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeSlackServer) URL() string { return f.server.URL + "/" }

func (f *fakeSlackServer) Calls() []fakeSlackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeSlackCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// newTestSlackEmitter builds a slackEmitter wired to a fake Slack
// server. Returns both so tests can drive the emitter and assert on
// the captured request log.
func newTestSlackEmitter(t *testing.T, request string) (*slackEmitter, *fakeSlackServer) {
	t.Helper()
	fs := newFakeSlackServer(t)
	cli := slack.New("xoxb-test", slack.OptionAPIURL(fs.URL()))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := newSlackEmitter(log, cli, "C123", "T0", "U1", "https://example.test/?session=t", request)
	return e, fs
}

// resetThrottle bypasses the slackUpdateMinInterval throttle so the
// next refreshLive call commits immediately. Cheaper than redesigning
// the emitter to inject a clock for these tests.
func resetThrottle(e *slackEmitter) {
	e.mu.Lock()
	e.lastUpdate = time.Time{}
	e.mu.Unlock()
}

// TestSlackEmitter_NotifyPostsDirectly pins that Notify spawns its own
// thread message rather than feeding the live status slot, and that
// the icon prefix is the milestone check (not the hourglass an earlier
// version emitted).
func TestSlackEmitter_NotifyPostsDirectly(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "trim the README")
	e.Notify("Starting", "Spinning up sandbox…")

	calls := fs.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 post, got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "postMessage" {
		t.Errorf("want post, got %s", calls[0].Method)
	}
	if !strings.HasPrefix(calls[0].Text, ":white_check_mark: Starting") {
		t.Errorf("expected check icon + title, got %q", calls[0].Text)
	}
	if !strings.Contains(calls[0].Text, "Spinning up sandbox") {
		t.Errorf("expected body in posted text, got %q", calls[0].Text)
	}
	if strings.Contains(calls[0].Text, "<@U1>") {
		t.Errorf("Notify must not @mention — that's reserved for the terminal post; got %q", calls[0].Text)
	}
}

// TestSlackEmitter_NotifyStartKindEscapesTitle confirms a title with
// Slack control sequences (a hostile prompt that somehow lands here
// in future, or a Claude-controlled string) doesn't broadcast.
func TestSlackEmitter_NotifyStartEscapesTitle(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "")
	id := e.Start(blocks.KindNotify, "<!channel> hello", nil)
	e.Done(id, "")

	calls := fs.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 post, got %d: %+v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Text, "&lt;!channel&gt;") {
		t.Errorf("expected escaped <!channel>, got %q", calls[0].Text)
	}
}

func TestSlackEmitter_TeeNotifyBuffersBody(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "")
	emit := blocks.Tee(blocks.NewRecorder(0), e)

	emit.Notify("Bootstrap spec — no changes", "Agent reviewed the validation run and reported no spec improvements were warranted.")

	calls := fs.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 post, got %d: %+v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Text, "Bootstrap spec") {
		t.Errorf("expected notify title, got %q", calls[0].Text)
	}
	if !strings.Contains(calls[0].Text, "Agent reviewed the validation run") {
		t.Errorf("expected buffered notify body, got %q", calls[0].Text)
	}
}

// TestSlackEmitter_FailUnknownIDIsNoOp pins the long-flagged regression:
// Fail() with an unregistered id (double-terminate, post-Abort) used
// to post a spurious ":x: Step failed" into the thread, the exact
// noise this design exists to remove.
func TestSlackEmitter_FailUnknownIDIsNoOp(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "")
	e.Fail("nonexistent-id", "shouldn't post")
	if calls := fs.Calls(); len(calls) != 0 {
		t.Errorf("Fail() with unknown id should not post anything, got %d calls: %+v", len(calls), calls)
	}
}

// TestSlackEmitter_FailKnownIDPostsErrorLine pairs with the unknown-id
// test above so a future "guard too aggressive" regression doesn't
// quietly suppress the legitimate failure post.
func TestSlackEmitter_FailKnownIDPostsErrorLine(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "")
	id := e.Start(blocks.KindToolUse, "Running gh pr create", nil)
	e.Fail(id, "exit 1")

	posts := 0
	for _, c := range fs.Calls() {
		if c.Method == "postMessage" && strings.HasPrefix(c.Text, ":x: ") {
			posts++
			if !strings.Contains(c.Text, "Running gh pr create") {
				t.Errorf("expected failure post to include the tool title, got %q", c.Text)
			}
			if !strings.Contains(c.Text, "exit 1") {
				t.Errorf("expected failure post to include the summary, got %q", c.Text)
			}
		}
	}
	if posts != 1 {
		t.Errorf("want exactly 1 :x: failure post, got %d (calls: %+v)", posts, fs.Calls())
	}
}

// TestSlackEmitter_LiveMessageLifecycle drives a representative turn:
// the live status message is lazy-created on the first non-Notify
// block, edited in place on subsequent state changes, and Result
// force-flushes it before posting a separate terminal message that
// @mentions the user.
func TestSlackEmitter_LiveMessageLifecycle(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "trim the README")

	id1 := e.Start(blocks.KindToolUse, "Reading README.md", nil)
	resetThrottle(e)
	e.Done(id1, "287 lines")
	resetThrottle(e)
	id2 := e.Start(blocks.KindToolUse, "Editing README.md", nil)
	resetThrottle(e)
	e.Done(id2, "12 lines")
	e.Result("Done!", "https://example.test/pull/1")

	calls := fs.Calls()
	if len(calls) < 2 {
		t.Fatalf("want at least 2 calls (live post + terminal post), got %d: %+v", len(calls), calls)
	}

	// First call must be the live-message PostMessage (lazy create).
	if calls[0].Method != "postMessage" {
		t.Fatalf("first call should be the live PostMessage, got %s", calls[0].Method)
	}
	liveTS := calls[0].TS

	// Last call must be the terminal PostMessage with @mention + :tada:.
	last := calls[len(calls)-1]
	if last.Method != "postMessage" {
		t.Errorf("terminal call should be a post, got %s", last.Method)
	}
	if !strings.Contains(last.Text, "<@U1>") {
		t.Errorf("terminal post should @mention the user (the only mention in the thread), got %q", last.Text)
	}
	if !strings.Contains(last.Text, ":tada: Done!") {
		t.Errorf("terminal post should include :tada: Done!, got %q", last.Text)
	}
	if !strings.Contains(last.Text, "View full details") {
		t.Errorf("terminal post should include the deep link, got %q", last.Text)
	}

	// Every UpdateMessage in between must edit the live ts (we never
	// edit anything else; the failure posts are PostMessages).
	updates := 0
	for _, c := range calls[1 : len(calls)-1] {
		if c.Method == "updateMessage" {
			updates++
			if c.TS != liveTS {
				t.Errorf("update should target the live ts %s, got %s", liveTS, c.TS)
			}
		}
	}
	if updates == 0 {
		t.Errorf("expected at least one UpdateMessage between the lazy create and the terminal post, calls=%+v", calls)
	}

	// Counter line on the terminal-state live message has both Read
	// and Edit incremented (Done updates counters before the next
	// refreshLive); verify against the latest live UpdateMessage.
	var lastLiveUpdate string
	for _, c := range calls {
		if c.Method == "updateMessage" && c.TS == liveTS {
			lastLiveUpdate = c.Text
		}
	}
	if !strings.Contains(lastLiveUpdate, "Read 1") {
		t.Errorf("live message should reflect Read counter, got %q", lastLiveUpdate)
	}
	if !strings.Contains(lastLiveUpdate, "Edit 1") {
		t.Errorf("live message should reflect Edit counter, got %q", lastLiveUpdate)
	}
	if !strings.Contains(lastLiveUpdate, ":white_check_mark: Done — trim the README") {
		t.Errorf("terminal-state live header should include user request, got %q", lastLiveUpdate)
	}
}

func TestSlackEmitter_TeeResultPostsTerminalMentionAndPR(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "fix slack notifications")
	emit := blocks.Tee(blocks.NewRecorder(0), e)

	id := emit.Start(blocks.KindToolUse, "Reading README.md", nil)
	emit.Done(id, "287 lines")
	emit.Notify("Bootstrap spec — no changes", "Agent reviewed the validation run and reported no spec improvements were warranted.")
	emit.Result("Done!", "https://github.com/hetchyhq/hetchy/pull/183\n\nReply here to make further changes to this PR.")

	calls := fs.Calls()
	notifyIdx := -1
	terminalIdx := -1
	for i, c := range calls {
		if c.Method == "postMessage" && strings.Contains(c.Text, "Bootstrap spec") {
			notifyIdx = i
		}
		if c.Method == "postMessage" && strings.Contains(c.Text, ":tada: Done!") {
			terminalIdx = i
		}
	}
	if notifyIdx < 0 {
		t.Fatalf("missing bootstrap notify post in calls: %+v", calls)
	}
	if terminalIdx < 0 {
		t.Fatalf("missing terminal result post in calls: %+v", calls)
	}
	if terminalIdx <= notifyIdx {
		t.Fatalf("terminal result should be posted after bootstrap notify; calls=%+v", calls)
	}
	terminal := calls[terminalIdx].Text
	if !strings.Contains(terminal, "<@U1>") {
		t.Errorf("terminal result should @mention the user, got %q", terminal)
	}
	if !strings.Contains(terminal, "https://github.com/hetchyhq/hetchy/pull/183") {
		t.Errorf("terminal result should include PR URL, got %q", terminal)
	}
	if !strings.Contains(terminal, "View full details") {
		t.Errorf("terminal result should include conversation link, got %q", terminal)
	}
	if !e.terminated || e.lastTerminalKind != blocks.KindResult {
		t.Errorf("emitter terminal state = %v/%v, want result", e.terminated, e.lastTerminalKind)
	}
}

func TestSlackEmitter_TeeErrorPostsTerminalMention(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "ship it")
	emit := blocks.Tee(blocks.NewRecorder(0), e)

	emit.Error("Agent failed", "Something went wrong while running the agent.")

	calls := fs.Calls()
	if len(calls) < 1 {
		t.Fatalf("expected Slack calls, got none")
	}
	last := calls[len(calls)-1]
	if last.Method != "postMessage" {
		t.Fatalf("terminal error should be a post, got %s: %+v", last.Method, calls)
	}
	if !strings.Contains(last.Text, "<@U1>") {
		t.Errorf("terminal error should @mention the user, got %q", last.Text)
	}
	if !strings.Contains(last.Text, ":x: Agent failed") {
		t.Errorf("terminal error should include title, got %q", last.Text)
	}
	if !strings.Contains(last.Text, "Something went wrong") {
		t.Errorf("terminal error should include body, got %q", last.Text)
	}
	if !e.terminated || e.lastTerminalKind != blocks.KindError {
		t.Errorf("emitter terminal state = %v/%v, want error", e.terminated, e.lastTerminalKind)
	}
}

func TestSlackEmitter_ClaudeTextDoesNotLookTerminal(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "ship it")

	setupID := e.Start(blocks.KindSetup, "Sandbox setup", nil)
	resetThrottle(e)
	e.Done(setupID, "ready")
	resetThrottle(e)
	textID := e.Start(blocks.KindClaudeText, "Done!", nil)
	e.Append(textID, "Done!")
	e.Done(textID, "")

	for _, c := range fs.Calls() {
		if strings.Contains(c.Text, ":hourglass_flowing_sand: Done!") {
			t.Fatalf("claude text should not make Slack look terminal before Hetchy finalizes; calls=%+v", fs.Calls())
		}
	}
}

// TestSlackEmitter_RequestEscapedInLiveHeader confirms a malicious or
// accidental Slack control sequence in the user prompt cannot
// broadcast on the very first PostMessage of the live message — the
// escape happens before the request reaches MsgOptionText.
func TestSlackEmitter_RequestEscapedInLiveHeader(t *testing.T) {
	e, fs := newTestSlackEmitter(t, "<!channel> ship it")
	id := e.Start(blocks.KindToolUse, "Reading file.go", nil)
	e.Result("Done!", "url")
	_ = id

	for _, c := range fs.Calls() {
		if strings.Contains(c.Text, "<!channel>") {
			t.Errorf("found unescaped <!channel> in %s text: %q", c.Method, c.Text)
		}
	}
}
