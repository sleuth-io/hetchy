package bot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
)

// silentBot returns a Bot with a discarded logger and empty convos. Tests
// that don't need a working Daytona client can use it directly. Tests that
// need Get() to fail can override b.daytona by calling withFailingDaytona.
func silentBot(t *testing.T, statePath string) *Bot {
	t.Helper()
	return &Bot{
		cfg:    Config{StateFile: statePath},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		convos: make(map[string]*conversation),
	}
}

// withFailingDaytona configures b.daytona to point at a closed loopback port
// so Get/Create/etc. fail predictably without external network.
func withFailingDaytona(t *testing.T, b *Bot) {
	t.Helper()
	dc, err := daytona.NewClientWithConfig(&types.DaytonaConfig{
		APIUrl: "http://127.0.0.1:1",
		APIKey: "fake-key-for-tests",
	})
	if err != nil {
		t.Fatalf("NewClientWithConfig: %v", err)
	}
	b.daytona = dc
}

func TestSaveState_EmptyConvos(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	b := silentBot(t, path)

	b.saveState()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		t.Fatalf("invalid JSON written: %v\n%s", err, data)
	}
	if len(ps.Conversations) != 0 {
		t.Errorf("Conversations = %d, want 0", len(ps.Conversations))
	}
}

func TestSaveState_RoundTripJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	b := silentBot(t, path)
	b.convos["thread-1"] = &conversation{
		sandbox: &daytona.Sandbox{ID: "sb-abc"},
		branch:  "feature/sf-1",
		prURL:   "https://github.com/owner/repo/pull/42",
		history: []string{"first turn", "second turn"},
	}

	b.saveState()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	got, ok := ps.Conversations["thread-1"]
	if !ok {
		t.Fatalf("thread-1 missing in: %s", data)
	}
	if got.SandboxID != "sb-abc" || got.Branch != "feature/sf-1" || got.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("persisted fields wrong: %+v", got)
	}
	if len(got.History) != 2 || got.History[0] != "first turn" || got.History[1] != "second turn" {
		t.Errorf("history wrong: %v", got.History)
	}
}

func TestSaveState_FailsSilentlyOnUnwritablePath(t *testing.T) {
	// Path inside a non-existent directory triggers a write error. saveState
	// logs and returns; callers shouldn't observe a panic.
	b := silentBot(t, "/nonexistent-dir-xyz/state.json")
	b.saveState() // must not panic
}

func TestLoadState_FileNotFoundIsNoError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-file.json")
	b := silentBot(t, path)

	if err := b.loadState(context.Background()); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(b.convos) != 0 {
		t.Errorf("convos should be empty, got %d", len(b.convos))
	}
}

func TestLoadState_InvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b := silentBot(t, path)

	err := b.loadState(context.Background())
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadState_EmptyConversationsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte(`{"conversations":{}}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b := silentBot(t, path)

	if err := b.loadState(context.Background()); err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(b.convos) != 0 {
		t.Errorf("convos should be empty, got %d", len(b.convos))
	}
}

func TestLoadState_DropsConversationsWhenSandboxGetFails(t *testing.T) {
	// Persisted state references sandboxes that can't be fetched (Daytona is
	// pointed at a closed port). loadState should log and skip them, not error.
	dir := t.TempDir()
	path := filepath.Join(dir, "stale.json")
	body := []byte(`{
		"conversations": {
			"thread-x": {
				"sandbox_id": "sb-gone",
				"branch": "feature/sf-old",
				"pr_url": "https://github.com/owner/repo/pull/1",
				"history": ["was working on something"]
			}
		}
	}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b := silentBot(t, path)
	withFailingDaytona(t, b)

	if err := b.loadState(context.Background()); err != nil {
		t.Fatalf("loadState should swallow per-conv Get failures, got %v", err)
	}
	if len(b.convos) != 0 {
		t.Errorf("expected 0 convos restored (sandbox unreachable), got %d", len(b.convos))
	}
}
