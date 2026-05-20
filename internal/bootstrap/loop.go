package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
)

// Runner abstracts the sandbox-side script execution so the loop can
// be unit-tested without spinning up a real Daytona sandbox. The
// production implementation lives in internal/bot and bridges to the
// existing runScript helper.
//
// Run executes scriptBody in the sandbox with env exposed and returns
// when the script exits. The returned string is the script's combined
// stdout+stderr — bootstrap persists it in the bootstrap_log column so
// auto-heal runs have the failure trace. ReadFile fetches a path from
// the sandbox (used to extract the agent's artifacts after bootstrap
// completes). WriteFile is symmetric (used to drop the prompt body in).
type Runner interface {
	Run(ctx context.Context, label, scriptBody string, env map[string]string) (string, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte) error
}

// LoopInput carries everything the bootstrap loop needs.
type LoopInput struct {
	OwnerRepo       string
	Path            string
	Hints           *Hints
	SuppliedSecrets map[string]string // injected into the sandbox env

	// RepoDir is the absolute path inside the sandbox where the repo is
	// cloned. Required so the bootstrap shell script can cd into it
	// before invoking claude — the script runs in its own subshell, so
	// it can't rely on a cwd set by a prior setup step. Empty value
	// fails the loop early.
	RepoDir string

	// Preamble, when non-empty, is prepended to the rendered bootstrap
	// prompt. AutoHeal sets this to AutoHealPromptPreamble(...) so a
	// heal run reaches the agent with prior-spec context and the
	// "bias toward minimal update" framing. First-encounter runs leave
	// it empty.
	Preamble string
}

// LoopResult is what the loop produces: a Spec ready to be persisted,
// plus the raw transcript captured from the sandbox (for the
// bootstrap_log column).
//
// On the failure path Spec is nil but Manifest and PartialScripts may
// be populated from a best-effort read of /tmp/hetchy-spec — what the
// agent wrote before the loop tripped. AutoHeal-via-StatusFailing
// uses these so the next attempt sees the prior scripts in the heal
// preamble rather than empty placeholders.
type LoopResult struct {
	Spec           *Spec
	Manifest       *Manifest
	PartialScripts PartialScripts
	Log            string
}

// PartialScripts holds whichever of setup.sh/start.sh/stop.sh/health.sh
// and lessons.md the agent managed to write before the bootstrap loop
// failed. All fields are independently optional — a file that didn't
// get written is an empty string.
type PartialScripts struct {
	Setup   string
	Start   string
	Stop    string
	Health  string
	Lessons string
}

// ErrLoopFailed signals that bootstrap exhausted its iteration budget
// or the agent produced unusable artifacts. Callers turn this into the
// "I couldn't get the repo running" user-facing message described in
// the spec doc's "When bootstrap can't fully succeed" section.
var ErrLoopFailed = errors.New("bootstrap: loop failed")

// bootstrapOutDir is where BootstrapScript writes the bootstrap
// artifacts (setup.sh, start.sh, stop.sh, health.sh, lessons.md,
// manifest.json). The same path is passed to the script via
// HETCHY_BOOTSTRAP_OUT_DIR — keeping it as a const here keeps the
// host- and sandbox-side reads from drifting.
const bootstrapOutDir = "/tmp/hetchy-spec"

