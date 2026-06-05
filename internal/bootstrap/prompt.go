package bootstrap

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PromptArgs holds the minimum context the bootstrap prompt needs
// beyond the static hints: which repo we're bootstrapping and which
// secrets the user has already supplied (names only, never values).
//
// Preamble, when non-empty, is prepended verbatim to the rendered
// prompt. AutoHeal uses it to inject the "this is an AUTO-HEAL run"
// context (prior scripts + last failure trace) so the agent biases
// toward a minimal update of the existing spec rather than starting
// over from scratch.
type PromptArgs struct {
	OwnerRepo       string
	Path            string
	SuppliedSecrets []string
	Preamble        string
}

// BuildPrompt renders the bootstrap prompt the agent will work against.
// The structure is fixed by docs/research/repo-bootstrap-and-validation.md
// — process steps 0-9, four success criteria, the manifest schema. Hints
// are inserted as labeled, easy-to-skim sections; everything is text so
// the agent's tokenization stays predictable.
//
// The prompt is opinionated about a few things that cost real-world
// frustration when omitted:
//
//   - Step 0 tells the agent to prefer Dev Container specs when present.
//   - Step 1 explicitly tells the agent to interpret docs, not run them
//     literally. Hetchy itself (with its Doppler + WorkOS scaffolding)
//     is a good example of why.
//   - Step 2 names the grep pattern for finding the source-of-truth env
//     consumer (os.Getenv, process.env, os.environ). Without this, the
//     agent treats README's required-vars list as authoritative and gets
//     blocked on user secrets it doesn't actually need.
//   - Step 7 is explicit about NOT fabricating fake third-party API keys.
//     This is the most common failure mode for naive "make it run"
//     prompts — the app appears to start, then dies later.
func BuildPrompt(hints *Hints, args PromptArgs) string {
	var b strings.Builder

	// AutoHeal injects "this used to work, here's what changed" context
	// at the top of the prompt. Prepend before the main body so the
	// agent's first impression is the heal framing rather than a
	// fresh-bootstrap framing.
	if args.Preamble != "" {
		b.WriteString(args.Preamble)
		if !strings.HasSuffix(args.Preamble, "\n") {
			b.WriteByte('\n')
		}
	}

	pathSuffix := ""
	if args.Path != "" {
		pathSuffix = " at path " + args.Path
	}
	fmt.Fprintf(&b, `You are bootstrapping repo %s%s so that Hetchy can run it
end-to-end and validate future PRs against it.

Goal: the app responds well enough that we can take a screenshot of a UI
feature or exercise an API endpoint. Full production functionality is NOT
required. Where you can't get there without real third-party credentials,
do partial bootstrap (auth bypassed, external services skipped or mocked)
and declare what's missing in manifest.json.

You must produce six artifacts at fixed paths:

  /tmp/hetchy-spec/setup.sh      — idempotent. Installs deps, runs
                                   migrations, seeds dev data, and
                                   prepares durable build/runtime state.
                                   Safe to re-run on every task. It may
                                   start services needed for provisioning,
                                   but runtime services must not live only
                                   here; start.sh must be able to restore
                                   them after a sandbox stop/resume.
  /tmp/hetchy-spec/start.sh      — re-runnable runtime bring-up. Starts
                                   required runtime dependencies and makes
                                   the current checkout/build the active
                                   app. Safe to call before the agent works,
                                   after the agent rebuilds, and after
                                   sandbox resume. It should recover from
                                   stale app-owned processes, usually by
                                   calling or duplicating stop.sh logic. It
                                   must start long-lived services in the
                                   background/daemon mode and then return.
                                   Do not leave a foreground dev server,
                                   foreground nginx, or watch command as the
                                   final process.
                                   Runtime scratch files (pid files, logs,
                                   Redis dump.rdb, sqlite/dev DB files, etc.)
                                   must live outside the git checkout, e.g.
                                   under /tmp/hetchy-runtime/<repo>. Do not
                                   pollute the repo with untracked runtime
                                   artifacts.
  /tmp/hetchy-spec/stop.sh       — idempotently stops app-owned runtime
                                   processes. Usually leave shared services
                                   like Postgres running unless this repo
                                   specifically owns them.
  /tmp/hetchy-spec/health.sh     — exits 0 iff the app is healthy.
                                   Typically: curl -fsS <url>
  /tmp/hetchy-spec/lessons.md    — concise repo-specific operational
                                   memory. Include only concrete commands,
                                   dependencies, ordering requirements, and
                                   failure modes future runs must remember.
  /tmp/hetchy-spec/manifest.json — JSON manifest, schema below.

Manifest schema:

  {
    "kind": "<short label, e.g. node-web, rails+pg, compose, cli>",
    "services": [
      { "name": "web",
        "port": 3000,
        "url": "http://localhost:3000",
        "kind": "ui" | "api" | "admin" | "worker" }
    ],
    "required_secrets": [
      { "name": "STRIPE_SECRET_KEY",
        "user_supplied": true,
        "hint": "Stripe test key — required for the checkout flow but
                 not for the app to start" }
    ],
    "deferred_capabilities": [
      "Real authentication (currently AUTH_BYPASS=1)"
    ],
    "suggested_repo_changes": [
      "Add an AGENTS.md with a 'make bootstrap' target"
    ],
    "validation_capability": {
      "can_run_ui": true,
      "default_url": "http://localhost:3000",
      "health_route": "/health",
      "browser_smoke_target": "http://localhost:3000/settings",
      "test_commands": ["npm test -- --runInBand", "go test ./..."],
      "build_command": "npm run build",
      "reload_command": "/tmp/hetchy-spec/stop.sh && /tmp/hetchy-spec/start.sh && /tmp/hetchy-spec/health.sh",
      "auth_bypass": "Set AUTH_BYPASS=1 and use user@example.test",
      "seed_data": "setup.sh creates a demo account",
      "required_mocks": ["Stripe webhook calls are mocked"],
      "slow_or_flaky_tests": ["npm run e2e:full"],
      "evidence_required": ["screenshot", "curl"],
      "notes": "Use Playwright CLI for UI changes; use curl for API endpoints."
    }
  }

Process:

  0. If the detection hints include a Dev Container spec
     (devcontainer.json), treat it as the strongest setup signal. Try
     the reference CLI first:

       devcontainer up --workspace-folder "$PWD" --config <path>

     If it works, base setup/start/stop/health/lessons on that environment using
     devcontainer exec and forwarded ports. If it fails because nested
     Docker, privileges, mounts, or networking are unavailable in the
     sandbox, translate the spec's image/build/dockerComposeFile/features
     and lifecycle commands into ordinary setup/start/stop/health scripts, then
     declare the unsupported container capability in manifest.json.

  1. Read the README and any docs/ contributor guides. They are written
     for humans on dev workstations — INTERPRET, don't execute literally.
     Skip developer-only tooling (Doppler, dev hostnames, live-reload
     watchers). Look for AUTH_BYPASS / CI / TEST flags that elide
     external dependencies; for bootstrap purposes, prefer those paths.

     CRITICAL — landing-page reachability: a SECOND agent will later use
     Playwright CLI against the running app to screenshot UI changes. That
     agent has no credentials and will get stuck on any login wall,
     onboarding form, or "create your first workspace" first-run
     screen. Find EVERY env var or config flag that lets the app skip
     these screens (not just auth bypass — also org-bypass, default-
     workspace, skip-onboarding, demo-mode, seeded-user flags) and
     bake the FULL set into start.sh's environment so the running app
     lands an unauthenticated browser on a usable page directly.
     Common patterns to grep for: AUTH_BYPASS, BYPASS_*, SKIP_*_ONBOARD,
     DEFAULT_ORG, DEMO_*, SEED_*, NODE_ENV=test, CI=1. A single bypass
     flag is often insufficient — apps frequently chain auth → org
     selection → onboarding, so each stage may need its own opt-out.

  2. Find the source of truth for required env vars. The README's list
     is a superset for the dev experience; the actual binary often
     requires fewer. Grep the codebase for os.Getenv, process.env,
     os.environ, ENV[, etc., and find the function that decides
     "fail to start" — that is the authoritative list.

  3. Inspect the repo structure beyond the hints below. The hints are
     starting points, not a complete inventory.

  4. Write setup.sh and run it from a clean checkout. It must be
     idempotent and limited to durable provisioning/build/migration work.

  5. Write stop.sh and start.sh. start.sh must be safe to run multiple
     times in the same sandbox and must make the current checkout/build
     active. If the app needs a restart after code changes before E2E
     validation, encode that in start.sh instead of relying on a future
     agent to remember it. start.sh must return after launching services;
     health.sh is the readiness oracle. When you test it manually, run it
     with a timeout so a foreground server cannot burn minutes unnoticed.
     Keep pid/log/db/runtime files out of the repo checkout; use /tmp or
     another ignored runtime directory so future agents don't see dirty
     untracked files from the app itself.

  6. Run stop.sh, run start.sh, then run health.sh. Iterate until health
     passes. Record the URL the app is on.

  7. Write lessons.md with the operational facts your scripts encode and
     future validation must obey. Keep it short and repo-specific. Good:
     "After rebuilding ./dist/foo, run start.sh before HTTP validation."
     Bad: generic advice like "run tests before committing."

  8. Real third-party credentials handling:
     - If a credential has a documented test-mode bypass
       (AUTH_BYPASS=1, NODE_ENV=test, etc.) that lets the app boot, USE it.
       Bootstrap succeeds with reduced functionality.
     - If no bypass exists, declare the secret in manifest.json with
       user_supplied=true. Bootstrap continues with whatever functionality
       you can get; the user fills in the real value through the secrets
       UI before features that need it are exercised.
     - NEVER fabricate plausible-looking fake values for real third-party
       services (e.g. fake Stripe sk_test_… keys). The app will appear
       to start and then fail later in confusing ways.

  9. For every UI service, navigate to its root URL with Playwright CLI
     and take a screenshot. The sandbox ships the 'playwright-cli'
     binary and the companion skill at
     $HOME/.claude/skills/playwright-cli — read its SKILL.md for the
     full command list. Typical flow:

       playwright-cli open <root url>
       playwright-cli snapshot
       playwright-cli screenshot --filename=/tmp/hetchy-validate/landing.png
       playwright-cli close

     Browser binaries live at $PLAYWRIGHT_BROWSERS_PATH, normally
     /opt/ms-playwright. Do not run 'playwright install' just to capture proof.
     If you need finer control than playwright-cli exposes, drop an
     ordinary Playwright script under /tmp/hetchy-validate so it
     resolves the sandbox-provided package and browsers rather than
     waiting on browser installation. The screenshot must show real
     content — not an error page or blank screen.

  10. Populate validation_capability with the repeatable test contract
     future runs should follow after editing code: canonical tests,
     build/reload command, default URL, browser smoke target, auth
     bypass/seed user, known mocks, flaky tests to avoid, and proof
     artifacts expected by reviewers.

  11. Populate suggested_repo_changes if you hit friction that a small
     repo change would have eliminated. Examples: add a 'make bootstrap'
     target; expose required env vars via a --print-required-env flag;
     add a docker-compose profile that starts with bypass flags. ~3 max.

Be concise in your shell scripts. No comments unless they explain a
non-obvious choice. The scripts run on every future task — keep them
fast and idempotent. Echo before long-running dependency or migration
steps so future runs show useful progress while package managers are
downloading quietly. Use set -euo pipefail (or at least set -o pipefail)
before pipelines where the exit status matters.

`,
		args.OwnerRepo, pathSuffix,
	)

	if len(args.SuppliedSecrets) > 0 {
		fmt.Fprintf(&b, "Secrets already injected into the sandbox env (names only): %s\n\n",
			strings.Join(args.SuppliedSecrets, ", "))
	}

	b.WriteString("--- DETECTION HINTS (NOT AUTHORITATIVE — verify and adapt) ---\n\n")
	renderHints(&b, hints)

	return b.String()
}

