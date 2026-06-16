package runstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// --- fake querier ---

type fakeQuerier struct {
	createAgentRun                   func(ctx context.Context, arg sqlc.CreateAgentRunParams) (sqlc.AgentRun, error)
	getAgentRun                      func(ctx context.Context, id string) (sqlc.AgentRun, error)
	getAgentRunByRequest             func(ctx context.Context, arg sqlc.GetAgentRunByRequestParams) (sqlc.AgentRun, error)
	getActiveAgentRunForThread       func(ctx context.Context, arg sqlc.GetActiveAgentRunForThreadParams) (sqlc.AgentRun, error)
	getLatestAgentRunForThread       func(ctx context.Context, arg sqlc.GetLatestAgentRunForThreadParams) (sqlc.AgentRun, error)
	listLatestAgentRunsForThreads    func(ctx context.Context, arg sqlc.ListLatestAgentRunsForThreadsParams) ([]sqlc.AgentRun, error)
	updateAgentRunKind               func(ctx context.Context, arg sqlc.UpdateAgentRunKindParams) error
	updateAgentRunBranch             func(ctx context.Context, arg sqlc.UpdateAgentRunBranchParams) error
	updateAgentRunSandbox            func(ctx context.Context, arg sqlc.UpdateAgentRunSandboxParams) error
	updateAgentRunSession            func(ctx context.Context, arg sqlc.UpdateAgentRunSessionParams) error
	updateAgentRunCommand            func(ctx context.Context, arg sqlc.UpdateAgentRunCommandParams) error
	updateAgentRunState              func(ctx context.Context, arg sqlc.UpdateAgentRunStateParams) error
	updateAgentRunOutcome            func(ctx context.Context, arg sqlc.UpdateAgentRunOutcomeParams) error
	touchAgentRunLease               func(ctx context.Context, arg sqlc.TouchAgentRunLeaseParams) error
	updateAgentRunLogCursor          func(ctx context.Context, arg sqlc.UpdateAgentRunLogCursorParams) error
	listExpiredAgentRuns             func(ctx context.Context, limit int32) ([]sqlc.AgentRun, error)
	listStaleAgentRuns               func(ctx context.Context, arg sqlc.ListStaleAgentRunsParams) ([]sqlc.AgentRun, error)
	listActiveAgentRunsForLeaseOwner func(ctx context.Context, arg sqlc.ListActiveAgentRunsForLeaseOwnerPrefixParams) ([]sqlc.AgentRun, error)
	claimAgentRunLease               func(ctx context.Context, arg sqlc.ClaimAgentRunLeaseParams) (sqlc.AgentRun, error)
	claimAgentRunLeaseFromOwner      func(ctx context.Context, arg sqlc.ClaimAgentRunLeaseFromOwnerParams) (sqlc.AgentRun, error)
	claimStaleAgentRunLease          func(ctx context.Context, arg sqlc.ClaimStaleAgentRunLeaseParams) (sqlc.AgentRun, error)
	appendAgentRunEvent              func(ctx context.Context, arg sqlc.AppendAgentRunEventParams) (int64, error)
	listAgentRunEventsFromSeq        func(ctx context.Context, arg sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error)
}

