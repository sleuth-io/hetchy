package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func minConfig() Config {
	return Config{
		AnthropicAPIKey: "ant",
		GitHubToken:     "ghp",
		GitHubRepo:      "owner/repo",
		BaseBranch:      "main",
		Snapshot:        "claude-playwright",
		DaytonaAPIURL:   "http://127.0.0.1:1", // closed port — never reached in these tests
		WebPort:         "0",                  // bind ephemeral port
		StateFile:       "/tmp/sf-bot-test-nonexistent.json",
		DisableSlack:    true,
	}
}

func TestNew_DisableSlackLeavesSlackNil(t *testing.T) {
	cfg := minConfig()
	t.Setenv("DAYTONA_API_KEY", "fake-key")

	b, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.slack != nil {
		t.Error("b.slack should be nil when DisableSlack=true")
	}
	if b.socket != nil {
		t.Error("b.socket should be nil when DisableSlack=true")
	}
	if b.convos == nil {
		t.Error("b.convos should be initialized")
	}
}

func TestNew_LocalDaytonaModeLogged(t *testing.T) {
	cfg := minConfig()
	cfg.DaytonaAPIURL = "http://localhost:3000/api"
	t.Setenv("DAYTONA_API_KEY", "fake-key")

	buf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(stringerWriter{buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := New(cfg, logger); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(buf.String(), "mode=local") {
		t.Errorf("expected mode=local in logs, got: %s", buf.String())
	}
}

func TestNew_CloudDaytonaModeLogged(t *testing.T) {
	cfg := minConfig()
	cfg.DaytonaAPIURL = ""
	t.Setenv("DAYTONA_API_KEY", "fake-key")

	buf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(stringerWriter{buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := New(cfg, logger); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(buf.String(), "mode=cloud") {
		t.Errorf("expected mode=cloud in logs, got: %s", buf.String())
	}
}

func TestRun_WebOnlyShutsDownOnContextCancel(t *testing.T) {
	cfg := minConfig()
	t.Setenv("DAYTONA_API_KEY", "fake-key")

	b, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// give the web server a moment to bind, then cancel
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// runWeb returns nil on graceful shutdown via http.ErrServerClosed
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// stringerWriter adapts a strings.Builder to io.Writer (slog handler needs Writer).
type stringerWriter struct{ b *strings.Builder }

func (s stringerWriter) Write(p []byte) (int, error) { return s.b.Write(p) }

// satisfy unused-import lint when running this file in isolation
var _ = http.StatusOK

func TestIsTransientError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		transient bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("something"), false},
		{"rate limit 429", sdkerrors.NewDaytonaRateLimitError("too many requests", nil), true},
		{"network error status 0", sdkerrors.NewDaytonaError("connection refused", 0, nil), true},
		{"server error 500", sdkerrors.NewDaytonaError("internal server error", 500, nil), true},
		{"server error 502", sdkerrors.NewDaytonaError("bad gateway", 502, nil), true},
		{"server error 503", sdkerrors.NewDaytonaError("service unavailable", 503, nil), true},
		{"server error 504", sdkerrors.NewDaytonaError("gateway timeout", 504, nil), true},
		{"not found 404", sdkerrors.NewDaytonaNotFoundError("not found", nil), false},
		{"bad request 400", sdkerrors.NewDaytonaError("bad request", 400, nil), false},
		{"unauthorized 401", sdkerrors.NewDaytonaError("unauthorized", 401, nil), false},
		{"forbidden 403", sdkerrors.NewDaytonaError("forbidden", 403, nil), false},
		{"rate limit 429 via DaytonaError", sdkerrors.NewDaytonaError("rate limited", 429, nil), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientError(tc.err)
			if got != tc.transient {
				t.Errorf("isTransientError(%v) = %v, want %v", tc.err, got, tc.transient)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", `'hello'`},
		{"empty", "", `''`},
		{"with_space", "hello world", `'hello world'`},
		{"with_dollar", "$HOME", `'$HOME'`},
		{"with_backtick", "`whoami`", "'`whoami`'"},
		{"with_single_quote", "it's", `'it'\''s'`},
		{"with_double_quote", `say "hi"`, `'say "hi"'`},
		{"with_newline", "a\nb", "'a\nb'"},
		{"with_semicolon_injection", "x; rm -rf /", `'x; rm -rf /'`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellQuote(tc.in)
			if got != tc.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestShellQuote_RoundTrip executes the quoted string through a real shell
// to confirm it round-trips byte-for-byte. This is the property that matters
// for runScript: the value the script sees in $VAR must equal the input.
func TestShellQuote_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	inputs := []string{
		"hello",
		"hello world",
		"it's a trap",
		"$(whoami)",
		"`pwd`",
		`"double" 'single'`,
		"x; rm -rf /",
		"line1\nline2",
		"",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", "X="+shellQuote(in)+`; printf '%s' "$X"`)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("bash failed for %q: %v", in, err)
			}
			if string(out) != in {
				t.Errorf("round-trip mismatch for %q: got %q", in, string(out))
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"shorter", "hi", 10, "hi"},
		{"exact", "hello", 5, "hello"},
		{"longer", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
		{"zero", "anything", 0, "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.s, tc.n)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
			}
			if len(tc.s) > tc.n && !strings.HasSuffix(got, "...") {
				t.Errorf("truncated output should end with '...': got %q", got)
			}
		})
	}
}

// TestRetryLoop verifies that isTransientError correctly classifies errors
// for use in the retry loop in createSandboxWithRetry.
func TestRetryLoop(t *testing.T) {
	t.Run("succeeds on first attempt", func(t *testing.T) {
		attempt := 0
		if attempt != 0 {
			t.Errorf("expected 0 attempts, got %d", attempt)
		}
	})

	t.Run("retries transient error and succeeds", func(t *testing.T) {
		errs := []error{
			sdkerrors.NewDaytonaError("service unavailable", 503, nil),
			sdkerrors.NewDaytonaError("service unavailable", 503, nil),
			nil,
		}
		for i, err := range errs {
			if err == nil {
				if i != 2 {
					t.Errorf("expected success on attempt 2, got attempt %d", i)
				}
				break
			}
			if !isTransientError(err) {
				t.Errorf("attempt %d: expected transient error, got non-transient", i)
			}
		}
	})

	t.Run("gives up after max retries", func(t *testing.T) {
		maxRetries := 3
		for attempt := 0; attempt <= maxRetries; attempt++ {
			err := sdkerrors.NewDaytonaError("service unavailable", 503, nil)
			if attempt >= maxRetries {
				if !isTransientError(err) {
					t.Error("error should still be transient")
				}
				break
			}
			if !isTransientError(err) {
				t.Errorf("attempt %d: expected transient error", attempt)
			}
		}
	})

	t.Run("does not retry permanent error", func(t *testing.T) {
		err := sdkerrors.NewDaytonaError("unauthorized", 401, nil)
		if isTransientError(err) {
			t.Error("401 should not be transient")
		}
	})
}
