package bot

import (
	"errors"
	"strings"
	"testing"
)

func TestBootstrapSkippedMessage(t *testing.T) {
	const base = "Couldn't auto-bootstrap this repo for end-to-end validation — running the agent without a validation spec."
	got := bootstrapSkippedMessage(nil)
	if got != base+" Check server logs for details." {
		t.Fatalf("nil error message = %q", got)
	}

	got = bootstrapSkippedMessage(errors.New(`save spec: bootstrap: upsert spec: ERROR: invalid byte sequence for encoding "UTF8": 0xe2 0x80 0x5b (SQLSTATE 22021)`))
	if !strings.Contains(got, "Reason: could not save the generated spec because captured sandbox output contained invalid UTF-8.") {
		t.Fatalf("invalid UTF-8 message = %q", got)
	}
}

func TestBootstrapErrorSummary(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid utf8",
			err:  errors.New(`save spec: bootstrap: upsert spec: ERROR: invalid byte sequence for encoding "UTF8"`),
			want: "could not save the generated spec because captured sandbox output contained invalid UTF-8",
		},
		{
			name: "save spec",
			err:  errors.New("save spec: bootstrap: upsert spec: connection reset"),
			want: "could not save the generated spec",
		},
		{
			name: "parse manifest",
			err:  errors.New("parse manifest: unexpected end of JSON input"),
			want: "the generated manifest was not valid JSON",
		},
		{
			name: "read manifest",
			err:  errors.New("read manifest: unexpected EOF"),
			want: "could not read the generated manifest from the sandbox",
		},
		{
			name: "read setup",
			err:  errors.New("read setup.sh: permission denied"),
			want: "could not read the generated setup script from the sandbox",
		},
		{
			name: "read start",
			err:  errors.New("read start.sh: permission denied"),
			want: "could not read the generated start script from the sandbox",
		},
		{
			name: "read health",
			err:  errors.New("read health.sh: permission denied"),
			want: "could not read the generated health check from the sandbox",
		},
		{
			name: "setup clone",
			err:  errors.New("setup-clone: bootstrap: run script: exit status 128"),
			want: "could not prepare the repository checkout for bootstrap",
		},
		{
			name: "detect",
			err:  errors.New("detect: inspect repository: no supported project files"),
			want: "could not inspect the repository for bootstrap hints",
		},
		{
			name: "default",
			err:  errors.New("unexpected internal failure"),
			want: "bootstrap returned an internal error; check server logs for details",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bootstrapErrorSummary(tt.err); got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}
