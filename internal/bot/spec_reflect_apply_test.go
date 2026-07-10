package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/bootstrap"
)

// fixedTime is a deterministic timestamp for the reason-log assertions
// so BootstrapLog entries are reproducible across runs.
var fixedTime = time.Date(2026, 7, 10, 16, 0, 0, 0, time.UTC)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// readerFromMap turns a map of improvement-dir path → content into the
// read closure applySpecImprovementsFromReader expects. A present entry
// with non-empty content reads as (content, true); everything else is
// the clean "file absent" (", false) case.
func readerFromMap(files map[string]string) func(string) (string, bool) {
	return func(path string) (string, bool) {
		v, ok := files[path]
		if !ok || v == "" {
			return "", false
		}
		return v, true
	}
}

func baseSpec() *bootstrap.Spec {
	return &bootstrap.Spec{
		InstallationID:   42,
		RepoID:           7,
		Path:             "",
		SpecVersion:      3,
		ValidationStatus: bootstrap.StatusValidated,
		SetupScript:      "old-setup",
		StartScript:      "old-start",
		StopScript:       "old-stop",
		HealthCheck:      "old-health",
		LessonsMD:        "old-lessons",
		BootstrapLog:     "prior log",
	}
}

func TestApplySpecImprovements_NoneMarkerNotifiesAndSkips(t *testing.T) {
	store := &fakeBootstrapStore{spec: baseSpec()}
	emit := newCaptureEmitter()

	read := readerFromMap(map[string]string{
		specImprovementsDir + "/none.txt": "everything ran smoothly",
	})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if len(store.savedSpecs) != 0 {
		t.Fatalf("expected no spec save on none.txt, got %d", len(store.savedSpecs))
	}
	if !emit.hasCall("notify", "everything ran smoothly") {
		t.Fatalf("expected notify surfacing the none.txt reason, calls=%v", emit.Calls)
	}
	if !emit.hasCall("notify", "no spec improvements were warranted") {
		t.Fatalf("expected the 'no changes' notify title/body, calls=%v", emit.Calls)
	}
}

func TestApplySpecImprovements_NoMarkerNoImprovementsSilentSkip(t *testing.T) {
	store := &fakeBootstrapStore{spec: baseSpec()}
	emit := newCaptureEmitter()

	// No none.txt and no improved scripts: silent skip, no save, no notify.
	read := readerFromMap(map[string]string{})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if len(store.savedSpecs) != 0 {
		t.Fatalf("expected no save on empty reflection, got %d", len(store.savedSpecs))
	}
	if len(emit.Calls) != 0 {
		t.Fatalf("expected no notifications on silent skip, calls=%v", emit.Calls)
	}
}

func TestApplySpecImprovements_PatchesOnlyChangedScripts(t *testing.T) {
	store := &fakeBootstrapStore{spec: baseSpec()}
	emit := newCaptureEmitter()

	read := readerFromMap(map[string]string{
		specImprovementsDir + "/setup.sh":   "new-setup",
		specImprovementsDir + "/health.sh":  "new-health",
		specImprovementsDir + "/lessons.md": "new-lessons",
		specImprovementsDir + "/reason.md":  "installed jq",
	})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if len(store.savedSpecs) != 1 {
		t.Fatalf("expected exactly one spec save, got %d", len(store.savedSpecs))
	}
	saved := store.savedSpecs[0]

	if saved.SpecVersion != 4 {
		t.Fatalf("SpecVersion = %d, want 4 (bumped from 3)", saved.SpecVersion)
	}
	if saved.ValidationStatus != bootstrap.StatusStale {
		t.Fatalf("ValidationStatus = %q, want stale", saved.ValidationStatus)
	}
	// Changed scripts are replaced.
	if saved.SetupScript != "new-setup" {
		t.Fatalf("SetupScript = %q, want new-setup", saved.SetupScript)
	}
	if saved.HealthCheck != "new-health" {
		t.Fatalf("HealthCheck = %q, want new-health", saved.HealthCheck)
	}
	if saved.LessonsMD != "new-lessons" {
		t.Fatalf("LessonsMD = %q, want new-lessons", saved.LessonsMD)
	}
	// Untouched scripts keep their prior value.
	if saved.StartScript != "old-start" {
		t.Fatalf("StartScript = %q, want old-start (unchanged)", saved.StartScript)
	}
	if saved.StopScript != "old-stop" {
		t.Fatalf("StopScript = %q, want old-stop (unchanged)", saved.StopScript)
	}
	// The reason is appended to the bootstrap log with the changed set.
	if !strings.Contains(saved.BootstrapLog, "installed jq") {
		t.Fatalf("BootstrapLog missing reason: %q", saved.BootstrapLog)
	}
	if !strings.Contains(saved.BootstrapLog, "setup.sh, health.sh, lessons.md") {
		t.Fatalf("BootstrapLog missing changed list: %q", saved.BootstrapLog)
	}
	if !strings.Contains(saved.BootstrapLog, "prior log") {
		t.Fatalf("BootstrapLog dropped prior content: %q", saved.BootstrapLog)
	}
	// User is told what changed, at the correct version, with the reason.
	if !emit.hasCall("notify", "spec v4") {
		t.Fatalf("expected notify referencing spec v4, calls=%v", emit.Calls)
	}
	if !emit.hasCall("notify", "**Why:** installed jq") {
		t.Fatalf("expected notify body to include the reason, calls=%v", emit.Calls)
	}
}

