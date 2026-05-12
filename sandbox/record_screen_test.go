package sandbox

import (
	"os"
	"strings"
	"testing"
)

func TestRecordScreenHelperUsesX11GrabMP4(t *testing.T) {
	body, err := os.ReadFile("hetchy-record-screen")
	if err != nil {
		t.Fatalf("read helper: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"/tmp/hetchy-validate",
		".mp4",
		"ffmpeg -y",
		"-f x11grab",
		"-c:v libx264",
		"-pix_fmt yuv420p",
		"${display}.${screen}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("helper missing %q\n%s", want, got)
		}
	}
}

func TestDockerfileAvoidsPlaywrightChromeAptInstall(t *testing.T) {
	body, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright",
		"playwright install chromium",
		"/opt/google/chrome/chrome",
		"-path '*/chrome-linux/chrome'",
		"-path '*/chrome-linux64/chrome'",
		"ffmpeg xvfb xauth x11-utils",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Dockerfile missing %q\n%s", want, got)
		}
	}
	for _, bad := range []string{
		"playwright install --with-deps",
		"playwright install chrome",
	} {
		if strings.Contains(got, bad) {
			t.Errorf("Dockerfile should not contain %q\n%s", bad, got)
		}
	}
}
