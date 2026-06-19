package bot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
)

// appendMode controls how the persister stitches the live recorder
// snapshot into rec.ResponseBlocks. The two modes mirror the
// terminal-Upsert helpers (appendBlocksToFirstTurn / appendBlocksAsNewTurn /
// appendBlocksToLastTurn) in bot.go — the persister and the terminal save MUST agree on
// shape, or a tick that lands after the terminal write would corrupt
// the row (e.g. produce 2 turns where the UI expects 1, dropping the
// second turn permanently).
type appendMode int

const (
	// appendToFirstTurn merges current blocks into rec.ResponseBlocks[0].
	// Used by "first encounter" paths: new chat with default repo and
	// awaiting-repo reply. The history slice has exactly one entry (the
	// user's first message) and one bot turn holds everything that happened.
	appendToFirstTurn appendMode = iota
	// appendAsNewTurn appends a fresh turn at the tail. Used by
	// handleFollowUp where rec.ResponseBlocks already has N completed
	// turns and the current run is producing turn N+1.
	appendAsNewTurn
	// appendToLastTurn merges current blocks into the existing last
	// history turn. Used by retry-after-failure after the retry message
	// has already been appended to history.
	appendToLastTurn
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
	convs    conversationStore
	recorder *blocks.Recorder

	// orgID, threadID, history, creatorID are all immutable for the
	// lifetime of one HandleRequest call (history grows only at the
	// terminal save via appendBlocksAsNewTurn, which fires after the
	// persister has stopped).
	orgID, threadID string
	history         []string
	creatorID       string

	// priorBlocks captures rec.ResponseBlocks at HandleRequest
	// entry. The mode controls how the live recorder snapshot gets
	// stitched in alongside it (see appendMode).
	priorBlocks [][]blocks.Block
	mode        appendMode

	tick    time.Duration
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopMu  sync.Mutex
	stopped bool
}

// newChatPersister captures the immutable handler state. The caller
// starts the goroutine via Run; Stop blocks until the loop exits.
// mode MUST match the terminal Upsert's append helper:
// appendBlocksToFirstTurn → appendToFirstTurn, appendBlocksAsNewTurn
// → appendAsNewTurn, appendBlocksToLastTurn → appendToLastTurn. A mismatch
// causes a late tick to overwrite the terminal save with a different shape,
// dropping turns from the UI.
func newChatPersister(log *slog.Logger, convs conversationStore, recorder *blocks.Recorder, rec convstore.Record, mode appendMode, tick time.Duration) *chatPersister {
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
		mode:        mode,
		tick:        tick,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// snapshot builds a Record ready for SaveProgress. Mirrors the
// terminal-Upsert append helper (appendToFirstTurn for first-encounter
// paths, appendAsNewTurn for follow-ups) so the row's response_blocks
// shape is identical whether we're writing mid-run or at end-of-turn.
func (p *chatPersister) snapshot() convstore.Record {
	current := p.recorder.Snapshot()
	var blocksOut [][]blocks.Block
	switch p.mode {
	case appendAsNewTurn:
		blocksOut = make([][]blocks.Block, 0, len(p.priorBlocks)+1)
		blocksOut = append(blocksOut, p.priorBlocks...)
		blocksOut = append(blocksOut, current)
	case appendToFirstTurn:
		// Merge the recorder snapshot into the existing first turn
		// (or seed turn 0 with it when the run started fresh).
		if len(p.priorBlocks) == 0 {
			blocksOut = [][]blocks.Block{current}
		} else {
			blocksOut = make([][]blocks.Block, len(p.priorBlocks))
			for i, t := range p.priorBlocks {
				blocksOut[i] = append([]blocks.Block(nil), t...)
			}
			blocksOut[0] = append(blocksOut[0], current...)
		}
	case appendToLastTurn:
		blocksOut = make([][]blocks.Block, len(p.priorBlocks))
		for i, t := range p.priorBlocks {
			blocksOut[i] = append([]blocks.Block(nil), t...)
		}
		// handleRetryAfterFailure calls appendBlocksAsNewTurn before
		// constructing this persister, so priorBlocks and history should
		// already be aligned. Keep the padding as a defensive guard.
		for len(blocksOut) < len(p.history) {
			blocksOut = append(blocksOut, nil)
		}
		if len(blocksOut) > len(p.history) {
			panic(fmt.Sprintf("chatPersister appendToLastTurn: priorBlocks (%d) longer than history (%d)", len(blocksOut), len(p.history)))
		}
		if len(p.history) == 0 {
			panic("chatPersister appendToLastTurn: history is empty")
		}
		last := len(p.history) - 1
		blocksOut[last] = append(blocksOut[last], current...)
	}
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
