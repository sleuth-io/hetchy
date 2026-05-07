package screenshots

import (
	"strings"
	"testing"
)

func TestUploadInstructions_NonZeroSlotCount(t *testing.T) {
	got := UploadInstructions(3)

	wants := []string{
		"HETCHY_SCREENSHOT_SLOTS",
		"put_url",
		"get_url",
		"curl -fSs -X PUT",
		"jq",
		"PR body or a PR comment",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("upload-instructions missing %q\n%s", w, got)
		}
	}
	// The legacy local-file pattern must only appear in the negative
	// "DO NOT do this" sense — never as a copy-pasteable example.
	if strings.Contains(got, "(screenshot-001.png)") {
		t.Errorf("upload-instructions still presents the legacy filename markdown pattern\n%s", got)
	}
}

func TestUploadInstructions_ZeroOrNegativeReturnsEmpty(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := UploadInstructions(n); got != "" {
			t.Errorf("UploadInstructions(%d) = %q, want empty", n, got)
		}
	}
}
