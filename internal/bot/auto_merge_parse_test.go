package bot

import (
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

func TestParseAutoMergeAssessmentFromBlocks(t *testing.T) {
	blocksIn := []blocks.Block{{
		Kind: blocks.KindClaudeText,
		Body: "done\n\nHETCHY_AUTO_MERGE_ASSESSMENT\n```json\n" + safeAutoMergeJSON("abc123") + "\n```",
	}}
	got, err := parseAutoMergeAssessmentFromBlocks(blocksIn)
	if err != nil {
		t.Fatalf("parse assessment: %v", err)
	}
	if got.Recommendation != autoMergeRecommendationSafe || got.Risk != autoMergeRiskLow || got.Confidence != autoMergeConfidenceHigh {
		t.Fatalf("unexpected assessment: %+v", got)
	}
	if got.HeadSHA != "abc123" {
		t.Fatalf("head_sha = %q, want abc123", got.HeadSHA)
	}
}

func TestParseAutoMergeAssessmentRejectsMalformedOrMissing(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing marker", body: safeAutoMergeJSON("abc123")},
		{name: "missing object", body: autoMergeAssessmentMarker + "\nnot json"},
		{name: "invalid json", body: autoMergeAssessmentMarker + "\n{\"recommendation\":"},
		{name: "missing head", body: autoMergeAssessmentMarker + "\n" + strings.ReplaceAll(safeAutoMergeJSON("abc123"), `"head_sha":"abc123"`, `"head_sha":""`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseAutoMergeAssessmentFromBlocks([]blocks.Block{{Kind: blocks.KindClaudeText, Body: tt.body}})
			if err == nil {
				t.Fatal("parse assessment succeeded, want error")
			}
		})
	}
}
