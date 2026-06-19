package bot

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

type fakeConversationStore struct {
	mu              sync.Mutex
	rec             convstore.Record
	getErr          error
	searchResult    []convstore.Record
	searchErr       error
	searchOpts      []convstore.SearchOptions
	saveProgressErr error
	upsertErr       error
	deleteErr       error
	renameErr       error

	upserts       []convstore.Record
	progressSaves []convstore.Record
	taskSaves     []map[string]bool
	attachments   []convstore.Attachment
}

func (f *fakeConversationStore) Get(context.Context, string, string) (convstore.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return convstore.Record{}, f.getErr
	}
	return cloneRecord(f.rec), nil
}

func (f *fakeConversationStore) Search(_ context.Context, _ string, opts convstore.SearchOptions) ([]convstore.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchOpts = append(f.searchOpts, opts)
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

func (f *fakeConversationStore) SaveRunMetadata(_ context.Context, rec convstore.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec.SandboxID != "" {
		f.rec.SandboxID = rec.SandboxID
	}
	if rec.Branch != "" {
		f.rec.Branch = rec.Branch
	}
	if rec.PRURL != "" {
		f.rec.PRURL = rec.PRURL
	}
	return nil
}

func (f *fakeConversationStore) SaveTaskOptions(_ context.Context, _, _ string, opts map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taskSaves = append(f.taskSaves, cloneTaskOptions(opts))
	return nil
}

func (f *fakeConversationStore) SaveAttachments(_ context.Context, attachments []convstore.Attachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range attachments {
		if a.ID == "" {
			a.ID = convstore.NewAttachmentID()
		}
		f.attachments = append(f.attachments, cloneAttachment(a))
	}
	return nil
}

func (f *fakeConversationStore) ListAttachments(_ context.Context, orgID, threadID string) ([]convstore.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]convstore.Attachment, 0, len(f.attachments))
	for _, a := range f.attachments {
		if a.OrgID != orgID || a.ThreadID != threadID {
			continue
		}
		out = append(out, cloneAttachment(a))
	}
	return out, nil
}

func (f *fakeConversationStore) ListAttachmentsForTurn(_ context.Context, orgID, threadID string, turnIndex int) ([]convstore.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []convstore.Attachment
	for _, a := range f.attachments {
		if a.OrgID == orgID && a.ThreadID == threadID && a.TurnIndex == turnIndex {
			out = append(out, cloneAttachment(a))
		}
	}
	return out, nil
}

func (f *fakeConversationStore) GetAttachment(_ context.Context, orgID, attachmentID string) (convstore.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.attachments {
		if a.OrgID == orgID && a.ID == attachmentID {
			return cloneAttachment(a), nil
		}
	}
	return convstore.Attachment{}, convstore.ErrNotFound
}

func (f *fakeConversationStore) DeleteAttachmentsForTurn(_ context.Context, orgID, threadID string, turnIndex int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.attachments[:0]
	for _, a := range f.attachments {
		if a.OrgID == orgID && a.ThreadID == threadID && a.TurnIndex == turnIndex {
			continue
		}
		out = append(out, a)
	}
	f.attachments = out
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

func cloneAttachment(in convstore.Attachment) convstore.Attachment {
	in.Data = append([]byte(nil), in.Data...)
	return in
}

func testCoreBot(convs conversationStore) *Bot {
	return &Bot{
		log:          discardLogger(),
		convs:        convs,
		retryBackoff: 0,
		// Default tests to a deterministic branch name so assertions
		// on rec.Branch stay stable and so no test fires HTTP to the
		// real Anthropic API while branch-naming. Individual tests can
		// override branchNameFn to exercise LLM-driven slug behaviour.
		branchNameFn: func(_ context.Context, _ orgcfg.Config, _ string) string {
			return "feature/sf-req-1"
		},
		followUpModeFn: func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision {
			return followUpModeDecision{Mode: followUpModeChange, Confidence: 1, Reason: "test default"}
		},
	}
}
