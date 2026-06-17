package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	workos "github.com/workos/workos-go/v7"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// formatExpiry trims a WorkOS RFC-3339 timestamp to a friendly
// "YYYY-MM-DD" for display. Falls back to the raw input when parsing
// fails so we never blank a real value due to an unexpected format.
func formatExpiry(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.DateOnly)
}

func formatPGExpiry(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.DateOnly)
}

// displayName returns "First Last" when both are present, falling back
// to whichever single name is set, or the email as a last resort. Used
// by both Profile and Member so the rule lives in one place.
func displayName(first, last, email string) string {
	switch {
	case first != "" && last != "":
		return first + " " + last
	case first != "":
		return first
	case last != "":
		return last
	default:
		return email
	}
}

// ErrForbidden is returned when an action requires the admin role and
// the caller doesn't hold it. Handlers should map this to HTTP 403.
var ErrForbidden = errors.New("auth: forbidden -- admin role required")

// ErrCrossOrg is returned when a caller tries to act on a membership or
// invitation that belongs to a different organization. Handlers should
// map this to HTTP 404 (don't leak existence of the other org's record).
var ErrCrossOrg = errors.New("auth: resource does not belong to caller's organization")

// Profile is the auth-package view of a WorkOS user. Mirrors only the
// subset hetchy needs; the rest of the user record stays inside WorkOS.
type Profile struct {
	UserID    string
	Email     string
	FirstName string
	LastName  string
}

// DisplayName is "First Last" trimmed, or the email when both names are
// blank — useful for sidebars and member lists.
func (p Profile) DisplayName() string {
	return displayName(p.FirstName, p.LastName, p.Email)
}

// Member is a resolved organization membership joined with the user's
// name and email. ListMembers does a per-user GET to populate Email/
// FirstName/LastName since the membership endpoint only returns IDs.
type Member struct {
	MembershipID string
	UserID       string
	Email        string
	FirstName    string
	LastName     string
	RoleSlug     string
	Status       string // active | inactive | pending
}

// DisplayName mirrors Profile.DisplayName so templates can use the same
// helper without caring whether they have a Profile or a Member.
func (m Member) DisplayName() string {
	return displayName(m.FirstName, m.LastName, m.Email)
}

// Invitation is a pending WorkOS invitation. Already-accepted/revoked
// invitations are filtered out by ListInvitations — the settings UI
// only cares about ones still actionable.
type Invitation struct {
	ID        string
	Email     string
	RoleSlug  string
	ExpiresAt string
}

// GetProfile returns the user's display profile.
func (s *Service) GetProfile(ctx context.Context, userID string) (Profile, error) {
	if s.cfg.Bypass {
		return Profile{UserID: userID, Email: s.cfg.BypassEmail, FirstName: "Bypass", LastName: "User"}, nil
	}
	if s.IsLocalMode() {
		u, err := s.local.q.GetLocalAuthUserByID(ctx, userID)
		if err != nil {
			return Profile{}, fmt.Errorf("get local user: %w", err)
		}
		return Profile{UserID: u.ID, Email: u.Email, FirstName: u.FirstName, LastName: u.LastName}, nil
	}
	u, err := s.client.UserManagement().Get(ctx, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("get user: %w", err)
	}
	p := Profile{UserID: u.ID, Email: u.Email}
	if u.FirstName != nil {
		p.FirstName = *u.FirstName
	}
	if u.LastName != nil {
		p.LastName = *u.LastName
	}
	return p, nil
}

// UserHasMultipleOrgs reports whether the user belongs to more than one
// active organization. The "Switch organization" menu item is gated on
// this so single-org users aren't offered a link that would just sign
// them straight back into their only org. We stop iterating as soon as a
// second membership is seen — the exact count is irrelevant.
//
// The answer is memoized per user for multiOrgCacheTTL so the SPA
// catch-all handler, which calls this on every page render, doesn't make a
// WorkOS round-trip each time. Membership changes are picked up once the
// cached entry expires.
func (s *Service) UserHasMultipleOrgs(ctx context.Context, userID string) (bool, error) {
	if s.cfg.Bypass {
		return false, nil
	}
	if s.IsLocalMode() {
		count, err := s.local.q.CountLocalAuthUserOrgs(ctx, userID)
		if err != nil {
			return false, fmt.Errorf("count local user orgs: %w", err)
		}
		return count > 1, nil
	}
	if v, ok := s.multiOrgCacheGet(userID); ok {
		return v, nil
	}
	uid := userID
	active := workos.OrganizationMembershipCreatedDataStatusActive
	// Only two memberships are ever needed to answer "more than one?", so cap
	// the server-side page size at 2 to keep the response payload small.
	limit := 2
	it := s.client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
		UserID:           &uid,
		Statuses:         []workos.UserManagementOrganizationMembershipStatuses{active},
		PaginationParams: workos.PaginationParams{Limit: &limit},
	})
	count := 0
	multi := false
	for it.Next() {
		count++
		if count > 1 {
			multi = true
			break
		}
	}
	if err := it.Err(); err != nil {
		return false, fmt.Errorf("list user memberships: %w", err)
	}
	s.multiOrgCacheSet(userID, multi)
	return multi, nil
}

