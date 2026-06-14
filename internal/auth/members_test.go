package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

// --- pure helpers -----------------------------------------------------------

func TestFormatExpiry(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"rfc3339 to date", "2026-03-04T15:04:05Z", "2026-03-04"},
		{"rfc3339 with offset normalizes to utc", "2026-03-04T23:30:00+05:00", "2026-03-04"},
		{"rfc3339 offset crosses utc day boundary", "2026-03-04T02:00:00+05:00", "2026-03-03"},
		{"unparseable falls back to raw", "not-a-timestamp", "not-a-timestamp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatExpiry(tc.in); got != tc.want {
				t.Fatalf("formatExpiry(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name              string
		first, last, mail string
		want              string
	}{
		{"both names", "Ada", "Lovelace", "ada@example.com", "Ada Lovelace"},
		{"first only", "Ada", "", "ada@example.com", "Ada"},
		{"last only", "", "Lovelace", "ada@example.com", "Lovelace"},
		{"email fallback", "", "", "ada@example.com", "ada@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayName(tc.first, tc.last, tc.mail); got != tc.want {
				t.Fatalf("displayName(%q,%q,%q) = %q, want %q", tc.first, tc.last, tc.mail, got, tc.want)
			}
		})
	}
}

// TestProfileAndMemberDisplayName exercises the exported DisplayName methods
// so both wrappers around displayName are covered.
func TestProfileAndMemberDisplayName(t *testing.T) {
	p := Profile{FirstName: "Grace", LastName: "Hopper", Email: "grace@example.com"}
	if got := p.DisplayName(); got != "Grace Hopper" {
		t.Fatalf("Profile.DisplayName() = %q, want Grace Hopper", got)
	}
	m := Member{Email: "anon@example.com"}
	if got := m.DisplayName(); got != "anon@example.com" {
		t.Fatalf("Member.DisplayName() = %q, want anon@example.com", got)
	}
}

func TestCountAdmins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/organization_memberships" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("organization_id"); got != "org_x" {
			t.Fatalf("organization_id = %q, want org_x", got)
		}
		writeMembershipsPage(w, []map[string]any{
			membershipRow("om_admin1", "user_a", "org_x", "active", "admin"),
			membershipRow("om_admin2", "user_b", "org_x", "active", "admin"),
			// Inactive admin must not be counted.
			membershipRow("om_admin3", "user_c", "org_x", "inactive", "admin"),
			// Active non-admin must not be counted.
			membershipRow("om_member", "user_d", "org_x", "active", "member"),
		})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	count, err := s.countAdmins(context.Background(), "org_x")
	if err != nil {
		t.Fatalf("countAdmins: %v", err)
	}
	if count != 2 {
		t.Fatalf("countAdmins = %d, want 2", count)
	}
}

// TestCountAdminsPaginates proves countAdmins sums admins across pages,
// not just the first. The single-page happy-path test would pass even if
// the iterator loop stopped after page one; this one wouldn't.
func TestCountAdminsPaginates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/organization_memberships" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		// Page 1 carries an `after` cursor; page 2 (requested with that
		// cursor) closes the list. One active admin on each page.
		if r.URL.Query().Get("after") == "" {
			writeJSON(w, map[string]any{
				"data":          []map[string]any{membershipRow("om_a", "user_a", "org_x", "active", "admin")},
				"list_metadata": map[string]any{"before": nil, "after": "cursor_page2"},
			})
			return
		}
		writeJSON(w, map[string]any{
			"data":          []map[string]any{membershipRow("om_b", "user_b", "org_x", "active", "admin")},
			"list_metadata": map[string]any{"before": nil, "after": nil},
		})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	count, err := s.countAdmins(context.Background(), "org_x")
	if err != nil {
		t.Fatalf("countAdmins: %v", err)
	}
	if count != 2 {
		t.Fatalf("countAdmins across two pages = %d, want 2", count)
	}
}

func TestCountAdminsPropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if _, err := s.countAdmins(context.Background(), "org_x"); err == nil {
		t.Fatal("expected error from countAdmins on WorkOS failure")
	}
}

func TestResolveOrgMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user_management/organization_memberships/om_match":
			writeJSON(w, membershipRow("om_match", "user_a", "org_x", "active", "admin"))
		case "/user_management/organization_memberships/om_other":
			writeJSON(w, membershipRow("om_other", "user_b", "org_y", "active", "member"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}

	t.Run("same org", func(t *testing.T) {
		m, err := s.resolveOrgMember(context.Background(), "om_match", "org_x")
		if err != nil {
			t.Fatalf("resolveOrgMember: %v", err)
		}
		if m.ID != "om_match" {
			t.Fatalf("membership id = %q, want om_match", m.ID)
		}
	})

	t.Run("cross org", func(t *testing.T) {
		_, err := s.resolveOrgMember(context.Background(), "om_other", "org_x")
		if !errors.Is(err, ErrCrossOrg) {
			t.Fatalf("err = %v, want ErrCrossOrg", err)
		}
	})

	t.Run("get fails", func(t *testing.T) {
		_, err := s.resolveOrgMember(context.Background(), "om_missing", "org_x")
		if err == nil || !strings.Contains(err.Error(), "get membership") {
			t.Fatalf("err = %v, want get membership wrap", err)
		}
	})
}

// --- profile / password reset ----------------------------------------------

func TestGetProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/users/user_1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(w, userRow("user_1", "ada@example.com", "Ada", "Lovelace"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	p, err := s.GetProfile(context.Background(), "user_1")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.Email != "ada@example.com" || p.FirstName != "Ada" || p.LastName != "Lovelace" {
		t.Fatalf("profile = %+v", p)
	}
}

func TestGetProfileError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if _, err := s.GetProfile(context.Background(), "user_1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetProfileBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true, BypassEmail: "bypass@example.com"}}
	p, err := s.GetProfile(context.Background(), "user_b")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.UserID != "user_b" || p.Email != "bypass@example.com" {
		t.Fatalf("bypass profile = %+v", p)
	}
}

func TestUpdateProfile(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/user_management/users/user_1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, userRow("user_1", "ada@example.com", "Grace", "Hopper"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.UpdateProfile(context.Background(), "user_1", "Grace", "Hopper"); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if gotBody["first_name"] != "Grace" || gotBody["last_name"] != "Hopper" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestUpdateProfileBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	if err := s.UpdateProfile(context.Background(), "u", "a", "b"); err != nil {
		t.Fatalf("bypass UpdateProfile: %v", err)
	}
}

func TestRequestPasswordReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user_management/password_reset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, map[string]any{
			"object":             "password_reset",
			"id":                 "pr_1",
			"user_id":            "user_1",
			"email":              "ada@example.com",
			"expires_at":         "2026-03-04T15:04:05Z",
			"created_at":         "2026-03-04T14:04:05Z",
			"password_reset_url": "https://authkit.example.com/reset/abc",
		})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	url, err := s.RequestPasswordReset(context.Background(), "ada@example.com")
	if err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if url != "https://authkit.example.com/reset/abc" {
		t.Fatalf("reset url = %q", url)
	}
}

func TestRequestPasswordResetBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	url, err := s.RequestPasswordReset(context.Background(), "x@example.com")
	if err != nil || url != "/" {
		t.Fatalf("bypass reset = %q, %v", url, err)
	}
}

func TestRequestPasswordResetError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if _, err := s.RequestPasswordReset(context.Background(), "x@example.com"); err == nil {
		t.Fatal("expected error")
	}
}

// --- members ----------------------------------------------------------------

func TestListMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user_management/organization_memberships":
			writeMembershipsPage(w, []map[string]any{
				membershipRow("om_a", "user_a", "org_x", "active", "admin"),
				membershipRow("om_b", "user_b", "org_x", "pending", "member"),
			})
		case "/user_management/users/user_a":
			writeJSON(w, userRow("user_a", "ada@example.com", "Ada", "Lovelace"))
		case "/user_management/users/user_b":
			// Simulate a transient per-user GET failure -> placeholder email.
			http.Error(w, "rate limited", http.StatusBadRequest)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	members, err := s.ListMembers(context.Background(), "org_x")
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	if members[0].Email != "ada@example.com" || members[0].RoleSlug != "admin" || members[0].Status != "active" {
		t.Fatalf("members[0] = %+v", members[0])
	}
	if !strings.Contains(members[1].Email, "unavailable") {
		t.Fatalf("members[1] email = %q, want placeholder", members[1].Email)
	}
}

func TestListMembersBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true, BypassUser: "u", BypassEmail: "b@e.com", BypassRole: "admin"}}
	members, err := s.ListMembers(context.Background(), "org_x")
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 1 || members[0].RoleSlug != "admin" {
		t.Fatalf("bypass members = %+v", members)
	}
}

func TestListMembersListError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if _, err := s.ListMembers(context.Background(), "org_x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFindOrgUserByEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/users" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("email"); got != "ada@example.com" {
			t.Fatalf("email filter = %q", got)
		}
		writeListPage(w, []map[string]any{userRow("user_a", "ada@example.com", "Ada", "Lovelace")})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	p, ok, err := s.FindOrgUserByEmail(context.Background(), "org_x", "ada@example.com")
	if err != nil {
		t.Fatalf("FindOrgUserByEmail: %v", err)
	}
	if !ok || p.UserID != "user_a" || p.FirstName != "Ada" {
		t.Fatalf("found = %v, %+v", ok, p)
	}
}

func TestFindOrgUserByEmailNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeListPage(w, nil)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	_, ok, err := s.FindOrgUserByEmail(context.Background(), "org_x", "nobody@example.com")
	if err != nil {
		t.Fatalf("FindOrgUserByEmail: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for no match")
	}
}

func TestFindOrgUserByEmailEmptyAndBypass(t *testing.T) {
	s := &Service{}
	if _, ok, err := s.FindOrgUserByEmail(context.Background(), "org_x", ""); ok || err != nil {
		t.Fatalf("empty email should be no-op, got ok=%v err=%v", ok, err)
	}

	b := &Service{cfg: Config{Bypass: true, BypassUser: "u", BypassEmail: "b@e.com"}}
	p, ok, err := b.FindOrgUserByEmail(context.Background(), "org_x", "b@e.com")
	if err != nil || !ok || p.UserID != "u" {
		t.Fatalf("bypass match = %v %+v %v", ok, p, err)
	}
	if _, ok, _ := b.FindOrgUserByEmail(context.Background(), "org_x", "other@e.com"); ok {
		t.Fatal("bypass non-matching email should be ok=false")
	}
}

// --- invitations ------------------------------------------------------------

func TestListInvitations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user_management/invitations" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeListPage(w, []map[string]any{
			inviteRow("inv_pending", "p@example.com", "pending", "member"),
			// Accepted invitations are filtered out by ListInvitations.
			inviteRow("inv_accepted", "a@example.com", "accepted", "admin"),
		})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	invites, err := s.ListInvitations(context.Background(), "org_x")
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("got %d invites, want 1 pending", len(invites))
	}
	if invites[0].ID != "inv_pending" || invites[0].RoleSlug != "member" {
		t.Fatalf("invite = %+v", invites[0])
	}
	if invites[0].ExpiresAt != "2026-03-04" {
		t.Fatalf("expires = %q, want formatted date", invites[0].ExpiresAt)
	}
}

func TestListInvitationsBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	invites, err := s.ListInvitations(context.Background(), "org_x")
	if err != nil || invites != nil {
		t.Fatalf("bypass invites = %+v, %v", invites, err)
	}
}

