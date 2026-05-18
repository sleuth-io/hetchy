package bot

import (
	"strings"
	"testing"
)

func TestAppendAttachmentContextAddsSandboxPaths(t *testing.T) {
	got := appendAttachmentContext("Fix the UI", []sandboxAttachmentRef{{
		Filename:    "screenshot.png",
		Path:        "/tmp/hetchy-attachments/req/01-screenshot.png",
		ContentType: "image/png",
		SizeBytes:   1234,
	}})
	for _, want := range []string{
		"Fix the UI",
		"Attached files are available in the sandbox",
		"screenshot.png (image/png, 1234 bytes): /tmp/hetchy-attachments/req/01-screenshot.png",
		"manifest.json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("attachment prompt missing %q:\n%s", want, got)
		}
	}
}

func TestSafeSandboxFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "screenshot.png", want: "screenshot.png"},
		{in: "../secrets.json", want: "secrets.json"},
		{in: "logs 2026/05.txt", want: "05.txt"},
		{in: "bad\x00name?.json", want: "badname_.json"},
		{in: "  ", want: "attachment"},
	}
	for _, tc := range cases {
		if got := safeSandboxFilename(tc.in); got != tc.want {
			t.Fatalf("safeSandboxFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
