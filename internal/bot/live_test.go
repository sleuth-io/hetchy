package bot

import (
	"context"
	"testing"
)

func TestLiveRunSubscribeAfterFiltersFutureDuplicateSeq(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := newLiveRun(ctx, cancel)
	sub := run.SubscribeAfter(10)
	defer run.Unsubscribe(sub)

	run.Emit(liveEvent{Event: "block_append", Seq: 10, Data: []byte(`{"id":"p1"}`)})
	select {
	case ev := <-sub.ch:
		t.Fatalf("received duplicate event at seq cutoff: %+v", ev)
	default:
	}

	run.Emit(liveEvent{Event: "block_append", Seq: 11, Data: []byte(`{"id":"p1"}`)})
	select {
	case ev := <-sub.ch:
		if ev.Seq != 11 {
			t.Fatalf("event seq = %d, want 11", ev.Seq)
		}
	default:
		t.Fatal("expected event beyond seq cutoff")
	}
}

func TestLiveRunLifecycleAndRegistry(t *testing.T) {
	registry := newLiveRegistry()
	run, ok := registry.RegisterIfAbsent(context.Background(), "org_1", "thread_1")
	if !ok {
		t.Fatal("first register should win")
	}
	if got := registry.Get("org_1", "thread_1"); got != run {
		t.Fatal("registry get did not return registered run")
	}
	if existing, ok := registry.RegisterIfAbsent(context.Background(), "org_1", "thread_1"); ok || existing != run {
		t.Fatalf("second register = (%p, %v), want existing false", existing, ok)
	}

	if run.Context() == nil {
		t.Fatal("run context should be set")
	}
	run.SetSandboxID("", true)
	if got := run.SandboxID(); got != "" {
		t.Fatalf("empty sandbox id should be ignored, got %q", got)
	}
	run.SetSandboxID("sandbox-1", true)
	if got := run.SandboxID(); got != "sandbox-1" {
		t.Fatalf("sandbox id = %q, want sandbox-1", got)
	}
	if id, cleanup := run.CancelCleanupSandboxID(); id != "sandbox-1" || !cleanup {
		t.Fatalf("cancel cleanup = (%q, %v), want sandbox-1 true", id, cleanup)
	}

	sub := run.Subscribe()
	run.Emit(liveEvent{Event: "notify", Data: []byte(`{"text":"hi"}`)})
	select {
	case ev := <-sub.ch:
		if ev.Event != "notify" {
			t.Fatalf("event = %+v, want notify", ev)
		}
	default:
		t.Fatal("expected live event")
	}
	run.Unsubscribe(sub)
	select {
	case _, ok := <-sub.ch:
		if ok {
			t.Fatal("subscription channel should be closed")
		}
	default:
		t.Fatal("subscription channel should close synchronously")
	}

	registry.Done("org_1", "thread_1", run)
	if got := registry.Get("org_1", "thread_1"); got != nil {
		t.Fatal("registry should remove run after Done")
	}
	select {
	case <-run.Done():
	default:
		t.Fatal("run Done channel should be closed")
	}
	if run.Cancel() {
		t.Fatal("closed run should not cancel again")
	}
}

func TestLiveRunCancelMarksCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := newLiveRun(ctx, cancel)

	if !run.Cancel() {
		t.Fatal("first cancel should return true")
	}
	if !run.Cancelled() {
		t.Fatal("run should be marked cancelled")
	}
	if run.Cancel() {
		t.Fatal("second cancel should return false")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel should cancel context")
	}
}
