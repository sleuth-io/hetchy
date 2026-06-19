package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/bootstrap"
)

// specImprovementsDir is where the agent drops its post-validation
// reflection. The validation prompt instructs claude to write any
// improved scripts here; the bot reads them after the agent finishes
// and patches the saved spec accordingly. Constant rather than a
// parameter so the host- and sandbox-side paths can't drift.
const specImprovementsDir = "/tmp/hetchy-spec/improved"

// applySpecImprovements reads /tmp/hetchy-spec/improved/{setup,start,
// stop,health}.sh and lessons.md from the sandbox after a successful
// agent run, and if any are present saves a new spec version with
// those artifacts patched in. The agent's reflection prompt also lets it drop a `none.txt`
// marker — we read that too so the persisted bootstrap_log can record
// "agent considered improvements and chose none".
//
// Best-effort: any read or save failure is logged + emitted as a hint
// but never fails the parent task. The agent has already produced the
// PR; spec maintenance is housekeeping.
func (b *Bot) applySpecImprovements(ctx context.Context, sb *daytona.Sandbox, sessionID string, spec *bootstrap.Spec, repo repoCtx, emit blocks.Emitter) {
	if spec == nil || b.bootstrap == nil {
		return
	}

	// One-shot read session — uses a distinct session ID so it does not
	// interfere with the agent's session, which the caller cleans up on success.
	readSessionID := sessionID + "-improvements"
	if err := sb.Process.CreateSession(ctx, readSessionID); err != nil {
		b.log.Warn("spec-improvements: create session", "error", err, "repo", repo.Slug)
		return
	}
	defer func() { b.deleteSandboxSession(sb, readSessionID) }()

	read := func(path string) (string, bool) {
		// `[ -f path ] && head -c 65536 path || true` — exit-0 on
		// either branch so a missing file is a clean empty read
		// instead of an error. The 64 KB cap is intentional: the
		// agent is supposed to drop a small improved script + a
		// one-paragraph reason, anything materially larger is a
		// signal the agent got confused (e.g. dumped the whole
		// repo) and we don't want a multi-MB blob landing in spec
		// script columns. An exact-cap read is treated as
		// suspicious by the reflection log so we know the value
		// was truncated.
		cmd := fmt.Sprintf("if [ -f %s ]; then head -c 65536 %s; fi", shellQuote(path), shellQuote(path))
		out, err := b.shLines(ctx, sb.ID, sb.Process, readSessionID, "spec-read", cmd, 30*time.Second, 0, false, func(string) {})
		if err != nil {
			b.log.Warn("spec-improvements: read", "error", err, "path", path, "repo", repo.Slug)
			return "", false
		}
		s := strings.TrimSpace(out)
		if len(s) >= 65536 {
			b.log.Warn("spec-improvements: read hit 64 KB cap; suspect truncation",
				"path", path, "repo", repo.Slug, "bytes", len(s))
		}
		return s, s != ""
	}

	if note, ok := read(specImprovementsDir + "/none.txt"); ok {
		// Agent considered improvements and decided none were warranted.
		// Surface the reason in the chat so the user sees the agent did
		// reflect — silently doing nothing would look like the prompt
		// section was ignored.
		emit.Notify("Bootstrap spec — no changes",
			"Agent reviewed the validation run and reported no spec improvements were warranted: "+note)
		return
	}

	improvedSetup, hasSetup := read(specImprovementsDir + "/setup.sh")
	improvedStart, hasStart := read(specImprovementsDir + "/start.sh")
	improvedStop, hasStop := read(specImprovementsDir + "/stop.sh")
	improvedHealth, hasHealth := read(specImprovementsDir + "/health.sh")
	improvedLessons, hasLessons := read(specImprovementsDir + "/lessons.md")
	reason, _ := read(specImprovementsDir + "/reason.md")

	if !hasSetup && !hasStart && !hasStop && !hasHealth && !hasLessons {
		// Agent didn't write a none.txt and didn't write any improved
		// scripts — silent skip. Either the prompt section was dropped
		// (model regression) or the model decided to skip the entire
		// reflection. Logged at debug rather than warned so it doesn't
		// noisily flag every task.
		b.log.Debug("spec-improvements: no marker and no improvements; skipping", "repo", repo.Slug)
		return
	}

	// Re-fetch the spec before patching. Between the start of this
	// task and now the user may have clicked Delete bootstrap on the
	// Repositories tab — that DELETE drops the row, but the in-memory
	// `*spec` we loaded at task start still looks valid. SaveSpec is
	// a generic UPSERT, so writing the patched version would silently
	// resurrect the deleted row with SpecVersion+1, which from the
	// user's perspective looks like the Delete button doesn't work.
	// ErrNotFound here is the legitimate "deleted during the run"
	// signal; bail with a debug log.
	if _, err := b.bootstrap.GetSpec(ctx, spec.InstallationID, spec.RepoID, spec.Path); err != nil {
		if errors.Is(err, bootstrap.ErrNotFound) {
			b.log.Debug("spec-improvements: spec deleted during run, skipping",
				"repo", repo.Slug)
			return
		}
		b.log.Warn("spec-improvements: re-fetch spec",
			"error", err, "repo", repo.Slug)
		return
	}

	// Patch the existing spec in-place: keep services, secrets, deferred
	// capabilities, success/failure counts, fingerprint — only the
	// scripts the agent rewrote should change. SpecVersion bumps so any
	// future drift detection can see "this is a different generation."
	// ValidationStatus drops to Stale because the new scripts have
	// never actually been executed; store.go writes NULL for
	// last_validated_at on stale specs, which keeps a future drift
	// check from treating the un-tested replacements as "freshly
	// proven good." The next task on this repo runs the new scripts
	// for real, and SaveSpec will stamp validated then.
	patched := *spec
	patched.SpecVersion++
	patched.ValidationStatus = bootstrap.StatusStale
	var changed []string
	if hasSetup {
		patched.SetupScript = improvedSetup
		changed = append(changed, "setup.sh")
	}
	if hasStart {
		patched.StartScript = improvedStart
		changed = append(changed, "start.sh")
	}
	if hasStop {
		patched.StopScript = improvedStop
		changed = append(changed, "stop.sh")
	}
	if hasHealth {
		patched.HealthCheck = improvedHealth
		changed = append(changed, "health.sh")
	}
	if hasLessons {
		patched.LessonsMD = improvedLessons
		changed = append(changed, "lessons.md")
	}
	// Append the agent's reason to the bootstrap_log so a future
	// AutoHeal / debugging session has the rationale alongside the
	// original transcript. Re-truncate after the append so the
	// column doesn't grow without bound across N improvements.
	if reason != "" {
		ts := time.Now().UTC().Format(time.RFC3339)
		entry := fmt.Sprintf("\n\n--- spec improvement at %s (changed: %s) ---\n%s\n",
			ts, strings.Join(changed, ", "), reason)
		patched.BootstrapLog = truncateLogTail(strings.TrimRight(patched.BootstrapLog, "\n") + entry)
	}

	if err := b.bootstrap.SaveSpec(ctx, &patched); err != nil {
		b.log.Warn("spec-improvements: save", "error", err, "repo", repo.Slug)
		emit.Notify("Bootstrap spec — couldn't save improvements",
			fmt.Sprintf("Agent suggested updates to %s but the save failed: `%v`. The PR is unaffected.", strings.Join(changed, ", "), err))
		return
	}

	b.log.Info("spec-improvements: applied",
		"repo", repo.Slug, "changed", strings.Join(changed, ","), "spec_version", patched.SpecVersion)
	body := fmt.Sprintf("Agent learned from this task and updated %s. Saved as spec v%d — the next task on this repo will use the improved scripts.",
		humanList(changed), patched.SpecVersion)
	if reason != "" {
		body += "\n\n**Why:** " + reason
	}
	emit.Notify("Bootstrap spec — improved", body)
}

// humanList renders a small list with Oxford-style joining: "a", "a
// and b", "a, b, and c". Used in the user-facing notify body — Slack-
// style "a,b,c" reads awkwardly when there are only two items.
func humanList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	default:
		return strings.Join(xs[:len(xs)-1], ", ") + ", and " + xs[len(xs)-1]
	}
}
