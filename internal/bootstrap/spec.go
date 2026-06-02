// Package bootstrap encapsulates the "given a repo, figure out how to
// run it" pipeline: detection (programmatic hints), the LLM-driven
// bootstrap loop that produces an executable spec, persistence of that
// spec per-repo, and the runtime apply path that re-runs the spec on
// every subsequent task.
//
// The package is intentionally split:
//
//   - spec.go   — pure data shapes (Spec, Service, Secret, Manifest).
//     No database, no I/O, easy to test.
//   - store.go  — DB CRUD for specs and per-repo secret values,
//     mirroring the orgcfg.Store pattern (encryption on
//     write, decryption on read).
//   - detect.go — programmatic hint cascade (Phase 2).
//   - prompt.go — bootstrap prompt template + hints renderer.
//   - loop.go   — Claude Code invocation in the sandbox + verification.
//
// The design rationale lives in docs/research/repo-bootstrap-and-validation.md.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
)

var errEmptyManifest = errors.New("bootstrap: manifest is empty")

// CurrentBootstrapGeneration is the version of the bootstrap prompt,
// manifest contract, and validation expectations this binary knows how
// to produce. Bump this when existing validated specs should be
// regenerated through auto-heal so they pick up new schema/prompt
// behavior, not just when one saved repo's scripts changed.
const CurrentBootstrapGeneration int32 = 2

// ValidationStatus mirrors the CHECK constraint on
// repo_setup_specs.validation_status.
type ValidationStatus string

const (
	StatusValidated ValidationStatus = "validated"
	// Partial = setup/start/stop/health all pass, but the spec declared one
	// or more deferred capabilities (auth bypassed, downstream services
	// skipped, etc.). Tasks that touch a deferred capability cannot be
	// fully validated.
	StatusPartial ValidationStatus = "partial"
	// Stale = the source fingerprint changed since last validation.
	// The next task triggers re-validation before applying.
	StatusStale ValidationStatus = "stale"
	// Failing = bootstrap couldn't reach even partial success. The user
	// is notified; the spec is kept around so auto-heal can seed a
	// retry from the prior failure.
	StatusFailing ValidationStatus = "failing"
)

// Service is one process the agent spec brings up. The kind hint tells
// the validator how to exercise it: "ui" → screenshot, "api" → curl
// against representative endpoints, "worker" → log inspection.
type Service struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	URL  string `json:"url"`
	Kind string `json:"kind"` // ui | api | admin | worker
}

// Secret describes one env var the spec needs at apply time. UserSupplied
// = true means the bootstrap couldn't mint it safely (real third-party
// API key, OAuth client ID, etc.) and Hetchy must pause for the user
// to fill it in via the secrets UI before the spec can run end-to-end.
type Secret struct {
	Name         string `json:"name"`
	UserSupplied bool   `json:"user_supplied"`
	Hint         string `json:"hint,omitempty"`
}

// Spec is the high-level, decoded view of a repo_setup_specs row. The
// raw JSONB columns become typed slices; the rest is a 1:1 mapping.
type Spec struct {
	InstallationID int64
	RepoID         int64
	Path           string

	SpecVersion int32
	// BootstrapGeneration records which global bootstrap prompt/schema
	// generation produced this spec. SpecVersion is per-repo and bumps
	// on auto-heal/self-learning; BootstrapGeneration is global and lets
	// old validated rows refresh when the application revs the contract.
	// The zero value is treated as old, so tests that expect a current
	// saved spec should set this to CurrentBootstrapGeneration.
	BootstrapGeneration int32
	Kind                string

	SetupScript string
	StartScript string
	HealthCheck string
	StopScript  string
	LessonsMD   string

	Services             []Service
	RequiredSecrets      []Secret
	DeferredCapabilities []string
	SuggestedRepoChanges []string
	ValidationCapability ValidationCapability

	SourceFingerprint string

	ValidationStatus ValidationStatus
	SuccessCount     int32
	FailureCount     int32
	BootstrapLog     string
}

// Manifest is what the bootstrap LLM writes to
// /tmp/hetchy-spec/manifest.json. It is the wire format between the
// agent's bootstrap loop and the host harness.
type Manifest struct {
	Kind                 string               `json:"kind"`
	Services             []Service            `json:"services"`
	RequiredSecrets      []Secret             `json:"required_secrets"`
	DeferredCapabilities []string             `json:"deferred_capabilities,omitempty"`
	SuggestedRepoChanges []string             `json:"suggested_repo_changes,omitempty"`
	ValidationCapability ValidationCapability `json:"validation_capability,omitzero"`
}

// ValidationCapability is the repeatable testability contract for a repo.
// The executable scripts say how to start the app; this says how future
// runs should prove changed behavior after editing code.
type ValidationCapability struct {
	CanRunUI           bool     `json:"can_run_ui,omitempty"`
	DefaultURL         string   `json:"default_url,omitempty"`
	HealthRoute        string   `json:"health_route,omitempty"`
	BrowserSmokeTarget string   `json:"browser_smoke_target,omitempty"`
	TestCommands       []string `json:"test_commands,omitempty"`
	BuildCommand       string   `json:"build_command,omitempty"`
	ReloadCommand      string   `json:"reload_command,omitempty"`
	AuthBypass         string   `json:"auth_bypass,omitempty"`
	SeedData           string   `json:"seed_data,omitempty"`
	RequiredMocks      []string `json:"required_mocks,omitempty"`
	SlowOrFlakyTests   []string `json:"slow_or_flaky_tests,omitempty"`
	EvidenceRequired   []string `json:"evidence_required,omitempty"`
	Notes              string   `json:"notes,omitempty"`
}

// ParseManifest decodes the JSON the bootstrap loop captured from the
// sandbox. Returns a typed error so callers can distinguish "agent
// produced no manifest" from "manifest was malformed."
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, errEmptyManifest
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("bootstrap: parse manifest: %w", err)
	}
	return &m, nil
}

// HasDeferred reports whether the spec couldn't bring the app to full
// functionality. Drives the "validated" vs "partial" status decision
// in the loop.
func (m *Manifest) HasDeferred() bool {
	return len(m.DeferredCapabilities) > 0
}

// NeedsGenerationUpgrade reports whether a saved spec predates the
// current bootstrap prompt/schema generation. Zero can occur only in
// in-memory tests or malformed rows; treat it as old so the runtime
// fails toward regeneration instead of silently trusting an unversioned
// spec.
func NeedsGenerationUpgrade(spec *Spec) bool {
	return spec != nil && spec.BootstrapGeneration < CurrentBootstrapGeneration
}