func (f *fakeQuerier) CreateAgentRun(ctx context.Context, arg sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
	if f.createAgentRun == nil {
		panic("fakeQuerier.createAgentRun not set")
	}
	return f.createAgentRun(ctx, arg)
}
func (f *fakeQuerier) GetAgentRun(ctx context.Context, id string) (sqlc.AgentRun, error) {
	if f.getAgentRun == nil {
		panic("fakeQuerier.getAgentRun not set")
	}
	return f.getAgentRun(ctx, id)
}
func (f *fakeQuerier) GetAgentRunByRequest(ctx context.Context, arg sqlc.GetAgentRunByRequestParams) (sqlc.AgentRun, error) {
	if f.getAgentRunByRequest == nil {
		panic("fakeQuerier.getAgentRunByRequest not set")
	}
	return f.getAgentRunByRequest(ctx, arg)
}
func (f *fakeQuerier) GetActiveAgentRunForThread(ctx context.Context, arg sqlc.GetActiveAgentRunForThreadParams) (sqlc.AgentRun, error) {
	if f.getActiveAgentRunForThread == nil {
		panic("fakeQuerier.getActiveAgentRunForThread not set")
	}
	return f.getActiveAgentRunForThread(ctx, arg)
}
func (f *fakeQuerier) GetLatestAgentRunForThread(ctx context.Context, arg sqlc.GetLatestAgentRunForThreadParams) (sqlc.AgentRun, error) {
	if f.getLatestAgentRunForThread == nil {
		panic("fakeQuerier.getLatestAgentRunForThread not set")
	}
	return f.getLatestAgentRunForThread(ctx, arg)
}
func (f *fakeQuerier) ListLatestAgentRunsForThreads(ctx context.Context, arg sqlc.ListLatestAgentRunsForThreadsParams) ([]sqlc.AgentRun, error) {
	if f.listLatestAgentRunsForThreads == nil {
		panic("fakeQuerier.listLatestAgentRunsForThreads not set")
	}
	return f.listLatestAgentRunsForThreads(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunKind(ctx context.Context, arg sqlc.UpdateAgentRunKindParams) error {
	if f.updateAgentRunKind == nil {
		return nil
	}
	return f.updateAgentRunKind(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunBranch(ctx context.Context, arg sqlc.UpdateAgentRunBranchParams) error {
	if f.updateAgentRunBranch == nil {
		return nil
	}
	return f.updateAgentRunBranch(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunSandbox(ctx context.Context, arg sqlc.UpdateAgentRunSandboxParams) error {
	if f.updateAgentRunSandbox == nil {
		return nil
	}
	return f.updateAgentRunSandbox(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunSession(ctx context.Context, arg sqlc.UpdateAgentRunSessionParams) error {
	if f.updateAgentRunSession == nil {
		return nil
	}
	return f.updateAgentRunSession(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunCommand(ctx context.Context, arg sqlc.UpdateAgentRunCommandParams) error {
	if f.updateAgentRunCommand == nil {
		return nil
	}
	return f.updateAgentRunCommand(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunState(ctx context.Context, arg sqlc.UpdateAgentRunStateParams) error {
	if f.updateAgentRunState == nil {
		return nil
	}
	return f.updateAgentRunState(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunOutcome(ctx context.Context, arg sqlc.UpdateAgentRunOutcomeParams) error {
	if f.updateAgentRunOutcome == nil {
		return nil
	}
	return f.updateAgentRunOutcome(ctx, arg)
}
func (f *fakeQuerier) TouchAgentRunLease(ctx context.Context, arg sqlc.TouchAgentRunLeaseParams) error {
	if f.touchAgentRunLease == nil {
		return nil
	}
	return f.touchAgentRunLease(ctx, arg)
}
func (f *fakeQuerier) UpdateAgentRunLogCursor(ctx context.Context, arg sqlc.UpdateAgentRunLogCursorParams) error {
	if f.updateAgentRunLogCursor == nil {
		return nil
	}
	return f.updateAgentRunLogCursor(ctx, arg)
}
func (f *fakeQuerier) ListExpiredAgentRuns(ctx context.Context, limit int32) ([]sqlc.AgentRun, error) {
	if f.listExpiredAgentRuns == nil {
		panic("fakeQuerier.listExpiredAgentRuns not set")
	}
	return f.listExpiredAgentRuns(ctx, limit)
}
func (f *fakeQuerier) ListStaleAgentRuns(ctx context.Context, arg sqlc.ListStaleAgentRunsParams) ([]sqlc.AgentRun, error) {
	if f.listStaleAgentRuns == nil {
		panic("fakeQuerier.listStaleAgentRuns not set")
	}
	return f.listStaleAgentRuns(ctx, arg)
}
func (f *fakeQuerier) ListActiveAgentRunsForLeaseOwnerPrefix(ctx context.Context, arg sqlc.ListActiveAgentRunsForLeaseOwnerPrefixParams) ([]sqlc.AgentRun, error) {
	if f.listActiveAgentRunsForLeaseOwner == nil {
		panic("fakeQuerier.listActiveAgentRunsForLeaseOwner not set")
	}
	return f.listActiveAgentRunsForLeaseOwner(ctx, arg)
}
func (f *fakeQuerier) ClaimAgentRunLease(ctx context.Context, arg sqlc.ClaimAgentRunLeaseParams) (sqlc.AgentRun, error) {
	if f.claimAgentRunLease == nil {
		panic("fakeQuerier.claimAgentRunLease not set")
	}
	return f.claimAgentRunLease(ctx, arg)
}
func (f *fakeQuerier) ClaimAgentRunLeaseFromOwner(ctx context.Context, arg sqlc.ClaimAgentRunLeaseFromOwnerParams) (sqlc.AgentRun, error) {
	if f.claimAgentRunLeaseFromOwner == nil {
		panic("fakeQuerier.claimAgentRunLeaseFromOwner not set")
	}
	return f.claimAgentRunLeaseFromOwner(ctx, arg)
}
func (f *fakeQuerier) ClaimStaleAgentRunLease(ctx context.Context, arg sqlc.ClaimStaleAgentRunLeaseParams) (sqlc.AgentRun, error) {
	if f.claimStaleAgentRunLease == nil {
		panic("fakeQuerier.claimStaleAgentRunLease not set")
	}
	return f.claimStaleAgentRunLease(ctx, arg)
}
func (f *fakeQuerier) AppendAgentRunEvent(ctx context.Context, arg sqlc.AppendAgentRunEventParams) (int64, error) {
	if f.appendAgentRunEvent == nil {
		panic("fakeQuerier.appendAgentRunEvent not set")
	}
	return f.appendAgentRunEvent(ctx, arg)
}
func (f *fakeQuerier) ListAgentRunEventsFromSeq(ctx context.Context, arg sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error) {
	if f.listAgentRunEventsFromSeq == nil {
		panic("fakeQuerier.listAgentRunEventsFromSeq not set")
	}
	return f.listAgentRunEventsFromSeq(ctx, arg)
}

// --- fake txQuerier ---

type fakeTxQuerier struct {
	claimAgentRunForCancel  func(ctx context.Context, arg sqlc.ClaimAgentRunForCancelParams) (sqlc.AgentRun, error)
	appendAgentRunEvent     func(ctx context.Context, arg sqlc.AppendAgentRunEventParams) (int64, error)
	updateAgentRunState     func(ctx context.Context, arg sqlc.UpdateAgentRunStateParams) error
	updateAgentRunLogCursor func(ctx context.Context, arg sqlc.UpdateAgentRunLogCursorParams) error
}

func (f *fakeTxQuerier) ClaimAgentRunForCancel(ctx context.Context, arg sqlc.ClaimAgentRunForCancelParams) (sqlc.AgentRun, error) {
	if f.claimAgentRunForCancel == nil {
		panic("fakeTxQuerier.claimAgentRunForCancel not set")
	}
	return f.claimAgentRunForCancel(ctx, arg)
}
func (f *fakeTxQuerier) AppendAgentRunEvent(ctx context.Context, arg sqlc.AppendAgentRunEventParams) (int64, error) {
	if f.appendAgentRunEvent == nil {
		return 1, nil
	}
	return f.appendAgentRunEvent(ctx, arg)
}
func (f *fakeTxQuerier) UpdateAgentRunState(ctx context.Context, arg sqlc.UpdateAgentRunStateParams) error {
	if f.updateAgentRunState == nil {
		return nil
	}
	return f.updateAgentRunState(ctx, arg)
}
func (f *fakeTxQuerier) UpdateAgentRunLogCursor(ctx context.Context, arg sqlc.UpdateAgentRunLogCursorParams) error {
	if f.updateAgentRunLogCursor == nil {
		return nil
	}
	return f.updateAgentRunLogCursor(ctx, arg)
}

// newFakeStore creates a Store backed by the given fakeQuerier.
// Calling Cancel or AppendEventsAndAdvanceCursor with a non-empty ID on this
// store will panic — use newFakeStoreWithTx for those paths.
func newFakeStore(q *fakeQuerier) *Store {
	return &Store{
		q: q,
		tx: func(_ context.Context, _ func(txQuerier) error) error {
			panic("runstore: tx called on a non-tx fakeStore — use newFakeStoreWithTx")
		},
	}
}

// newFakeStoreWithTx creates a Store backed by q and a synchronous txRunner.
func newFakeStoreWithTx(q *fakeQuerier, txq *fakeTxQuerier) *Store {
	return &Store{
		q: q,
		tx: func(_ context.Context, fn func(txQuerier) error) error {
			return fn(txq)
		},
	}
}

func makeRun(id, orgID, threadID string) sqlc.AgentRun {
	return sqlc.AgentRun{
		ID:            id,
		OrgID:         orgID,
		ThreadID:      threadID,
		RunKind:       "fresh",
		RequestID:     "req-" + id,
		TriggerSource: TriggerUser,
		State:         StatePreparing,
	}
}

// --- Enabled ---

func TestEnabledNilStore(t *testing.T) {
	var s *Store
	if s.Enabled() {
		t.Error("nil store should not be enabled")
	}
}

func TestEnabledNilQuerier(t *testing.T) {
	s := &Store{}
	if s.Enabled() {
		t.Error("store with nil querier should not be enabled")
	}
}

func TestEnabledWithQuerier(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	if !s.Enabled() {
		t.Error("store with querier should be enabled")
	}
}

// --- Get ---

func TestGetDisabled(t *testing.T) {
	s := &Store{}
	_, err := s.Get(t.Context(), "run-1")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled Get: want pgx.ErrNoRows, got %v", err)
	}
}

func TestGetReturnsRun(t *testing.T) {
	want := makeRun("run-1", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		getAgentRun: func(_ context.Context, id string) (sqlc.AgentRun, error) {
			if id != "run-1" {
				t.Errorf("Get: unexpected id %q", id)
			}
			return want, nil
		},
	})
	got, err := s.Get(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "run-1" || got.OrgID != "org-1" || got.ThreadID != "thread-1" {
		t.Errorf("Get: wrong result %+v", got)
	}
}

func TestGetPropagatesError(t *testing.T) {
	sentinel := errors.New("db error")
	s := newFakeStore(&fakeQuerier{
		getAgentRun: func(_ context.Context, _ string) (sqlc.AgentRun, error) { return sqlc.AgentRun{}, sentinel },
	})
	if _, err := s.Get(t.Context(), "x"); !errors.Is(err, sentinel) {
		t.Errorf("Get propagate: want sentinel, got %v", err)
	}
}

// --- LatestForThread ---

func TestLatestForThreadDisabled(t *testing.T) {
	s := &Store{}
	if _, err := s.LatestForThread(t.Context(), "org", "thread"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled LatestForThread: want ErrNoRows, got %v", err)
	}
}

func TestLatestForThreadReturnsRun(t *testing.T) {
	want := makeRun("run-2", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		getLatestAgentRunForThread: func(_ context.Context, arg sqlc.GetLatestAgentRunForThreadParams) (sqlc.AgentRun, error) {
			return want, nil
		},
	})
	got, err := s.LatestForThread(t.Context(), "org-1", "thread-1")
	if err != nil {
		t.Fatalf("LatestForThread: %v", err)
	}
	if got.ID != "run-2" {
		t.Errorf("LatestForThread: wrong id %q", got.ID)
	}
}

// --- LatestForThreads ---

func TestLatestForThreadsDisabledReturnsEmpty(t *testing.T) {
	s := &Store{}
	got, err := s.LatestForThreads(t.Context(), "org", []string{"t1"})
	if err != nil || len(got) != 0 {
		t.Errorf("disabled LatestForThreads: want empty, got %v / %v", got, err)
	}
}

func TestLatestForThreadsEmptySliceReturnsEmpty(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	got, err := s.LatestForThreads(t.Context(), "org", nil)
	if err != nil || len(got) != 0 {
		t.Errorf("empty threadIDs: want empty, got %v / %v", got, err)
	}
}

func TestLatestForThreadsReturnsMap(t *testing.T) {
	rows := []sqlc.AgentRun{
		makeRun("run-a", "org-1", "thread-a"),
		makeRun("run-b", "org-1", "thread-b"),
	}
	s := newFakeStore(&fakeQuerier{
		listLatestAgentRunsForThreads: func(_ context.Context, arg sqlc.ListLatestAgentRunsForThreadsParams) ([]sqlc.AgentRun, error) {
			return rows, nil
		},
	})
	got, err := s.LatestForThreads(t.Context(), "org-1", []string{"thread-a", "thread-b"})
	if err != nil {
		t.Fatalf("LatestForThreads: %v", err)
	}
	if len(got) != 2 || got["thread-a"].ID != "run-a" || got["thread-b"].ID != "run-b" {
		t.Errorf("LatestForThreads: wrong map %v", got)
	}
}

func TestLatestForThreadsPropagatesError(t *testing.T) {
	sentinel := errors.New("db error")
	s := newFakeStore(&fakeQuerier{
		listLatestAgentRunsForThreads: func(_ context.Context, _ sqlc.ListLatestAgentRunsForThreadsParams) ([]sqlc.AgentRun, error) {
			return nil, sentinel
		},
	})
	if _, err := s.LatestForThreads(t.Context(), "org", []string{"t"}); !errors.Is(err, sentinel) {
		t.Errorf("LatestForThreads error: want sentinel, got %v", err)
	}
}

// --- ActiveForThread ---

func TestActiveForThreadDisabled(t *testing.T) {
	s := &Store{}
	if _, err := s.ActiveForThread(t.Context(), "org", "thread"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled ActiveForThread: want ErrNoRows, got %v", err)
	}
}

func TestActiveForThreadReturnsRun(t *testing.T) {
	want := makeRun("run-3", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		getActiveAgentRunForThread: func(_ context.Context, _ sqlc.GetActiveAgentRunForThreadParams) (sqlc.AgentRun, error) {
			return want, nil
		},
	})
	got, err := s.ActiveForThread(t.Context(), "org-1", "thread-1")
	if err != nil || got.ID != "run-3" {
		t.Errorf("ActiveForThread: want run-3, got %v / %v", got.ID, err)
	}
}

// --- Create ---

func TestCreateDisabledReturnsEmpty(t *testing.T) {
	s := &Store{}
	r, inserted, err := s.Create(t.Context(), Run{}, "owner", time.Minute)
	if err != nil || inserted || r.ID != "" {
		t.Errorf("disabled Create: want zero, got %+v %v %v", r, inserted, err)
	}
}

func TestCreateInsertsNewRun(t *testing.T) {
	row := makeRun("run-new", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		createAgentRun: func(_ context.Context, _ sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
			return row, nil
		},
	})
	got, inserted, err := s.Create(t.Context(), Run{
		ID: "run-new", OrgID: "org-1", ThreadID: "thread-1",
		RunKind: "fresh", RequestID: "req-1", UserRequest: "do something",
	}, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("Create insert: %v", err)
	}
	if !inserted || got.ID != "run-new" {
		t.Errorf("Create insert: inserted=%v id=%q", inserted, got.ID)
	}
}

func TestCreateDeduplicatesByRequestID(t *testing.T) {
	existing := makeRun("run-existing", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		createAgentRun: func(_ context.Context, _ sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, pgx.ErrNoRows
		},
		getAgentRunByRequest: func(_ context.Context, _ sqlc.GetAgentRunByRequestParams) (sqlc.AgentRun, error) {
			return existing, nil
		},
	})
	got, inserted, err := s.Create(t.Context(), Run{OrgID: "org-1", ThreadID: "thread-1", RequestID: "req-dup"}, "w", time.Minute)
	if err != nil || inserted || got.ID != "run-existing" {
		t.Errorf("Create dedup: inserted=%v id=%q err=%v", inserted, got.ID, err)
	}
}

func TestCreateFallsBackToActiveThread(t *testing.T) {
	active := makeRun("run-active", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		createAgentRun: func(_ context.Context, _ sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, pgx.ErrNoRows
		},
		getAgentRunByRequest: func(_ context.Context, _ sqlc.GetAgentRunByRequestParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, pgx.ErrNoRows
		},
		getActiveAgentRunForThread: func(_ context.Context, _ sqlc.GetActiveAgentRunForThreadParams) (sqlc.AgentRun, error) {
			return active, nil
		},
	})
	got, inserted, err := s.Create(t.Context(), Run{OrgID: "org-1", ThreadID: "thread-1", RequestID: "req-new"}, "w", time.Minute)
	if err != nil || inserted || got.ID != "run-active" {
		t.Errorf("Create fallback: inserted=%v id=%q err=%v", inserted, got.ID, err)
	}
}

func TestCreateErrorsWhenRequestIDUsedByTerminalRun(t *testing.T) {
	s := newFakeStore(&fakeQuerier{
		createAgentRun: func(_ context.Context, _ sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, pgx.ErrNoRows
		},
		getAgentRunByRequest: func(_ context.Context, _ sqlc.GetAgentRunByRequestParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, pgx.ErrNoRows
		},
		getActiveAgentRunForThread: func(_ context.Context, _ sqlc.GetActiveAgentRunForThreadParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, pgx.ErrNoRows
		},
	})
	_, _, err := s.Create(t.Context(), Run{OrgID: "org-1", ThreadID: "thread-1", RequestID: "req-terminal"}, "w", time.Minute)
	if err == nil {
		t.Error("Create terminal: expected error, got nil")
	}
}

func TestCreatePropagatesCreateError(t *testing.T) {
	sentinel := errors.New("insert failed")
	s := newFakeStore(&fakeQuerier{
		createAgentRun: func(_ context.Context, _ sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, sentinel
		},
	})
	_, _, err := s.Create(t.Context(), Run{}, "w", time.Minute)
	if !errors.Is(err, sentinel) {
		t.Errorf("Create error propagate: want sentinel, got %v", err)
	}
}

func TestCreateDefaultsTriggerSourceToUser(t *testing.T) {
	var gotSource string
	s := newFakeStore(&fakeQuerier{
		createAgentRun: func(_ context.Context, arg sqlc.CreateAgentRunParams) (sqlc.AgentRun, error) {
			gotSource = arg.TriggerSource
			return sqlc.AgentRun{TriggerSource: arg.TriggerSource}, nil
		},
	})
	if _, _, err := s.Create(t.Context(), Run{TriggerSource: ""}, "w", time.Minute); err != nil {
		t.Fatalf("Create trigger default: %v", err)
	}
	if gotSource != TriggerUser {
		t.Errorf("Create trigger default: want %q, got %q", TriggerUser, gotSource)
	}
}

// --- Update* methods (void, errors ignored) ---

func TestNewNilReturnsDisabledStore(t *testing.T) {
	s := New(nil)
	if s == nil || s.Enabled() {
		t.Error("New(nil): want non-nil disabled store")
	}
}

func TestUpdateKindNoop(t *testing.T) {
	// disabled store: no panic
	var s *Store
	s.UpdateKind(t.Context(), "run-1", "fresh", "w")

	// empty id: no panic
	newFakeStore(&fakeQuerier{}).UpdateKind(t.Context(), "", "fresh", "w")
}

func TestUpdateBranchNoop(t *testing.T) {
	var s *Store
	s.UpdateBranch(t.Context(), "run-1", "main", "w")
	newFakeStore(&fakeQuerier{}).UpdateBranch(t.Context(), "", "main", "w")
}

func TestUpdateSandboxNoop(t *testing.T) {
	var s *Store
	s.UpdateSandbox(t.Context(), "run-1", "sb-1", "w")
	newFakeStore(&fakeQuerier{}).UpdateSandbox(t.Context(), "", "sb-1", "w")
}

func TestUpdateSessionNoop(t *testing.T) {
	var s *Store
	s.UpdateSession(t.Context(), "run-1", "sess-1", "w")
	newFakeStore(&fakeQuerier{}).UpdateSession(t.Context(), "", "sess-1", "w")
}

func TestUpdateCommandNoop(t *testing.T) {
	var s *Store
	s.UpdateCommand(t.Context(), "run-1", "sess", "cmd", "step", "w", time.Minute)
	newFakeStore(&fakeQuerier{}).UpdateCommand(t.Context(), "", "sess", "cmd", "step", "w", time.Minute)
}

func TestUpdateStateNoop(t *testing.T) {
	var s *Store
	s.UpdateState(t.Context(), "run-1", StateRunning, "", "w")
	newFakeStore(&fakeQuerier{}).UpdateState(t.Context(), "", StateRunning, "", "w")
}

func TestUpdateOutcomeNoop(t *testing.T) {
	var s *Store
	s.UpdateOutcome(t.Context(), "run-1", OutcomeCompletedNoPR, nil, nil, "w")
	newFakeStore(&fakeQuerier{}).UpdateOutcome(t.Context(), "", OutcomeCompletedNoPR, nil, nil, "w")
}

func TestTouchLeaseNoop(t *testing.T) {
	var s *Store
	s.TouchLease(t.Context(), "run-1", "w", time.Minute)
	newFakeStore(&fakeQuerier{}).TouchLease(t.Context(), "", "w", time.Minute)
}

func TestUpdateLogCursorNoop(t *testing.T) {
	var s *Store
	s.UpdateLogCursor(t.Context(), "run-1", 1, "w")
	newFakeStore(&fakeQuerier{}).UpdateLogCursor(t.Context(), "", 1, "w")
}

func TestUpdateKindCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunKind: func(_ context.Context, arg sqlc.UpdateAgentRunKindParams) error {
			called = true
			if arg.ID != "run-1" || arg.RunKind != "recovery" {
				t.Errorf("UpdateKind args: %+v", arg)
			}
			return nil
		},
	})
	s.UpdateKind(t.Context(), "run-1", "recovery", "worker")
	if !called {
		t.Error("UpdateKind: querier not called")
	}
}

func TestUpdateBranchCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunBranch: func(_ context.Context, arg sqlc.UpdateAgentRunBranchParams) error {
			called = true
			if arg.Branch != "feature/x" {
				t.Errorf("UpdateBranch: want feature/x, got %q", arg.Branch)
			}
			return nil
		},
	})
	s.UpdateBranch(t.Context(), "run-1", "feature/x", "w")
	if !called {
		t.Error("UpdateBranch: querier not called")
	}
}

func TestUpdateSandboxCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunSandbox: func(_ context.Context, arg sqlc.UpdateAgentRunSandboxParams) error {
			called = true
			return nil
		},
	})
	s.UpdateSandbox(t.Context(), "run-1", "sandbox-x", "w")
	if !called {
		t.Error("UpdateSandbox: querier not called")
	}
}

func TestUpdateSessionCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunSession: func(_ context.Context, _ sqlc.UpdateAgentRunSessionParams) error {
			called = true
			return nil
		},
	})
	s.UpdateSession(t.Context(), "run-1", "sess-1", "w")
	if !called {
		t.Error("UpdateSession: querier not called")
	}
}

func TestUpdateCommandCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunCommand: func(_ context.Context, arg sqlc.UpdateAgentRunCommandParams) error {
			called = true
			if arg.CommandID != "cmd-1" || arg.CommandStep != "step-a" {
				t.Errorf("UpdateCommand args: %+v", arg)
			}
			return nil
		},
	})
	s.UpdateCommand(t.Context(), "run-1", "sess-1", "cmd-1", "step-a", "w", time.Minute)
	if !called {
		t.Error("UpdateCommand: querier not called")
	}
}

func TestUpdateStateCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunState: func(_ context.Context, arg sqlc.UpdateAgentRunStateParams) error {
			called = true
			if arg.State != StateRunning {
				t.Errorf("UpdateState: want %q, got %q", StateRunning, arg.State)
			}
			return nil
		},
	})
	s.UpdateState(t.Context(), "run-1", StateRunning, "", "w")
	if !called {
		t.Error("UpdateState: querier not called")
	}
}

func TestUpdateOutcomeCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunOutcome: func(_ context.Context, arg sqlc.UpdateAgentRunOutcomeParams) error {
			called = true
			if arg.Outcome != OutcomeCompletedNoPR {
				t.Errorf("UpdateOutcome: want %q, got %q", OutcomeCompletedNoPR, arg.Outcome)
			}
			return nil
		},
	})
	score := int32(90)
	s.UpdateOutcome(t.Context(), "run-1", OutcomeCompletedNoPR, map[string]any{"k": "v"}, &score, "w")
	if !called {
		t.Error("UpdateOutcome: querier not called")
	}
}

func TestUpdateOutcomeWithNilDetail(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunOutcome: func(_ context.Context, arg sqlc.UpdateAgentRunOutcomeParams) error {
			called = true
			if string(arg.OutcomeDetail) != "{}" {
				t.Errorf("UpdateOutcome nil detail: want {}, got %s", arg.OutcomeDetail)
			}
			return nil
		},
	})
	s.UpdateOutcome(t.Context(), "run-1", OutcomeCompletedNoPR, nil, nil, "w")
	if !called {
		t.Error("UpdateOutcome nil: querier not called")
	}
}

func TestTouchLeaseCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		touchAgentRunLease: func(_ context.Context, arg sqlc.TouchAgentRunLeaseParams) error {
			called = true
			if arg.ID != "run-1" || arg.LeaseOwner != "worker" {
				t.Errorf("TouchLease args: %+v", arg)
			}
			return nil
		},
	})
	s.TouchLease(t.Context(), "run-1", "worker", time.Minute)
	if !called {
		t.Error("TouchLease: querier not called")
	}
}

