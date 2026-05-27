package sxsync

import (
	"testing"

	"github.com/hetchyhq/hetchy/internal/agents"
)

func TestBotDescriptionUsesProfileDescription(t *testing.T) {
	got := botDescription(agents.Profile{
		DisplayName: "Reviewer",
		Description: "Reviews pull requests.",
	})

	if got != "Reviews pull requests." {
		t.Fatalf("botDescription() = %q, want profile description", got)
	}
}

func TestBotDescriptionFallsBackToAgentName(t *testing.T) {
	got := botDescription(agents.Profile{DisplayName: "Reviewer"})

	if got != "Custom Hetchy agent: Reviewer" {
		t.Fatalf("botDescription() = %q, want display name fallback", got)
	}
}

func TestBotDescriptionFallsBackToStableIdentifier(t *testing.T) {
	got := botDescription(agents.Profile{Slug: "reviewer", SXBot: "review-bot"})

	if got != "Custom Hetchy agent: reviewer" {
		t.Fatalf("botDescription() = %q, want slug fallback", got)
	}
}
