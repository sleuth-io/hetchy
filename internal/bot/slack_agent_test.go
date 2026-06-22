package bot

import (
	"context"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
)

func TestExtractSlackAgent(t *testing.T) {
	b := &Bot{log: discardLogger(), agents: agents.NewStore(nil)}
	cases := []struct {
		name        string
		in          string
		wantAgent   string
		wantCleaned string
	}{
		{"plain slug stays prose", "bob fix the API", "", "bob fix the API"},
		{"colon slug", "bob: fix the API", "bob", "fix the API"},
		{"at catalog slug", "@python-reviewer make the page responsive", "python-reviewer", "make the page responsive"},
		{"colon display name", "Architect: design this", "architect", "design this"},
		{"unknown explicit", "@sally ship it", "sally", "ship it"},
		{"natural slug phrase", "use the python-reviewer to make the page responsive", "python-reviewer", "make the page responsive"},
		{"natural display name phrase", "Use Code Reviewer to fix the API", "code-reviewer", "fix the API"},
		{"natural agent suffix phrase", "use the code reviewer agent to improve layout", "code-reviewer", "improve layout"},
		{"unknown natural phrase stays prose", "use the release bot to ship it", "", "use the release bot to ship it"},
		{"unresolved slack user mention stays prose", "<@U07ABC123XYZ> can you take a look?", "", "<@U07ABC123XYZ> can you take a look?"},
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

func TestExtractSlackRepoMention(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"exact repo", "sleuth-io/hetchy", "sleuth-io/hetchy", true},
		{"repo in sentence", "add this in the sleuth-io/hetchy repository", "sleuth-io/hetchy", true},
		{"github url", "use https://github.com/sleuth-io/hetchy please", "sleuth-io/hetchy", true},
		{"github url path", "see https://github.com/sleuth-io/hetchy/pull/252", "sleuth-io/hetchy", true},
		{"github git url", "use https://github.com/sleuth-io/hetchy.git please", "sleuth-io/hetchy", true},
		{"no repo", "use the Hetchy Bot to ship it", "", false},
		{"invalid repo token", "use acme/web$site", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractSlackRepoMention(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("extractSlackRepoMention(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
