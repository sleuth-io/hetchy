package bot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

// chatPersister periodically writes the in-flight conversation row to
// convstore so a mid-run reload (or a bot crash) doesn't lose the
// blocks accumulated so far. Without this, the only persistence
// happens at the terminal end-of-turn save — anyone who reloaded the
// page mid-run got a blank chat until the run finished.
//
// The persister writes via convstore.SaveProgress, which only touches
// the immutable-during-run fields (history, response_blocks,
// creator_id). The terminal Upsert at end-of-turn is unchanged and
// still owns the canonical sandbox_id / branch / pr_url / GitHub
// fields. This split avoids the race where a periodic save with an
// empty sandbox_id would clobber the terminal save's real value.
type chatPersister struct {
	log      *slog.Logger
	convs    *convstore.Store
	recorder *blocks.Recorder

	// orgID, threadID, history, creatorID are all immutable for the
	// lifetime of one HandleRequest call (history grows only at the
	// terminal save via appendBlocksAsNewTurn, which fires after the
	// persister has stopped).
	orgID, threadID string
	history         []string
	creatorID       string

	// priorBlocks captures rec.ResponseBlocks at HandleRequest
	// entry. For a fresh chat this is nil; for a follow-up it's the
	// previously-saved turn list. Each tick, we append the recorder
	// snapshot as the latest turn.
	priorBlocks [][]blocks.Block

	tick    time.Duration
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopMu  sync.Mutex
	stopped bool
}

// newChatPersister captures the immutable handler state. The caller
// starts the goroutine via Run; Stop blocks until the loop exits.
func newChatPersister(log *slog.Logger, convs *convstore.Store, recorder *blocks.Recorder, rec convstore.Record, tick time.Duration) *chatPersister {
	prior := make([][]blocks.Block, len(rec.ResponseBlocks))
	for i, t := range rec.ResponseBlocks {
		prior[i] = append([]blocks.Block(nil), t...)
	}
	return &chatPersister{
		log:         log,
		convs:       convs,
		recorder:    recorder,
		orgID:       rec.OrgID,
		threadID:    rec.ThreadID,
		history:     append([]string(nil), rec.History...),
		creatorID:   rec.CreatorID,
		priorBlocks: prior,
		tick:        tick,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// snapshot builds a Record ready for SaveProgress: immutable fields
// plus the live recorder snapshot appended as the current turn's
// blocks.
func (p *chatPersister) snapshot() convstore.Record {
	current := p.recorder.Snapshot()
	blocksOut := make([][]blocks.Block, 0, len(p.priorBlocks)+1)
	blocksOut = append(blocksOut, p.priorBlocks...)
	blocksOut = append(blocksOut, current)
	return convstore.Record{
		OrgID:          p.orgID,
		ThreadID:       p.threadID,
		History:        append([]string(nil), p.history...),
		CreatorID:      p.creatorID,
		ResponseBlocks: blocksOut,
	}
}

// Run launches the persister loop. Returns when Stop is called or
// ctx is done. Does NOT flush on exit — the handler's terminal
// Upsert is responsible for the final canonical state, and our
// stale shadow could otherwise overwrite it. The caller relies on
// the previous tick's flush to cover up to the last 2 s of blocks.
func (p *chatPersister) Run(ctx context.Context) {
	defer close(p.doneCh)
	t := time.NewTicker(p.tick)
	defer t.Stop()

	var lastDigest [32]byte
	flush := func() {
		rec := p.snapshot()
		// Hash the blocks payload so an idle tick (no new blocks
		// since last flush) doesn't churn the row.
		buf, err := json.Marshal(rec.ResponseBlocks)
		if err != nil {
			return
		}
		d := sha256.Sum256(buf)
		if d == lastDigest {
			return
		}
		lastDigest = d
		// Use a fresh background context so a cancelled parent
		// doesn't sabotage the in-flight save.
		if err := p.convs.SaveProgress(context.Background(), rec); err != nil {
			p.log.Warn("periodic persist failed",
				"org", rec.OrgID, "thread", rec.ThreadID, "error", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-t.C:
			flush()
		}
	}
}

// Stop signals the loop to exit and waits for it to finish.
// Idempotent. Safe to call multiple times.
func (p *chatPersister) Stop() {
	if p == nil {
		return
	}
	p.stopMu.Lock()
	if p.stopped {
		p.stopMu.Unlock()
		<-p.doneCh
		return
	}
	p.stopped = true
	p.stopMu.Unlock()
	close(p.stopCh)
	<-p.doneCh
}
