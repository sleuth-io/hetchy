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

//go:embed scripts/sandbox-common.sh
var sandboxCommonScript string

// agentScript, followupScript, and setupCloneScript are the
// on-the-wire script bodies the bot writes to the sandbox. Shared
// helper scripts are prepended so their functions live in the same
// shell scope as the user-visible scripts. We do the join here (vs.
// having each script `source` a separately-deployed file) so runScript
// only has to push one file per invocation and there's no chance of a
// half-deployed set. setupCloneScript needs sandbox-common.sh too so
// the bootstrap path can call hetchy_prepare_repo_workdir, which uses
// the volume-cached repo checkout to skip a full network clone.
var agentScript = claudeWatchdogScript + "\n" + sandboxCommonScript + "\n" + agentScriptBody

var followupScript = claudeWatchdogScript + "\n" + sandboxCommonScript + "\n" + followupScriptBody

var setupCloneScript = sandboxCommonScript + "\n" + setupCloneScriptBody