func TestUpdateLogCursorCallsQuerier(t *testing.T) {
	called := false
	s := newFakeStore(&fakeQuerier{
		updateAgentRunLogCursor: func(_ context.Context, arg sqlc.UpdateAgentRunLogCursorParams) error {
			called = true
			if arg.LogCursor != 42 {
				t.Errorf("UpdateLogCursor: want 42, got %d", arg.LogCursor)
			}
			return nil
		},
	})
	s.UpdateLogCursor(t.Context(), "run-1", 42, "worker")
	if !called {
		t.Error("UpdateLogCursor: querier not called")
	}
}

// --- ListExpired ---

func TestListExpiredDisabledReturnsNil(t *testing.T) {
	s := &Store{}
	got, err := s.ListExpired(t.Context(), 10)
	if err != nil || got != nil {
		t.Errorf("disabled ListExpired: want nil, got %v / %v", got, err)
	}
}

func TestListExpiredReturnsRuns(t *testing.T) {
	rows := []sqlc.AgentRun{makeRun("run-exp", "org-1", "thread-1")}
	s := newFakeStore(&fakeQuerier{
		listExpiredAgentRuns: func(_ context.Context, limit int32) ([]sqlc.AgentRun, error) {
			if limit != 5 {
				t.Errorf("ListExpired: want limit 5, got %d", limit)
			}
			return rows, nil
		},
	})
	got, err := s.ListExpired(t.Context(), 5)
	if err != nil || len(got) != 1 || got[0].ID != "run-exp" {
		t.Errorf("ListExpired: %v / %v", got, err)
	}
}

