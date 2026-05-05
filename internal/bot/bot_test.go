package bot

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
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
		{"forbidden 403", sdkerrors.NewDaytonaError("forbidden", 403, nil), false},
		{"rate limit 429 via DaytonaError", sdkerrors.NewDaytonaError("rate limited", 429, nil), true},
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

// TestHandleRequest_AskForRepo verifies that when the org has no
// default repo, HandleRequest emits a Notify telling the user to
// reply with owner/name and does not try to create a sandbox.
func TestHandleRequest_AskForRepo(t *testing.T) {
	b := &Bot{
		log:          discardLogger(),
		convs:        convstore.New(nil),
		retryBackoff: 0,
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when no default repo is set")
		return nil, sdkerrors.NewDaytonaError("forced failure", 401, nil)
	}
	oc := orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "ant"}

	emit := newCaptureEmitter()
	b.HandleRequest(context.Background(), oc, "do something", "req-1", "thread-1", emit)

	if !emit.hasCall("notify", "Which repository") {
		t.Errorf("expected a Notify with 'Which repository', got Calls=%v", emit.Calls)
	}
	if emit.hasCall("error", "") {
		t.Errorf("did not expect any Error call, got Calls=%v", emit.Calls)
	}
	if emit.hasCall("result", "") {
		t.Errorf("did not expect any Result call, got Calls=%v", emit.Calls)
	}
}

func TestHandleRequest_MissingAnthropic(t *testing.T) {
	b := &Bot{log: discardLogger(), convs: convstore.New(nil), retryBackoff: 0}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when config is incomplete")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()
	b.HandleRequest(context.Background(), orgcfg.Config{OrgID: "o"}, "do something", "req", "thread", emit)
	if !emit.hasCall("error", "Missing Claude credentials") {
		t.Errorf("expected Error call with 'Missing Claude credentials', got Calls=%v", emit.Calls)
	}
}

