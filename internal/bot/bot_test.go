package bot

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
)

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientError(tc.err); got != tc.transient {
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
			if got := shellQuote(tc.in); got != tc.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestShellQuote_RoundTrip executes the quoted string through a real shell
// to confirm it round-trips byte-for-byte.
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