// Run drives the bootstrap loop end to end:
//
//  1. Render the bootstrap prompt from hints + args.
//  2. Drop the prompt + bootstrap.sh into the sandbox.
//  3. Invoke bootstrap.sh, which calls Claude Code, runs the agent's
//     setup/start/stop/health/lessons, and emits the artifacts at known paths.
//  4. Read back the artifacts, parse the manifest, decide validation
//     status (validated vs. partial vs. failing).
//  5. Compute the source fingerprint from the host-side hints.
//
// The loop does NOT persist on its own — callers Save() the returned
// LoopResult.Spec via the Store. This separation lets the auto-heal
// path replay a loop without having to reconcile DB writes itself.
func Run(ctx context.Context, runner Runner, in LoopInput) (*LoopResult, error) {
	if runner == nil {
		return nil, errors.New("bootstrap: runner is required")
	}
	if in.Hints == nil {
		return nil, errors.New("bootstrap: hints are required")
	}
	if in.RepoDir == "" {
		return nil, errors.New("bootstrap: RepoDir is required")
	}

	prompt := BuildPrompt(in.Hints, PromptArgs{
		OwnerRepo:       in.OwnerRepo,
		Path:            in.Path,
		SuppliedSecrets: sortedNames(in.SuppliedSecrets),
		Preamble:        in.Preamble,
	})

	if err := runner.WriteFile(ctx, "/tmp/hetchy-bootstrap-prompt.txt", []byte(prompt)); err != nil {
		return nil, fmt.Errorf("bootstrap: write prompt: %w", err)
	}

	env := map[string]string{
		"HETCHY_BOOTSTRAP_PROMPT_FILE": "/tmp/hetchy-bootstrap-prompt.txt",
		"HETCHY_BOOTSTRAP_OUT_DIR":     bootstrapOutDir,
		"HETCHY_BOOTSTRAP_REPO_DIR":    in.RepoDir,
	}
	maps.Copy(env, in.SuppliedSecrets)

	log, err := runner.Run(ctx, "bootstrap", BootstrapScript, env)
	if err != nil {
		// We still try to read whatever artifacts the agent produced,
		// since a non-zero exit can mean "verification failed but the
		// agent wrote something." Useful for auto-heal seeding.
		partial, scripts := readArtifactsBestEffort(ctx, runner, bootstrapOutDir)
		return &LoopResult{Spec: nil, Manifest: partial, PartialScripts: scripts, Log: log},
			fmt.Errorf("%w: %w", ErrLoopFailed, err)
	}

	return ResultFromArtifacts(ctx, runner, in, log)
}

// ResultFromArtifacts reads the files BootstrapScript leaves in the sandbox
// and turns them into the same LoopResult Run would return after a successful
// bootstrap command. Recovery uses this when the Hetchy process dies after
// launching bootstrap.sh: the Daytona command may keep running, and once it
// exits we can still read/save the generated spec instead of starting over.
func ResultFromArtifacts(ctx context.Context, runner Runner, in LoopInput, log string) (*LoopResult, error) {
	if runner == nil {
		return nil, errors.New("bootstrap: runner is required")
	}
	if in.Hints == nil {
		return nil, errors.New("bootstrap: hints are required")
	}

	manifestBytes, err := runner.ReadFile(ctx, bootstrapOutDir+"/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read manifest: %w", err)
	}
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: parse manifest: %w", err)
	}

	setup, err := runner.ReadFile(ctx, bootstrapOutDir+"/setup.sh")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read setup.sh: %w", err)
	}
	start, err := runner.ReadFile(ctx, bootstrapOutDir+"/start.sh")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read start.sh: %w", err)
	}
	stop, err := runner.ReadFile(ctx, bootstrapOutDir+"/stop.sh")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read stop.sh: %w", err)
	}
	health, err := runner.ReadFile(ctx, bootstrapOutDir+"/health.sh")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read health.sh: %w", err)
	}
	lessons, err := runner.ReadFile(ctx, bootstrapOutDir+"/lessons.md")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read lessons.md: %w", err)
	}

	status := StatusValidated
	if manifest.HasDeferred() {
		status = StatusPartial
	}

	spec := &Spec{
		Path:                 in.Path,
		SpecVersion:          1,
		Kind:                 manifest.Kind,
		SetupScript:          string(setup),
		StartScript:          string(start),
		HealthCheck:          string(health),
		StopScript:           string(stop),
		LessonsMD:            string(lessons),
		Services:             manifest.Services,
		RequiredSecrets:      manifest.RequiredSecrets,
		DeferredCapabilities: manifest.DeferredCapabilities,
		SuggestedRepoChanges: manifest.SuggestedRepoChanges,
		SourceFingerprint:    Fingerprint(in.Hints),
		ValidationStatus:     status,
	}
	return &LoopResult{Spec: spec, Manifest: manifest, Log: log}, nil
}

