package bot

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

type fakeConversationStore struct {
	mu              sync.Mutex
	rec             convstore.Record
	getErr          error
	searchResult    []convstore.Record
	searchErr       error
	saveProgressErr error
	upsertErr       error
	deleteErr       error
	renameErr       error

	upserts       []convstore.Record
	progressSaves []convstore.Record
	taskSaves     []map[string]bool
}

func (f *fakeConversationStore) Get(context.Context, string, string) (convstore.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return convstore.Record{}, f.getErr
	}
	return cloneRecord(f.rec), nil
}

func (f *fakeConversationStore) Search(context.Context, string, convstore.SearchOptions) ([]convstore.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	out := make([]convstore.Record, 0, len(f.searchResult))
	for _, rec := range f.searchResult {
		out = append(out, cloneRecord(rec))
	}
	return out, nil
}

func (f *fakeConversationStore) SaveProgress(_ context.Context, rec convstore.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveProgressErr != nil {
		return f.saveProgressErr
	}
	f.progressSaves = append(f.progressSaves, cloneRecord(rec))
	return nil
}

func (f *fakeConversationStore) SaveTaskOptions(_ context.Context, _, _ string, opts map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taskSaves = append(f.taskSaves, cloneTaskOptions(opts))
	return nil
}

func (f *fakeConversationStore) Upsert(_ context.Context, rec convstore.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts = append(f.upserts, cloneRecord(rec))
	f.rec = cloneRecord(rec)
	f.getErr = nil
	return nil
}

func (f *fakeConversationStore) Delete(context.Context, string, string) error { return f.deleteErr }

func (f *fakeConversationStore) Rename(context.Context, string, string, string) error {
	return f.renameErr
}

func (f *fakeConversationStore) lastUpsert(t *testing.T) convstore.Record {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.upserts) == 0 {
		t.Fatal("expected at least one upsert")
	}
	return cloneRecord(f.upserts[len(f.upserts)-1])
}

func cloneRecord(rec convstore.Record) convstore.Record {
	rec.History = append([]string(nil), rec.History...)
	rec.ResponseBlocks = cloneResponseBlocks(rec.ResponseBlocks)
	rec.TaskOptions = cloneTaskOptions(rec.TaskOptions)
	return rec
}

func cloneResponseBlocks(in [][]blocks.Block) [][]blocks.Block {
	if in == nil {
		return nil
	}
	out := make([][]blocks.Block, len(in))
	for i := range in {
		out[i] = append([]blocks.Block(nil), in[i]...)
	}
	return out
}

func cloneTaskOptions(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	maps.Copy(out, in)
	return out
}

func testCoreBot(convs conversationStore) *Bot {
	return &Bot{
		log:          discardLogger(),
		convs:        convs,
		retryBackoff: 0,
	}
}