func TestSendInvitation(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user_management/invitations" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, inviteRow("inv_new", "new@example.com", "pending", "member"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.SendInvitation(context.Background(), "new@example.com", "org_x", "member", "user_inviter"); err != nil {
		t.Fatalf("SendInvitation: %v", err)
	}
	if gotBody["email"] != "new@example.com" || gotBody["organization_id"] != "org_x" ||
		gotBody["role_slug"] != "member" || gotBody["inviter_user_id"] != "user_inviter" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestSendInvitationBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	if err := s.SendInvitation(context.Background(), "a@e.com", "org_x", "member", "u"); err != nil {
		t.Fatalf("bypass SendInvitation: %v", err)
	}
}

func TestRevokeInvitation(t *testing.T) {
	var revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user_management/invitations/inv_x" && r.Method == http.MethodGet:
			writeJSON(w, inviteRow("inv_x", "x@example.com", "pending", "member"))
		case r.URL.Path == "/user_management/invitations/inv_x/revoke" && r.Method == http.MethodPost:
			revoked = true
			writeJSON(w, inviteRow("inv_x", "x@example.com", "revoked", "member"))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.RevokeInvitation(context.Background(), "inv_x", "org_x"); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}
	if !revoked {
		t.Fatal("expected revoke endpoint to be hit")
	}
}

func TestRevokeInvitationCrossOrg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			t.Fatal("revoke must not be called when org mismatches")
		}
		writeJSON(w, inviteRow("inv_x", "x@example.com", "pending", "member"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	// inviteRow sets organization_id to org_x; expect ErrCrossOrg for org_y.
	if err := s.RevokeInvitation(context.Background(), "inv_x", "org_y"); !errors.Is(err, ErrCrossOrg) {
		t.Fatalf("err = %v, want ErrCrossOrg", err)
	}
}

func TestRevokeInvitationGetError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.RevokeInvitation(context.Background(), "inv_x", "org_x"); err == nil ||
		!strings.Contains(err.Error(), "get invitation") {
		t.Fatalf("err = %v, want get invitation wrap", err)
	}
}

func TestRevokeInvitationBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	if err := s.RevokeInvitation(context.Background(), "inv_x", "org_x"); err != nil {
		t.Fatalf("bypass RevokeInvitation: %v", err)
	}
}

// --- remove / update member -------------------------------------------------

