package bot

import (
	"context"
	"strconv"
	"strings"

	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

// checkpointVolumeSubdir is the directory on the shared Daytona cache volume
// under which per-run WIP snapshots are stored (as `<key>.tar.gz`). The volume
// already holds the repo checkout and dependency caches; WIP snapshots live
// alongside them and are re-mounted into a replacement sandbox on recovery.
const checkpointVolumeSubdir = "hetchy-wip"

// checkpointDirForVolume returns the absolute checkpoint directory inside a
// sandbox that has the shared cache volume mounted. The mount path is a fixed
// constant, so this is independent of whether the per-request cache env was
// derived — which matters on the recovery/continue path where the repo's
// CacheMounted flag is not carried through.
func checkpointDirForVolume() string {
	return daytonaCacheMountPath + "/" + checkpointVolumeSubdir
}

// checkpointKeyForRun returns the snapshot key for a run. It is fully derivable
// from the run ID, so recovery on any worker can locate the snapshot without
// persisting extra state. Returns "" for an empty run ID so callers can treat
// "no run" as "no checkpointing".
func checkpointKeyForRun(runID string) string {
	return runID
}

type resumeCheckpointContextKey struct{}

// contextWithResumeCheckpoint marks a run context as a reconstruct-and-resume
// launch, carrying the checkpoint key whose volume snapshot the agent script
// should restore into the fresh sandbox before the agent runs.
func contextWithResumeCheckpoint(ctx context.Context, ref string) context.Context {
	if ref == "" {
		return ctx
	}
	return context.WithValue(ctx, resumeCheckpointContextKey{}, ref)
}

// resumeCheckpointFromContext returns the checkpoint key a reconstructed run
// should restore, or "" when this is a normal (non-resume) launch.
func resumeCheckpointFromContext(ctx context.Context) string {
	ref, _ := ctx.Value(resumeCheckpointContextKey{}).(string)
	return ref
}

// addCheckpointRunEnv injects the WIP-checkpoint and (when resuming) restore
// env vars consumed by agent.sh / followup.sh. It is a no-op unless a durable
// run is in scope, so runs without run tracking behave exactly as before. The
// script itself gates on whether the shared cache volume is actually mounted,
// so these vars are safe to set unconditionally.
//
//   - HETCHY_CHECKPOINT_INTERVAL_SECONDS / _DIR / _KEY start the background
//     snapshot loop when checkpointing is enabled and the volume is mounted.
//   - HETCHY_RESTORE_CHECKPOINT_KEY makes the script restore prior in-progress
//     work first; it is set only on a reconstruct-and-resume launch.
func (b *Bot) addCheckpointRunEnv(ctx context.Context, env map[string]string) {
	if env == nil {
		return
	}
	run, ok := agentRunFromContext(ctx)
	if !ok {
		return
	}
	key := checkpointKeyForRun(run.ID)
	if key == "" {
		return
	}
	dir := checkpointDirForVolume()
	if b.cfg.CheckpointIntervalSeconds > 0 {
		env["HETCHY_CHECKPOINT_INTERVAL_SECONDS"] = strconv.Itoa(b.cfg.CheckpointIntervalSeconds)
		env["HETCHY_CHECKPOINT_DIR"] = dir
		env["HETCHY_CHECKPOINT_KEY"] = key
	}
	if resumeKey := resumeCheckpointFromContext(ctx); resumeKey != "" {
		env["HETCHY_CHECKPOINT_DIR"] = dir
		env["HETCHY_RESTORE_CHECKPOINT_KEY"] = resumeKey
	}
}

// resumeEligibleRun reports whether a run whose sandbox was permanently lost
// should be reconstructed rather than failed. Any run that reached a real
// agent turn qualifies: with a checkpoint the agent resumes mid-turn, and
// without one it re-runs from scratch — both strictly better than an
// unrecoverable failure. A run with no ID (durable-run tracking disabled)
// cannot be reconstructed.
func (b *Bot) resumeEligibleRun(run runstore.Run) bool {
	return strings.TrimSpace(run.ID) != ""
}

// shouldReconstructLostSandbox decides whether a sandbox-gone recovery should
// rebuild-and-resume instead of failing/deferring. Reconstruct only when
// mid-turn resume is enabled, the sandbox is genuinely gone (a permanent 404,
// not a transient outage — transient errors must keep deferring for retry),
// and the run is resume-eligible.
func (b *Bot) shouldReconstructLostSandbox(run runstore.Run, err error) bool {
	return b.cfg.MidTurnResumeEnabled && isPermanentRecoverySandboxError(err) && b.resumeEligibleRun(run)
}

// handleRecoverySandboxGone is the recovery decision point when an interrupted
// run's sandbox cannot be fetched or restarted. When mid-turn resume is enabled
// and the sandbox is genuinely gone (a permanent 404, not a transient outage),
// it reconstructs a fresh sandbox and resumes; otherwise it preserves the
// existing behavior (fail on permanent errors, defer + retry on transient ones).
func (b *Bot) handleRecoverySandboxGone(ctx context.Context, run runstore.Run, live *liveRun, title, body string, err error) {
	if b.shouldReconstructLostSandbox(run, err) {
		b.log.Info("agent run recovery reconstructing lost sandbox",
			"run_id", run.ID,
			"org", run.OrgID,
			"thread", run.ThreadID,
			"sandbox", run.SandboxID,
			"checkpoint_key", checkpointKeyForRun(run.ID),
		)
		b.reconstructRunAndResume(ctx, run, live)
		return
	}
	b.handleRecoverySetupError(ctx, run, live, title, body, err)
}

// reconstructRunAndResume creates a fresh sandbox for a run whose original
// sandbox was lost, then re-drives the agent through the normal continue path
// with a resume marker so agent.sh / followup.sh restore the run's WIP
// checkpoint before the agent runs. Transient failures (event load, sandbox
// create) defer the run back to StateRecovering for the next sweep rather than
// failing it outright.
func (b *Bot) reconstructRunAndResume(ctx context.Context, run runstore.Run, live *liveRun) {
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.deferRecoveryForRetry(run, "reconstruct load events", err)
		return
	}
	em := newAgentRunEmitterAfterEvents(b.runs, run.ID, b.workerID, live, events)
	inputs, err := b.recoveredRunInputs(ctx, run, em)
	if err != nil {
		b.finishRecoveredFailure(ctx, run, live, "Agent failed",
			"The interrupted run's sandbox was lost and its repository context could not be reloaded to rebuild it: `"+err.Error()+"`", err)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, "", b.workerID)
	em.Notify("Rebuilding sandbox",
		"The original sandbox was lost. Rebuilding a fresh sandbox and restoring your in-progress work to continue where the run left off…")
	if eErr := em.Err(); eErr != nil {
		b.deferRecoveryForRetry(run, "reconstruct emit notify", eErr)
		return
	}

	// Extend the lease across the sandbox-creation window: creation can take
	// minutes and no run events (which normally refresh the lease) are emitted
	// until the agent restarts, so without this a concurrent stale sweep could
	// re-claim and reconstruct a second time.
	b.runs.TouchLease(context.Background(), run.ID, b.workerID, agentRunLeaseDuration)

	// Standard flavor keeps reconstruction independent of billing admission —
	// recovery is a system action, and the resumed agent turn does the real
	// work regardless of sandbox flavor.
	flavor := billing.MustFlavor(billing.FlavorStandard)
	sb, _, err := b.createFollowUpReplacementSandbox(ctx, inputs.oc, inputs.repo, flavor)
	if err != nil {
		b.deferRecoveryForRetry(run, "reconstruct create sandbox", err)
		return
	}
	b.runs.UpdateSandbox(context.Background(), run.ID, sb.ID, b.workerID)
	run.SandboxID = sb.ID
	// Drop the lost sandbox's command/session pointers so the continue path
	// launches a fresh agent turn instead of trying to reattach to a command
	// that no longer exists.
	run.SessionID = ""
	run.CommandID = ""
	run.CommandStep = ""
	run.CommandStartSeq = 0
	run.LogCursor = 0

	resumeCtx := contextWithResumeCheckpoint(ctx, checkpointKeyForRun(run.ID))
	b.continueRecoveredRun(resumeCtx, sb, run, live, false)
}
