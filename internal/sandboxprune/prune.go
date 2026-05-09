// Package sandboxprune detects and archives Daytona sandboxes that are no
// longer being driven by hetchy. Three classes are handled:
//
//  1. True orphans — no conversation row references the sandbox at all.
//     Happens when a fresh run crashes before the terminal Upsert writes
//     sandbox_id to the DB.
//
//  2. Error-state sandboxes — conversation exists with agent_state='error'.
//     Hetchy intentionally leaves these running for debugging, but they
//     should be cleaned up eventually.
//
//  3. Crash-during-follow-up — conversation has agent_state='running' but
//     the heartbeat has gone stale, meaning the process died without
//     completing the run and nobody is driving the sandbox anymore.
package sandboxprune

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	apiclient "github.com/daytonaio/daytona/libs/api-client-go"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db"
)

// Result summarises a single prune run.
// The fields satisfy: Active + Skipped + Pruned + Errors == Sandboxes (live mode)
// and: Active + Skipped + WouldPrune == Sandboxes (dry-run mode).
type Result struct {
	Sandboxes  int // total sandboxes listed from Daytona
	Active     int // sandboxes with a live, heartbeating run — skipped
	Skipped    int // sandboxes already at rest (archived/destroyed) — skipped
	WouldPrune int // prunable sandboxes identified in dry-run mode (no changes made)
	Pruned     int // sandboxes archived successfully in live mode
	Errors     int // archive attempts that failed
}

// Run lists every sandbox in Daytona for the given env label, cross-references
// with the set of actively-running sandbox IDs in the hetchy database, and
// archives every sandbox that has no live run driving it.
//
// env is matched against the "hetchy-env" label set on each sandbox at creation
// time; only sandboxes with that label are considered, so a dev/staging prune
// cannot accidentally archive prod sandboxes even when Daytona credentials are
// shared across deployments. Note: sandboxes created before this label was
// introduced will not be returned by the Daytona list and must be cleaned up
// manually on first deploy.
//
// staleThreshold controls how old agent_heartbeat_at must be before a 'running'
// conversation is considered crashed. 10 minutes is a safe default; the heartbeat
// is updated every 2 seconds.
//
// When dryRun is true the function logs what it would do but makes no changes to
// Daytona (the Daytona API is still queried to build the candidate list).
//
// Query ordering: Daytona is listed first, then the DB active-set is queried.
// This ordering closes the TOCTOU race where a bot path running
// createSandboxWithRetry → BeginAgentRun could fall entirely between two
// reversed queries: if the sandbox exists in Daytona, the DB write is guaranteed
// observable by the subsequent (later) DB query; if the sandbox was created after
// the Daytona list, it never enters the candidate set at all.
func Run(ctx context.Context, log *slog.Logger, dc *daytona.Client, store *db.Store, env string, staleThreshold time.Duration, dryRun bool) (Result, error) {
	sandboxes, err := listAll(ctx, dc, env)
	if err != nil {
		return Result{}, fmt.Errorf("list daytona sandboxes: %w", err)
	}
	log.Info("daytona sandboxes listed", "count", len(sandboxes), "env", env)

	active, err := loadActiveIDs(ctx, store, staleThreshold)
	if err != nil {
		return Result{}, fmt.Errorf("load active sandbox IDs: %w", err)
	}
	log.Info("active sandbox IDs loaded from database", "count", len(active))

	res := Result{Sandboxes: len(sandboxes)}

	for _, sb := range sandboxes {
		// Sandboxes already at rest consume no compute; skip them.
		if sb.State == apiclient.SANDBOXSTATE_ARCHIVED || sb.State == apiclient.SANDBOXSTATE_DESTROYED {
			res.Skipped++
			log.Debug("sandbox already at rest, skipping", "id", sb.ID, "state", sb.State)
			continue
		}

		if active[sb.ID] {
			res.Active++
			log.Debug("sandbox active, skipping", "id", sb.ID, "state", sb.State)
			continue
		}

		log.Info("sandbox is prunable", "id", sb.ID, "name", sb.Name, "state", sb.State, "dry_run", dryRun)

		if dryRun {
			res.WouldPrune++
			continue
		}

		if err := archiveSandbox(ctx, log, sb); err != nil {
			log.Error("sandbox archive failed", "id", sb.ID, "error", err)
			res.Errors++
		} else {
			res.Pruned++
		}
	}

	return res, nil
}

// loadActiveIDs returns the set of sandbox IDs that belong to a run
// actively in progress: agent_state = 'running' and heartbeat fresher
// than staleThreshold. Every other sandbox — true orphans, error-state,
// completed, and crashed (stale heartbeat) — is absent from this set
// and therefore eligible for archival.
func loadActiveIDs(ctx context.Context, store *db.Store, staleThreshold time.Duration) (map[string]bool, error) {
	threshold := pgtype.Interval{
		Microseconds: staleThreshold.Microseconds(),
		Valid:        true,
	}
	ids, err := store.Queries.ListActiveSandboxIDs(ctx, threshold)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// listAll pages through the Daytona sandbox list for the given env label until
// all matching sandboxes have been fetched, returning them as a flat slice.
func listAll(ctx context.Context, dc *daytona.Client, env string) ([]*daytona.Sandbox, error) {
	const pageSize = 100
	labels := map[string]string{"hetchy-env": env}
	var all []*daytona.Sandbox
	page := 1
	limit := pageSize
	for {
		result, err := dc.List(ctx, labels, &page, &limit)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		all = append(all, result.Items...)
		if page >= result.TotalPages {
			break
		}
		page++
	}
	return all, nil
}

// archiveSandbox stops (if started) then archives the sandbox. Archiving (not
// hard deleting) is the conservative choice: it releases compute resources while
// keeping the filesystem snapshot recoverable for the Daytona retention window.
// Stop is skipped for sandboxes that are already stopped — calling Stop on a
// stopped sandbox returns an error that would pollute the logs needlessly.
// Stop errors on a started sandbox are non-fatal — we proceed to Archive
// regardless, since an already-stopped sandbox archives cleanly.
func archiveSandbox(ctx context.Context, log *slog.Logger, sb *daytona.Sandbox) error {
	if sb.State == apiclient.SANDBOXSTATE_STARTED {
		if err := sb.Stop(ctx); err != nil {
			log.Warn("sandbox stop failed, proceeding to archive", "id", sb.ID, "error", err)
		}
	}
	if err := sb.Archive(ctx); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	log.Info("sandbox archived", "id", sb.ID)
	return nil
}
