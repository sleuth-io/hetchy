package bot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
)

func TestIsNotBase64(t *testing.T) {
	valid := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	for _, r := range valid {
		if isNotBase64(r) {
			t.Fatalf("isNotBase64(%q) = true, want false", r)
		}
	}
	for _, r := range " \n-_" {
		if !isNotBase64(r) {
			t.Fatalf("isNotBase64(%q) = false, want true", r)
		}
	}
}

func TestDetectViaSandboxDecodesTarAndRunsBootstrapDetect(t *testing.T) {
	encoded := tarGzBase64(t, map[string]string{
		"package.json": `{"scripts":{"dev":"vite","test":"vitest"}}`,
		"go.mod":       "module example.com/hetchy\n\ngo 1.24\n",
		"README.md":    "# Test App\n\nRun it with npm run dev.\n",
		"src/main.go":  "package main\n",
	})
	var sawDetectCommand bool
	b := &Bot{
		log: discardLogger(),
		shLinesFn: func(_ context.Context, sandboxID string, _ sandboxProcess, sessionID, step, cmd string, timeout, idleTimeout time.Duration, suppressInputEcho bool, _ func(string)) (string, error) {
			if sandboxID != "sandbox-1" || sessionID != "session-1" || step != "detect-tar" {
				t.Fatalf("detect shLines args sandbox=%q session=%q step=%q", sandboxID, sessionID, step)
			}
			if timeout != 90*time.Second || idleTimeout != 0 || suppressInputEcho {
				t.Fatalf("detect timeouts/suppression = timeout:%v idle:%v suppress:%v", timeout, idleTimeout, suppressInputEcho)
			}
			if !strings.Contains(cmd, "find . -maxdepth 4") || !strings.Contains(cmd, "tar --null") || !strings.Contains(cmd, "base64") {
				t.Fatalf("detect command missing expected tar pipeline:\n%s", cmd)
			}
			sawDetectCommand = true
			return "[stderr noise]\n" + encoded[:40] + "\n" + encoded[40:] + "\n", nil
		},
	}

	hints, tempRoot, err := b.detectViaSandbox(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, "session-1", "/home/daytona/work")
	if err != nil {
		t.Fatalf("detectViaSandbox: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()
	if !sawDetectCommand {
		t.Fatal("detect command was not invoked")
	}
	if hints.PackageJSON == nil || hints.PackageJSON.Scripts["dev"] != "vite" {
		t.Fatalf("package hints = %+v", hints.PackageJSON)
	}
	if hints.GoMod == nil || hints.GoMod.Module != "example.com/hetchy" {
		t.Fatalf("go.mod hints = %+v", hints.GoMod)
	}
	if !strings.Contains(hints.ReadmeExcerpt, "Run it with npm run dev") {
		t.Fatalf("readme excerpt = %q", hints.ReadmeExcerpt)
	}
	if _, err := os.Stat(tempRoot); err != nil {
		t.Fatalf("temp root should exist for caller cleanup: %v", err)
	}
}

func tarGzBase64(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}
