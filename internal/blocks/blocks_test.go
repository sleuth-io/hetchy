package blocks

import (
	"encoding/json"
	"testing"
	"time"
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

// stampingRecorder records every (StartAt|DoneAt|FailAt) call's
// timestamp so the tee-coordination tests below can assert all
// wrapped emitters saw the *same* time.Time, not two
// independently-sampled ones.
type stampingRecorder struct {
	startedAt []time.Time
	endedAt   []time.Time
}

func (s *stampingRecorder) Start(kind Kind, _ string, _ map[string]any) string {
	return s.StartAt(kind, "", nil, time.Now().UTC())
}
func (s *stampingRecorder) StartAt(_ Kind, _ string, _ map[string]any, t time.Time) string {
	s.startedAt = append(s.startedAt, t)
	return "id"
}
func (s *stampingRecorder) Append(string, string)           {}
func (s *stampingRecorder) Done(id, summary string)         { s.DoneAt(id, summary, time.Now().UTC()) }
func (s *stampingRecorder) Fail(id, summary string)         { s.FailAt(id, summary, time.Now().UTC()) }
func (s *stampingRecorder) DoneAt(_, _ string, t time.Time) { s.endedAt = append(s.endedAt, t) }
func (s *stampingRecorder) FailAt(_, _ string, t time.Time) { s.endedAt = append(s.endedAt, t) }
func (s *stampingRecorder) Notify(string, string)           {}
func (s *stampingRecorder) Result(string, string)           {}
func (s *stampingRecorder) Error(string, string)            {}

// TestTee_SharesStartedAtAndEndedAt is the regression test for the
// minute-boundary disagreement between the persisted Block.StartedAt
// and the live SSE chip: the tee must sample time.Now() once and
// hand the same value to every wrapped emitter, not let each child
// sample independently.
func TestTee_SharesStartedAtAndEndedAt(t *testing.T) {
	a := &stampingRecorder{}
	b := &stampingRecorder{}
	emit := Tee(a, b)

	id := emit.Start(KindToolUse, "t", nil)
	emit.Done(id, "")
	id2 := emit.Start(KindToolUse, "t2", nil)
	emit.Fail(id2, "")

	if len(a.startedAt) != 2 || len(b.startedAt) != 2 {
		t.Fatalf("want 2 starts each, got a=%d b=%d", len(a.startedAt), len(b.startedAt))
	}
	for i := range a.startedAt {
		if !a.startedAt[i].Equal(b.startedAt[i]) {
			t.Errorf("StartAt #%d disagrees across emitters: a=%v b=%v",
				i, a.startedAt[i], b.startedAt[i])
		}
	}
	if len(a.endedAt) != 2 || len(b.endedAt) != 2 {
		t.Fatalf("want 2 ends each, got a=%d b=%d", len(a.endedAt), len(b.endedAt))
	}
	for i := range a.endedAt {
		if !a.endedAt[i].Equal(b.endedAt[i]) {
			t.Errorf("DoneAt/FailAt #%d disagrees across emitters: a=%v b=%v",
				i, a.endedAt[i], b.endedAt[i])
		}
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