func TestRemoveMember(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user_management/organization_memberships/om_target" && r.Method == http.MethodGet:
			writeJSON(w, membershipRow("om_target", "user_target", "org_x", "active", "member"))
		case r.URL.Path == "/user_management/organization_memberships/om_target" && r.Method == http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.RemoveMember(context.Background(), "om_target", "org_x", "user_caller"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if !deleted {
		t.Fatal("expected delete endpoint to be hit")
	}
}

func TestRemoveMemberSelf(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, membershipRow("om_self", "user_caller", "org_x", "active", "member"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	err := s.RemoveMember(context.Background(), "om_self", "org_x", "user_caller")
	if err == nil || !strings.Contains(err.Error(), "your own membership") {
		t.Fatalf("err = %v, want self-removal guard", err)
	}
}

func TestRemoveMemberLastAdmin(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
		}
		switch r.URL.Path {
		case "/user_management/organization_memberships/om_admin":
			writeJSON(w, membershipRow("om_admin", "user_target", "org_x", "active", "admin"))
		case "/user_management/organization_memberships":
			// Only one active admin -> guard trips.
			writeMembershipsPage(w, []map[string]any{
				membershipRow("om_admin", "user_target", "org_x", "active", "admin"),
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	err := s.RemoveMember(context.Background(), "om_admin", "org_x", "user_caller")
	if err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("err = %v, want last-admin guard", err)
	}
	// The guard must block before the destructive call — assert the
	// DELETE never reached WorkOS, so the test fails if the guard is
	// removed rather than relying on the mock's incidental response.
	if deleted {
		t.Fatal("last-admin guard tripped but DeleteOrganizationMembership was still called")
	}
}

func TestRemoveMemberCrossOrg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, membershipRow("om_x", "user_x", "org_other", "active", "member"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.RemoveMember(context.Background(), "om_x", "org_x", "user_caller"); !errors.Is(err, ErrCrossOrg) {
		t.Fatalf("err = %v, want ErrCrossOrg", err)
	}
}

func TestRemoveMemberBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	if err := s.RemoveMember(context.Background(), "om", "org_x", "u"); err != nil {
		t.Fatalf("bypass RemoveMember: %v", err)
	}
}

func TestUpdateMemberRole(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user_management/organization_memberships/om_target" && r.Method == http.MethodGet:
			writeJSON(w, membershipRow("om_target", "user_target", "org_x", "active", "member"))
		case r.URL.Path == "/user_management/organization_memberships/om_target" && r.Method == http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, membershipRow("om_target", "user_target", "org_x", "active", "admin"))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.UpdateMemberRole(context.Background(), "om_target", "org_x", "user_caller", "admin"); err != nil {
		t.Fatalf("UpdateMemberRole: %v", err)
	}
	if gotBody["role_slug"] != "admin" {
		t.Fatalf("body = %#v, want role_slug admin", gotBody)
	}
}

func TestUpdateMemberRoleSelf(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, membershipRow("om_self", "user_caller", "org_x", "active", "admin"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	err := s.UpdateMemberRole(context.Background(), "om_self", "org_x", "user_caller", "member")
	if err == nil || !strings.Contains(err.Error(), "your own role") {
		t.Fatalf("err = %v, want self-role guard", err)
	}
}

func TestUpdateMemberRoleDemoteLastAdmin(t *testing.T) {
	var mutated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			mutated = true
		}
		switch r.URL.Path {
		case "/user_management/organization_memberships/om_admin":
			writeJSON(w, membershipRow("om_admin", "user_target", "org_x", "active", "admin"))
		case "/user_management/organization_memberships":
			writeMembershipsPage(w, []map[string]any{
				membershipRow("om_admin", "user_target", "org_x", "active", "admin"),
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	err := s.UpdateMemberRole(context.Background(), "om_admin", "org_x", "user_caller", "member")
	if err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("err = %v, want last-admin demote guard", err)
	}
	// Assert the demotion never reached WorkOS, so removing the guard
	// fails this test instead of silently demoting the last admin.
	if mutated {
		t.Fatal("last-admin demote guard tripped but UpdateOrganizationMembership was still called")
	}
}

func TestUpdateMemberRoleBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	if err := s.UpdateMemberRole(context.Background(), "om", "org_x", "u", "admin"); err != nil {
		t.Fatalf("bypass UpdateMemberRole: %v", err)
	}
}

// --- organizations ----------------------------------------------------------

func TestCreateOrganization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/organizations" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, map[string]any{"object": "organization", "id": "org_new", "name": "Acme"})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	id, err := s.CreateOrganization(context.Background(), "Acme")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if id != "org_new" {
		t.Fatalf("id = %q, want org_new", id)
	}
}

