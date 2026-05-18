package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	toolbox "github.com/daytonaio/daytona/libs/toolbox-api-client-go"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

type bootstrapRunnerSHCall struct {
	step              string
	cmd               string
	timeout           time.Duration
	idleTimeout       time.Duration
	suppressInputEcho bool
}

func newTestFileSystem(serverURL string) *daytona.FileSystemService {
	cfg := toolbox.NewConfiguration()
	cfg.Servers = toolbox.ServerConfigurations{{URL: serverURL}}
	return daytona.NewFileSystemService(toolbox.NewAPIClient(cfg), nil)
}

func TestBotRunnerRunUsesShLinesAndMergesEnv(t *testing.T) {
	var calls []bootstrapRunnerSHCall
	b := &Bot{
		log: discardLogger(),
		shLinesFn: func(_ context.Context, sandboxID string, _ sandboxProcess, sessionID, step, cmd string, timeout, idleTimeout time.Duration, suppressInputEcho bool, onLine func(string)) (string, error) {
			if sandboxID != "sandbox-1" || sessionID != "session-1" {
				t.Fatalf("shLines sandbox/session = %q/%q", sandboxID, sessionID)
			}
			calls = append(calls, bootstrapRunnerSHCall{
				step:              step,
				cmd:               cmd,
				timeout:           timeout,
				idleTimeout:       idleTimeout,
				suppressInputEcho: suppressInputEcho,
			})
			if strings.HasPrefix(step, "bootstrap-run-") {
				onLine("[hetchy-bootstrap] verifying artifacts")
				return "bootstrap output", nil
			}
			return "", nil
		},
	}
	runner := &botRunner{
		b:         b,
		sb:        &daytona.Sandbox{ID: "sandbox-1"},
		sessionID: "session-1",
		emit:      newCaptureEmitter(),
		baseEnv:   map[string]string{"BASE": "base", "OVERRIDE": "base-value"},
	}

	out, err := runner.Run(context.Background(), "unit", "echo hello", map[string]string{"A": "alpha", "OVERRIDE": "step-value"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "bootstrap output" {
		t.Fatalf("output = %q", out)
	}
	if len(calls) != 2 {
		t.Fatalf("shLines calls = %+v", calls)
	}
	write := calls[0]
	if write.step != "bootstrap-write-unit" || write.timeout != 30*time.Second || !write.suppressInputEcho {
		t.Fatalf("write call = %+v", write)
	}
	if !strings.Contains(write.cmd, "run_claude_with_watchdog") || !strings.Contains(write.cmd, "echo hello") {
		t.Fatalf("write command missing script body/watchdog:\n%s", write.cmd)
	}
	run := calls[1]
	if run.step != "bootstrap-run-unit" || run.timeout != 60*time.Minute || run.idleTimeout != 15*time.Minute || run.suppressInputEcho {
		t.Fatalf("run call = %+v", run)
	}
	for _, want := range []string{"A='alpha'", "BASE='base'", "OVERRIDE='step-value'", "bash /tmp/sf-unit.sh"} {
		if !strings.Contains(run.cmd, want) {
			t.Fatalf("run command missing %q:\n%s", want, run.cmd)
		}
	}
}

func TestBotRunnerReadFileUsesDownloadFile(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/files/download" {
			t.Fatalf("path = %s, want /files/download", r.URL.Path)
		}
		requestedPath = r.URL.Query().Get("path")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("downloaded contents"))
	}))
	defer server.Close()

	var shLinesCalled bool
	b := &Bot{
		log: discardLogger(),
		shLinesFn: func(context.Context, string, sandboxProcess, string, string, string, time.Duration, time.Duration, bool, func(string)) (string, error) {
			shLinesCalled = true
			t.Fatal("ReadFile should not fall back to shLines when DownloadFile succeeds")
			return "", nil
		},
	}
	runner := &botRunner{
		b:         b,
		sb:        &daytona.Sandbox{ID: "sandbox-1", FileSystem: newTestFileSystem(server.URL)},
		sessionID: "session-1",
	}

	got, err := runner.ReadFile(context.Background(), "/tmp/prompt.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "downloaded contents" {
		t.Fatalf("read file = %q", got)
	}
	if requestedPath != "/tmp/prompt.txt" {
		t.Fatalf("download path = %q", requestedPath)
	}
	if shLinesCalled {
		t.Fatal("shLines was called")
	}
}

