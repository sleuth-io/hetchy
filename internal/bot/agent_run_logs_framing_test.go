package bot

import (
	"strings"
	"testing"
)

// framedAgentCommand builds the wrapper whose sentinels let a recovering worker
// tell "the script never started" from "it ran and exited N". Recovery replays
// the log against exactly these markers, so the shape is a contract between the
// two — worth pinning rather than reading back out of a log at 3am.
func TestFramedAgentCommandFramesTheScript(t *testing.T) {
	const runID = "run_abc"
	cmd := framedAgentCommand(runID, "/tmp/agent.sh")

	if !strings.HasPrefix(cmd, "bash -c ") {
		t.Fatalf("command must be a single bash -c invocation: %q", cmd)
	}
	for _, want := range []string{
		hetchyRunBeginSentinel(runID), // start marker
		hetchyRunEndPrefix(runID),     // exit-code marker
		"/tmp/agent.sh",
		"2>&1",         // stderr folded in, or recovery sees a partial log
		`exit "$code"`, // the script's status is the command's status
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("framed command is missing %q\ngot: %s", want, cmd)
		}
	}

	// The end marker has to carry the real exit code, not a literal.
	if !strings.Contains(cmd, `"$code"`) {
		t.Errorf("exit code is not interpolated: %s", cmd)
	}
}

// The framing has to survive a script path that would otherwise break the shell
// wrapper — sandbox paths are generated, and a space is not exotic.
func TestFramedAgentCommandQuotesAwkwardPaths(t *testing.T) {
	cmd := framedAgentCommand("run_1", "/tmp/my agent/run.sh")
	if !strings.Contains(cmd, `my agent`) {
		t.Fatalf("path lost from framed command: %s", cmd)
	}
	// Round-trip the marker the recovery side parses.
	code, ok := parseHetchyRunExitCode("run_1", hetchyRunEndPrefix("run_1")+"7__")
	if !ok || code != 7 {
		t.Fatalf("parseHetchyRunExitCode = (%d, %v), want (7, true)", code, ok)
	}
}

// Malformed end markers must not be read as a successful exit: a truncated or
// garbled line means the run's fate is unknown, not that it exited 0.
func TestParseHetchyRunExitCodeRejectsMalformedMarkers(t *testing.T) {
	prefix := hetchyRunEndPrefix("run_1")
	for _, line := range []string{
		prefix + "7",                            // missing the closing suffix
		prefix + "__",                           // no code at all
		prefix + "notanumber__",                 // unparseable
		"",                                      // empty line
		hetchyRunEndPrefix("other_run") + "0__", // a different run's marker
	} {
		if code, ok := parseHetchyRunExitCode("run_1", line); ok {
			t.Errorf("parseHetchyRunExitCode(%q) accepted a malformed marker as exit %d", line, code)
		}
	}
}