func renderHints(b *strings.Builder, h *Hints) {
	if h == nil {
		fmt.Fprintln(b, "(no hints — detection produced nothing)")
		return
	}

	if h.DevContainer != nil {
		fmt.Fprintf(b, "## Dev Container spec (%s)\n\n", h.DevContainer.Path)
		if h.DevContainer.Raw == nil {
			fmt.Fprintln(b, "(parse failed — see notes below)")
		} else {
			renderDevContainer(b, h.DevContainer.Raw)
		}
		if len(h.DevContainer.AlternatePaths) > 0 {
			fmt.Fprintf(b, "Alternate devcontainer configs: %s\n", strings.Join(h.DevContainer.AlternatePaths, ", "))
		}
		b.WriteString("\n")
	}

	if h.AgentsMD != "" {
		fmt.Fprintln(b, "## AGENTS.md")
		b.WriteString("\n")
		b.WriteString(h.AgentsMD)
		b.WriteString("\n\n")
	}

	if h.DockerCompose != nil {
		fmt.Fprintf(b, "## docker-compose (%s)\n\n", h.DockerCompose.Path)
		fmt.Fprintf(b, "Services declared: %s\n\n", strings.Join(h.DockerCompose.Services, ", "))
		fmt.Fprintln(b, "Excerpt:")
		fmt.Fprintln(b, "```yaml")
		b.WriteString(h.DockerCompose.Excerpt)
		b.WriteString("\n```\n\n")
	}

	if h.Dockerfile != nil {
		fmt.Fprintln(b, "## Dockerfile")
		if len(h.Dockerfile.Exposes) > 0 {
			fmt.Fprintf(b, "EXPOSE: %s\n", strings.Join(h.Dockerfile.Exposes, ", "))
		}
		if h.Dockerfile.Cmd != "" {
			fmt.Fprintf(b, "CMD: %s\n", h.Dockerfile.Cmd)
		}
		b.WriteString("\n")
	}

	if h.Makefile != nil {
		fmt.Fprintln(b, "## Makefile (run-ish targets)")
		names := sortedKeys(h.Makefile.RunTargets)
		for _, n := range names {
			doc := h.Makefile.RunTargets[n]
			if doc == "" {
				fmt.Fprintf(b, "- %s\n", n)
			} else {
				fmt.Fprintf(b, "- %s — %s\n", n, doc)
			}
		}
		if h.Makefile.HelpExcerpt != "" {
			fmt.Fprintln(b, "\nFirst 60 lines of Makefile:")
			fmt.Fprintln(b, "```makefile")
			b.WriteString(h.Makefile.HelpExcerpt)
			b.WriteString("\n```\n")
		}
		b.WriteString("\n")
	}

	if h.PackageJSON != nil && len(h.PackageJSON.Scripts) > 0 {
		fmt.Fprintln(b, "## package.json scripts")
		for _, k := range sortedKeys(h.PackageJSON.Scripts) {
			fmt.Fprintf(b, "- %s: %s\n", k, h.PackageJSON.Scripts[k])
		}
		b.WriteString("\n")
	}

	if h.GoMod != nil {
		fmt.Fprintln(b, "## go.mod")
		fmt.Fprintf(b, "module %s, go %s\n\n", h.GoMod.Module, h.GoMod.GoVer)
	}

	if h.EnvExample != nil {
		fmt.Fprintf(b, "## %s\n\n", h.EnvExample.Path)
		fmt.Fprintln(b, "Annotated entries (the *comment* above each entry usually tells")
		fmt.Fprintln(b, "you whether the value is mintable, where to source it, or what")
		fmt.Fprintln(b, "format it should be in — read these carefully):")
		b.WriteString("\n")
		for _, e := range h.EnvExample.Entries {
			if e.Comment != "" {
				fmt.Fprintf(b, "  # %s\n", e.Comment)
			}
			fmt.Fprintf(b, "  %s=%s\n", e.Key, e.Value)
		}
		b.WriteString("\n")
	}

	if h.ReadmeExcerpt != "" {
		fmt.Fprintln(b, "## README.md (first 200 lines)")
		fmt.Fprintln(b, "```markdown")
		b.WriteString(h.ReadmeExcerpt)
		b.WriteString("\n```\n\n")
	}

	if len(h.LanguageStats) > 0 {
		fmt.Fprintln(b, "## Language signals")
		for _, ext := range sortedKeys(h.LanguageStats) {
			fmt.Fprintf(b, "- %s: %d files\n", ext, h.LanguageStats[ext])
		}
		b.WriteString("\n")
	}

	if len(h.Notes) > 0 {
		fmt.Fprintln(b, "## Detection notes")
		for _, n := range h.Notes {
			fmt.Fprintf(b, "- %s\n", n)
		}
		b.WriteString("\n")
	}
}

// renderDevContainer pulls the high-value devcontainer.json fields into
// labeled lines instead of dumping the whole JSON. Keeps the prompt
// terse while preserving the actionable bits.
func renderDevContainer(b *strings.Builder, raw map[string]any) {
	for _, key := range []string{
		"image",
		"build",
		"dockerComposeFile",
		"service",
		"runServices",
		"features",
		"containerEnv",
		"remoteEnv",
		"forwardPorts",
		"portsAttributes",
		"workspaceFolder",
		"remoteUser",
		"containerUser",
		"initializeCommand",
		"onCreateCommand",
		"updateContentCommand",
		"postCreateCommand",
		"postStartCommand",
	} {
		v, ok := raw[key]
		if !ok {
			continue
		}
		fmt.Fprintf(b, "%s: %s\n", key, renderDevContainerValue(v))
	}
}

func renderDevContainerValue(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
