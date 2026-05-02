package auth

import (
	"context"
	"errors"
	"fmt"

	workos "github.com/workos/workos-go/v7"
)

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
	switch {
	case p.FirstName != "" && p.LastName != "":
		return p.FirstName + " " + p.LastName
	case p.FirstName != "":
		return p.FirstName
	case p.LastName != "":
		return p.LastName
	default:
		return p.Email
	}
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
	switch {
	case m.FirstName != "" && m.LastName != "":
		return m.FirstName + " " + m.LastName
	case m.FirstName != "":
		return m.FirstName
	case m.LastName != "":
		return m.LastName
	default:
		return m.Email
	}
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

// UpdateProfile rewrites the first and last name on a user. Email/
// password/MFA are *not* mutable here — those flows go through AuthKit's
// hosted pages so we don't have to reimplement password policy / MFA UX.
func (s *Service) UpdateProfile(ctx context.Context, userID, firstName, lastName string) error {
	if s.cfg.Bypass {
		return nil
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

// ListInvitations returns invitations on orgID that are still pending
// (not yet accepted/revoked/expired). Accepted invitations show up as
// active memberships in ListMembers, so surfacing them here would
// double-list users.
func (s *Service) ListInvitations(ctx context.Context, orgID string) ([]Invitation, error) {
	if s.cfg.Bypass {
		return nil, nil
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
		i := Invitation{ID: inv.ID, Email: inv.Email, ExpiresAt: inv.ExpiresAt}
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
func (s *Service) SendInvitation(ctx context.Context, email, orgID, roleSlug, inviterUserID string) error {
	if s.cfg.Bypass {
		return nil
	}
	org, role, inv := orgID, roleSlug, inviterUserID
	_, err := s.client.UserManagement().SendInvitation(ctx, &workos.UserManagementSendInvitationParams{
		Email:          email,
		OrganizationID: &org,
		RoleSlug:       &role,
		InviterUserID:  &inv,
	})
	return err
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
func (s *Service) countAdmins(ctx context.Context, orgID string) (int, error) {
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
