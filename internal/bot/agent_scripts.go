package bot

import _ "embed"

//go:embed scripts/agent.sh
var agentScriptBody string

//go:embed scripts/followup.sh
var followupScriptBody string

//go:embed scripts/setup-clone.sh
var setupCloneScriptBody string

//go:embed scripts/claude-watchdog.sh
var claudeWatchdogScript string

//go:embed scripts/claude-tmux-runner.sh
var claudeTmuxRunnerScript string

//go:embed scripts/codex-runner.sh
var codexRunnerScript string

//go:embed scripts/sandbox-common.sh
var sandboxCommonScript string

//go:embed scripts/sandbox-repo-cache.sh
var sandboxRepoCacheScript string

//go:embed scripts/sandbox-spec.sh
var sandboxSpecScript string

//go:embed scripts/sandbox-checkpoint.sh
var sandboxCheckpointScript string

// agentScript, followupScript, and setupCloneScript are the
// on-the-wire script bodies the bot writes to the sandbox. Shared
// helper scripts are prepended so their functions live in the same
// shell scope as the user-visible scripts. We do the join here (vs.
// having each script `source` a separately-deployed file) so runScript
// only has to push one file per invocation and there's no chance of a
// half-deployed set. setupCloneScript needs the repo-cache helpers too
// so the bootstrap path can call hetchy_prepare_repo_workdir, which uses
// the volume-cached repo checkout to skip a full network clone.
var sandboxRepoCacheHelpersScript = sandboxCommonScript + "\n" + sandboxRepoCacheScript

var sandboxRuntimeHelpersScript = sandboxRepoCacheHelpersScript + "\n" + sandboxSpecScript + "\n" + sandboxCheckpointScript

var agentScript = claudeWatchdogScript + "\n" + claudeTmuxRunnerScript + "\n" + codexRunnerScript + "\n" + sandboxRuntimeHelpersScript + "\n" + agentScriptBody

var followupScript = claudeWatchdogScript + "\n" + claudeTmuxRunnerScript + "\n" + codexRunnerScript + "\n" + sandboxRuntimeHelpersScript + "\n" + followupScriptBody

var setupCloneScript = sandboxRepoCacheHelpersScript + "\n" + setupCloneScriptBody
