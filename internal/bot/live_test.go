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
