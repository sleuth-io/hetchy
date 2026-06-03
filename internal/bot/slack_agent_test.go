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
		{"plain slug stays prose", "bob fix the API", "", "bob fix the API"},
		{"colon slug", "bob: fix the API", "bob", "fix the API"},
		{"at alias", "@frontend make the page responsive", "alice", "make the page responsive"},
		{"colon display name", "Archy: design this", "archy", "design this"},
		{"unknown explicit", "@sally ship it", "sally", "ship it"},
		{"natural alias phrase", "use the frontend to make the page responsive", "alice", "make the page responsive"},
		{"natural display name phrase", "Use Alice to fix the API", "alice", "fix the API"},
		{"natural agent suffix phrase", "use the frontend agent to improve layout", "alice", "improve layout"},
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
		{"exact repo", "hetchyhq/hetchy", "hetchyhq/hetchy", true},
		{"repo in sentence", "add this in the hetchyhq/hetchy repository", "hetchyhq/hetchy", true},
		{"github url", "use https://github.com/hetchyhq/hetchy please", "hetchyhq/hetchy", true},
		{"github url path", "see https://github.com/hetchyhq/hetchy/pull/252", "hetchyhq/hetchy", true},
		{"github git url", "use https://github.com/hetchyhq/hetchy.git please", "hetchyhq/hetchy", true},
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
