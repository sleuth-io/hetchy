package bot

import (
	"context"
	"sync"
	"time"

	"github.com/hetchyhq/hetchy/internal/sessionlease"
)

// liveRun is the replica-local handle the agent goroutine holds while a
// chat turn is in flight. It is *not* the source of truth for SSE
// delivery — that's the `conversation_events` table + the `events.Fanout`
// LISTEN loop. liveRun only carries the state the owner needs to keep
// running on this replica (ctx + cancel + Daytona handle for the cancel-
// cleanup path).
//
// Before the multi-replica refactor, liveRun also buffered the full SSE
// history and the subscriber set in memory; that worked for single-
// replica but couldn't survive a restart and couldn't be observed from a
// peer replica. Those responsibilities moved to:
//
//   - events.Store (durable append-only log keyed by org_id, thread_id,
//     monotonic seq) — replaces history[]
//   - events.Fanout (one LISTEN per process; cross-replica delivery) —
//     replaces the subscriber map
//   - sessionlease.Lease (active_sessions row with renewable lease) —
//     replaces RegisterIfAbsent's TOCTOU-safe ownership claim
type liveRun struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu                     sync.Mutex
	cancelled              bool
	sandboxID              string
	cleanupSandboxOnCancel bool

	// lease is the active_sessions row this run owns. nil for tests that
	// construct a liveRun directly without going through liveRegistry.
	lease *sessionlease.Lease
}

func newLiveRun(ctx context.Context, cancel context.CancelFunc, lease *sessionlease.Lease) *liveRun {
	return &liveRun{
		ctx:    ctx,
		cancel: cancel,
		lease:  lease,
	}
}

func (r *liveRun) Context() context.Context { return r.ctx }

// Cancel marks the run cancelled and stops the agent context. Returns
// true on the winning call (the one that actually flipped the flag); a
// repeat call returns false so the cleanup-once dance below stays
// honest.
func (r *liveRun) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled {
		return false
	}
	r.cancelled = true
	r.cancel()
	return true
}

// Cancelled reports the local cancel flag. Distinct from
// lease.Cancelled, which is the cross-replica view from the DB —
// callers prefer the local flag for in-process branches and the lease
// flag for the renew-loop / recovery path.
func (r *liveRun) Cancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled {
		return true
	}
	if r.lease != nil && r.lease.Cancelled() {
		return true
	}
	return false
}

func (r *liveRun) Lease() *sessionlease.Lease { return r.lease }

func (r *liveRun) SetSandboxID(id string, cleanupOnCancel bool) {
	if id == "" {
		return
	}
	r.mu.Lock()
	r.sandboxID = id
	r.cleanupSandboxOnCancel = cleanupOnCancel
	r.mu.Unlock()
	if r.lease != nil {
		// Propagate to the lease (best-effort; the agent path also
		// stamps session_token + command_id once they arrive).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.lease.SetSandbox(ctx, id, "", "")
	}
}

func (r *liveRun) SandboxID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sandboxID
}

func (r *liveRun) CancelCleanupSandboxID() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sandboxID, r.cleanupSandboxOnCancel
}

// liveRegistry tracks the per-replica subset of in-flight runs whose
// agent goroutines are running on *this* process. Ownership is asserted
// at the cluster level by sessionlease.Manager; this map is just the
// local index so the cancel handler and Delete-conversation guard can
// find the run that this replica is driving.
//
// The map is intentionally NOT consulted by /chat/stream anymore — that
// endpoint is fully stateless across replicas via events.Fanout.
type liveRegistry struct {
	mu   sync.Mutex
	runs map[string]*liveRun
}

type liveRunContextKey struct{}

func contextWithLiveRun(ctx context.Context, run *liveRun) context.Context {
	return context.WithValue(ctx, liveRunContextKey{}, run)
}

func liveRunFromContext(ctx context.Context) *liveRun {
	run, _ := ctx.Value(liveRunContextKey{}).(*liveRun)
	return run
}

func liveRunCancelled(ctx context.Context) bool {
	run := liveRunFromContext(ctx)
	return run != nil && run.Cancelled()
}

func setLiveRunSandboxID(ctx context.Context, sandboxID string, cleanupOnCancel bool) {
	if run := liveRunFromContext(ctx); run != nil {
		run.SetSandboxID(sandboxID, cleanupOnCancel)
	}
}

func newLiveRegistry() *liveRegistry {
	return &liveRegistry{runs: map[string]*liveRun{}}
}

func liveKey(orgID, threadID string) string { return orgID + "\x00" + threadID }

// Register stores the run under (orgID, threadID). Called by chatHandler
// after the sessionlease.Claim has already won the cluster-level race,
// so we never overwrite a peer's run — at most we overwrite a stale
// local entry from a previous turn that didn't clean up (which is the
// correct behaviour: lease ownership trumps stale local state).
func (r *liveRegistry) Register(orgID, threadID string, run *liveRun) {
	key := liveKey(orgID, threadID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[key] = run
}

// Get returns the local run for this (orgID, threadID), or nil if this
// replica is not the owner. /chat/cancel uses this to find an
// in-process run to abort; /chat/stream does NOT — it goes through
// events.Fanout regardless.
func (r *liveRegistry) Get(orgID, threadID string) *liveRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[liveKey(orgID, threadID)]
}

// Done removes the run from the registry and cancels its ctx. Safe to
// call from a defer in the goroutine that called Register.
func (r *liveRegistry) Done(orgID, threadID string, run *liveRun) {
	key := liveKey(orgID, threadID)
	r.mu.Lock()
	if r.runs[key] == run {
		delete(r.runs, key)
	}
	r.mu.Unlock()
	run.Cancel()
}