func TestApplySpecImprovements_NoReasonOmitsWhyAndLogEntry(t *testing.T) {
	store := &fakeBootstrapStore{spec: baseSpec()}
	emit := newCaptureEmitter()

	read := readerFromMap(map[string]string{
		specImprovementsDir + "/start.sh": "new-start",
		specImprovementsDir + "/stop.sh":  "new-stop",
	})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if len(store.savedSpecs) != 1 {
		t.Fatalf("expected one save, got %d", len(store.savedSpecs))
	}
	saved := store.savedSpecs[0]
	if saved.StartScript != "new-start" {
		t.Fatalf("StartScript = %q, want new-start", saved.StartScript)
	}
	if saved.StopScript != "new-stop" {
		t.Fatalf("StopScript = %q, want new-stop", saved.StopScript)
	}
	// No reason.md → BootstrapLog is untouched (no improvement entry).
	if saved.BootstrapLog != "prior log" {
		t.Fatalf("BootstrapLog changed without a reason: %q", saved.BootstrapLog)
	}
	if emit.hasCall("notify", "**Why:**") {
		t.Fatalf("did not expect a Why line without a reason, calls=%v", emit.Calls)
	}
	if !emit.hasCall("notify", "start.sh and stop.sh") {
		t.Fatalf("expected notify naming start.sh and stop.sh, calls=%v", emit.Calls)
	}
}

func TestApplySpecImprovements_SpecDeletedDuringRunSkips(t *testing.T) {
	// GetSpec returns ErrNotFound: the user hit Delete bootstrap mid-run,
	// so the improvements must NOT resurrect the row.
	store := &fakeBootstrapStore{spec: nil} // nil spec → GetSpec yields ErrNotFound
	emit := newCaptureEmitter()

	read := readerFromMap(map[string]string{
		specImprovementsDir + "/setup.sh": "new-setup",
	})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if len(store.savedSpecs) != 0 {
		t.Fatalf("expected no save after mid-run delete, got %d", len(store.savedSpecs))
	}
	if len(emit.Calls) != 0 {
		t.Fatalf("expected no notify on skip, calls=%v", emit.Calls)
	}
}

func TestApplySpecImprovements_RefetchErrorSkips(t *testing.T) {
	// A non-ErrNotFound GetSpec failure is logged and bails without saving.
	store := &fakeBootstrapStore{specErr: errors.New("db down")}
	emit := newCaptureEmitter()

	read := readerFromMap(map[string]string{
		specImprovementsDir + "/setup.sh": "new-setup",
	})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if len(store.savedSpecs) != 0 {
		t.Fatalf("expected no save on re-fetch error, got %d", len(store.savedSpecs))
	}
}

func TestApplySpecImprovements_SaveFailureNotifiesUser(t *testing.T) {
	store := &fakeBootstrapStore{spec: baseSpec(), saveErr: errors.New("boom")}
	emit := newCaptureEmitter()

	read := readerFromMap(map[string]string{
		specImprovementsDir + "/setup.sh": "new-setup",
	})

	applySpecImprovementsFromReader(context.Background(), read, store, baseSpec(),
		"acme/widgets", emit, quietLogger(), fixedTime)

	if !emit.hasCall("notify", "couldn't save improvements") {
		t.Fatalf("expected a save-failure notify, calls=%v", emit.Calls)
	}
	if !emit.hasCall("notify", "boom") {
		t.Fatalf("expected the save error surfaced to the user, calls=%v", emit.Calls)
	}
	if !emit.hasCall("notify", "PR is unaffected") {
		t.Fatalf("expected reassurance that the PR is unaffected, calls=%v", emit.Calls)
	}
}
