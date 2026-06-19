package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sleuth-io/hetchy/internal/auth"
)

const (
	localInviteFlashCookieName = "hetchy_local_invite_flash"
	localInviteFlashTTL        = 5 * time.Minute
)

// inviteHandler creates a pending WorkOS invitation. WorkOS sends the
// email; once accepted the recipient gets a session bound to this org.
func (b *Bot) inviteHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	role := strings.TrimSpace(r.FormValue("role"))
	if email == "" || !strings.Contains(email, "@") {
		http.Error(w, "valid email required", http.StatusBadRequest)
		return
	}
	if role == "" {
		role = "member"
	}
	if !validRoleSlug(role) {
		http.Error(w, "unknown role", http.StatusBadRequest)
		return
	}
	inviteToken, err := b.auth.SendInvitation(r.Context(), email, p.OrgID, role, p.UserID)
	if err != nil {
		b.log.Error("send invitation failed", "error", err, "org", p.OrgID, "email", email)
		http.Error(w, "send invite: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("invitation sent", "org", p.OrgID, "email", email, "role", role, "inviter", p.UserID)
	dest := "/settings/org?tab=members&saved=invited"
	if inviteToken != "" {
		inviteURL := b.cfg.PublicBaseURL() + "/signup?invite=" + url.QueryEscape(inviteToken)
		if err := b.setLocalInviteURLFlash(w, inviteURL); err != nil {
			b.log.Error("set local invite flash failed", "error", err, "org", p.OrgID)
			http.Error(w, "send invite: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (b *Bot) setLocalInviteURLFlash(w http.ResponseWriter, inviteURL string) error {
	value, err := b.encodeLocalInviteFlash(inviteURL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     localInviteFlashCookieName,
		Value:    value,
		Path:     "/settings/org",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   b.cfg.CookieSecure,
		Expires:  time.Now().Add(localInviteFlashTTL),
		MaxAge:   int(localInviteFlashTTL.Seconds()),
	})
	return nil
}

func (b *Bot) consumeLocalInviteURLFlash(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(localInviteFlashCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     localInviteFlashCookieName,
		Value:    "",
		Path:     "/settings/org",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   b.cfg.CookieSecure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
	value, err := b.decodeLocalInviteFlash(cookie.Value)
	if err != nil {
		b.log.Warn("decode local invite flash failed", "error", err)
		return ""
	}
	return value
}

func (b *Bot) encodeLocalInviteFlash(inviteURL string) (string, error) {
	raw := []byte(inviteURL)
	if b.cipher != nil {
		encrypted, err := b.cipher.Encrypt(inviteURL)
		if err != nil {
			return "", err
		}
		raw = encrypted
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (b *Bot) decodeLocalInviteFlash(value string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	if b.cipher == nil {
		return string(raw), nil
	}
	return b.cipher.Decrypt(raw)
}

// invitationActionHandler handles /settings/org/invitations/{id}/revoke.
// The trailing slash on the route registration means we need to parse
// the id and action out of the path ourselves.
func (b *Bot) invitationActionHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	id, action, ok := splitIDAction(r.URL.Path, "/settings/org/invitations/")
	if !ok || action != "revoke" {
		http.Error(w, "unknown action", http.StatusNotFound)
		return
	}
	if err := b.auth.RevokeInvitation(r.Context(), id, p.OrgID); err != nil {
		if errors.Is(err, auth.ErrCrossOrg) {
			http.NotFound(w, r)
			return
		}
		b.log.Error("revoke invitation failed", "error", err, "org", p.OrgID, "id", id)
		http.Error(w, "revoke: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("invitation revoked", "org", p.OrgID, "id", id, "actor", p.UserID)
	http.Redirect(w, r, "/settings/org?tab=members&saved=revoked", http.StatusFound)
}

// memberActionHandler handles /settings/org/members/{id}/{remove|role}.
func (b *Bot) memberActionHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	id, action, ok := splitIDAction(r.URL.Path, "/settings/org/members/")
	if !ok {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	switch action {
	case "remove":
		if err := b.auth.RemoveMember(r.Context(), id, p.OrgID, p.UserID); err != nil {
			if errors.Is(err, auth.ErrCrossOrg) {
				http.NotFound(w, r)
				return
			}
			b.log.Warn("remove member rejected", "error", err, "org", p.OrgID, "id", id, "actor", p.UserID)
			http.Error(w, "remove: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Info("member removed", "org", p.OrgID, "id", id, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=members&saved=removed", http.StatusFound)
	case "role":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			http.Error(w, "role required", http.StatusBadRequest)
			return
		}
		if !validRoleSlug(role) {
			http.Error(w, "unknown role", http.StatusBadRequest)
			return
		}
		if err := b.auth.UpdateMemberRole(r.Context(), id, p.OrgID, p.UserID, role); err != nil {
			if errors.Is(err, auth.ErrCrossOrg) {
				http.NotFound(w, r)
				return
			}
			b.log.Warn("update role rejected", "error", err, "org", p.OrgID, "id", id, "actor", p.UserID)
			http.Error(w, "update role: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Info("member role updated", "org", p.OrgID, "id", id, "role", role, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=members&saved=role", http.StatusFound)
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

// orgDeleteHandler permanently destroys the caller's organization. It
// tears down the org's Slack connection (so inbound events can't
// resurrect the row mid-wipe), finds the WorkOS users who belong only
// to this org, removes the WorkOS organization, deletes only those
// sole-org WorkOS users, wipes every per-org row this app owns
// (org_configs, conversations, agent profiles + runs, GitHub App
// installations, repo bootstrap specs and secret values), and finally
// logs the user out — their session was bound to an org that no longer
// exists.
//
// Slack teardown FIRST is intentional. The Slack dispatch goroutine
// would otherwise be a live writer: an AppUninstalledEvent or
// TokensRevokedEvent landing during the wipe re-Upserts the org
// config, undoing the DELETE we just did. StopOrg synchronously
// cancels the connection and waits for the dispatch loop to drain
// before returning, closing that race. If the pre-org-delete WorkOS
// calls abort, the handler asks Slack to reload this still-existing
// org config before returning the 500.
//
// The WorkOS membership scan runs before any destructive WorkOS call:
// once the org is gone, we can no longer distinguish sole-org users
// from users who should keep access elsewhere. The WorkOS deletions
// then run before the local wipe: a membership-scan or org-delete
// failure aborts while the org still exists, and a user-delete failure
// aborts before local data is removed so ops can see exactly what
// remains. Local data is wiped in a single transaction so a mid-flight
// DB failure rolls everything back. The remaining risk window —
// WorkOS-deleted-but-DB-wipe-fails — is logged with every ID needed
// for ops recovery; the user can no longer use this org's UI to retry
// (the WorkOS org is gone), so this is the failure mode we minimize by
// logging and surfacing a generic 500 rather than leaking pgx internals
// to the browser.
func (b *Bot) orgDeleteHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	orgID := p.OrgID
	b.log.Info("org delete initiated", "org", orgID, "actor", p.UserID)
	// Tear down Slack before touching WorkOS or the DB. StopOrg
	// cancels the dispatch goroutine and blocks until it drains, so
	// no Slack event can race the wipe by calling Upsert behind us.
	if b.slack != nil {
		b.slack.StopOrg(orgID)
	}
	deletableUserIDs, err := b.usersOnlyInOrganization(r.Context(), orgID)
	if err != nil {
		b.log.Error("org delete: workos membership scan failed", "error", err, "org", orgID, "actor", p.UserID)
		b.restoreSlackAfterDeleteAbort(r.Context(), orgID)
		http.Error(w, "Could not inspect organization members. Please try again or contact support.", http.StatusInternalServerError)
		return
	}
	b.log.Info("org delete: workos users selected for deletion", "org", orgID, "actor", p.UserID, "user_ids", deletableUserIDs)
	if err := b.deleteWorkOSOrganization(r.Context(), orgID); err != nil {
		b.log.Error("org delete: workos delete failed", "error", err, "org", orgID, "actor", p.UserID)
		b.restoreSlackAfterDeleteAbort(r.Context(), orgID)
		http.Error(w, "Could not delete the organization. Please try again or contact support.", http.StatusInternalServerError)
		return
	}
	if err := b.deleteWorkOSUsers(r.Context(), deletableUserIDs); err != nil {
		b.log.Error("org delete: workos user delete failed after org delete succeeded",
			"error", err, "org", orgID, "actor", p.UserID, "user_count", len(deletableUserIDs), "user_ids", deletableUserIDs)
		http.Error(w, "Could not delete organization users. Please contact support.", http.StatusInternalServerError)
		return
	}
	if err := b.orgs.Delete(r.Context(), orgID); err != nil {
		// WorkOS org is gone by this point; the row set is rolled back
		// by WithTx, so the org is functionally empty but the local
		// shell row may still exist. Log enough for ops to clean up.
		b.log.Error("org delete: local wipe failed after workos delete succeeded",
			"error", err, "org", orgID, "actor", p.UserID)
		http.Error(w, "Could not delete organization data. Please contact support.", http.StatusInternalServerError)
		return
	}
	b.log.Info("org deleted", "org", orgID, "actor", p.UserID, "deleted_users", len(deletableUserIDs))
	// Reuse LogoutHandler to revoke the session at WorkOS and clear
	// the session cookie. It writes its own redirect to the app root,
	// which renders the landing/login page for an unauthenticated
	// request.
	b.auth.LogoutHandler(w, r)
}

func (b *Bot) usersOnlyInOrganization(ctx context.Context, orgID string) ([]string, error) {
	if b.usersOnlyInOrgFn != nil {
		return b.usersOnlyInOrgFn(ctx, orgID)
	}
	return b.auth.UsersOnlyInOrganization(ctx, orgID)
}

func (b *Bot) deleteWorkOSOrganization(ctx context.Context, orgID string) error {
	if b.deleteWorkOSOrgFn != nil {
		return b.deleteWorkOSOrgFn(ctx, orgID)
	}
	return b.auth.DeleteOrganization(ctx, orgID)
}

func (b *Bot) deleteWorkOSUsers(ctx context.Context, userIDs []string) error {
	if b.deleteWorkOSUsersFn != nil {
		return b.deleteWorkOSUsersFn(ctx, userIDs)
	}
	return b.auth.DeleteUsers(ctx, userIDs)
}

func (b *Bot) restoreSlackAfterDeleteAbort(ctx context.Context, orgID string) {
	if b.slack == nil {
		return
	}
	b.log.Info("org delete aborted before local wipe; restoring slack connection", "org", orgID)
	b.slack.RestartOrg(ctx, orgID)
}

// validRoleSlug guards POSTed role values against typos and arbitrary
// strings. Hardcoded list mirrors the dropdown options; if the WorkOS
// dashboard adds custom roles, extend this set.
func validRoleSlug(s string) bool {
	switch s {
	case "admin", "member":
		return true
	}
	return false
}

// splitIDAction parses paths shaped like prefix/{id}/{action}, returning
// the id and action segments. Both must be non-empty for ok to be true.
// id is restricted to a conservative WorkOS-id charset so a path
// segment containing whitespace, slashes (already split), or special
// characters can't be passed to upstream APIs.
func splitIDAction(path, prefix string) (id, action string, ok bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	if !isSafeID(parts[0]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// isSafeID restricts WorkOS-style ids to ASCII letters, digits, and
// underscores -- the actual id alphabet plus a defensive guard against
// anything weirder slipping through to outbound API calls.
func isSafeID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
