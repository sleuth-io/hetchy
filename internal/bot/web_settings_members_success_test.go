package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sleuth-io/hetchy/internal/auth"
)

// fakeMemberDirectory is an in-memory memberDirectory that records calls and
// returns caller-configured results, letting the settings handlers exercise
// their success and error branches without a live WorkOS/local auth backend.
type fakeMemberDirectory struct {
	sendToken string
	sendErr   error
	revokeErr error
	removeErr error
	roleErr   error

	sentEmail    string
	sentOrg      string
	sentRole     string
	sentInviter  string
	revokedID    string
	revokedOrg   string
	removedID    string
	removedOrg   string
	removedActor string
	roleID       string
	roleOrg      string
	roleActor    string
	roleAssigned string
}

func (f *fakeMemberDirectory) SendInvitation(_ context.Context, email, orgID, roleSlug, inviterUserID string) (string, error) {
	f.sentEmail, f.sentOrg, f.sentRole, f.sentInviter = email, orgID, roleSlug, inviterUserID
	return f.sendToken, f.sendErr
}

func (f *fakeMemberDirectory) RevokeInvitation(_ context.Context, id, expectedOrgID string) error {
	f.revokedID, f.revokedOrg = id, expectedOrgID
	return f.revokeErr
}

func (f *fakeMemberDirectory) RemoveMember(_ context.Context, membershipID, expectedOrgID, callerUserID string) error {
	f.removedID, f.removedOrg, f.removedActor = membershipID, expectedOrgID, callerUserID
	return f.removeErr
}

func (f *fakeMemberDirectory) UpdateMemberRole(_ context.Context, membershipID, expectedOrgID, callerUserID, roleSlug string) error {
	f.roleID, f.roleOrg, f.roleActor, f.roleAssigned = membershipID, expectedOrgID, callerUserID, roleSlug
	return f.roleErr
}

func adminMemberRequest(method, target, body string) *http.Request {
	req := settingsFormRequest(method, target, body)
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user_admin",
		OrgID:  "org_test",
		Role:   "admin",
	}))
}

func TestInviteMemberFromSettings(t *testing.T) {
	setFlashOK := func(http.ResponseWriter, string) error { return nil }

	t.Run("workos success sets flash for returned token", func(t *testing.T) {
		dir := &fakeMemberDirectory{sendToken: "tok_abc"}
		var flashURL string
		setFlash := func(_ http.ResponseWriter, u string) error {
			flashURL = u
			return nil
		}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com&role=admin")
		inviteMemberFromSettings(rec, req, dir, "https://app.example.test", setFlash, discardLogger())
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=invited" {
			t.Fatalf("redirect = %q", got)
		}
		if dir.sentEmail != "new@example.com" || dir.sentRole != "admin" || dir.sentInviter != "user_admin" {
			t.Fatalf("send args = %+v", dir)
		}
		if flashURL != "https://app.example.test/signup?invite=tok_abc" {
			t.Fatalf("flash url = %q", flashURL)
		}
	})

	t.Run("empty token skips flash and defaults role to member", func(t *testing.T) {
		dir := &fakeMemberDirectory{sendToken: ""}
		flashed := false
		setFlash := func(http.ResponseWriter, string) error {
			flashed = true
			return nil
		}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com")
		inviteMemberFromSettings(rec, req, dir, "https://app.example.test", setFlash, discardLogger())
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		if flashed {
			t.Fatal("flash should not be set when no token returned")
		}
		if dir.sentRole != "member" {
			t.Fatalf("default role = %q want member", dir.sentRole)
		}
	})

	t.Run("send error surfaces 500", func(t *testing.T) {
		dir := &fakeMemberDirectory{sendErr: errors.New("workos down")}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com&role=member")
		inviteMemberFromSettings(rec, req, dir, "https://app.example.test", setFlashOK, discardLogger())
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d want 500", rec.Code)
		}
	})

	t.Run("flash error surfaces 500", func(t *testing.T) {
		dir := &fakeMemberDirectory{sendToken: "tok_abc"}
		setFlash := func(http.ResponseWriter, string) error { return errors.New("cookie boom") }
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invite", "email=new%40example.com&role=member")
		inviteMemberFromSettings(rec, req, dir, "https://app.example.test", setFlash, discardLogger())
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d want 500", rec.Code)
		}
	})

	t.Run("validation and authz guards", func(t *testing.T) {
		cases := []struct {
			name   string
			method string
			body   string
			role   string
			want   int
		}{
			{"non-post", http.MethodGet, "email=a%40b.com", "admin", http.StatusMethodNotAllowed},
			{"non-admin", http.MethodPost, "email=a%40b.com", "member", http.StatusForbidden},
			{"bad email", http.MethodPost, "email=nope", "admin", http.StatusBadRequest},
			{"unknown role", http.MethodPost, "email=a%40b.com&role=owner", "admin", http.StatusBadRequest},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				dir := &fakeMemberDirectory{}
				rec := httptest.NewRecorder()
				req := settingsFormRequest(tc.method, "/settings/org/invite", tc.body)
				req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
					UserID: "user_admin", OrgID: "org_test", Role: tc.role,
				}))
				inviteMemberFromSettings(rec, req, dir, "https://app.example.test", setFlashOK, discardLogger())
				if rec.Code != tc.want {
					t.Fatalf("status = %d want %d", rec.Code, tc.want)
				}
			})
		}
	})

	t.Run("cross-origin rejected", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invite", "email=a%40b.com&role=admin")
		req.Header.Set("Origin", "http://evil.example")
		inviteMemberFromSettings(rec, req, dir, "https://app.example.test", setFlashOK, discardLogger())
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d want 403", rec.Code)
		}
	})
}