func TestCreateOrganizationBypassAndError(t *testing.T) {
	b := &Service{cfg: Config{Bypass: true}}
	if id, err := b.CreateOrganization(context.Background(), "Acme"); err != nil || id != "org_bypass" {
		t.Fatalf("bypass create = %q, %v", id, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()
	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if _, err := s.CreateOrganization(context.Background(), "Acme"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetOrganizationName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/org_x" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(w, map[string]any{"object": "organization", "id": "org_x", "name": "Acme Corp"})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	name, err := s.GetOrganizationName(context.Background(), "org_x")
	if err != nil {
		t.Fatalf("GetOrganizationName: %v", err)
	}
	if name != "Acme Corp" {
		t.Fatalf("name = %q", name)
	}
}

func TestGetOrganizationNameBypassAndError(t *testing.T) {
	b := &Service{cfg: Config{Bypass: true}}
	if name, err := b.GetOrganizationName(context.Background(), "org_x"); err != nil || name != "org_x" {
		t.Fatalf("bypass name = %q, %v", name, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer server.Close()
	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if _, err := s.GetOrganizationName(context.Background(), "org_x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAddUserToOrganization(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user_management/organization_memberships" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, membershipRow("om_new", "user_a", "org_x", "active", "admin"))
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	if err := s.AddUserToOrganization(context.Background(), "user_a", "org_x", "admin"); err != nil {
		t.Fatalf("AddUserToOrganization: %v", err)
	}
	if gotBody["user_id"] != "user_a" || gotBody["organization_id"] != "org_x" || gotBody["role_slug"] != "admin" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestAddUserToOrganizationBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	if err := s.AddUserToOrganization(context.Background(), "u", "org_x", "admin"); err != nil {
		t.Fatalf("bypass AddUserToOrganization: %v", err)
	}
}

func TestOrganizationHasFeatureFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/org_x/feature-flags" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeListPage(w, []map[string]any{
			{"object": "feature_flag", "id": "ff_1", "slug": "other", "name": "Other", "enabled": true, "default_value": false, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
			{"object": "feature_flag", "id": "ff_2", "slug": "beta", "name": "Beta", "enabled": true, "default_value": false, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
		})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	on, err := s.OrganizationHasFeatureFlag(context.Background(), "org_x", "beta")
	if err != nil {
		t.Fatalf("OrganizationHasFeatureFlag: %v", err)
	}
	if !on {
		t.Fatal("expected beta flag to be enabled")
	}

	off, err := s.OrganizationHasFeatureFlag(context.Background(), "org_x", "missing")
	if err != nil {
		t.Fatalf("OrganizationHasFeatureFlag: %v", err)
	}
	if off {
		t.Fatal("expected missing flag to be off")
	}
}

func TestOrganizationHasFeatureFlagShortCircuits(t *testing.T) {
	// Bypass, nil client, blank org, and blank slug must all return false
	// without a WorkOS round-trip.
	cases := []struct {
		name string
		svc  *Service
		org  string
		slug string
	}{
		{"bypass", &Service{cfg: Config{Bypass: true}}, "org_x", "beta"},
		{"nil client", &Service{}, "org_x", "beta"},
		{"blank org", &Service{client: workos.NewClient("sk_test")}, "  ", "beta"},
		{"blank slug", &Service{client: workos.NewClient("sk_test")}, "org_x", "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			on, err := tc.svc.OrganizationHasFeatureFlag(context.Background(), tc.org, tc.slug)
			if err != nil || on {
				t.Fatalf("expected (false, nil), got (%v, %v)", on, err)
			}
		})
	}
}

// --- signup / redirect / middleware ----------------------------------------

func TestSignupHandlerRedirectsToAuthKit(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")
	s.cfg.RedirectURI = "https://app.example.com/callback"
	s.client = workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL("https://api.workos.test"))

	req := httptest.NewRequest(http.MethodGet, "/signup", nil)
	rec := httptest.NewRecorder()
	s.SignupHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://api.workos.test/user_management/authorize?") {
		t.Fatalf("unexpected redirect target: %s", loc)
	}
	if !strings.Contains(loc, "screen_hint=sign-up") {
		t.Fatalf("expected sign-up screen hint, got %s", loc)
	}
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("expected oauth state cookie to be set")
	}
}

func TestSignupHandlerBypassRedirectsHome(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}, statePath: "/"}
	req := httptest.NewRequest(http.MethodGet, "/signup", nil)
	rec := httptest.NewRecorder()
	s.SignupHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("bypass signup redirect = %q, want /", loc)
	}
}

