package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/slack-go/slack"

	"github.com/sleuth-io/hetchy/internal/auth"
)

// slackUserResolver maps a Slack user_id to the WorkOS user_id of the
// matching hetchy member, so chats started from Slack are attributed to
// their author and show up under that user's LHN filter.
//
// Lookup is two hops: Slack users.info gives us the user's primary
// email, then WorkOS UserManagement.List filters by email + org. A miss
// at either hop yields "" — we never block message handling on it.
//
// Results are cached for the process lifetime, including misses. That
// means a Slack user who joins the org after the bot starts will still
// be unmapped until the bot is restarted; the alternative (TTL or
// invalidation) wasn't worth the complexity given how rarely org
// membership changes. Restart-to-refresh is documented behaviour.
type slackUserResolver struct {
	log *slog.Logger

	// emailLookup returns the email for a Slack user_id. The default
	// implementation calls cli.GetUserInfoContext; tests inject a stub.
	emailLookup func(ctx context.Context, cli *slack.Client, slackUserID string) (string, error)
	// memberLookup returns the WorkOS user_id for an email in orgID, or
	// "" with a nil error when no member matches. Default delegates to
	// auth.Service.FindOrgUserByEmail.
	memberLookup func(ctx context.Context, orgID, email string) (string, error)

	mu    sync.Mutex
	cache map[resolverKey]string // (orgID, slackUserID) -> workosUserID ("" = no match)
}

// resolverKey is a struct rather than a delimited string so a value
// containing the delimiter (or any other char) can never collide two
// distinct (orgID, slackUserID) pairs onto the same cache entry.
type resolverKey struct {
	orgID, slackUserID string
}

// newSlackUserResolver wires the resolver to live Slack + WorkOS lookups
// using the bot's auth service. Tests construct a resolver directly to
// stub emailLookup / memberLookup.
func newSlackUserResolver(log *slog.Logger, a *auth.Service) *slackUserResolver {
	return &slackUserResolver{
		log: log,
		emailLookup: func(ctx context.Context, cli *slack.Client, slackUserID string) (string, error) {
			u, err := cli.GetUserInfoContext(ctx, slackUserID)
			if err != nil {
				return "", err
			}
			if u == nil {
				return "", errors.New("slack: nil user info")
			}
			return u.Profile.Email, nil
		},
		memberLookup: func(ctx context.Context, orgID, email string) (string, error) {
			p, ok, err := a.FindOrgUserByEmail(ctx, orgID, email)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", nil
			}
			return p.UserID, nil
		},
		cache: make(map[resolverKey]string),
	}
}

// Resolve returns the WorkOS user_id for the slack user, or "" if no
// mapping can be made. Errors are logged and swallowed — attribution is
// best-effort, not a precondition for handling the message.
func (r *slackUserResolver) Resolve(ctx context.Context, cli *slack.Client, orgID, slackUserID string) string {
	if orgID == "" || slackUserID == "" {
		return ""
	}
	key := resolverKey{orgID: orgID, slackUserID: slackUserID}

	r.mu.Lock()
	if v, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return v
	}
	r.mu.Unlock()

	email, err := r.emailLookup(ctx, cli, slackUserID)
	if err != nil {
		r.log.Warn("slack user resolver: email lookup failed",
			"org", orgID, "slack_user", slackUserID, "error", err)
		// Don't cache transient errors — next message will retry.
		return ""
	}
	if email == "" {
		// Slack user has no email visible to us (rare: bot lacks
		// users:read.email scope, or the user has email hidden).
		// Cache as "no match" so we don't repeatedly hammer Slack.
		r.store(key, "")
		return ""
	}

	workosID, err := r.memberLookup(ctx, orgID, email)
	if err != nil {
		r.log.Warn("slack user resolver: member lookup failed",
			"org", orgID, "email", email, "error", err)
		return ""
	}
	r.store(key, workosID)
	return workosID
}

func (r *slackUserResolver) store(key resolverKey, val string) {
	r.mu.Lock()
	r.cache[key] = val
	r.mu.Unlock()
}