func TestRevokeInvitationFromSettings(t *testing.T) {
	t.Run("success redirects", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invitations/inv_1/revoke", "")
		revokeInvitationFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/settings/org?tab=members&saved=revoked" {
			t.Fatalf("redirect = %q", got)
		}
		if dir.revokedID != "inv_1" || dir.revokedOrg != "org_test" {
			t.Fatalf("revoke args id=%q org=%q", dir.revokedID, dir.revokedOrg)
		}
	})

	t.Run("cross-org returns 404", func(t *testing.T) {
		dir := &fakeMemberDirectory{revokeErr: auth.ErrCrossOrg}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invitations/inv_1/revoke", "")
		revokeInvitationFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})

	t.Run("generic error returns 500", func(t *testing.T) {
		dir := &fakeMemberDirectory{revokeErr: errors.New("boom")}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invitations/inv_1/revoke", "")
		revokeInvitationFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d want 500", rec.Code)
		}
	})

	t.Run("unknown action returns 404", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/invitations/inv_1/delete", "")
		revokeInvitationFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})

	t.Run("guards", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/invitations/inv_1/revoke", "")
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Role: "member"}))
		revokeInvitationFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin status = %d want 403", rec.Code)
		}

		rec = httptest.NewRecorder()
		req = adminMemberRequest(http.MethodGet, "/settings/org/invitations/inv_1/revoke", "")
		revokeInvitationFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("non-post status = %d want 405", rec.Code)
		}
	})
}

func TestMemberActionFromSettings(t *testing.T) {
	t.Run("remove success", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/remove", "")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/settings/org?tab=members&saved=removed" {
			t.Fatalf("status=%d loc=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
		if dir.removedID != "mem_1" || dir.removedOrg != "org_test" || dir.removedActor != "user_admin" {
			t.Fatalf("remove args = %+v", dir)
		}
	})

	t.Run("remove cross-org 404", func(t *testing.T) {
		dir := &fakeMemberDirectory{removeErr: auth.ErrCrossOrg}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/remove", "")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})

	t.Run("remove generic error 400", func(t *testing.T) {
		dir := &fakeMemberDirectory{removeErr: errors.New("cannot remove self")}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/remove", "")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d want 400", rec.Code)
		}
	})

	t.Run("role success", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/role", "role=admin")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/settings/org?tab=members&saved=role" {
			t.Fatalf("status=%d loc=%q", rec.Code, rec.Header().Get("Location"))
		}
		if dir.roleID != "mem_1" || dir.roleAssigned != "admin" || dir.roleOrg != "org_test" || dir.roleActor != "user_admin" {
			t.Fatalf("role args = %+v", dir)
		}
	})

	t.Run("role cross-org 404", func(t *testing.T) {
		dir := &fakeMemberDirectory{roleErr: auth.ErrCrossOrg}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/role", "role=member")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d want 404", rec.Code)
		}
	})

	t.Run("role generic error 400", func(t *testing.T) {
		dir := &fakeMemberDirectory{roleErr: errors.New("last admin")}
		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/role", "role=member")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d want 400", rec.Code)
		}
	})

	t.Run("role validation", func(t *testing.T) {
		cases := []struct {
			name string
			body string
			want int
		}{
			{"empty role", "role=", http.StatusBadRequest},
			{"unknown role", "role=owner", http.StatusBadRequest},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				dir := &fakeMemberDirectory{}
				rec := httptest.NewRecorder()
				req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/role", tc.body)
				memberActionFromSettings(rec, req, dir, discardLogger())
				if rec.Code != tc.want {
					t.Fatalf("status = %d want %d", rec.Code, tc.want)
				}
			})
		}
	})

	t.Run("bad path and unknown action", func(t *testing.T) {
		dir := &fakeMemberDirectory{}

		rec := httptest.NewRecorder()
		req := adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1", "")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("bad path status = %d want 404", rec.Code)
		}

		rec = httptest.NewRecorder()
		req = adminMemberRequest(http.MethodPost, "/settings/org/members/mem_1/frobnicate", "")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown action status = %d want 404", rec.Code)
		}
	})

	t.Run("guards", func(t *testing.T) {
		dir := &fakeMemberDirectory{}
		rec := httptest.NewRecorder()
		req := settingsFormRequest(http.MethodPost, "/settings/org/members/mem_1/remove", "")
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Role: "member"}))
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin status = %d want 403", rec.Code)
		}

		rec = httptest.NewRecorder()
		req = adminMemberRequest(http.MethodGet, "/settings/org/members/mem_1/remove", "")
		memberActionFromSettings(rec, req, dir, discardLogger())
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("non-post status = %d want 405", rec.Code)
		}
	})
}
