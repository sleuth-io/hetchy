package artifacts

import (
	"strings"
	"testing"
)

func TestUploadInstructions_NonZeroSlotCount(t *testing.T) {
	got := UploadInstructions(3)

	wants := []string{
		"HETCHY_ARTIFACT_SLOTS",
		"HETCHY_ARTIFACT_SLOT_URL",
		"HETCHY_ARTIFACT_SLOT_TOKEN",
		"kind",
		"content_type",
		"video/mp4",
		"H.264",
		"whole-screen MP4",
		"curl -fSs -X PUT",
		"Authorization: Bearer",
		"GitHub inline playback is not guaranteed",
		"Do not print $HETCHY_ARTIFACT_SLOTS",
		"signed URLs",
		"slot index, kind, content_type",
		`text like "$GET_URL"`,
		"<<'EOF'",
		"gh pr view",
		"PR body still contains literal GET_URL",
		"PR body is missing expanded artifact URL",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("upload instructions missing %q\n%s", w, got)
		}
	}
	if strings.Contains(got, "HETCHY_SCREENSHOT_SLOTS") {
		t.Errorf("upload instructions should not mention legacy screenshot slots\n%s", got)
	}
}

func TestProofInstructionsWithSlots(t *testing.T) {
	got := ProofInstructions(2)
	for _, want := range []string{
		"scratch validation artifacts",
		"not repository",
		"Prefer /tmp/hetchy-validate/",
		".playwright-cli/<name>.png",
		"Do NOT stage, commit, push",
		"GitHub blob/raw URLs",
		"Keep app runtime scratch out of the repo",
		"/tmp/hetchy-runtime",
		"dump.rdb",
		"HETCHY_ARTIFACT_SLOTS",
		"curl -fSs -X PUT",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("proof instructions missing %q\n%s", want, got)
		}
	}
}

func TestProofInstructionsWithoutSlots(t *testing.T) {
	got := ProofInstructions(0)
	for _, want := range []string{
		"No artifact upload slots are available",
		"mark validation incomplete",
		"committing a generated proof file",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("proof instructions without slots missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "HETCHY_ARTIFACT_SLOTS") || strings.Contains(got, "put_url") {
		t.Errorf("proof instructions without slots should not mention upload env vars or URLs\n%s", got)
	}
}

func TestUploadInstructions_ZeroOrNegativeReturnsEmpty(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := UploadInstructions(n); got != "" {
			t.Errorf("UploadInstructions(%d) = %q, want empty", n, got)
		}
	}
}