// readArtifactsBestEffort tries to grab whichever of the four
// artifacts the agent managed to write before the bootstrap script
// failed. Used to seed auto-heal — knowing what the agent declared
// (even if verification didn't pass) is better than starting from
// scratch. outDir comes from the same const Run uses to build the
// env for the script, so the host- and sandbox-side reads can't
// drift.
//
// Each read is independently best-effort: a missing or unreadable
// file returns the zero value for that slot, never an error. The
// caller treats anything non-empty as "the agent got this far"
// context for the heal preamble.
func readArtifactsBestEffort(ctx context.Context, runner Runner, outDir string) (*Manifest, PartialScripts) {
	var manifest *Manifest
	if data, err := runner.ReadFile(ctx, outDir+"/manifest.json"); err == nil {
		if m, err := ParseManifest(data); err == nil {
			manifest = m
		}
	}
	read := func(name string) string {
		data, err := runner.ReadFile(ctx, outDir+"/"+name)
		if err != nil {
			return ""
		}
		return string(data)
	}
	return manifest, PartialScripts{
		Setup:   read("setup.sh"),
		Start:   read("start.sh"),
		Stop:    read("stop.sh"),
		Health:  read("health.sh"),
		Lessons: read("lessons.md"),
	}
}

// Fingerprint hashes the detection-relevant subset of the repo. If
// this hash changes between runs, the saved spec is stale and must be
// re-validated (or, more likely, re-bootstrapped).
//
// We hash the *content* of every file the detect cascade examined. A
// rename or content change to any of them flips the hash. We don't
// hash the readme excerpt's ENTIRE content — just the first 200 lines
// the cascade actually examined — so a benign README addendum below
// line 200 doesn't cause spurious re-validation.
func Fingerprint(h *Hints) string {
	if h == nil {
		return ""
	}
	hasher := sha256.New()
	addPath := func(rel string) {
		full := filepath.Join(h.Path, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			return
		}
		fmt.Fprintf(hasher, "%s\n%x\n", rel, sha256.Sum256(data))
	}
	if h.DevContainer != nil {
		addPath(h.DevContainer.Path)
		for _, path := range h.DevContainer.AlternatePaths {
			addPath(path)
		}
	}
	for _, candidate := range []string{
		"AGENTS.md", "agents.md",
		"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
		"Dockerfile", "Makefile", "package.json", "go.mod",
		".env.example", ".env.sample", ".env.template",
	} {
		addPath(candidate)
	}
	// README excerpt — only the part the cascade actually feeds the
	// LLM. Hashing the full file would over-trigger drift.
	if h.ReadmeExcerpt != "" {
		fmt.Fprintf(hasher, "readme:%x\n", sha256.Sum256([]byte(h.ReadmeExcerpt)))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BootstrapScript is the embedded shell script that runs inside the
// sandbox. It hands the bootstrap prompt off to claude (the same way
// agent.sh does for the main agent flow), then enforces the four
// success checks before declaring the run complete.
//
// It is deliberately defensive: every artifact path must exist with
// non-empty content; setup.sh must be idempotent (runs twice in a
// row); stop.sh must be idempotent; start.sh must return after
// launching runtime services, and then we wait for health.sh to pass
// before returning success.
//
// The script writes its own log to stderr so a non-zero exit's tail
// is what the bot persists into bootstrap_log for auto-heal context.
//
//nolint:dupword // The embedded shell script naturally has repeated "fi" tokens.
const BootstrapScript = `#!/bin/bash
set -euo pipefail

: "${HETCHY_BOOTSTRAP_PROMPT_FILE:?required}"
: "${HETCHY_BOOTSTRAP_OUT_DIR:?required}"
: "${HETCHY_BOOTSTRAP_REPO_DIR:?required}"

mkdir -p "${HETCHY_BOOTSTRAP_OUT_DIR}"

# Each shLines call runs in its own subshell, so the cwd from the
# preceding setup-clone step doesn't survive. cd here so the agent's
# tools (Read/Edit/Bash) operate on the cloned repo by default.
cd "${HETCHY_BOOTSTRAP_REPO_DIR}"

# Same pre-create as agent.sh / followup.sh — the Playwright MCP server
# needs a writable output dir and a writable browser profile dir before
# the first screenshot. Some snapshots keep browser binaries under
# root-owned /opt/ms-playwright, so pin the MCP profile under /tmp
# instead of letting it try to create /opt/ms-playwright/mcp-chrome-*.
export PLAYWRIGHT_MCP_OUTPUT_DIR="${PLAYWRIGHT_MCP_OUTPUT_DIR:-${HETCHY_BOOTSTRAP_REPO_DIR}/.playwright-mcp}"
export PLAYWRIGHT_MCP_USER_DATA_DIR="${PLAYWRIGHT_MCP_USER_DATA_DIR:-/tmp/hetchy-playwright-mcp/user-data}"
export PLAYWRIGHT_MCP_HEADLESS="${PLAYWRIGHT_MCP_HEADLESS:-1}"
export PLAYWRIGHT_MCP_NO_SANDBOX="${PLAYWRIGHT_MCP_NO_SANDBOX:-1}"
mkdir -p "$PLAYWRIGHT_MCP_OUTPUT_DIR" "$PLAYWRIGHT_MCP_USER_DATA_DIR"
chmod u+rwx "$PLAYWRIGHT_MCP_OUTPUT_DIR" "$PLAYWRIGHT_MCP_USER_DATA_DIR" 2>/dev/null || true
export PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/opt/ms-playwright}"
export HETCHY_PLAYWRIGHT_VALIDATE_DIR="${HETCHY_PLAYWRIGHT_VALIDATE_DIR:-/tmp/hetchy-validate}"
mkdir -p "$HETCHY_PLAYWRIGHT_VALIDATE_DIR" 2>/dev/null || true
if command -v npm >/dev/null 2>&1; then
  GLOBAL_NODE_MODULES="$(npm root -g 2>/dev/null || true)"
  if [[ -n "$GLOBAL_NODE_MODULES" ]]; then
    case ":${NODE_PATH:-}:" in
      *":${GLOBAL_NODE_MODULES}:"*) ;;
      *)
        if [[ -n "${NODE_PATH:-}" ]]; then
          export NODE_PATH="${GLOBAL_NODE_MODULES}:${NODE_PATH}"
        else
          export NODE_PATH="${GLOBAL_NODE_MODULES}"
        fi
        ;;
    esac
    mkdir -p "${HETCHY_PLAYWRIGHT_VALIDATE_DIR}/node_modules" 2>/dev/null || true
    if [[ -d "${GLOBAL_NODE_MODULES}/playwright" ]]; then
      rm -rf "${HETCHY_PLAYWRIGHT_VALIDATE_DIR}/node_modules/playwright" 2>/dev/null || true
      ln -s "${GLOBAL_NODE_MODULES}/playwright" "${HETCHY_PLAYWRIGHT_VALIDATE_DIR}/node_modules/playwright" 2>/dev/null || true
    fi
    if [[ -d "${GLOBAL_NODE_MODULES}/playwright-core" ]]; then
      rm -rf "${HETCHY_PLAYWRIGHT_VALIDATE_DIR}/node_modules/playwright-core" 2>/dev/null || true
      ln -s "${GLOBAL_NODE_MODULES}/playwright-core" "${HETCHY_PLAYWRIGHT_VALIDATE_DIR}/node_modules/playwright-core" 2>/dev/null || true
    fi
  fi
fi

run_with_timeout() {
  local seconds="$1"
  shift
  if [[ ! "$seconds" =~ ^[0-9]+$ || "$seconds" -le 0 ]]; then
    seconds=120
  fi
  if command -v timeout >/dev/null 2>&1; then
    timeout "${seconds}s" "$@"
    return $?
  fi
  "$@" &
  local child_pid=$!
  local elapsed=0
  while kill -0 "$child_pid" 2>/dev/null; do
    if [[ "$elapsed" -ge "$seconds" ]]; then
      kill "$child_pid" 2>/dev/null || true
      sleep 1
      kill -KILL "$child_pid" 2>/dev/null || true
      wait "$child_pid" >/dev/null 2>&1 || true
      return 124
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  wait "$child_pid"
}

# Strip out the alternate credential — claude's auth precedence puts
# ANTHROPIC_API_KEY ahead of CLAUDE_CODE_OAUTH_TOKEN, so a stray value
# inherited from a snapshot or sibling shell would silently win over the
# token the bot injected. Mirrors agent.sh's auth hygiene.
if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
elif [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_AUTH_TOKEN
fi

# Claude Code expects its config to acknowledge onboarding before it
# will run non-interactively in a fresh sandbox.
mkdir -p "$HOME/.claude"
printf '{"hasCompletedOnboarding":true}\n' > "$HOME/.claude.json"

echo "[hetchy-bootstrap] invoking claude" >&2
# stream-json + verbose mirrors agent.sh — gives the bot typed Block
# updates in real time. The agent is told (in the prompt) to write
# its artifacts to ${HETCHY_BOOTSTRAP_OUT_DIR}; we just verify
# they show up. run_claude_with_watchdog is provided by the watchdog
# prelude that the bot prepends to this script before writing it to
# the sandbox; see internal/bot/scripts/claude-watchdog.sh. The bot
# forces bootstrap to opus/high effort through HETCHY_CLAUDE_MODEL and
# HETCHY_CLAUDE_EFFORT because this step determines future repo runs.
run_claude_with_watchdog "${HETCHY_BOOTSTRAP_PROMPT_FILE}"

echo "[hetchy-bootstrap] verifying artifacts" >&2
for f in setup.sh start.sh stop.sh health.sh lessons.md manifest.json; do
  path="${HETCHY_BOOTSTRAP_OUT_DIR}/${f}"
  if [[ ! -s "$path" ]]; then
    echo "[hetchy-bootstrap] missing or empty: $path" >&2
    exit 70
  fi
done

chmod +x "${HETCHY_BOOTSTRAP_OUT_DIR}/setup.sh" \
         "${HETCHY_BOOTSTRAP_OUT_DIR}/start.sh" \
         "${HETCHY_BOOTSTRAP_OUT_DIR}/stop.sh" \
         "${HETCHY_BOOTSTRAP_OUT_DIR}/health.sh"

echo "[hetchy-bootstrap] running setup.sh (idempotency check: run twice)" >&2
"${HETCHY_BOOTSTRAP_OUT_DIR}/setup.sh"
"${HETCHY_BOOTSTRAP_OUT_DIR}/setup.sh"

echo "[hetchy-bootstrap] stopping stale app runtime" >&2
"${HETCHY_BOOTSTRAP_OUT_DIR}/stop.sh" > "${HETCHY_BOOTSTRAP_OUT_DIR}/stop.log" 2>&1 || true

START_TIMEOUT="${HETCHY_START_TIMEOUT_SECONDS:-120}"
if [[ ! "$START_TIMEOUT" =~ ^[0-9]+$ || "$START_TIMEOUT" -le 0 ]]; then
  START_TIMEOUT=120
fi
trap '"${HETCHY_BOOTSTRAP_OUT_DIR}/stop.sh" > "${HETCHY_BOOTSTRAP_OUT_DIR}/stop.log" 2>&1 || true' EXIT

echo "[hetchy-bootstrap] running start.sh (${START_TIMEOUT}s timeout; start.sh must return after launching services)" >&2
if run_with_timeout "$START_TIMEOUT" "${HETCHY_BOOTSTRAP_OUT_DIR}/start.sh" > "${HETCHY_BOOTSTRAP_OUT_DIR}/start.log" 2>&1; then
  echo "[hetchy-bootstrap] start.sh completed; polling health.sh" >&2
else
  code=$?
  if [[ "$code" -eq 124 ]]; then
    echo "[hetchy-bootstrap] start.sh timed out after ${START_TIMEOUT}s; it must background/daemonize long-lived services" >&2
  else
    echo "[hetchy-bootstrap] start.sh exited non-zero (${code})" >&2
  fi
  exit 71
fi

echo "[hetchy-bootstrap] polling health.sh (90s budget)" >&2
for i in {1..90}; do
  if "${HETCHY_BOOTSTRAP_OUT_DIR}/health.sh" >/dev/null 2>&1; then
    echo "[hetchy-bootstrap] healthy after ${i}s" >&2
    exit 0
  fi
  sleep 1
done

echo "[hetchy-bootstrap] health check never passed" >&2
exit 71
`
