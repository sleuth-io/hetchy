package bot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slack-go/slack"
)

// newTestResolver builds a resolver with stubbed lookups so unit tests
// don't touch Slack or WorkOS. Tests assert behavior via the returned
// counters and the resolver's cache.
func newTestResolver(emailFn func(string) (string, error), memberFn func(string, string) (string, error)) (*slackUserResolver, *atomic.Int32, *atomic.Int32) {
	var emailCalls, memberCalls atomic.Int32
	r := &slackUserResolver{
		log: discardLogger(),
		emailLookup: func(_ context.Context, _ *slack.Client, slackUserID string) (string, error) {
			emailCalls.Add(1)
			return emailFn(slackUserID)
		},
		memberLookup: func(_ context.Context, orgID, email string) (string, error) {
			memberCalls.Add(1)
			return memberFn(orgID, email)
		},
		cache: make(map[string]string),
	}
	return r, &emailCalls, &memberCalls
}

func TestSlackUserResolver_HitCachesAndAvoidsRefetch(t *testing.T) {
	r, emailCalls, memberCalls := newTestResolver(
		func(string) (string, error) { return "alice@example.com", nil },
		func(_, email string) (string, error) {
			if email == "alice@example.com" {
				return "user_123", nil
			}
			return "", nil
		},
	)
	for i := range 5 {
		got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE")
		if got != "user_123" {
			t.Fatalf("call %d: got %q, want user_123", i, got)
		}
	}
	if got := emailCalls.Load(); got != 1 {
		t.Errorf("email lookup ran %d times, want 1 (cache should hit on subsequent calls)", got)
	}
	if got := memberCalls.Load(); got != 1 {
		t.Errorf("member lookup ran %d times, want 1", got)
	}
}

func TestSlackUserResolver_MissIsCachedAsEmpty(t *testing.T) {
	// User has an email but no matching org member. The empty result
	// should be cached so we don't repeatedly hammer the WorkOS API
	// for slack-only users (the common case in mixed orgs).
	r, emailCalls, memberCalls := newTestResolver(
		func(string) (string, error) { return "outsider@example.com", nil },
		func(string, string) (string, error) { return "", nil },
	)
	for i := range 3 {
		got := r.Resolve(context.Background(), nil, "org_a", "U_OUT")
		if got != "" {
			t.Fatalf("call %d: got %q, want empty", i, got)
		}
	}
	if got := emailCalls.Load(); got != 1 {
		t.Errorf("email lookup ran %d times, want 1", got)
	}
	if got := memberCalls.Load(); got != 1 {
		t.Errorf("member lookup ran %d times, want 1 (miss should cache)", got)
	}
}

func TestSlackUserResolver_EmptyEmailCachedToAvoidSlackHammering(t *testing.T) {
	// Bot lacks users:read.email, or the slack user has email hidden:
	// emailLookup returns "" with nil error. We should NOT call
	// memberLookup in that case, and we should cache so subsequent
	// messages from the same slack user don't re-call Slack.
	r, emailCalls, memberCalls := newTestResolver(
		func(string) (string, error) { return "", nil },
		func(string, string) (string, error) {
			t.Fatal("memberLookup should not be called when email is empty")
			return "", nil
		},
	)
	for i := range 3 {
		if got := r.Resolve(context.Background(), nil, "org_a", "U_HIDDEN"); got != "" {
			t.Fatalf("call %d: got %q, want empty", i, got)
		}
	}
	if got := emailCalls.Load(); got != 1 {
		t.Errorf("email lookup ran %d times, want 1", got)
	}
	if got := memberCalls.Load(); got != 0 {
		t.Errorf("member lookup ran %d times, want 0", got)
	}
}