func TestMiddlewareBypassFabricatesPrincipal(t *testing.T) {
	s := &Service{cfg: Config{
		Bypass: true, BypassUser: "user_b", BypassEmail: "b@e.com",
		BypassOrg: "org_b", BypassRole: "admin",
	}}
	var got Principal
	var ok bool
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = FromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !ok {
		t.Fatal("expected principal on bypass")
	}
	if got.UserID != "user_b" || got.OrgID != "org_b" || got.Role != "admin" || got.SessionID != "bypass" {
		t.Fatalf("principal = %+v", got)
	}
}

func TestMiddlewareNoCookiePassesThrough(t *testing.T) {
	s := &Service{cfg: Config{CookiePassword: "test-cookie-password-keep-it-long"}}
	var ok bool
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok = FromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if ok {
		t.Fatal("expected no principal without a session cookie")
	}
}

func TestMiddlewareInvalidCookieClearsAndPassesThrough(t *testing.T) {
	s := &Service{cfg: Config{CookiePassword: "test-cookie-password-keep-it-long"}}
	var ok bool
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok = FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "not-a-real-sealed-session"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if ok {
		t.Fatal("expected no principal for invalid cookie")
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("expected invalid session cookie to be cleared")
	}
}

func TestMiddlewareValidCookieAttachesPrincipal(t *testing.T) {
	const password = "test-cookie-password-keep-it-long"
	s := &Service{cfg: Config{CookiePassword: password}}

	sealed := sealTestSession(t, password, jwtClaims{Sid: "sess_1", OrgID: "org_x", Role: "admin"},
		&workos.User{ID: "user_1", Email: "ada@example.com"})

	var got Principal
	var ok bool
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sealed})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("expected principal for a valid session cookie")
	}
	if got.SessionID != "sess_1" || got.OrgID != "org_x" || got.Role != "admin" {
		t.Fatalf("principal claims = %+v", got)
	}
	if got.UserID != "user_1" || got.Email != "ada@example.com" {
		t.Fatalf("principal user = %+v", got)
	}
}

func TestSwitchOrg(t *testing.T) {
	const password = "test-cookie-password-keep-it-long"
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user_management/authenticate" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, map[string]any{
			"user":            map[string]any{"object": "user", "id": "user_1", "email": "ada@example.com"},
			"organization_id": "org_target",
			"access_token":    fakeAccessToken(t, jwtClaims{Sid: "sess_2", OrgID: "org_target", Role: "member"}),
			"refresh_token":   "refresh_tok_2",
		})
	}))
	defer server.Close()

	s := &Service{
		cfg:    Config{CookiePassword: password},
		client: workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL)),
	}
	sealed := sealTestSession(t, password, jwtClaims{Sid: "sess_1", OrgID: "org_x", Role: "admin"},
		&workos.User{ID: "user_1", Email: "ada@example.com"})

	req := httptest.NewRequest(http.MethodGet, "/switch", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sealed})
	rec := httptest.NewRecorder()
	if err := s.SwitchOrg(rec, req, "org_target"); err != nil {
		t.Fatalf("SwitchOrg: %v", err)
	}
	if gotBody["grant_type"] != "refresh_token" || gotBody["organization_id"] != "org_target" {
		t.Fatalf("body = %#v", gotBody)
	}
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("expected a re-issued session cookie")
	}
}

func TestSwitchOrgBypass(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}
	req := httptest.NewRequest(http.MethodGet, "/switch", nil)
	if err := s.SwitchOrg(httptest.NewRecorder(), req, "org_x"); err != nil {
		t.Fatalf("bypass SwitchOrg: %v", err)
	}
}

func TestSwitchOrgMissingCookie(t *testing.T) {
	s := &Service{cfg: Config{CookiePassword: "test-cookie-password-keep-it-long"}}
	req := httptest.NewRequest(http.MethodGet, "/switch", nil)
	if err := s.SwitchOrg(httptest.NewRecorder(), req, "org_x"); err == nil {
		t.Fatal("expected error when session cookie is absent")
	}
}

