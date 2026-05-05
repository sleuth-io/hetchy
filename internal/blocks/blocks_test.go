package blocks

import (
	"encoding/json"
	"testing"
)

func TestRecorder_StartAppendDone(t *testing.T) {
	r := NewRecorder(0)
	id := r.Start(KindNotify, "Hello", nil)
	r.Append(id, "world")
	r.Done(id, "")

	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap))
	}
	if snap[0].Kind != KindNotify || snap[0].Title != "Hello" || snap[0].Body != "world" || snap[0].Status != StatusDone {
		t.Errorf("unexpected block: %+v", snap[0])
	}
}

func TestRecorder_OneShotHelpers(t *testing.T) {
	r := NewRecorder(0)
	r.Notify("title-n", "body-n")
	r.Result("title-r", "body-r")
	r.Error("title-e", "body-e")

	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(snap))
	}
	if snap[0].Kind != KindNotify || snap[0].Status != StatusDone {
		t.Errorf("notify wrong: %+v", snap[0])
	}
	if snap[1].Kind != KindResult || snap[1].Status != StatusDone {
		t.Errorf("result wrong: %+v", snap[1])
	}
	if snap[2].Kind != KindError || snap[2].Status != StatusError {
		t.Errorf("error wrong: %+v", snap[2])
	}
}

func TestRecorder_SnapshotCap(t *testing.T) {
	r := NewRecorder(2)
	for range 5 {
		id := r.Start(KindClaudeText, "t", nil)
		r.Done(id, "")
	}
	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Errorf("want 2 (capped), got %d", len(snap))
	}
}

func TestRecorder_FailMarksStatus(t *testing.T) {
	r := NewRecorder(0)
	id := r.Start(KindToolUse, "t", nil)
	r.Fail(id, "boom")
	snap := r.Snapshot()
	if snap[0].Status != StatusError || snap[0].Summary != "boom" {
		t.Errorf("want error status + summary, got %+v", snap[0])
	}
}

func TestTee_FansOut(t *testing.T) {
	a := NewRecorder(0)
	b := NewRecorder(0)
	emit := Tee(a, b)
	id := emit.Start(KindNotify, "shared", nil)
	emit.Append(id, "body")
	emit.Done(id, "")
	if len(a.Snapshot()) != 1 || len(b.Snapshot()) != 1 {
		t.Errorf("each emitter should have 1 block: a=%d b=%d", len(a.Snapshot()), len(b.Snapshot()))
	}
	if a.Snapshot()[0].Body != "body" || b.Snapshot()[0].Body != "body" {
		t.Errorf("body should fan out to both")
	}
}

func TestBlock_JSONRoundTrip(t *testing.T) {
	b := Block{
		ID: "b1", Kind: KindToolUse, Title: "Reading X", Body: "```\n…\n```",
		Status: StatusDone, Summary: "ok",
		Meta: map[string]any{"tool": "Read"},
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var got Block
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID || got.Kind != b.Kind || got.Title != b.Title || got.Body != b.Body {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
