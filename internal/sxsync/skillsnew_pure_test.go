package sxsync

import (
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
)

// --- skillsNewGraphQLErrors ---

func TestSkillsNewGraphQLErrorsJoinsMessages(t *testing.T) {
	errs := []skillsNewGraphQLError{
		{Message: "first error"},
		{Message: "  second error  "},
		{Message: ""},
	}
	got := skillsNewGraphQLErrors(errs)
	if !strings.Contains(got, "first error") || !strings.Contains(got, "second error") {
		t.Errorf("skillsNewGraphQLErrors = %q, want both non-blank messages", got)
	}
	// blank message must not contribute a part — exactly 2 messages means exactly one "; "
	if strings.Count(got, "; ") != 1 {
		t.Errorf("skillsNewGraphQLErrors = %q, want exactly one separator (blank message must be dropped)", got)
	}
}

func TestSkillsNewGraphQLErrorsFallsBackWhenAllMessagesBlank(t *testing.T) {
	errs := []skillsNewGraphQLError{
		{Message: ""},
		{Message: "  "},
	}
	got := skillsNewGraphQLErrors(errs)
	if got != "skills.new graphql error" {
		t.Errorf("skillsNewGraphQLErrors with blank messages = %q, want default message", got)
	}
}

func TestSkillsNewGraphQLErrorsSingleMessage(t *testing.T) {
	errs := []skillsNewGraphQLError{{Message: "token expired"}}
	got := skillsNewGraphQLErrors(errs)
	if got != "token expired" {
		t.Errorf("skillsNewGraphQLErrors = %q, want token expired", got)
	}
}

// --- runtimeTokenLabel ---

func TestRuntimeTokenLabelUsesDisplayName(t *testing.T) {
	got := runtimeTokenLabel(agents.Profile{
		DisplayName: "Code Reviewer",
		Slug:        "reviewer",
		SXBot:       "reviewer-bot",
	})
	if got != "Hetchy runtime: Code Reviewer" {
		t.Errorf("runtimeTokenLabel = %q, want display name", got)
	}
}

func TestRuntimeTokenLabelFallsBackToSlug(t *testing.T) {
	got := runtimeTokenLabel(agents.Profile{
		Slug:  "reviewer",
		SXBot: "reviewer-bot",
	})
	if got != "Hetchy runtime: reviewer" {
		t.Errorf("runtimeTokenLabel = %q, want slug fallback", got)
	}
}

func TestRuntimeTokenLabelFallsBackToSXBot(t *testing.T) {
	got := runtimeTokenLabel(agents.Profile{
		SXBot: "custom-bot",
	})
	if got != "Hetchy runtime: custom-bot" {
		t.Errorf("runtimeTokenLabel = %q, want sxbot fallback", got)
	}
}

func TestRuntimeTokenLabelUsesDefaultWhenAllFieldsBlank(t *testing.T) {
	got := runtimeTokenLabel(agents.Profile{})
	if got != "Hetchy runtime" {
		t.Errorf("runtimeTokenLabel = %q, want default label", got)
	}
}

// --- formatBytes ---

func TestFormatBytesSmallValueUsesBytes(t *testing.T) {
	got := formatBytes(1024)
	if got != "1024 bytes" {
		t.Errorf("formatBytes(1024) = %q, want bytes unit", got)
	}
}

func TestFormatBytesLargeValueUsesMiB(t *testing.T) {
	got := formatBytes(2 * 1024 * 1024)
	if got != "2 MiB" {
		t.Errorf("formatBytes(2 MiB) = %q, want MiB unit", got)
	}
}