func TestBotRunnerReadFileFallsBackWhenDownloadFileFails(t *testing.T) {
	var downloadAttempted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadAttempted = true
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"download failed"}`))
	}))
	defer server.Close()

	var calls []bootstrapRunnerSHCall
	b := &Bot{
		log: discardLogger(),
		shLinesFn: func(_ context.Context, _ string, _ sandboxProcess, _, step, cmd string, timeout, idleTimeout time.Duration, suppressInputEcho bool, _ func(string)) (string, error) {
			calls = append(calls, bootstrapRunnerSHCall{
				step:              step,
				cmd:               cmd,
				timeout:           timeout,
				idleTimeout:       idleTimeout,
				suppressInputEcho: suppressInputEcho,
			})
			return "fallback contents", nil
		},
	}
	runner := &botRunner{
		b:         b,
		sb:        &daytona.Sandbox{ID: "sandbox-1", FileSystem: newTestFileSystem(server.URL)},
		sessionID: "session-1",
	}

	got, err := runner.ReadFile(context.Background(), "/tmp/prompt.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "fallback contents" {
		t.Fatalf("read file = %q", got)
	}
	if !downloadAttempted {
		t.Fatal("DownloadFile was not attempted")
	}
	if len(calls) != 1 {
		t.Fatalf("shLines calls = %+v", calls)
	}
	if calls[0].step != "bootstrap-read" || calls[0].cmd != "cat '/tmp/prompt.txt'" || calls[0].timeout != 5*time.Minute || calls[0].suppressInputEcho {
		t.Fatalf("fallback call = %+v", calls[0])
	}
}

func TestBotRunnerReadAndWriteFileUseShLines(t *testing.T) {
	var calls []bootstrapRunnerSHCall
	b := &Bot{
		log: discardLogger(),
		shLinesFn: func(_ context.Context, _ string, _ sandboxProcess, _, step, cmd string, timeout, idleTimeout time.Duration, suppressInputEcho bool, _ func(string)) (string, error) {
			calls = append(calls, bootstrapRunnerSHCall{
				step:              step,
				cmd:               cmd,
				timeout:           timeout,
				idleTimeout:       idleTimeout,
				suppressInputEcho: suppressInputEcho,
			})
			if step == "bootstrap-read" {
				return "file contents", nil
			}
			return "", nil
		},
	}
	runner := &botRunner{b: b, sb: &daytona.Sandbox{ID: "sandbox-1"}, sessionID: "session-1"}

	if err := runner.WriteFile(context.Background(), "/tmp/prompt.txt", []byte("prompt body\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := runner.ReadFile(context.Background(), "/tmp/prompt.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "file contents" {
		t.Fatalf("read file = %q", got)
	}
	if len(calls) != 2 {
		t.Fatalf("shLines calls = %+v", calls)
	}
	if calls[0].step != "bootstrap-write" || calls[0].timeout != 30*time.Second || !calls[0].suppressInputEcho || !strings.Contains(calls[0].cmd, "prompt body") {
		t.Fatalf("write call = %+v", calls[0])
	}
	if calls[1].step != "bootstrap-read" || calls[1].timeout != 5*time.Minute || calls[1].suppressInputEcho || calls[1].cmd != "cat '/tmp/prompt.txt'" {
		t.Fatalf("read call = %+v", calls[1])
	}
}

// TestBootstrapLineRouter_ThreePhaseRouting walks the router through
// the same shape the bootstrap script produces in practice: pre-claude
// shell echoes, the invoke marker, claude stream-json events, and
// post-claude verification echoes. We expect three blocks: a pre-claude
// setup, a claude_text from the parser, and a post-claude setup. The
// previous router would dump everything into a single setup block —
// this test pins the new typed-block behavior so a regression to the
// old "wall of NDJSON" output is caught.
func TestBootstrapLineRouter_ThreePhaseRouting(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line("[hetchy-bootstrap] cloning repo")
	r.Line("[hetchy-bootstrap] detecting hints")
	r.Line(bootstrapEnterClaudeMarker)
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"This is a Go web service."}]}}`)
	r.Line(`{"type":"result","subtype":"success","result":"done"}`)
	r.Line("[hetchy-bootstrap] verifying artifacts")
	r.Line("[hetchy-bootstrap] healthy after 5s")
	r.Done("Bootstrap complete")

	if len(emit.Blocks) != 3 {
		t.Fatalf("want 3 blocks (pre-setup, claude_text, post-setup); got %d:\n%+v", len(emit.Blocks), emit.Blocks)
	}

	pre := emit.Blocks[0]
	if pre.Kind != blocks.KindSetup || pre.Title != "Bootstrapping repo" {
		t.Errorf("block[0] should be pre-claude setup; got kind=%v title=%q", pre.Kind, pre.Title)
	}
	if !strings.Contains(pre.Body.String(), "cloning repo") {
		t.Errorf("pre-claude block missing expected content: %q", pre.Body.String())
	}

	claudeBlock := emit.Blocks[1]
	if claudeBlock.Kind != blocks.KindClaudeText {
		t.Errorf("block[1] should be claude text; got %v", claudeBlock.Kind)
	}
	if !strings.Contains(claudeBlock.Body.String(), "Go web service") {
		t.Errorf("claude block missing expected content: %q", claudeBlock.Body.String())
	}

	post := emit.Blocks[2]
	if post.Kind != blocks.KindSetup || post.Title != "Verifying bootstrap" {
		t.Errorf("block[2] should be post-claude setup; got kind=%v title=%q", post.Kind, post.Title)
	}
	if !strings.Contains(post.Body.String(), "verifying artifacts") {
		t.Errorf("post-claude block missing expected content: %q", post.Body.String())
	}
}

