package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

const (
	StatePreparing  = "preparing"
	StateRunning    = "running"
	StateRecovering = "recovering"
	// StateFinalizing means the sandbox command has exited and Hetchy is
	// validating/projecting terminal output before marking the run terminal.
	StateFinalizing = "finalizing"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
)

const (
	TriggerUser = "user"
	TriggerJob  = "job"
)

const (
	OutcomeCompletedWithVerifiedPR = "completed_with_verified_pr"
	OutcomeCompletedNoPR           = "completed_no_pr"
	OutcomeFailedSetup             = "failed_setup"
	OutcomeFailedRuntime           = "failed_runtime"
	OutcomeFailedPRValidation      = "failed_pr_validation"
	OutcomeFailedTimeout           = "failed_timeout"
	OutcomeCancelledBeforePR       = "cancelled_before_pr"
	OutcomeCancelledAfterPR        = "cancelled_after_pr"
	OutcomeDegradedMissingSkills   = "degraded_missing_skills"
)

const agentRunEventPageLimit int32 = 5000

type Run struct {
	ID              string
	OrgID           string
	ThreadID        string
	RunKind         string
	RequestID       string
	TriggerSource   string
	JobID           string
	JobExecutionID  string
	SandboxID       string
	Branch          string
	UserRequest     string
	SessionID       string
	CommandID       string
	CommandStep     string
	CommandStartSeq int64
	State           string
	LogCursor       int64
	LeaseOwner      string
	LeaseExpiresAt  time.Time
	HeartbeatAt     time.Time
	LastError       string
	Outcome         string
	OutcomeDetail   []byte
	QualityScore    *int32
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Event struct {
	RunID     string
	Seq       int64
	Event     string
	Data      []byte
	CreatedAt time.Time
}

type PendingEvent struct {
	Event string
	Data  []byte
}

type Store struct {
	db *db.Store
}

func New(d *db.Store) *Store { return &Store{db: d} }

func (s *Store) Enabled() bool { return s != nil && s.db != nil }

func (s *Store) Create(ctx context.Context, r Run, leaseOwner string, leaseDuration time.Duration) (Run, bool, error) {
	if !s.Enabled() {
		return Run{}, false, nil
	}
	row, err := s.db.Queries.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		ID:             r.ID,
		OrgID:          r.OrgID,
		ThreadID:       r.ThreadID,
		RunKind:        r.RunKind,
		RequestID:      r.RequestID,
		TriggerSource:  triggerSourceOrDefault(r.TriggerSource),
		JobID:          optionalString(r.JobID),
		JobExecutionID: optionalString(r.JobExecutionID),
		UserRequest:    r.UserRequest,
		LeaseOwner:     leaseOwner,
		LeaseDuration:  interval(leaseDuration),
	})
	if err == nil {
		return fromRunRow(sqlc.AgentRun(row)), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, fmt.Errorf("create run: %w", err)
	}
	existing, err := s.db.Queries.GetAgentRunByRequest(ctx, sqlc.GetAgentRunByRequestParams{
		OrgID:     r.OrgID,
		RequestID: r.RequestID,
	})
	if err == nil {
		return fromRunRow(sqlc.AgentRun(existing)), false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, fmt.Errorf("get existing run: %w", err)
	}
	active, err := s.db.Queries.GetActiveAgentRunForThread(ctx, sqlc.GetActiveAgentRunForThreadParams{
		OrgID:    r.OrgID,
		ThreadID: r.ThreadID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, false, fmt.Errorf("request id already used by a terminal run: %w", err)
		}
		return Run{}, false, fmt.Errorf("get active run: %w", err)
	}
	return fromRunRow(sqlc.AgentRun(active)), false, nil
}

