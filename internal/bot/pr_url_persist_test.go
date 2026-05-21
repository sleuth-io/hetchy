package bot

import (
	"context"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

func TestPRURLPersistingEmitterFindsURLSplitAcrossAppends(t *testing.T) {
	convs := &fakeConversationStore{}
	emit := newPRURLPersistingEmitter(discardLogger(), convs, convstore.Record{
		OrgID:    "org-1",
		ThreadID: "thread-1",
	}, newCaptureEmitter())

	id := emit.Start(blocks.KindResult, "Done!", nil)
	emit.Append(id, "Opened https://github.com/hetchyhq/het")
	emit.Append(id, "chy/pull/222")

	const want = "https://github.com/hetchyhq/hetchy/pull/222"
	if got := emit.Latest(); got != want {
		t.Fatalf("Latest() = %q, want %q", got, want)
	}
	rec, err := convs.Get(context.Background(), "org-1", "thread-1")
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if rec.PRURL != want {
		t.Fatalf("persisted PRURL = %q, want %q", rec.PRURL, want)
	}
}
