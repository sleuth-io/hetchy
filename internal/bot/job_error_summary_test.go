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
	if detail != "" {
		t.Fatalf("detail = %q, want empty detail for short setup error", detail)
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

func TestJobLastErrorViewShowsDetailForLongSingleLineError(t *testing.T) {
	raw := strings.Repeat("x", 200)

	summary, detail := jobLastErrorView(raw)

	if got := len([]rune(summary)); got > jobLastErrorSummaryMaxRunes {
		t.Fatalf("summary length = %d runes, want <= %d", got, jobLastErrorSummaryMaxRunes)
	}
	if detail != raw {
		t.Fatalf("detail = %q, want raw error", detail)
	}
}

func TestSummarizeJobLastError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "codex transcript startup failure",
			raw:  `agent setup exited before runtime: step "run-script" exit 1 | codex did not produce a transcript within 60s __HETCHY_RUN_END run_123 1`,
			want: "Agent startup failed: Codex did not produce a transcript within 60s.",
		},
		{
			name: "setup failure without step",
			raw:  "agent setup exited before runtime: missing command wrapper",
			want: "Agent setup failed before the run started.",
		},
		{
			name: "org config load failure",
			raw:  "load org config: no such file",
			want: "Could not load organization settings: No such file.",
		},
		{
			name: "durable run load failure",
			raw:  "load durable run: context deadline exceeded",
			want: "Could not read the completed run status: Context deadline exceeded.",
		},
		{
			name: "missing durable run",
			raw:  "durable run was not created",
			want: "The scheduled job did not create an agent run.",
		},
		{
			name: "ended state",
			raw:  "job run ended in state timeout: upstream unavailable",
			want: "Job run ended in state Timeout: upstream unavailable.",
		},
		{
			name: "fallback",
			raw:  "sandbox command failed",
			want: "sandbox command failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeJobLastError(tc.raw); got != tc.want {
				t.Fatalf("summarizeJobLastError(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
