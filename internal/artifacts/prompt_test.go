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

func TestUploadInstructions_ZeroOrNegativeReturnsEmpty(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := UploadInstructions(n); got != "" {
			t.Errorf("UploadInstructions(%d) = %q, want empty", n, got)
		}
	}
}