// --- ListStale ---

func TestListStaleDisabledReturnsNil(t *testing.T) {
	s := &Store{}
	got, err := s.ListStale(t.Context(), 5, time.Hour)
	if err != nil || got != nil {
		t.Errorf("disabled ListStale: want nil, got %v / %v", got, err)
	}
}

func TestListStaleReturnsRuns(t *testing.T) {
	rows := []sqlc.AgentRun{makeRun("run-stale", "org-1", "thread-1")}
	s := newFakeStore(&fakeQuerier{
		listStaleAgentRuns: func(_ context.Context, _ sqlc.ListStaleAgentRunsParams) ([]sqlc.AgentRun, error) {
			return rows, nil
		},
	})
	got, err := s.ListStale(t.Context(), 5, time.Hour)
	if err != nil || len(got) != 1 || got[0].ID != "run-stale" {
		t.Errorf("ListStale: %v / %v", got, err)
	}
}

// --- ListActiveForLeaseOwnerPrefix ---

func TestListActiveForLeaseOwnerPrefixDisabledOrEmpty(t *testing.T) {
	s := &Store{}
	got, err := s.ListActiveForLeaseOwnerPrefix(t.Context(), "host-", 10)
	if err != nil || got != nil {
		t.Errorf("disabled ListActive: want nil, got %v / %v", got, err)
	}

	s2 := newFakeStore(&fakeQuerier{})
	got, err = s2.ListActiveForLeaseOwnerPrefix(t.Context(), "", 10)
	if err != nil || got != nil {
		t.Errorf("empty prefix ListActive: want nil, got %v / %v", got, err)
	}
}

func TestListActiveForLeaseOwnerPrefixReturnsRuns(t *testing.T) {
	rows := []sqlc.AgentRun{makeRun("run-active", "org-1", "thread-1")}
	s := newFakeStore(&fakeQuerier{
		listActiveAgentRunsForLeaseOwner: func(_ context.Context, arg sqlc.ListActiveAgentRunsForLeaseOwnerPrefixParams) ([]sqlc.AgentRun, error) {
			if arg.LeaseOwnerPrefix != "host-1" {
				t.Errorf("ListActive prefix: want host-1, got %q", arg.LeaseOwnerPrefix)
			}
			return rows, nil
		},
	})
	got, err := s.ListActiveForLeaseOwnerPrefix(t.Context(), "host-1", 10)
	if err != nil || len(got) != 1 || got[0].ID != "run-active" {
		t.Errorf("ListActive: %v / %v", got, err)
	}
}

// --- Claim ---

func TestClaimDisabled(t *testing.T) {
	s := &Store{}
	if _, err := s.Claim(t.Context(), "run-1", "w", time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled Claim: want ErrNoRows, got %v", err)
	}
}

func TestClaimReturnsRun(t *testing.T) {
	want := makeRun("run-1", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		claimAgentRunLease: func(_ context.Context, arg sqlc.ClaimAgentRunLeaseParams) (sqlc.AgentRun, error) {
			if arg.ID != "run-1" || arg.LeaseOwner != "worker" {
				t.Errorf("Claim args: %+v", arg)
			}
			return want, nil
		},
	})
	got, err := s.Claim(t.Context(), "run-1", "worker", time.Minute)
	if err != nil || got.ID != "run-1" {
		t.Errorf("Claim: %v / %v", got.ID, err)
	}
}

// --- ClaimFromOwner ---

func TestClaimFromOwnerDisabled(t *testing.T) {
	s := &Store{}
	if _, err := s.ClaimFromOwner(t.Context(), "r", "new", "old", time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled ClaimFromOwner: want ErrNoRows, got %v", err)
	}
}

func TestClaimFromOwnerReturnsRun(t *testing.T) {
	want := makeRun("run-1", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		claimAgentRunLeaseFromOwner: func(_ context.Context, arg sqlc.ClaimAgentRunLeaseFromOwnerParams) (sqlc.AgentRun, error) {
			if arg.PreviousLeaseOwner != "old-worker" {
				t.Errorf("ClaimFromOwner prev owner: want old-worker, got %q", arg.PreviousLeaseOwner)
			}
			return want, nil
		},
	})
	got, err := s.ClaimFromOwner(t.Context(), "run-1", "new-worker", "old-worker", time.Minute)
	if err != nil || got.ID != "run-1" {
		t.Errorf("ClaimFromOwner: %v / %v", got.ID, err)
	}
}

// --- ClaimStale ---

func TestClaimStaleDisabled(t *testing.T) {
	s := &Store{}
	if _, err := s.ClaimStale(t.Context(), "r", "w", time.Minute, time.Hour); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled ClaimStale: want ErrNoRows, got %v", err)
	}
}

func TestClaimStaleReturnsRun(t *testing.T) {
	want := makeRun("run-stale", "org-1", "thread-1")
	s := newFakeStore(&fakeQuerier{
		claimStaleAgentRunLease: func(_ context.Context, _ sqlc.ClaimStaleAgentRunLeaseParams) (sqlc.AgentRun, error) {
			return want, nil
		},
	})
	got, err := s.ClaimStale(t.Context(), "run-stale", "worker", time.Minute, time.Hour)
	if err != nil || got.ID != "run-stale" {
		t.Errorf("ClaimStale: %v / %v", got.ID, err)
	}
}

// --- Cancel ---

func TestCancelDisabledOrEmptyID(t *testing.T) {
	s := &Store{}
	if _, err := s.Cancel(t.Context(), "run-1", "", "", time.Minute, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("disabled Cancel: want ErrNoRows, got %v", err)
	}

	// empty id is rejected before tx is invoked
	s2 := newFakeStore(&fakeQuerier{})
	if _, err := s2.Cancel(t.Context(), "", "", "", time.Minute, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("empty id Cancel: want ErrNoRows, got %v", err)
	}
}

