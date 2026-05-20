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
		"-- command [args...]",
		"ffmpeg -y",
		"-f x11grab",
		"-c:v libx264",
		"-pix_fmt yuv420p",
		"${display}.${screen}",
		"ffmpeg_pid=$!",
		"\"${cmd[@]}\"",
		"kill -INT \"$ffmpeg_pid\"",
		"for _ in {1..30}",
		"xdpyinfo -display \"$display\"",
		"sleep 0.2",
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
		"HETCHY_PLAYWRIGHT_VALIDATE_DIR=/tmp/hetchy-validate",
		"NODE_PATH=/usr/local/nvm/versions/node/v${NODE_VERSION}/lib/node_modules",
		"test -d \"$NODE_PATH\"",
		"playwright install chromium",
		"/opt/google/chrome/chrome",
		"-path '*/chrome-linux/chrome'",
		"-path '*/chrome-linux64/chrome'",
		"chown -R daytona:daytona \"$PLAYWRIGHT_BROWSERS_PATH\"",
		"chmod -R a+rX \"$PLAYWRIGHT_BROWSERS_PATH\"",
		"hetchy-playwright-smoke",
		"su daytona -c",
		"ffmpeg xvfb xauth x11-utils",
		"xz-utils file",
		"docker-compose-plugin",
		"@devcontainers/cli",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Dockerfile missing %q\n%s", want, got)
		}
	}
	for _, bad := range []string{
		"playwright install --with-deps",
		"playwright install chrome",
		"/usr/local/nvm/v22.14.0/lib/node_modules",
	} {
		if strings.Contains(got, bad) {
			t.Errorf("Dockerfile should not contain %q\n%s", bad, got)
		}
	}
}

func TestPlaywrightSmokeUsesOrdinaryPlaywrightAPIs(t *testing.T) {
	body, err := os.ReadFile("hetchy-playwright-smoke")
	if err != nil {
		t.Fatalf("read helper: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"PLAYWRIGHT_BROWSERS_PATH",
		"NODE_PATH",
		"npm root -g",
		"global_node_modules",
		"Keep this NODE_PATH setup in sync",
		"require('playwright')",
		"chromium.launch",
		"page.screenshot",
		"--no-sandbox",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("smoke helper missing %q\n%s", want, got)
		}
	}
}