func (s *Store) Get(ctx context.Context, id string) (Run, error) {
	if !s.Enabled() {
		return Run{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.GetAgentRun(ctx, id)
	if err != nil {
		return Run{}, err
	}
	return fromRunRow(sqlc.AgentRun(row)), nil
}

func (s *Store) LatestForThread(ctx context.Context, orgID, threadID string) (Run, error) {
	if !s.Enabled() {
		return Run{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.GetLatestAgentRunForThread(ctx, sqlc.GetLatestAgentRunForThreadParams{
		OrgID:    orgID,
		ThreadID: threadID,
	})
	if err != nil {
		return Run{}, err
	}
	return fromRunRow(sqlc.AgentRun(row)), nil
}

func (s *Store) LatestForThreads(ctx context.Context, orgID string, threadIDs []string) (map[string]Run, error) {
	out := make(map[string]Run, len(threadIDs))
	if !s.Enabled() || len(threadIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Queries.ListLatestAgentRunsForThreads(ctx, sqlc.ListLatestAgentRunsForThreadsParams{
		OrgID:     orgID,
		ThreadIds: threadIDs,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		run := fromRunRow(sqlc.AgentRun(row))
		out[run.ThreadID] = run
	}
	return out, nil
}

func (s *Store) ActiveForThread(ctx context.Context, orgID, threadID string) (Run, error) {
	if !s.Enabled() {
		return Run{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.GetActiveAgentRunForThread(ctx, sqlc.GetActiveAgentRunForThreadParams{
		OrgID:    orgID,
		ThreadID: threadID,
	})
	if err != nil {
		return Run{}, err
	}
	return fromRunRow(sqlc.AgentRun(row)), nil
}

func (s *Store) UpdateKind(ctx context.Context, id, kind, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunKind(ctx, sqlc.UpdateAgentRunKindParams{ID: id, RunKind: kind, LeaseOwner: leaseOwner})
}

func (s *Store) UpdateBranch(ctx context.Context, id, branch, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunBranch(ctx, sqlc.UpdateAgentRunBranchParams{ID: id, Branch: branch, LeaseOwner: leaseOwner})
}

func (s *Store) UpdateSandbox(ctx context.Context, id, sandboxID, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunSandbox(ctx, sqlc.UpdateAgentRunSandboxParams{ID: id, SandboxID: sandboxID, LeaseOwner: leaseOwner})
}

func (s *Store) UpdateSession(ctx context.Context, id, sessionID, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunSession(ctx, sqlc.UpdateAgentRunSessionParams{ID: id, SessionID: sessionID, LeaseOwner: leaseOwner})
}

func (s *Store) UpdateCommand(ctx context.Context, id, sessionID, commandID, commandStep, leaseOwner string, leaseDuration time.Duration) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunCommand(ctx, sqlc.UpdateAgentRunCommandParams{
		ID:            id,
		SessionID:     sessionID,
		CommandID:     commandID,
		CommandStep:   commandStep,
		LeaseOwner:    leaseOwner,
		LeaseDuration: interval(leaseDuration),
	})
}

func (s *Store) UpdateState(ctx context.Context, id, state, lastErr, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunState(ctx, sqlc.UpdateAgentRunStateParams{ID: id, State: state, LastError: lastErr, LeaseOwner: leaseOwner})
}

func (s *Store) UpdateOutcome(ctx context.Context, id, outcome string, detail map[string]any, qualityScore *int32, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte(`{"error":"marshal outcome detail"}`)
	}
	_ = s.db.Queries.UpdateAgentRunOutcome(ctx, sqlc.UpdateAgentRunOutcomeParams{
		ID:            id,
		Outcome:       outcome,
		OutcomeDetail: raw,
		QualityScore:  qualityScore,
		LeaseOwner:    leaseOwner,
	})
}

func (s *Store) TouchLease(ctx context.Context, id, leaseOwner string, leaseDuration time.Duration) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.TouchAgentRunLease(ctx, sqlc.TouchAgentRunLeaseParams{
		ID:            id,
		LeaseOwner:    leaseOwner,
		LeaseDuration: interval(leaseDuration),
	})
}

func (s *Store) UpdateLogCursor(ctx context.Context, id string, cursor int64, leaseOwner string) {
	if !s.Enabled() || id == "" {
		return
	}
	_ = s.db.Queries.UpdateAgentRunLogCursor(ctx, sqlc.UpdateAgentRunLogCursorParams{ID: id, LogCursor: cursor, LeaseOwner: leaseOwner})
}

func (s *Store) ListExpired(ctx context.Context, limit int32) ([]Run, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.db.Queries.ListExpiredAgentRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRunRow(sqlc.AgentRun(row)))
	}
	return out, nil
}

func (s *Store) ListStale(ctx context.Context, limit int32, staleAfter time.Duration) ([]Run, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.db.Queries.ListStaleAgentRuns(ctx, sqlc.ListStaleAgentRunsParams{
		StaleAfter: interval(staleAfter),
		LimitCount: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRunRow(sqlc.AgentRun(row)))
	}
	return out, nil
}

func (s *Store) ListActiveForLeaseOwnerPrefix(ctx context.Context, prefix string, limit int32) ([]Run, error) {
	if !s.Enabled() || prefix == "" {
		return nil, nil
	}
	rows, err := s.db.Queries.ListActiveAgentRunsForLeaseOwnerPrefix(ctx, sqlc.ListActiveAgentRunsForLeaseOwnerPrefixParams{
		LeaseOwnerPrefix: prefix,
		LimitCount:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRunRow(sqlc.AgentRun(row)))
	}
	return out, nil
}

func (s *Store) Claim(ctx context.Context, id, leaseOwner string, leaseDuration time.Duration) (Run, error) {
	if !s.Enabled() {
		return Run{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.ClaimAgentRunLease(ctx, sqlc.ClaimAgentRunLeaseParams{
		ID:            id,
		LeaseOwner:    leaseOwner,
		LeaseDuration: interval(leaseDuration),
	})
	if err != nil {
		return Run{}, err
	}
	return fromRunRow(sqlc.AgentRun(row)), nil
}

func (s *Store) ClaimFromOwner(ctx context.Context, id, leaseOwner, previousLeaseOwner string, leaseDuration time.Duration) (Run, error) {
	if !s.Enabled() {
		return Run{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.ClaimAgentRunLeaseFromOwner(ctx, sqlc.ClaimAgentRunLeaseFromOwnerParams{
		ID:                 id,
		LeaseOwner:         leaseOwner,
		PreviousLeaseOwner: previousLeaseOwner,
		LeaseDuration:      interval(leaseDuration),
	})
	if err != nil {
		return Run{}, err
	}
	return fromRunRow(sqlc.AgentRun(row)), nil
}

func (s *Store) ClaimStale(ctx context.Context, id, leaseOwner string, leaseDuration, staleAfter time.Duration) (Run, error) {
	if !s.Enabled() {
		return Run{}, pgx.ErrNoRows
	}
	row, err := s.db.Queries.ClaimStaleAgentRunLease(ctx, sqlc.ClaimStaleAgentRunLeaseParams{
		ID:            id,
		LeaseOwner:    leaseOwner,
		LeaseDuration: interval(leaseDuration),
		StaleAfter:    interval(staleAfter),
	})
	if err != nil {
		return Run{}, err
	}
	return fromRunRow(sqlc.AgentRun(row)), nil
}

func (s *Store) Cancel(ctx context.Context, id, lastErr, leaseOwner string, leaseDuration time.Duration, events []PendingEvent) (Run, error) {
	if !s.Enabled() || id == "" {
		return Run{}, pgx.ErrNoRows
	}
	var run Run
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		row, err := q.ClaimAgentRunForCancel(ctx, sqlc.ClaimAgentRunForCancelParams{
			ID:            id,
			LeaseOwner:    leaseOwner,
			LeaseDuration: interval(leaseDuration),
		})
		if err != nil {
			return err
		}
		run = fromRunRow(sqlc.AgentRun(row))
		for _, ev := range events {
			if _, err := q.AppendAgentRunEvent(ctx, sqlc.AppendAgentRunEventParams{
				RunID:      id,
				Event:      ev.Event,
				Data:       ev.Data,
				LeaseOwner: leaseOwner,
			}); err != nil {
				return fmt.Errorf("append cancel event: %w", err)
			}
		}
		if err := q.UpdateAgentRunState(ctx, sqlc.UpdateAgentRunStateParams{
			ID:         id,
			State:      StateCancelled,
			LastError:  lastErr,
			LeaseOwner: leaseOwner,
		}); err != nil {
			return err
		}
		run.State = StateCancelled
		run.LastError = lastErr
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Store) AppendEvent(ctx context.Context, runID, event string, data []byte, leaseOwner string) (int64, error) {
	if !s.Enabled() || runID == "" {
		return 0, nil
	}

	seq, err := s.db.Queries.AppendAgentRunEvent(ctx, sqlc.AppendAgentRunEventParams{
		RunID:      runID,
		Event:      event,
		Data:       data,
		LeaseOwner: leaseOwner,
	})
	if err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	return seq, nil
}

func (s *Store) AppendEventsAndAdvanceCursor(ctx context.Context, runID string, events []PendingEvent, cursor int64, leaseOwner string) ([]int64, error) {
	if !s.Enabled() || runID == "" {
		return nil, nil
	}
	seqs := make([]int64, 0, len(events))
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		for _, ev := range events {
			seq, err := q.AppendAgentRunEvent(ctx, sqlc.AppendAgentRunEventParams{
				RunID:      runID,
				Event:      ev.Event,
				Data:       ev.Data,
				LeaseOwner: leaseOwner,
			})
			if err != nil {
				return fmt.Errorf("append event: %w", err)
			}
			seqs = append(seqs, seq)
		}
		if cursor > 0 {
			if err := q.UpdateAgentRunLogCursor(ctx, sqlc.UpdateAgentRunLogCursorParams{
				ID:         runID,
				LogCursor:  cursor,
				LeaseOwner: leaseOwner,
			}); err != nil {
				return fmt.Errorf("advance cursor: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return seqs, nil
}

func (s *Store) EventsAfter(ctx context.Context, runID string, seq int64) ([]Event, error) {
	var out []Event
	cursor := seq
	for {
		batch, err := s.EventsAfterLimit(ctx, runID, cursor, agentRunEventPageLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < int(agentRunEventPageLimit) {
			return out, nil
		}
		cursor = batch[len(batch)-1].Seq
	}
}

func (s *Store) EventsAfterLimit(ctx context.Context, runID string, seq int64, limit int32) ([]Event, error) {
	if !s.Enabled() || runID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = agentRunEventPageLimit
	}
	rows, err := s.db.Queries.ListAgentRunEventsFromSeq(ctx, sqlc.ListAgentRunEventsFromSeqParams{
		RunID: runID,
		Seq:   seq,
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, Event{
			RunID:     row.RunID,
			Seq:       row.Seq,
			Event:     row.Event,
			Data:      row.Data,
			CreatedAt: row.CreatedAt.Time,
		})
	}
	return out, nil
}

func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func triggerSourceOrDefault(source string) string {
	if source == "" {
		return TriggerUser
	}
	return source
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fromRunRow(row sqlc.AgentRun) Run {
	run := Run{
		ID:              row.ID,
		OrgID:           row.OrgID,
		ThreadID:        row.ThreadID,
		RunKind:         row.RunKind,
		RequestID:       row.RequestID,
		TriggerSource:   row.TriggerSource,
		SandboxID:       row.SandboxID,
		Branch:          row.Branch,
		UserRequest:     row.UserRequest,
		SessionID:       row.SessionID,
		CommandID:       row.CommandID,
		CommandStep:     row.CommandStep,
		CommandStartSeq: row.CommandStartSeq,
		State:           row.State,
		LogCursor:       row.LogCursor,
		LeaseOwner:      row.LeaseOwner,
		LeaseExpiresAt:  row.LeaseExpiresAt.Time,
		HeartbeatAt:     row.HeartbeatAt.Time,
		LastError:       row.LastError,
		Outcome:         row.Outcome,
		OutcomeDetail:   append([]byte(nil), row.OutcomeDetail...),
		QualityScore:    row.QualityScore,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}
	if row.JobID != nil {
		run.JobID = *row.JobID
	}
	if row.JobExecutionID != nil {
		run.JobExecutionID = *row.JobExecutionID
	}
	if run.TriggerSource == "" {
		run.TriggerSource = TriggerUser
	}
	return run
}