// nowFn returns the Service's clock, defaulting to time.Now when unset so
// production callers don't have to wire one up. Tests override Service.now
// to drive cache expiry deterministically.
func (s *Service) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// multiOrgCacheGet returns the cached membership-count answer for userID if
// one is present and unexpired. It takes a read lock so concurrent page
// renders — the common case once a user's entry is warm — don't serialize
// on the cache.
func (s *Service) multiOrgCacheGet(userID string) (bool, bool) {
	s.multiOrgMu.RLock()
	defer s.multiOrgMu.RUnlock()
	e, ok := s.multiOrgCache[userID]
	if !ok || !s.nowFn().Before(e.expires) {
		return false, false
	}
	return e.value, true
}

// multiOrgCacheSet stores value for userID with a fresh TTL.
func (s *Service) multiOrgCacheSet(userID string, value bool) {
	s.multiOrgMu.Lock()
	defer s.multiOrgMu.Unlock()
	if s.multiOrgCache == nil {
		s.multiOrgCache = make(map[string]multiOrgEntry)
	}
	s.multiOrgCache[userID] = multiOrgEntry{value: value, expires: s.nowFn().Add(multiOrgCacheTTL)}
}

// UserOrg is one organization the signed-in user belongs to, joined with
// its WorkOS display name. Current marks the org bound to the active
// session so the in-app picker can show which one is selected.
type UserOrg struct {
	OrgID    string
	Name     string
	RoleSlug string
	Current  bool
}