func TestCancelHappyPath(t *testing.T) {
	row := makeRun("run-1", "org-1", "thread-1")
	row.State = StateRunning
	txq := &fakeTxQuerier{
		claimAgentRunForCancel: func(_ context.Context, _ sqlc.ClaimAgentRunForCancelParams) (sqlc.AgentRun, error) {
			return row, nil
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)

	got, err := s.Cancel(t.Context(), "run-1", "stopped", "worker", time.Minute, []PendingEvent{
		{Event: "block_start", Data: []byte(`{}`)},
	})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.State != StateCancelled || got.LastError != "stopped" {
		t.Errorf("Cancel result: state=%q lastErr=%q", got.State, got.LastError)
	}
}

func TestCancelPropagatesClaimError(t *testing.T) {
	sentinel := errors.New("claim failed")
	txq := &fakeTxQuerier{
		claimAgentRunForCancel: func(_ context.Context, _ sqlc.ClaimAgentRunForCancelParams) (sqlc.AgentRun, error) {
			return sqlc.AgentRun{}, sentinel
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)
	if _, err := s.Cancel(t.Context(), "run-1", "", "", time.Minute, nil); !errors.Is(err, sentinel) {
		t.Errorf("Cancel claim error: want sentinel, got %v", err)
	}
}

func TestCancelPropagatesAppendEventError(t *testing.T) {
	sentinel := errors.New("append failed")
	row := makeRun("run-1", "org-1", "thread-1")
	txq := &fakeTxQuerier{
		claimAgentRunForCancel: func(_ context.Context, _ sqlc.ClaimAgentRunForCancelParams) (sqlc.AgentRun, error) {
			return row, nil
		},
		appendAgentRunEvent: func(_ context.Context, _ sqlc.AppendAgentRunEventParams) (int64, error) {
			return 0, sentinel
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)
	if _, err := s.Cancel(t.Context(), "run-1", "", "", time.Minute, []PendingEvent{{Event: "ev"}}); !errors.Is(err, sentinel) {
		t.Errorf("Cancel append error: want sentinel, got %v", err)
	}
}

func TestCancelPropagatesUpdateStateError(t *testing.T) {
	sentinel := errors.New("state update failed")
	row := makeRun("run-1", "org-1", "thread-1")
	txq := &fakeTxQuerier{
		claimAgentRunForCancel: func(_ context.Context, _ sqlc.ClaimAgentRunForCancelParams) (sqlc.AgentRun, error) {
			return row, nil
		},
		updateAgentRunState: func(_ context.Context, _ sqlc.UpdateAgentRunStateParams) error {
			return sentinel
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)
	if _, err := s.Cancel(t.Context(), "run-1", "", "", time.Minute, nil); !errors.Is(err, sentinel) {
		t.Errorf("Cancel state error: want sentinel, got %v", err)
	}
}

// --- AppendEvent ---

func TestAppendEventDisabledOrEmpty(t *testing.T) {
	s := &Store{}
	seq, err := s.AppendEvent(t.Context(), "run-1", "ev", nil, "w")
	if err != nil || seq != 0 {
		t.Errorf("disabled AppendEvent: want 0/nil, got %d/%v", seq, err)
	}

	s2 := newFakeStore(&fakeQuerier{})
	seq, err = s2.AppendEvent(t.Context(), "", "ev", nil, "w")
	if err != nil || seq != 0 {
		t.Errorf("empty runID AppendEvent: want 0/nil, got %d/%v", seq, err)
	}
}

func TestAppendEventReturnsSeq(t *testing.T) {
	s := newFakeStore(&fakeQuerier{
		appendAgentRunEvent: func(_ context.Context, arg sqlc.AppendAgentRunEventParams) (int64, error) {
			if arg.Event != "block_start" {
				t.Errorf("AppendEvent: want block_start, got %q", arg.Event)
			}
			return 7, nil
		},
	})
	seq, err := s.AppendEvent(t.Context(), "run-1", "block_start", []byte(`{}`), "worker")
	if err != nil || seq != 7 {
		t.Errorf("AppendEvent: want seq=7, got %d/%v", seq, err)
	}
}

func TestAppendEventPropagatesError(t *testing.T) {
	sentinel := errors.New("append failed")
	s := newFakeStore(&fakeQuerier{
		appendAgentRunEvent: func(_ context.Context, _ sqlc.AppendAgentRunEventParams) (int64, error) {
			return 0, sentinel
		},
	})
	if _, err := s.AppendEvent(t.Context(), "run-1", "ev", nil, "w"); !errors.Is(err, sentinel) {
		t.Errorf("AppendEvent error: want sentinel, got %v", err)
	}
}

// --- AppendEventsAndAdvanceCursor ---

func TestAppendEventsAndAdvanceCursorDisabledOrEmpty(t *testing.T) {
	s := &Store{}
	seqs, err := s.AppendEventsAndAdvanceCursor(t.Context(), "run-1", nil, 0, "w")
	if err != nil || seqs != nil {
		t.Errorf("disabled AppendEventsAndAdvanceCursor: want nil, got %v/%v", seqs, err)
	}
}

func TestAppendEventsAndAdvanceCursorHappyPath(t *testing.T) {
	var seq int64
	txq := &fakeTxQuerier{
		appendAgentRunEvent: func(_ context.Context, _ sqlc.AppendAgentRunEventParams) (int64, error) {
			seq++
			return seq, nil
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)
	events := []PendingEvent{{Event: "ev1"}, {Event: "ev2"}}
	seqs, err := s.AppendEventsAndAdvanceCursor(t.Context(), "run-1", events, 10, "worker")
	if err != nil || len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Errorf("AppendEventsAndAdvanceCursor: want [1,2], got %v/%v", seqs, err)
	}
}

func TestAppendEventsAndAdvanceCursorZeroCursorSkipsUpdate(t *testing.T) {
	cursorUpdated := false
	txq := &fakeTxQuerier{
		appendAgentRunEvent: func(_ context.Context, _ sqlc.AppendAgentRunEventParams) (int64, error) { return 1, nil },
		updateAgentRunLogCursor: func(_ context.Context, _ sqlc.UpdateAgentRunLogCursorParams) error {
			cursorUpdated = true
			return nil
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)
	_, err := s.AppendEventsAndAdvanceCursor(t.Context(), "run-1", []PendingEvent{{Event: "ev"}}, 0, "w")
	if err != nil || cursorUpdated {
		t.Errorf("zero cursor: cursor should not be updated, err=%v", err)
	}
}

func TestAppendEventsAndAdvanceCursorPropagatesAppendError(t *testing.T) {
	sentinel := errors.New("append err")
	txq := &fakeTxQuerier{
		appendAgentRunEvent: func(_ context.Context, _ sqlc.AppendAgentRunEventParams) (int64, error) {
			return 0, sentinel
		},
	}
	s := newFakeStoreWithTx(&fakeQuerier{}, txq)
	if _, err := s.AppendEventsAndAdvanceCursor(t.Context(), "run-1", []PendingEvent{{Event: "ev"}}, 0, "w"); !errors.Is(err, sentinel) {
		t.Errorf("AppendEventsAndAdvanceCursor error: want sentinel, got %v", err)
	}
}

// --- EventsAfterLimit ---

func TestEventsAfterLimitDisabledOrEmpty(t *testing.T) {
	s := &Store{}
	got, err := s.EventsAfterLimit(t.Context(), "run-1", 0, 10)
	if err != nil || got != nil {
		t.Errorf("disabled EventsAfterLimit: want nil, got %v/%v", got, err)
	}

	s2 := newFakeStore(&fakeQuerier{})
	got, err = s2.EventsAfterLimit(t.Context(), "", 0, 10)
	if err != nil || got != nil {
		t.Errorf("empty runID EventsAfterLimit: want nil, got %v/%v", got, err)
	}
}

func TestEventsAfterLimitDefaultsLimit(t *testing.T) {
	var gotLimit int32
	s := newFakeStore(&fakeQuerier{
		listAgentRunEventsFromSeq: func(_ context.Context, arg sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error) {
			gotLimit = arg.Limit
			return nil, nil
		},
	})
	_, _ = s.EventsAfterLimit(t.Context(), "run-1", 0, 0)
	if gotLimit != agentRunEventPageLimit {
		t.Errorf("EventsAfterLimit default: want %d, got %d", agentRunEventPageLimit, gotLimit)
	}
}

func TestEventsAfterLimitReturnsEvents(t *testing.T) {
	rows := []sqlc.AgentRunEvent{
		{RunID: "run-1", Seq: 1, Event: "block_start", Data: []byte(`{}`), CreatedAt: pgtype.Timestamptz{}},
	}
	s := newFakeStore(&fakeQuerier{
		listAgentRunEventsFromSeq: func(_ context.Context, _ sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error) {
			return rows, nil
		},
	})
	got, err := s.EventsAfterLimit(t.Context(), "run-1", 0, 10)
	if err != nil || len(got) != 1 || got[0].Seq != 1 || got[0].Event != "block_start" {
		t.Errorf("EventsAfterLimit: %v/%v", got, err)
	}
}

// --- EventsAfter (pagination) ---

func TestEventsAfterSinglePage(t *testing.T) {
	rows := make([]sqlc.AgentRunEvent, 3)
	for i := range rows {
		rows[i] = sqlc.AgentRunEvent{RunID: "run-1", Seq: int64(i + 1), Event: "ev"}
	}
	s := newFakeStore(&fakeQuerier{
		listAgentRunEventsFromSeq: func(_ context.Context, _ sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error) {
			return rows, nil
		},
	})
	got, err := s.EventsAfter(t.Context(), "run-1", 0)
	if err != nil || len(got) != 3 {
		t.Errorf("EventsAfter single page: want 3, got %d/%v", len(got), err)
	}
}

func TestEventsAfterPagination(t *testing.T) {
	// Simulate exactly one full page followed by an empty page.
	page := make([]sqlc.AgentRunEvent, agentRunEventPageLimit)
	for i := range page {
		page[i] = sqlc.AgentRunEvent{RunID: "run-1", Seq: int64(i + 1), Event: "ev"}
	}
	calls := 0
	s := newFakeStore(&fakeQuerier{
		listAgentRunEventsFromSeq: func(_ context.Context, arg sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error) {
			calls++
			if calls == 1 {
				return page, nil
			}
			return nil, nil
		},
	})
	got, err := s.EventsAfter(t.Context(), "run-1", 0)
	if err != nil || len(got) != int(agentRunEventPageLimit) || calls != 2 {
		t.Errorf("EventsAfter pagination: len=%d calls=%d err=%v", len(got), calls, err)
	}
}

func TestEventsAfterPropagatesError(t *testing.T) {
	sentinel := errors.New("list error")
	s := newFakeStore(&fakeQuerier{
		listAgentRunEventsFromSeq: func(_ context.Context, _ sqlc.ListAgentRunEventsFromSeqParams) ([]sqlc.AgentRunEvent, error) {
			return nil, sentinel
		},
	})
	if _, err := s.EventsAfter(t.Context(), "run-1", 0); !errors.Is(err, sentinel) {
		t.Errorf("EventsAfter error: want sentinel, got %v", err)
	}
}

// --- Helper functions ---

func TestTriggerSourceOrDefault(t *testing.T) {
	if got := triggerSourceOrDefault(""); got != TriggerUser {
		t.Errorf("triggerSourceOrDefault empty: want %q, got %q", TriggerUser, got)
	}
	if got := triggerSourceOrDefault(TriggerJob); got != TriggerJob {
		t.Errorf("triggerSourceOrDefault job: want %q, got %q", TriggerJob, got)
	}
}

func TestOptionalString(t *testing.T) {
	if got := optionalString(""); got != nil {
		t.Errorf("optionalString empty: want nil, got %v", got)
	}
	s := "hello"
	if got := optionalString(s); got == nil || *got != s {
		t.Errorf("optionalString non-empty: want %q, got %v", s, got)
	}
}

func TestInterval(t *testing.T) {
	d := 5 * time.Second
	iv := interval(d)
	if !iv.Valid || iv.Microseconds != d.Microseconds() {
		t.Errorf("interval: want %d us, got %+v", d.Microseconds(), iv)
	}
}

func TestFromRunRowSetsDefaults(t *testing.T) {
	row := sqlc.AgentRun{
		ID:             "r",
		OrgID:          "o",
		ThreadID:       "t",
		JobID:          strPtr("job-1"),
		JobExecutionID: strPtr("exec-1"),
	}
	run := fromRunRow(row)
	if run.JobID != "job-1" || run.JobExecutionID != "exec-1" {
		t.Errorf("fromRunRow optional strings: %+v", run)
	}
	if run.TriggerSource != TriggerUser {
		t.Errorf("fromRunRow trigger default: got %q", run.TriggerSource)
	}
}

func strPtr(s string) *string { return &s }