func TestSlackUserResolver_EmailErrorIsNotCached(t *testing.T) {
	// A transient Slack API error must not poison the cache —
	// otherwise a one-off rate-limit would permanently un-attribute a
	// user for the lifetime of the process.
	var attempts atomic.Int32
	r := &slackUserResolver{
		log: discardLogger(),
		emailLookup: func(_ context.Context, _ *slack.Client, _ string) (string, error) {
			if attempts.Add(1) == 1 {
				return "", errors.New("rate limited")
			}
			return "alice@example.com", nil
		},
		memberLookup: func(_ context.Context, _, _ string) (string, error) {
			return "user_123", nil
		},
		cache: make(map[string]string),
	}
	if got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE"); got != "" {
		t.Fatalf("first call (error path): got %q, want empty", got)
	}
	if got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE"); got != "user_123" {
		t.Fatalf("second call (recovered): got %q, want user_123", got)
	}
}

func TestSlackUserResolver_MemberErrorIsNotCached(t *testing.T) {
	var memberAttempts atomic.Int32
	r := &slackUserResolver{
		log: discardLogger(),
		emailLookup: func(_ context.Context, _ *slack.Client, _ string) (string, error) {
			return "alice@example.com", nil
		},
		memberLookup: func(_ context.Context, _, _ string) (string, error) {
			if memberAttempts.Add(1) == 1 {
				return "", errors.New("workos 5xx")
			}
			return "user_123", nil
		},
		cache: make(map[string]string),
	}
	if got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE"); got != "" {
		t.Fatalf("first call (error): got %q, want empty", got)
	}
	if got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE"); got != "user_123" {
		t.Fatalf("second call (recovered): got %q, want user_123", got)
	}
}

func TestSlackUserResolver_PerOrgCacheKeys(t *testing.T) {
	// Same Slack user_id can map to different WorkOS users in
	// different orgs (a contractor in two workspaces, etc.). The cache
	// key must include orgID so org A's mapping doesn't bleed into B.
	r, _, _ := newTestResolver(
		func(string) (string, error) { return "alice@example.com", nil },
		func(orgID, _ string) (string, error) {
			if orgID == "org_a" {
				return "user_a", nil
			}
			return "user_b", nil
		},
	)
	if got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE"); got != "user_a" {
		t.Errorf("org_a: got %q, want user_a", got)
	}
	if got := r.Resolve(context.Background(), nil, "org_b", "U_ALICE"); got != "user_b" {
		t.Errorf("org_b: got %q, want user_b", got)
	}
}

func TestSlackUserResolver_EmptyInputsReturnEmpty(t *testing.T) {
	r, emailCalls, memberCalls := newTestResolver(
		func(string) (string, error) {
			t.Fatal("emailLookup should not be called for empty inputs")
			return "", nil
		},
		func(string, string) (string, error) {
			t.Fatal("memberLookup should not be called for empty inputs")
			return "", nil
		},
	)
	if got := r.Resolve(context.Background(), nil, "", "U"); got != "" {
		t.Errorf("empty orgID: got %q", got)
	}
	if got := r.Resolve(context.Background(), nil, "org", ""); got != "" {
		t.Errorf("empty slackUserID: got %q", got)
	}
	if e, m := emailCalls.Load(), memberCalls.Load(); e != 0 || m != 0 {
		t.Errorf("unexpected lookups: email=%d, member=%d", e, m)
	}
}

func TestSlackUserResolver_ConcurrentResolveSafe(t *testing.T) {
	// Race-detector check: many goroutines resolving the same user
	// must not corrupt the cache. We tolerate redundant lookups (no
	// in-flight dedup is implemented) but require the final result to
	// be stable and the cache map to not data-race.
	r, _, _ := newTestResolver(
		func(string) (string, error) { return "alice@example.com", nil },
		func(string, string) (string, error) { return "user_123", nil },
	)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if got := r.Resolve(context.Background(), nil, "org_a", "U_ALICE"); got != "user_123" {
				t.Errorf("got %q, want user_123", got)
			}
		})
	}
	wg.Wait()
}