// ListUserOrgs returns the user's active organization memberships, each
// joined with the organization's WorkOS display name and sorted by name.
// currentOrgID flags the membership bound to the live session.
//
// This backs the in-app organization picker (the "Switch organization"
// menu item). Rather than bouncing the user out through a hosted AuthKit
// re-login to change orgs — which logs them out and drops them on the
// sign-in screen — we list the orgs they belong to here and switch the
// session in place via SwitchOrg, so they stay signed in.
//
// The per-org name lookup falls back to the org ID when the WorkOS
// Organizations().Get call fails, so a transient hiccup yields a still-
// selectable row rather than a blank label or a failed page.
func (s *Service) ListUserOrgs(ctx context.Context, userID, currentOrgID string) ([]UserOrg, error) {
	if s.cfg.Bypass {
		name := s.cfg.BypassOrg
		return []UserOrg{{
			OrgID:    s.cfg.BypassOrg,
			Name:     name,
			RoleSlug: s.cfg.BypassRole,
			Current:  s.cfg.BypassOrg == currentOrgID,
		}}, nil
	}
	if s.IsLocalMode() {
		rows, err := s.local.q.ListLocalAuthUserOrgs(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("list local user orgs: %w", err)
		}
		out := make([]UserOrg, 0, len(rows))
		for _, row := range rows {
			out = append(out, UserOrg{
				OrgID:    row.OrgID,
				Name:     row.OrgName,
				RoleSlug: row.RoleSlug,
				Current:  row.OrgID == currentOrgID,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	}
	uid := userID
	active := workos.OrganizationMembershipCreatedDataStatusActive
	it := s.client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
		UserID:   &uid,
		Statuses: []workos.UserManagementOrganizationMembershipStatuses{active},
	})
	var out []UserOrg
	for it.Next() {
		m := it.Current()
		o := UserOrg{OrgID: m.OrganizationID, Current: m.OrganizationID == currentOrgID}
		if m.Role != nil {
			o.RoleSlug = m.Role.Slug
		}
		if org, err := s.client.Organizations().Get(ctx, m.OrganizationID); err == nil {
			o.Name = org.Name
		} else {
			o.Name = m.OrganizationID
		}
		out = append(out, o)
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("list user orgs: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// UpdateProfile rewrites the first and last name on a user. Email/
// password/MFA are *not* mutable here — those flows go through AuthKit's
// hosted pages so we don't have to reimplement password policy / MFA UX.
func (s *Service) UpdateProfile(ctx context.Context, userID, firstName, lastName string) error {
	if s.cfg.Bypass {
		return nil
	}
	if s.IsLocalMode() {
		_, err := s.local.q.UpdateLocalAuthUserProfile(ctx, sqlc.UpdateLocalAuthUserProfileParams{
			ID:        userID,
			FirstName: strings.TrimSpace(firstName),
			LastName:  strings.TrimSpace(lastName),
		})
		return err
	}
	fn, ln := firstName, lastName
	_, err := s.client.UserManagement().Update(ctx, userID, &workos.UserManagementUpdateParams{
		FirstName: &fn,
		LastName:  &ln,
	})
	return err
}

// RequestPasswordReset asks WorkOS for a one-time password-reset URL on
// AuthKit's hosted page. We redirect the user there directly rather than
// emailing — they're already authenticated, so an extra email round-trip
// is friction without security benefit.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) (string, error) {
	if s.cfg.Bypass {
		return "/", nil
	}
	if s.IsLocalMode() {
		return "", errors.New("auth: password reset links are not available in local auth mode")
	}
	pr, err := s.client.UserManagement().ResetPassword(ctx, &workos.UserManagementResetPasswordParams{Email: email})
	if err != nil {
		return "", fmt.Errorf("reset password: %w", err)
	}
	return pr.PasswordResetURL, nil
}

// ListMembers returns active + pending memberships of orgID, joined
// with each user's name/email. The per-user GET inside the loop is
// O(N) network hops; orgs in this app are expected to be small (tens
// of users), so we don't bother with batching.
func (s *Service) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	if s.cfg.Bypass {
		return []Member{{
			MembershipID: "om_bypass", UserID: s.cfg.BypassUser,
			Email: s.cfg.BypassEmail, FirstName: "Bypass", LastName: "User",
			RoleSlug: s.cfg.BypassRole, Status: "active",
		}}, nil
	}
	if s.IsLocalMode() {
		rows, err := s.local.q.ListLocalAuthMembers(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("list local memberships: %w", err)
		}
		out := make([]Member, 0, len(rows))
		for _, row := range rows {
			out = append(out, Member{
				MembershipID: row.MembershipID,
				UserID:       row.UserID,
				Email:        row.Email,
				FirstName:    row.FirstName,
				LastName:     row.LastName,
				RoleSlug:     row.RoleSlug,
				Status:       "active",
			})
		}
		return out, nil
	}
	org := orgID
	it := s.client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
		OrganizationID: &org,
	})
	var out []Member
	for it.Next() {
		m := it.Current()
		mem := Member{
			MembershipID: m.ID,
			UserID:       m.UserID,
			Status:       string(m.Status),
		}
		if m.Role != nil {
			mem.RoleSlug = m.Role.Slug
		}
		// A per-user GET may transiently fail (rate limit, deleted user,
		// etc.). Render the row anyway with a placeholder rather than
		// blanking the whole members tab.
		u, err := s.client.UserManagement().Get(ctx, m.UserID)
		if err == nil {
			mem.Email = u.Email
			if u.FirstName != nil {
				mem.FirstName = *u.FirstName
			}
			if u.LastName != nil {
				mem.LastName = *u.LastName
			}
		} else {
			mem.Email = "(unavailable: " + m.UserID + ")"
		}
		out = append(out, mem)
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	return out, nil
}

// UsersOnlyInOrganization returns the active WorkOS users whose active
// organization memberships are all scoped to orgID. It is used by the
// organization-delete flow: deleting the WorkOS organization removes the
// org shell, but WorkOS users are environment-level records and must be
// deleted separately. Users who also belong to another org are deliberately
// skipped so deleting one org does not destroy access elsewhere.
func (s *Service) UsersOnlyInOrganization(ctx context.Context, orgID string) ([]string, error) {
	if s.cfg.Bypass {
		if s.cfg.BypassUser != "" && s.cfg.BypassOrg == orgID {
			return []string{s.cfg.BypassUser}, nil
		}
		return nil, nil
	}
	if s.IsLocalMode() {
		members, err := s.local.q.ListLocalAuthMembers(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("list local org memberships for deletion: %w", err)
		}
		var out []string
		for _, member := range members {
			count, err := s.local.q.CountLocalAuthUserOrgs(ctx, member.UserID)
			if err != nil {
				return nil, fmt.Errorf("count local orgs for user %s: %w", member.UserID, err)
			}
			if count <= 1 {
				out = append(out, member.UserID)
			}
		}
		sort.Strings(out)
		return out, nil
	}

	org := orgID
	it := s.client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
		OrganizationID: &org,
	})
	userIDs := map[string]struct{}{}
	for it.Next() {
		m := it.Current()
		if m.UserID != "" {
			userIDs[m.UserID] = struct{}{}
		}
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("list org memberships for deletion: %w", err)
	}

	var out []string
	for userID := range userIDs {
		user := userID
		memberships := s.client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
			UserID: &user,
		})
		onlyThisOrg := true
		for memberships.Next() {
			m := memberships.Current()
			if m.OrganizationID != orgID {
				onlyThisOrg = false
			}
		}
		if err := memberships.Err(); err != nil {
			return nil, fmt.Errorf("list memberships for user %s: %w", userID, err)
		}
		if onlyThisOrg {
			out = append(out, userID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// DeleteUsers permanently deletes WorkOS user accounts by ID. The caller
// must decide which IDs are safe to delete; this helper intentionally does
// no membership checks so those checks stay explicit at the call site.
func (s *Service) DeleteUsers(ctx context.Context, userIDs []string) error {
	if s.cfg.Bypass {
		return nil
	}
	if s.IsLocalMode() {
		for _, userID := range userIDs {
			if userID == "" {
				continue
			}
			if err := s.local.q.DeleteLocalAuthUser(ctx, userID); err != nil {
				return fmt.Errorf("delete local user %s: %w", userID, err)
			}
		}
		return nil
	}
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		if err := s.client.UserManagement().Delete(ctx, userID); err != nil {
			return fmt.Errorf("delete user %s: %w", userID, err)
		}
	}
	return nil
}

// FindOrgUserByEmail returns the WorkOS user that belongs to orgID and
// has the given email. The Slack integration uses this to map a Slack
// user (whose email comes from users.info) to the matching hetchy user
// so chats they start show up in their LHN filter.
//
// Returns ok=false (with no error) when no matching org-member exists,
// so callers can treat "no mapping" as a normal case rather than a
// failure. Email matching delegates to WorkOS's filter, which is
// case-insensitive on the indexed primary email — secondary emails are
// not consulted.
func (s *Service) FindOrgUserByEmail(ctx context.Context, orgID, email string) (Profile, bool, error) {
	if email == "" {
		return Profile{}, false, nil
	}
	if s.cfg.Bypass {
		if email == s.cfg.BypassEmail {
			return Profile{UserID: s.cfg.BypassUser, Email: email, FirstName: "Bypass", LastName: "User"}, true, nil
		}
		return Profile{}, false, nil
	}
	if s.IsLocalMode() {
		u, err := s.local.q.FindLocalAuthOrgUserByEmail(ctx, sqlc.FindLocalAuthOrgUserByEmailParams{
			OrgID:           orgID,
			EmailNormalized: normalizeEmail(email),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, false, nil
		}
		if err != nil {
			return Profile{}, false, fmt.Errorf("find local user by email: %w", err)
		}
		return Profile{UserID: u.ID, Email: u.Email, FirstName: u.FirstName, LastName: u.LastName}, true, nil
	}
	org := orgID
	em := email
	it := s.client.UserManagement().List(ctx, &workos.UserManagementListParams{
		OrganizationID: &org,
		Email:          &em,
	})
	if !it.Next() {
		if err := it.Err(); err != nil {
			return Profile{}, false, fmt.Errorf("find user by email: %w", err)
		}
		return Profile{}, false, nil
	}
	u := it.Current()
	p := Profile{UserID: u.ID, Email: u.Email}
	if u.FirstName != nil {
		p.FirstName = *u.FirstName
	}
	if u.LastName != nil {
		p.LastName = *u.LastName
	}
	return p, true, nil
}

// ListInvitations returns invitations on orgID that are still pending
// (not yet accepted/revoked/expired). Accepted invitations show up as
// active memberships in ListMembers, so surfacing them here would
// double-list users.
func (s *Service) ListInvitations(ctx context.Context, orgID string) ([]Invitation, error) {
	if s.cfg.Bypass {
		return nil, nil
	}
	if s.IsLocalMode() {
		rows, err := s.local.q.ListLocalAuthInvitations(ctx, orgID)
		if err != nil {
			return nil, fmt.Errorf("list local invitations: %w", err)
		}
		out := make([]Invitation, 0, len(rows))
		for _, row := range rows {
			expires := ""
			if row.ExpiresAt.Valid {
				expires = formatPGExpiry(row.ExpiresAt.Time)
			}
			out = append(out, Invitation{
				ID:        row.ID,
				Email:     row.Email,
				RoleSlug:  row.RoleSlug,
				ExpiresAt: expires,
			})
		}
		return out, nil
	}
	org := orgID
	it := s.client.UserManagement().ListInvitations(ctx, &workos.UserManagementListInvitationsParams{
		OrganizationID: &org,
	})
	var out []Invitation
	for it.Next() {
		inv := it.Current()
		if string(inv.State) != "pending" {
			continue
		}
		i := Invitation{ID: inv.ID, Email: inv.Email, ExpiresAt: formatExpiry(inv.ExpiresAt)}
		if inv.RoleSlug != nil {
			i.RoleSlug = *inv.RoleSlug
		}
		out = append(out, i)
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	return out, nil
}

// SendInvitation creates a pending invitation. WorkOS sends the email
// — the recipient lands in AuthKit, accepts, and gets a session bound
// to orgID. inviterUserID is shown in the invitation email.
func (s *Service) SendInvitation(ctx context.Context, email, orgID, roleSlug, inviterUserID string) (string, error) {
	if s.cfg.Bypass {
		return "", nil
	}
	if s.IsLocalMode() {
		return s.localSendInvitation(ctx, email, orgID, roleSlug, inviterUserID)
	}
	org, role, inv := orgID, roleSlug, inviterUserID
	_, err := s.client.UserManagement().SendInvitation(ctx, &workos.UserManagementSendInvitationParams{
		Email:          email,
		OrganizationID: &org,
		RoleSlug:       &role,
		InviterUserID:  &inv,
	})
	return "", err
}

// RevokeInvitation cancels a pending invitation. expectedOrgID gates the
// action: the invitation must belong to that org, otherwise ErrCrossOrg.
// Without this, an admin of org A could revoke an invitation belonging
// to org B by passing its opaque id -- WorkOS does not enforce
// tenant scoping on the by-id endpoints.
func (s *Service) RevokeInvitation(ctx context.Context, id, expectedOrgID string) error {
	if s.cfg.Bypass {
		return nil
	}
	if s.IsLocalMode() {
		inv, err := s.local.q.GetLocalAuthInvitation(ctx, id)
		if err != nil {
			return fmt.Errorf("get local invitation: %w", err)
		}
		if inv.OrgID != expectedOrgID {
			return ErrCrossOrg
		}
		return s.local.q.RevokeLocalAuthInvitation(ctx, sqlc.RevokeLocalAuthInvitationParams{ID: id, OrgID: expectedOrgID})
	}
	inv, err := s.client.UserManagement().GetInvitation(ctx, id)
	if err != nil {
		return fmt.Errorf("get invitation: %w", err)
	}
	if inv.OrganizationID == nil || *inv.OrganizationID != expectedOrgID {
		return ErrCrossOrg
	}
	_, err = s.client.UserManagement().RevokeInvitation(ctx, id)
	return err
}

// resolveOrgMember fetches a membership and asserts it belongs to
// expectedOrgID. Shared by RemoveMember/UpdateMemberRole so the org-
// scoping check lives in one place.
func (s *Service) resolveOrgMember(ctx context.Context, membershipID, expectedOrgID string) (*workos.UserOrganizationMembership, error) {
	m, err := s.client.UserManagement().GetOrganizationMembership(ctx, membershipID)
	if err != nil {
		return nil, fmt.Errorf("get membership: %w", err)
	}
	if m.OrganizationID != expectedOrgID {
		return nil, ErrCrossOrg
	}
	return m, nil
}

// RemoveMember deletes the membership by its id. The underlying user
// account survives -- they just lose access to this org. expectedOrgID
// scopes the action; callerUserID + the last-admin guard prevent the
// caller from removing themselves or stranding the org with no admins.
func (s *Service) RemoveMember(ctx context.Context, membershipID, expectedOrgID, callerUserID string) error {
	if s.cfg.Bypass {
		return nil
	}
	if s.IsLocalMode() {
		m, err := s.local.q.GetLocalAuthMembership(ctx, membershipID)
		if err != nil {
			return fmt.Errorf("get local membership: %w", err)
		}
		if m.OrgID != expectedOrgID {
			return ErrCrossOrg
		}
		if m.UserID == callerUserID {
			return errors.New("auth: you cannot remove your own membership")
		}
		if m.RoleSlug == "admin" {
			count, err := s.local.q.CountLocalAuthAdmins(ctx, expectedOrgID)
			if err != nil {
				return fmt.Errorf("count local admins: %w", err)
			}
			if count <= 1 {
				return errors.New("auth: cannot remove the last admin in the organization")
			}
		}
		return s.local.q.DeleteLocalAuthMembership(ctx, membershipID)
	}
	m, err := s.resolveOrgMember(ctx, membershipID, expectedOrgID)
	if err != nil {
		return err
	}
	if m.UserID == callerUserID {
		return errors.New("auth: you cannot remove your own membership")
	}
	if m.Role != nil && m.Role.Slug == "admin" {
		count, err := s.countAdmins(ctx, expectedOrgID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return errors.New("auth: cannot remove the last admin in the organization")
		}
	}
	return s.client.UserManagement().DeleteOrganizationMembership(ctx, membershipID)
}

// UpdateMemberRole changes the role on a membership. roleSlug must be
// one of the role slugs defined in the WorkOS dashboard. Same scoping
// + last-admin guards as RemoveMember -- demoting the only admin to a
// non-admin role is the same threat as removing them.
func (s *Service) UpdateMemberRole(ctx context.Context, membershipID, expectedOrgID, callerUserID, roleSlug string) error {
	if s.cfg.Bypass {
		return nil
	}
	if s.IsLocalMode() {
		m, err := s.local.q.GetLocalAuthMembership(ctx, membershipID)
		if err != nil {
			return fmt.Errorf("get local membership: %w", err)
		}
		if m.OrgID != expectedOrgID {
			return ErrCrossOrg
		}
		if m.UserID == callerUserID {
			return errors.New("auth: you cannot change your own role")
		}
		if m.RoleSlug == "admin" && roleSlug != "admin" {
			count, err := s.local.q.CountLocalAuthAdmins(ctx, expectedOrgID)
			if err != nil {
				return fmt.Errorf("count local admins: %w", err)
			}
			if count <= 1 {
				return errors.New("auth: cannot demote the last admin in the organization")
			}
		}
		_, err = s.local.q.UpdateLocalAuthMembershipRole(ctx, sqlc.UpdateLocalAuthMembershipRoleParams{ID: membershipID, RoleSlug: roleSlug})
		return err
	}
	m, err := s.resolveOrgMember(ctx, membershipID, expectedOrgID)
	if err != nil {
		return err
	}
	if m.UserID == callerUserID {
		return errors.New("auth: you cannot change your own role")
	}
	if m.Role != nil && m.Role.Slug == "admin" && roleSlug != "admin" {
		count, err := s.countAdmins(ctx, expectedOrgID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return errors.New("auth: cannot demote the last admin in the organization")
		}
	}
	_, err = s.client.UserManagement().UpdateOrganizationMembership(ctx, membershipID, &workos.UserManagementUpdateOrganizationMembershipParams{
		Role: workos.UserManagementRoleSingle{Slug: roleSlug},
	})
	return err
}

// countAdmins returns the number of active admin memberships in orgID.
// Used by the last-admin guards.
//
// KNOWN RACE: this is a read-then-write check across two WorkOS API
// calls. Two concurrent demote/remove requests targeting different
// admins in a two-admin org can both observe count == 2, both pass the
// guard, and both succeed -- leaving the org with zero admins. WorkOS
// has no transactional primitive that would close this window. If it
// becomes a real issue, gate read+mutate behind a short-TTL distributed
// lock keyed on orgID. For now the failure mode is a (rare) recovered-
// by-WorkOS-support situation rather than data loss.
func (s *Service) countAdmins(ctx context.Context, orgID string) (int, error) {
	if s.IsLocalMode() {
		count, err := s.local.q.CountLocalAuthAdmins(ctx, orgID)
		if err != nil {
			return 0, fmt.Errorf("count local admins: %w", err)
		}
		return int(count), nil
	}
	org := orgID
	it := s.client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
		OrganizationID: &org,
	})
	count := 0
	for it.Next() {
		m := it.Current()
		if string(m.Status) == "active" && m.Role != nil && m.Role.Slug == "admin" {
			count++
		}
	}
	if err := it.Err(); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return count, nil
}