// TestBootstrapLineRouter_NDJSONNeverLeaksToSetupBlock guards the
// specific regression that motivated this rewrite: claude stream-json
// must not appear in any setup block's body. The old router glued
// hundreds of `{"type":"assistant",...}` lines into the bootstrap
// setup block, overwhelming the chat UI.
func TestBootstrapLineRouter_NDJSONNeverLeaksToSetupBlock(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line("[hetchy-bootstrap] cloning repo")
	r.Line(bootstrapEnterClaudeMarker)
	r.Line(`{"type":"system","subtype":"init"}`)
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	r.Line("[hetchy-bootstrap] verifying artifacts")
	r.Done("ok")

	for i, b := range emit.Blocks {
		if b.Kind != blocks.KindSetup {
			continue
		}
		body := b.Body.String()
		if strings.Contains(body, `"type":"assistant"`) || strings.Contains(body, `"type":"system"`) {
			t.Errorf("block[%d] (%q) leaked NDJSON into a setup body:\n%s", i, b.Title, body)
		}
	}
}

// TestBootstrapLineRouter_LateLineAfterDone simulates shLines' finalFlush
// delivering one more line after the bot has already called Done() on
// the router. The previous implementation cleared r.parser without
// resetting the phase, so the next Line() in phaseInAgent would
// nil-deref r.parser.Finish(). The fix: Done/Fail also reset the
// phase, AND Line guards the parser deref.
func TestBootstrapLineRouter_LateLineAfterDone(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line(bootstrapEnterClaudeMarker)
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`)
	// Bot calls Done while we're still in the agent phase.
	r.Done("complete")
	// shLines flushes a trailing buffered line a moment later. Before
	// the fix this panicked on r.parser.Finish() / r.parser.Line().
	r.Line(`{"type":"system","subtype":"flush"}`)
}

// TestBootstrapLineRouter_NoClaudePhase covers the bail-early path: the
// script may exit before reaching the invoke marker (e.g. the prompt
// file is missing). In that case we expect a single pre-claude setup
// block, marked Done by the caller. No claude_text block should ever
// appear.
func TestBootstrapLineRouter_NoClaudePhase(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line("[hetchy-bootstrap] cloning repo")
	r.Line("[hetchy-bootstrap] missing prompt file")
	r.Fail("Bootstrap setup failed")

	if len(emit.Blocks) != 1 {
		t.Fatalf("want 1 block; got %d", len(emit.Blocks))
	}
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Errorf("block should be marked failed; got %v", emit.Blocks[0].Status)
	}
}
