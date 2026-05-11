package bot

import (
	"context"
	"testing"
)

func TestLiveRunCancelCancelsContextAndKeepsSandboxID(t *testing.T) {
	reg := newLiveRegistry()
	run, ok := reg.RegisterIfAbsent(context.Background(), "org", "thread")
	if !ok {
		t.Fatal("first register should win")
	}
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

func TestLiveRegistryRejectsDuplicateAndCleansUpOnDone(t *testing.T) {
	reg := newLiveRegistry()
	run, ok := reg.RegisterIfAbsent(context.Background(), "org", "thread")
	if !ok {
		t.Fatal("first register should win")
	}
	if _, ok := reg.RegisterIfAbsent(context.Background(), "org", "thread"); ok {
		t.Fatal("duplicate register should lose")
	}
	reg.Done("org", "thread", run)
	if got := reg.Get("org", "thread"); got != nil {
		t.Fatalf("run should be removed after Done, got %+v", got)
	}
}
