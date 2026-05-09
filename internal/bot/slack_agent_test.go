package bot

import (
	"context"
	"testing"

	"github.com/hetchyhq/hetchy/internal/agents"
)

func TestExtractSlackAgent(t *testing.T) {
	b := &Bot{log: discardLogger(), agents: agents.NewStore(nil)}
	cases := []struct {
		name        string
		in          string
		wantAgent   string
		wantCleaned string
	}{
		{"plain slug", "neckbeard fix the API", "neckbeard", "fix the API"},
		{"at alias", "@frontend make the page responsive", "scriptkiddy", "make the page responsive"},
		{"colon display name", "Archy: design this", "archy", "design this"},
		{"unknown explicit", "@sally ship it", "sally", "ship it"},
		{"unknown bare stays request", "please ship it", "", "please ship it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAgent, gotCleaned := b.extractSlackAgent(context.Background(), "org", tc.in, nil)
			if gotAgent != tc.wantAgent {
				t.Errorf("agent = %q, want %q", gotAgent, tc.wantAgent)
			}
			if gotCleaned != tc.wantCleaned {
				t.Errorf("cleaned = %q, want %q", gotCleaned, tc.wantCleaned)
			}
		})
	}
}
