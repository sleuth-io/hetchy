package bot

import (
	"context"
	"sync"
	"time"

	"github.com/hetchyhq/hetchy/internal/runstore"
)

type fakeRunStore struct {
	mu      sync.Mutex
	enabled bool

	createRun      runstore.Run
	createInserted bool
	createErr      error
	createCalls    []runstore.Run

	getRun runstore.Run
	getErr error

	latestRun   runstore.Run
	latestErr   error
	latestCalls int

	activeRun runstore.Run
	activeErr error

	expiredRuns  []runstore.Run
	expiredErr   error
	expiredCalls []int32

	staleRuns  []runstore.Run
	staleErr   error
	staleCalls []fakeRunStaleCall

	activePrefixRuns  []runstore.Run
	activePrefixErr   error
	activePrefixCalls []fakeRunActivePrefixCall

	claimRun   runstore.Run
	claimErr   error
	claimCalls []fakeRunClaim

	claimStaleRun   runstore.Run
	claimStaleErr   error
	claimStaleCalls []fakeRunClaimStale

	claimFromOwnerRun   runstore.Run
	claimFromOwnerErr   error
	claimFromOwnerCalls []fakeRunClaimFromOwner

	cancelRun     runstore.Run
	cancelErr     error
	cancelPending []runstore.PendingEvent

	events    []runstore.Event
	eventsErr error

	nextSeq         int64
	appendErr       error
	appended        []fakeRunEventAppend
	batchErr        error
	batchSeqs       []int64
	batches         []fakeRunEventBatch
	touched         []fakeRunTouch
	updateKinds     []string
	updateBranches  []string
	updateSandboxes []string
	updateSessions  []string
	updateCommands  []fakeRunCommandUpdate
	updateStates    []fakeRunStateUpdate
	updateOutcomes  []fakeRunOutcomeUpdate
	updateCursors   []int64
}

type fakeRunEventAppend struct {
	runID      string
	seq        int64
	event      string
	data       []byte
	leaseOwner string
}

type fakeRunEventBatch struct {
	runID      string
	events     []runstore.PendingEvent
	cursor     int64
	leaseOwner string
}

type fakeRunTouch struct {
	runID      string
	leaseOwner string
	duration   time.Duration
}

type fakeRunActivePrefixCall struct {
	prefix string
	limit  int32
}

type fakeRunStaleCall struct {
	limit      int32
	staleAfter time.Duration
}

type fakeRunClaim struct {
	runID      string
	leaseOwner string
	duration   time.Duration
}

type fakeRunClaimStale struct {
	runID      string
	leaseOwner string
	duration   time.Duration
	staleAfter time.Duration
}

type fakeRunClaimFromOwner struct {
	runID         string
	leaseOwner    string
	previousOwner string
	duration      time.Duration
}

type fakeRunCommandUpdate struct {
	sessionID  string
	commandID  string
	step       string
	leaseOwner string
	duration   time.Duration
}

type fakeRunStateUpdate struct {
	state      string
	lastErr    string
	leaseOwner string
}

type fakeRunOutcomeUpdate struct {
	outcome      string
	detail       map[string]any
	qualityScore *int32
	leaseOwner   string
}

func (f *fakeRunStore) Enabled() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *fakeRunStore) Create(_ context.Context, run runstore.Run, _ string, _ time.Duration) (runstore.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, run)
	return f.createRun, f.createInserted, f.createErr
}

func (f *fakeRunStore) Get(context.Context, string) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getRun, f.getErr
}

func (f *fakeRunStore) LatestForThread(context.Context, string, string) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latestCalls++
	return f.latestRun, f.latestErr
}

func (f *fakeRunStore) ActiveForThread(context.Context, string, string) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeRun, f.activeErr
}

func (f *fakeRunStore) UpdateKind(_ context.Context, _, kind, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateKinds = append(f.updateKinds, kind)
}

func (f *fakeRunStore) UpdateBranch(_ context.Context, _, branch, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateBranches = append(f.updateBranches, branch)
}

func (f *fakeRunStore) UpdateSandbox(_ context.Context, _, sandboxID, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateSandboxes = append(f.updateSandboxes, sandboxID)
}

func (f *fakeRunStore) UpdateSession(_ context.Context, _, sessionID, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateSessions = append(f.updateSessions, sessionID)
}

func (f *fakeRunStore) UpdateCommand(_ context.Context, _, sessionID, commandID, step, leaseOwner string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCommands = append(f.updateCommands, fakeRunCommandUpdate{sessionID: sessionID, commandID: commandID, step: step, leaseOwner: leaseOwner, duration: d})
}

func (f *fakeRunStore) UpdateState(_ context.Context, _, state, lastErr, leaseOwner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateStates = append(f.updateStates, fakeRunStateUpdate{state: state, lastErr: lastErr, leaseOwner: leaseOwner})
}

func (f *fakeRunStore) UpdateOutcome(_ context.Context, _, outcome string, detail map[string]any, qualityScore *int32, leaseOwner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateOutcomes = append(f.updateOutcomes, fakeRunOutcomeUpdate{outcome: outcome, detail: detail, qualityScore: qualityScore, leaseOwner: leaseOwner})
}

func (f *fakeRunStore) TouchLease(_ context.Context, runID, leaseOwner string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, fakeRunTouch{runID: runID, leaseOwner: leaseOwner, duration: d})
}

func (f *fakeRunStore) UpdateLogCursor(_ context.Context, _ string, cursor int64, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCursors = append(f.updateCursors, cursor)
}

func (f *fakeRunStore) ListExpired(_ context.Context, limit int32) ([]runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expiredCalls = append(f.expiredCalls, limit)
	return append([]runstore.Run(nil), f.expiredRuns...), f.expiredErr
}

