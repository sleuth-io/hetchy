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

// appendMode controls how the persister stitches the live recorder
// snapshot into rec.ResponseBlocks. The two modes mirror the
// terminal-Upsert helpers (appendBlocksToFirstTurn / appendBlocksAsNewTurn)
// in bot.go — the persister and the terminal save MUST agree on
// shape, or a tick that lands after the terminal write would corrupt
// the row (e.g. produce 2 turns where the UI expects 1, dropping the
// second turn permanently).
type appendMode int

const (
	// appendToFirstTurn merges current blocks into rec.ResponseBlocks[0].
	// Used by every "first encounter" path: new chat with default repo,
	// awaiting-repo reply, retry-after-failure. The history slice has
	// exactly one entry (the user's first message) and one bot turn
	// holds everything that happened.
	appendToFirstTurn appendMode = iota
	// appendAsNewTurn appends a fresh turn at the tail. Used by
	// handleFollowUp where rec.ResponseBlocks already has N completed
	// turns and the current run is producing turn N+1.
	appendAsNewTurn
)

// chatPersister keeps the conversation row's response_blocks JSONB[]
// snapshot eventually consistent with the durable event log. Each
// emitted block is *already* in conversation_events (the source of
// truth for SSE streaming), so the persister no longer needs to fire
// frequent ticks just to avoid losing a mid-run crash — that case is
// now covered by recovery + replay.
//
// What the persister still buys us: a derived row that the sidebar
// list (/api/conversations) and chat detail (/api/conversations/{id})
// can render without scanning the events table. We tick on a long
// interval (30s) as a safety net and write one last snapshot on Stop
// so a normal turn end has the freshest data in the row.
//
// The persister writes via convstore.SaveProgress, which keeps
// branch / pr_url / github_owner / github_repo / agent_slug / model
// untouched (those still come from the terminal Upsert) and stamps
// sandbox_id monotonically once it's known.
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
// → appendAsNewTurn. A mismatch causes a late tick to overwrite the
// terminal save with a different shape, dropping turns from the UI.
func newChatPersister(log *slog.Logger, convs *convstore.Store, recorder *blocks.Recorder, rec convstore.Record, mode appendMode, tick time.Duration) *chatPersister {
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
// ctx is done. The loop ticks slowly (default 30s) — durable
// event streaming is the primary durability path now, the row's
// JSONB[] snapshot is just a derived list-view cache. The terminal
// Stop fires one final flush so a normal turn end leaves the
// freshest data in the row without depending on the next tick.
func (p *chatPersister) Run(ctx context.Context) {
	defer close(p.doneCh)
	t := time.NewTicker(p.tick)
	defer t.Stop()

	var lastDigest [32]byte
	flush := func() {
		rec := p.snapshot()
		buf, err := json.Marshal(rec.ResponseBlocks)
		if err != nil {
			return
		}
		d := sha256.Sum256(buf)
		if d == lastDigest {
			return
		}
		lastDigest = d
		if err := p.convs.SaveProgress(context.Background(), rec); err != nil {
			p.log.Warn("periodic persist failed",
				"org", rec.OrgID, "thread", rec.ThreadID, "error", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-p.stopCh:
			flush()
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