// TestHandleRequest_SubscriptionTokenSatisfiesCredCheck verifies that
// an org with only a Claude Code OAuth token (no API key) still passes
// the credential check at HandleRequest. The "missing creds" error
// should not fire — the request should advance to the next branch
// (which here trips the no-default-repo Notify, since we don't bother
// configuring a sandbox in this test).
func TestHandleRequest_SubscriptionTokenSatisfiesCredCheck(t *testing.T) {
	b := &Bot{log: discardLogger(), convs: convstore.New(nil), retryBackoff: 0}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when no default repo is set")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()
	b.HandleRequest(context.Background(), orgcfg.Config{OrgID: "o", ClaudeCodeOAuthToken: "sk-ant-oat01-…"}, "do something", "req", "thread", emit)
	if emit.hasCall("error", "Missing Claude credentials") {
		t.Errorf("subscription token alone should satisfy cred check, got Calls=%v", emit.Calls)
	}
	if !emit.hasCall("notify", "Which repository") {
		t.Errorf("expected to advance to repo prompt, got Calls=%v", emit.Calls)
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

func TestRetryLoop(t *testing.T) {
	err503 := sdkerrors.NewDaytonaError("service unavailable", 503, nil)
	err401 := sdkerrors.NewDaytonaError("unauthorized", 401, nil)
	err429 := sdkerrors.NewDaytonaError("rate limited", 429, nil)

	cases := []struct {
		name      string
		returns   []error
		wantCalls int
		wantErr   bool
	}{
		{name: "succeeds on first attempt", returns: []error{nil}, wantCalls: 1},
		{name: "retries transient error and succeeds", returns: []error{err503, err503, nil}, wantCalls: 3},
		{name: "gives up after max retries", returns: []error{err503, err503, err503}, wantCalls: 3, wantErr: true},
		{name: "does not retry permanent error", returns: []error{err401}, wantCalls: 1, wantErr: true},
		{name: "429 via DaytonaError triggers retry", returns: []error{err429, nil}, wantCalls: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := &Bot{
				log:          discardLogger(),
				retryBackoff: 0,
			}
			b.createFn = func(_ context.Context, _ any) (*daytona.Sandbox, error) {
				err := tc.returns[calls]
				calls++
				return nil, err
			}

			_, err := b.createSandboxWithRetry(context.Background(), types.SnapshotParams{})
			if (err != nil) != tc.wantErr {
				t.Errorf("wantErr=%v, got err=%v", tc.wantErr, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("wantCalls=%d, got calls=%d", tc.wantCalls, calls)
			}
		})
	}
}

func TestRetryWithBackoff(t *testing.T) {
	err502 := sdkerrors.NewDaytonaError("bad gateway", 502, nil)
	err401 := sdkerrors.NewDaytonaError("unauthorized", 401, nil)
	sentinel := errors.New("done")

	cases := []struct {
		name      string
		returns   []error
		wantCalls int
		wantErr   bool
	}{
		{name: "succeeds on first attempt", returns: []error{nil}, wantCalls: 1},
		{name: "retries 502 and succeeds", returns: []error{err502, nil}, wantCalls: 2},
		{name: "exhausts retries on repeated 502", returns: []error{err502, err502, err502}, wantCalls: 3, wantErr: true},
		{name: "does not retry permanent error", returns: []error{err401}, wantCalls: 1, wantErr: true},
		{name: "wraps non-daytona error without retry", returns: []error{sentinel}, wantCalls: 1, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := &Bot{log: discardLogger(), retryBackoff: 0}
			err := b.retryWithBackoff(context.Background(), "test-op", func() error {
				err := tc.returns[calls]
				calls++
				return err
			})
			if (err != nil) != tc.wantErr {
				t.Errorf("wantErr=%v, got err=%v", tc.wantErr, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("wantCalls=%d, got calls=%d", tc.wantCalls, calls)
			}
		})
	}
}

// TestParseOwnerRepo locks in the contract used by the "ask for repo"
// flow: anything the user might paste — bare `owner/name`, a github.com
// URL, a clone-style `.git` suffix, trailing punctuation — should
// resolve to the same pair, while gibberish or path-traversal shapes
// must be rejected.
func TestParseOwnerRepo(t *testing.T) {
	cases := []struct {
		in    string
		owner string
		name  string
		ok    bool
	}{
		{"acme/website", "acme", "website", true},
		{"  acme/website  ", "acme", "website", true},
		{"https://github.com/acme/website", "acme", "website", true},
		{"http://github.com/acme/website", "acme", "website", true},
		{"github.com/acme/website", "acme", "website", true},
		{"acme/website.git", "acme", "website", true},
		{"https://github.com/acme/website.git", "acme", "website", true},
		{"https://github.com/acme/website/tree/main", "acme", "website", true},
		{"acme/website.", "acme", "website", true},
		{"acme/website,", "acme", "website", true},
		{"acme/website!", "acme", "website", true},
		{"acme/website)", "acme", "website", true},
		{"the auth one", "", "", false},
		{"acme", "", "", false},
		{"acme/", "", "", false},
		{"/website", "", "", false},
		{"acme//website", "", "", false},
		{"acme/web site", "", "", false},
		{"./website", "", "", false},
		{"-acme/website", "", "", false},
		{"acme/.", "", "", false},
		{"acme/..", "", "", false},
		{"acme/web$site", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotOwner, gotName, gotOk := parseOwnerRepo(tc.in)
			if gotOk != tc.ok || gotOwner != tc.owner || gotName != tc.name {
				t.Errorf("parseOwnerRepo(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, gotOwner, gotName, gotOk, tc.owner, tc.name, tc.ok)
			}
		})
	}
}

func TestValidGitHubName(t *testing.T) {
	good := []string{"acme", "ACME", "acme-co", "acme_co", "v1.2", "a", "a1b2c3"}
	bad := []string{"", ".", "..", ".acme", "-acme", "acme/website", "acme co", "acme$", strings.Repeat("a", 101)}
	for _, s := range good {
		if !validGitHubName(s) {
			t.Errorf("validGitHubName(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validGitHubName(s) {
			t.Errorf("validGitHubName(%q) = true, want false", s)
		}
	}
}