func (f *fakeRunStore) ListStale(_ context.Context, limit int32, staleAfter time.Duration) ([]runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staleCalls = append(f.staleCalls, fakeRunStaleCall{limit: limit, staleAfter: staleAfter})
	return append([]runstore.Run(nil), f.staleRuns...), f.staleErr
}

func (f *fakeRunStore) ListActiveForLeaseOwnerPrefix(_ context.Context, prefix string, limit int32) ([]runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activePrefixCalls = append(f.activePrefixCalls, fakeRunActivePrefixCall{prefix: prefix, limit: limit})
	return append([]runstore.Run(nil), f.activePrefixRuns...), f.activePrefixErr
}

func (f *fakeRunStore) Claim(_ context.Context, id, leaseOwner string, d time.Duration) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls = append(f.claimCalls, fakeRunClaim{runID: id, leaseOwner: leaseOwner, duration: d})
	if f.claimErr != nil {
		return runstore.Run{}, f.claimErr
	}
	out := f.claimRun
	if out.ID == "" {
		out.ID = id
	}
	if out.LeaseOwner == "" {
		out.LeaseOwner = leaseOwner
	}
	return out, nil
}

func (f *fakeRunStore) ClaimStale(_ context.Context, id, leaseOwner string, d, staleAfter time.Duration) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimStaleCalls = append(f.claimStaleCalls, fakeRunClaimStale{runID: id, leaseOwner: leaseOwner, duration: d, staleAfter: staleAfter})
	if f.claimStaleErr != nil {
		return runstore.Run{}, f.claimStaleErr
	}
	out := f.claimStaleRun
	if out.ID == "" {
		out.ID = id
	}
	if out.LeaseOwner == "" {
		out.LeaseOwner = leaseOwner
	}
	return out, nil
}

func (f *fakeRunStore) ClaimFromOwner(_ context.Context, id, leaseOwner, previousOwner string, d time.Duration) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimFromOwnerCalls = append(f.claimFromOwnerCalls, fakeRunClaimFromOwner{runID: id, leaseOwner: leaseOwner, previousOwner: previousOwner, duration: d})
	if f.claimFromOwnerErr != nil {
		return runstore.Run{}, f.claimFromOwnerErr
	}
	out := f.claimFromOwnerRun
	if out.ID == "" {
		out.ID = id
	}
	if out.LeaseOwner == "" {
		out.LeaseOwner = leaseOwner
	}
	return out, nil
}

func (f *fakeRunStore) Cancel(_ context.Context, id, lastErr, leaseOwner string, _ time.Duration, events []runstore.PendingEvent) (runstore.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelPending = append([]runstore.PendingEvent(nil), events...)
	if f.cancelRun.ID == "" {
		f.cancelRun.ID = id
	}
	f.cancelRun.State = runstore.StateCancelled
	f.cancelRun.LastError = lastErr
	f.cancelRun.LeaseOwner = leaseOwner
	return f.cancelRun, f.cancelErr
}

func (f *fakeRunStore) AppendEvent(_ context.Context, runID, event string, data []byte, leaseOwner string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return 0, f.appendErr
	}
	f.nextSeq++
	f.appended = append(f.appended, fakeRunEventAppend{
		runID:      runID,
		seq:        f.nextSeq,
		event:      event,
		data:       append([]byte(nil), data...),
		leaseOwner: leaseOwner,
	})
	return f.nextSeq, nil
}

func (f *fakeRunStore) AppendEventsAndAdvanceCursor(_ context.Context, runID string, events []runstore.PendingEvent, cursor int64, leaseOwner string) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	copied := append([]runstore.PendingEvent(nil), events...)
	f.batches = append(f.batches, fakeRunEventBatch{runID: runID, events: copied, cursor: cursor, leaseOwner: leaseOwner})
	if len(f.batchSeqs) >= len(events) {
		seqs := append([]int64(nil), f.batchSeqs[:len(events)]...)
		f.appendBatchEventsLocked(runID, events, seqs)
		return seqs, nil
	}
	seqs := make([]int64, len(events))
	for i := range seqs {
		f.nextSeq++
		seqs[i] = f.nextSeq
	}
	f.appendBatchEventsLocked(runID, events, seqs)
	return seqs, nil
}

func (f *fakeRunStore) appendBatchEventsLocked(runID string, events []runstore.PendingEvent, seqs []int64) {
	for i, ev := range events {
		if seqs[i] > f.nextSeq {
			f.nextSeq = seqs[i]
		}
		f.events = append(f.events, runstore.Event{
			RunID: runID,
			Seq:   seqs[i],
			Event: ev.Event,
			Data:  append([]byte(nil), ev.Data...),
		})
	}
}

func (f *fakeRunStore) EventsAfter(_ context.Context, runID string, afterSeq int64) ([]runstore.Event, error) {
	return f.eventsAfter(runID, afterSeq, 0)
}

func (f *fakeRunStore) EventsAfterLimit(_ context.Context, runID string, afterSeq int64, limit int32) ([]runstore.Event, error) {
	return f.eventsAfter(runID, afterSeq, limit)
}

func (f *fakeRunStore) eventsAfter(runID string, afterSeq int64, limit int32) ([]runstore.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	out := make([]runstore.Event, 0, len(f.events)+len(f.appended))
	for _, ev := range f.events {
		if ev.Seq > afterSeq && (ev.RunID == "" || ev.RunID == runID) {
			out = append(out, ev)
		}
	}
	for _, ev := range f.appended {
		if ev.seq > afterSeq && ev.runID == runID {
			out = append(out, runstore.Event{
				RunID: ev.runID,
				Seq:   ev.seq,
				Event: ev.event,
				Data:  append([]byte(nil), ev.data...),
			})
		}
	}
	if limit > 0 && len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
}
