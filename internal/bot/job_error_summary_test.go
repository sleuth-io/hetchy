package bot

import (
	"strings"
	"testing"
)

func TestJobLastErrorViewSummarizesStartupTranscriptFailure(t *testing.T) {
	raw := `agent setup exited before runtime: step "run-script" exit 1: ...(truncated)... | agent.sh starting tmux session hetchy-claude-14469 for interactive claude | claude did not produce a transcript within 60s __HETCHY_RUN_END run_91751bbdee55a65abe2ede81a7c2c342 1`

	summary, detail := jobLastErrorView(raw)

	want := "Agent startup failed: Claude did not produce a transcript within 60s."
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	if detail != raw {
		t.Fatalf("detail = %q, want raw error", detail)
	}
	if strings.Contains(summary, "HETCHY_RUN_END") || strings.Contains(summary, "truncated") {
		t.Fatalf("summary leaked raw log noise: %q", summary)
	}
}

func TestJobLastErrorViewSummarizesSetupStepExit(t *testing.T) {
	raw := `agent setup exited before runtime: step "run-script" exit 1: command failed`

	summary, detail := jobLastErrorView(raw)

	want := "Agent startup failed while running the agent (exit code 1)."
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	if detail != raw {
		t.Fatalf("detail = %q, want raw error", detail)
	}
}

func TestJobLastErrorViewKeepsShortErrorsSimple(t *testing.T) {
	summary, detail := jobLastErrorView("failed once")

	if summary != "failed once" {
		t.Fatalf("summary = %q, want failed once", summary)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty detail for short error", detail)
	}
}
