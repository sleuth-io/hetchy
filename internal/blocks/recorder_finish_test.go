package blocks

import (
	"testing"
	"time"
)

// FailAt and DoneAt exist so a recovering worker can close blocks with the time
// the work actually ended, not the time recovery noticed. Using time.Now would
// stamp a run that died an hour ago as finishing now.
func TestRecorderFinishAtUsesTheSuppliedTime(t *testing.T) {
	ended := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	r := NewRecorder(0)
	id := r.Start(KindSetup, "Setup", nil)
	r.FailAt(id, "Setup failed", ended)

	blocks := r.Snapshot()
	if len(blocks) != 1 {
		t.Fatalf("want one block, got %d", len(blocks))
	}
	got := blocks[0]
	if got.Status != StatusError {
		t.Errorf("Status = %v, want %v", got.Status, StatusError)
	}
	if got.Summary != "Setup failed" {
		t.Errorf("Summary = %q", got.Summary)
	}
	if !got.EndedAt.Equal(ended) {
		t.Errorf("EndedAt = %v, want the supplied %v", got.EndedAt, ended)
	}
}

// Finishing an id that was never started must be a no-op rather than inventing
// a block: recovery replays logs that may reference blocks it never saw open.
func TestRecorderFinishAtUnknownIDIsANoOp(t *testing.T) {
	r := NewRecorder(0)
	r.FailAt("never-started", "boom", time.Now().UTC())
	if got := r.Snapshot(); len(got) != 0 {
		t.Fatalf("finishing an unknown id created blocks: %+v", got)
	}
}
