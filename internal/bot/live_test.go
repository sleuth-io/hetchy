package bot

import (
	"context"
	"testing"
)

func TestLiveRunCancelCancelsContextAndKeepsSandboxID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := newLiveRun(ctx, cancel, nil)
	run.SetSandboxID("sandbox-1", true)

	if run.Cancelled() {
		t.Fatal("run should not start cancelled")
	}
	if got := run.SandboxID(); got != "sandbox-1" {
		t.Fatalf("SandboxID = %q, want sandbox-1", got)
	}
	if got, cleanup := run.CancelCleanupSandboxID(); got != "sandbox-1" || !cleanup {
		t.Fatalf("CancelCleanupSandboxID = (%q, %v), want (sandbox-1, true)", got, cleanup)
	}
	if !run.Cancel() {
		t.Fatal("first cancel should report true")
	}
	if !run.Cancelled() {
		t.Fatal("run should report cancelled")
	}
	if err := run.Context().Err(); err == nil {
		t.Fatal("run context should be cancelled")
	}
	if run.Cancel() {
		t.Fatal("second cancel should report false")
	}
}

// TestLiveRegistry asserts the local-only registry semantics: Register
// stores, Get returns the registered run, Done removes and cancels.
// Cluster-level uniqueness (no two replicas owning the same turn) is
// asserted in sessionlease tests, not here — this map is intentionally
// per-replica.
func TestLiveRegistry(t *testing.T) {
	reg := newLiveRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	run := newLiveRun(ctx, cancel, nil)
	reg.Register("org", "thread", run)

	if got := reg.Get("org", "thread"); got != run {
		t.Fatalf("Get = %v, want %v", got, run)
	}
	reg.Done("org", "thread", run)
	if got := reg.Get("org", "thread"); got != nil {
		t.Fatalf("run should be removed after Done, got %+v", got)
	}
	if !run.Cancelled() {
		t.Fatal("Done should cancel the run")
	}
}