func TestSwitchOrgRefreshError(t *testing.T) {
	const password = "test-cookie-password-keep-it-long"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusUnauthorized)
	}))
	defer server.Close()

	s := &Service{
		cfg:    Config{CookiePassword: password},
		client: workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL)),
	}
	sealed := sealTestSession(t, password, jwtClaims{Sid: "sess_1", OrgID: "org_x", Role: "admin"},
		&workos.User{ID: "user_1", Email: "ada@example.com"})
	req := httptest.NewRequest(http.MethodGet, "/switch", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sealed})
	if err := s.SwitchOrg(httptest.NewRecorder(), req, "org_target"); err == nil {
		t.Fatal("expected error when WorkOS refresh fails")
	}
}

// --- test helpers -----------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeListPage(w http.ResponseWriter, rows []map[string]any) {
	writeJSON(w, map[string]any{
		"data":          rows,
		"list_metadata": map[string]any{"before": nil, "after": nil},
	})
}

// writeMembershipsPage mirrors writeListPage but is named for the membership
// endpoints to keep the test bodies readable.
func writeMembershipsPage(w http.ResponseWriter, rows []map[string]any) {
	writeListPage(w, rows)
}

func membershipRow(id, userID, orgID, status, role string) map[string]any {
	return map[string]any{
		"object":            "organization_membership",
		"id":                id,
		"user_id":           userID,
		"organization_id":   orgID,
		"status":            status,
		"directory_managed": false,
		"created_at":        "2026-01-15T12:00:00.000Z",
		"updated_at":        "2026-01-15T12:00:00.000Z",
		"role":              map[string]any{"slug": role},
	}
}

func userRow(id, email, first, last string) map[string]any {
	return map[string]any{
		"object":     "user",
		"id":         id,
		"email":      email,
		"first_name": first,
		"last_name":  last,
		"created_at": "2026-01-15T12:00:00.000Z",
		"updated_at": "2026-01-15T12:00:00.000Z",
	}
}

func inviteRow(id, email, state, role string) map[string]any {
	org := "org_x"
	return map[string]any{
		"object":          "invitation",
		"id":              id,
		"email":           email,
		"state":           state,
		"expires_at":      "2026-03-04T15:04:05Z",
		"organization_id": org,
		"role_slug":       role,
		"created_at":      "2026-01-15T12:00:00.000Z",
		"updated_at":      "2026-01-15T12:00:00.000Z",
		"token":           "tok_" + id,
	}
}

// jwtClaims is the minimal claim set the WorkOS session helper reads off the
// access token when authenticating a sealed cookie.
type jwtClaims struct {
	Sid   string `json:"sid"`
	OrgID string `json:"org_id"`
	Role  string `json:"role"`
}

// fakeAccessToken builds an unsigned JWT (header.payload.signature) whose
// payload carries the given claims. The WorkOS session helper does not verify
// the inner JWT signature on unseal — it only base64url-decodes the payload —
// so a fabricated token is sufficient to exercise the Middleware authed path.
//
// This is not a Middleware weakness: the trust boundary is the session
// cookie's AES-GCM seal (keyed by CookiePassword, applied by
// SealSessionFromAuthResponse). An attacker can't produce a cookie that
// unseals without that key, so the inner JWT — already vouched for by WorkOS
// at login and sealed in — is not re-verified. A test in this package can
// forge the inner token precisely because it owns the sealing key.
func fakeAccessToken(t *testing.T, claims jwtClaims) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt part: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := enc(map[string]string{"alg": "none", "typ": "JWT"})
	payload := enc(claims)
	return header + "." + payload + ".sig"
}

// sealTestSession produces a sealed session cookie value equivalent to what
// CallbackHandler writes, so Middleware's AuthenticateSession accepts it.
func sealTestSession(t *testing.T, password string, claims jwtClaims, user *workos.User) string {
	t.Helper()
	sealed, err := workos.SealSessionFromAuthResponse(
		fakeAccessToken(t, claims), "refresh_tok", user, nil, password)
	if err != nil {
		t.Fatalf("seal session: %v", err)
	}
	return sealed
}
