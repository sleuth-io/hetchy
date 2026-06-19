package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

type localAuthMemoryQuerier struct {
	sqlc.Querier

	now         time.Time
	users       map[string]sqlc.LocalAuthUser
	usersByMail map[string]string
	orgs        map[string]sqlc.LocalAuthOrg
	memberships map[string]sqlc.LocalAuthMembership
	sessions    map[string]sqlc.LocalAuthSession
	invitations map[string]sqlc.LocalAuthInvitation
}

func newLocalAuthMemoryQuerier(now time.Time) *localAuthMemoryQuerier {
	return &localAuthMemoryQuerier{
		now:         now,
		users:       map[string]sqlc.LocalAuthUser{},
		usersByMail: map[string]string{},
		orgs:        map[string]sqlc.LocalAuthOrg{},
		memberships: map[string]sqlc.LocalAuthMembership{},
		sessions:    map[string]sqlc.LocalAuthSession{},
		invitations: map[string]sqlc.LocalAuthInvitation{},
	}
}

func newLocalAuthFlowService(t *testing.T) (*Service, *localAuthMemoryQuerier) {
	t.Helper()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	q := newLocalAuthMemoryQuerier(now)
	s, err := New(Config{Mode: AuthModeLocal, LocalQueries: q})
	if err != nil {
		t.Fatalf("New local auth service: %v", err)
	}
	s.now = func() time.Time { return now }
	return s, q
}

func localAuthTS(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func localAuthUniqueViolation() error {
	return &pgconn.PgError{Code: "23505", ConstraintName: "local_auth_users_email_normalized_key"}
}

func (q *localAuthMemoryQuerier) CreateLocalAuthUser(_ context.Context, arg sqlc.CreateLocalAuthUserParams) (sqlc.LocalAuthUser, error) {
	if _, exists := q.usersByMail[arg.EmailNormalized]; exists {
		return sqlc.LocalAuthUser{}, localAuthUniqueViolation()
	}
	row := sqlc.LocalAuthUser{
		ID:              arg.ID,
		Email:           arg.Email,
		EmailNormalized: arg.EmailNormalized,
		PasswordHash:    arg.PasswordHash,
		FirstName:       arg.FirstName,
		LastName:        arg.LastName,
		CreatedAt:       localAuthTS(q.now),
		UpdatedAt:       localAuthTS(q.now),
	}
	q.users[row.ID] = row
	q.usersByMail[row.EmailNormalized] = row.ID
	return row, nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthUserByEmail(_ context.Context, emailNormalized string) (sqlc.LocalAuthUser, error) {
	id, ok := q.usersByMail[emailNormalized]
	if !ok {
		return sqlc.LocalAuthUser{}, pgx.ErrNoRows
	}
	return q.users[id], nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthUserByID(_ context.Context, id string) (sqlc.LocalAuthUser, error) {
	user, ok := q.users[id]
	if !ok {
		return sqlc.LocalAuthUser{}, pgx.ErrNoRows
	}
	return user, nil
}

func (q *localAuthMemoryQuerier) UpdateLocalAuthUserPassword(_ context.Context, arg sqlc.UpdateLocalAuthUserPasswordParams) error {
	user, ok := q.users[arg.ID]
	if !ok {
		return pgx.ErrNoRows
	}
	user.PasswordHash = arg.PasswordHash
	user.UpdatedAt = localAuthTS(q.now)
	q.users[arg.ID] = user
	return nil
}

func (q *localAuthMemoryQuerier) UpdateLocalAuthUserProfile(_ context.Context, arg sqlc.UpdateLocalAuthUserProfileParams) (sqlc.LocalAuthUser, error) {
	user, ok := q.users[arg.ID]
	if !ok {
		return sqlc.LocalAuthUser{}, pgx.ErrNoRows
	}
	user.FirstName = arg.FirstName
	user.LastName = arg.LastName
	user.UpdatedAt = localAuthTS(q.now)
	q.users[arg.ID] = user
	return user, nil
}

func (q *localAuthMemoryQuerier) DeleteLocalAuthUser(_ context.Context, id string) error {
	user, ok := q.users[id]
	if !ok {
		return pgx.ErrNoRows
	}
	delete(q.usersByMail, user.EmailNormalized)
	delete(q.users, id)
	for membershipID, membership := range q.memberships {
		if membership.UserID == id {
			delete(q.memberships, membershipID)
		}
	}
	for sessionID, session := range q.sessions {
		if session.UserID == id {
			delete(q.sessions, sessionID)
		}
	}
	return nil
}

func (q *localAuthMemoryQuerier) CreateLocalAuthOrg(_ context.Context, arg sqlc.CreateLocalAuthOrgParams) (sqlc.LocalAuthOrg, error) {
	row := sqlc.LocalAuthOrg{ID: arg.ID, Name: arg.Name, CreatedAt: localAuthTS(q.now), UpdatedAt: localAuthTS(q.now)}
	q.orgs[row.ID] = row
	return row, nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthOrg(_ context.Context, id string) (sqlc.LocalAuthOrg, error) {
	org, ok := q.orgs[id]
	if !ok {
		return sqlc.LocalAuthOrg{}, pgx.ErrNoRows
	}
	return org, nil
}

func (q *localAuthMemoryQuerier) UpdateLocalAuthOrgName(_ context.Context, arg sqlc.UpdateLocalAuthOrgNameParams) (sqlc.LocalAuthOrg, error) {
	org, ok := q.orgs[arg.ID]
	if !ok {
		return sqlc.LocalAuthOrg{}, pgx.ErrNoRows
	}
	org.Name = arg.Name
	org.UpdatedAt = localAuthTS(q.now)
	q.orgs[arg.ID] = org
	return org, nil
}

func (q *localAuthMemoryQuerier) DeleteLocalAuthOrg(_ context.Context, id string) error {
	if _, ok := q.orgs[id]; !ok {
		return pgx.ErrNoRows
	}
	delete(q.orgs, id)
	return nil
}

func (q *localAuthMemoryQuerier) CreateLocalAuthMembership(_ context.Context, arg sqlc.CreateLocalAuthMembershipParams) (sqlc.LocalAuthMembership, error) {
	for id, membership := range q.memberships {
		if membership.UserID == arg.UserID && membership.OrgID == arg.OrgID {
			membership.RoleSlug = arg.RoleSlug
			membership.UpdatedAt = localAuthTS(q.now)
			q.memberships[id] = membership
			return membership, nil
		}
	}
	row := sqlc.LocalAuthMembership{
		ID:        arg.ID,
		UserID:    arg.UserID,
		OrgID:     arg.OrgID,
		RoleSlug:  arg.RoleSlug,
		CreatedAt: localAuthTS(q.now),
		UpdatedAt: localAuthTS(q.now),
	}
	q.memberships[row.ID] = row
	return row, nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthMembership(_ context.Context, id string) (sqlc.LocalAuthMembership, error) {
	membership, ok := q.memberships[id]
	if !ok {
		return sqlc.LocalAuthMembership{}, pgx.ErrNoRows
	}
	return membership, nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthMembershipForUserOrg(_ context.Context, arg sqlc.GetLocalAuthMembershipForUserOrgParams) (sqlc.LocalAuthMembership, error) {
	for _, membership := range q.memberships {
		if membership.UserID == arg.UserID && membership.OrgID == arg.OrgID {
			return membership, nil
		}
	}
	return sqlc.LocalAuthMembership{}, pgx.ErrNoRows
}

func (q *localAuthMemoryQuerier) UpdateLocalAuthMembershipRole(_ context.Context, arg sqlc.UpdateLocalAuthMembershipRoleParams) (sqlc.LocalAuthMembership, error) {
	membership, ok := q.memberships[arg.ID]
	if !ok {
		return sqlc.LocalAuthMembership{}, pgx.ErrNoRows
	}
	membership.RoleSlug = arg.RoleSlug
	membership.UpdatedAt = localAuthTS(q.now)
	q.memberships[arg.ID] = membership
	return membership, nil
}

func (q *localAuthMemoryQuerier) DeleteLocalAuthMembership(_ context.Context, id string) error {
	if _, ok := q.memberships[id]; !ok {
		return pgx.ErrNoRows
	}
	delete(q.memberships, id)
	return nil
}

func (q *localAuthMemoryQuerier) CountLocalAuthAdmins(_ context.Context, orgID string) (int32, error) {
	var count int32
	for _, membership := range q.memberships {
		if membership.OrgID == orgID && membership.RoleSlug == "admin" {
			count++
		}
	}
	return count, nil
}

func (q *localAuthMemoryQuerier) CountLocalAuthUserOrgs(_ context.Context, userID string) (int32, error) {
	seen := map[string]struct{}{}
	for _, membership := range q.memberships {
		if membership.UserID == userID {
			seen[membership.OrgID] = struct{}{}
		}
	}
	return int32(len(seen)), nil
}

func (q *localAuthMemoryQuerier) ListLocalAuthUserOrgs(_ context.Context, userID string) ([]sqlc.ListLocalAuthUserOrgsRow, error) {
	var rows []sqlc.ListLocalAuthUserOrgsRow
	for _, membership := range q.memberships {
		if membership.UserID != userID {
			continue
		}
		rows = append(rows, sqlc.ListLocalAuthUserOrgsRow{
			MembershipID: membership.ID,
			UserID:       membership.UserID,
			OrgID:        membership.OrgID,
			RoleSlug:     membership.RoleSlug,
			OrgName:      q.orgs[membership.OrgID].Name,
		})
	}
	return rows, nil
}

func (q *localAuthMemoryQuerier) ListLocalAuthMembers(_ context.Context, orgID string) ([]sqlc.ListLocalAuthMembersRow, error) {
	var rows []sqlc.ListLocalAuthMembersRow
	for _, membership := range q.memberships {
		if membership.OrgID != orgID {
			continue
		}
		user := q.users[membership.UserID]
		rows = append(rows, sqlc.ListLocalAuthMembersRow{
			MembershipID: membership.ID,
			UserID:       membership.UserID,
			OrgID:        membership.OrgID,
			RoleSlug:     membership.RoleSlug,
			Email:        user.Email,
			FirstName:    user.FirstName,
			LastName:     user.LastName,
		})
	}
	return rows, nil
}

func (q *localAuthMemoryQuerier) FindLocalAuthOrgUserByEmail(_ context.Context, arg sqlc.FindLocalAuthOrgUserByEmailParams) (sqlc.LocalAuthUser, error) {
	userID, ok := q.usersByMail[arg.EmailNormalized]
	if !ok {
		return sqlc.LocalAuthUser{}, pgx.ErrNoRows
	}
	for _, membership := range q.memberships {
		if membership.UserID == userID && membership.OrgID == arg.OrgID {
			return q.users[userID], nil
		}
	}
	return sqlc.LocalAuthUser{}, pgx.ErrNoRows
}

func (q *localAuthMemoryQuerier) CreateLocalAuthInvitation(_ context.Context, arg sqlc.CreateLocalAuthInvitationParams) (sqlc.LocalAuthInvitation, error) {
	row := sqlc.LocalAuthInvitation{
		ID:              arg.ID,
		OrgID:           arg.OrgID,
		Email:           arg.Email,
		EmailNormalized: arg.EmailNormalized,
		RoleSlug:        arg.RoleSlug,
		TokenHash:       arg.TokenHash,
		ExpiresAt:       arg.ExpiresAt,
		CreatedBy:       arg.CreatedBy,
		CreatedAt:       localAuthTS(q.now),
		UpdatedAt:       localAuthTS(q.now),
	}
	q.invitations[row.ID] = row
	return row, nil
}

func (q *localAuthMemoryQuerier) ListLocalAuthInvitations(_ context.Context, orgID string) ([]sqlc.LocalAuthInvitation, error) {
	var rows []sqlc.LocalAuthInvitation
	for _, invitation := range q.invitations {
		if invitation.OrgID == orgID && !invitation.AcceptedAt.Valid && !invitation.RevokedAt.Valid && invitation.ExpiresAt.Valid && q.now.Before(invitation.ExpiresAt.Time) {
			rows = append(rows, invitation)
		}
	}
	return rows, nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthInvitationByTokenHash(_ context.Context, tokenHash []byte) (sqlc.LocalAuthInvitation, error) {
	for _, invitation := range q.invitations {
		if string(invitation.TokenHash) == string(tokenHash) {
			return invitation, nil
		}
	}
	return sqlc.LocalAuthInvitation{}, pgx.ErrNoRows
}

func (q *localAuthMemoryQuerier) GetLocalAuthInvitation(_ context.Context, id string) (sqlc.LocalAuthInvitation, error) {
	invitation, ok := q.invitations[id]
	if !ok {
		return sqlc.LocalAuthInvitation{}, pgx.ErrNoRows
	}
	return invitation, nil
}

func (q *localAuthMemoryQuerier) AcceptLocalAuthInvitation(_ context.Context, id string) error {
	invitation, ok := q.invitations[id]
	if !ok {
		return pgx.ErrNoRows
	}
	if invitation.AcceptedAt.Valid || invitation.RevokedAt.Valid || !invitation.ExpiresAt.Valid || !q.now.Before(invitation.ExpiresAt.Time) {
		return nil
	}
	invitation.AcceptedAt = localAuthTS(q.now)
	invitation.UpdatedAt = localAuthTS(q.now)
	q.invitations[id] = invitation
	return nil
}

func (q *localAuthMemoryQuerier) RevokeLocalAuthInvitation(_ context.Context, arg sqlc.RevokeLocalAuthInvitationParams) error {
	invitation, ok := q.invitations[arg.ID]
	if !ok {
		return pgx.ErrNoRows
	}
	if invitation.OrgID != arg.OrgID || invitation.AcceptedAt.Valid || invitation.RevokedAt.Valid {
		return nil
	}
	invitation.RevokedAt = localAuthTS(q.now)
	invitation.UpdatedAt = localAuthTS(q.now)
	q.invitations[arg.ID] = invitation
	return nil
}

func (q *localAuthMemoryQuerier) CreateLocalAuthSession(_ context.Context, arg sqlc.CreateLocalAuthSessionParams) (sqlc.LocalAuthSession, error) {
	row := sqlc.LocalAuthSession{
		ID:          arg.ID,
		UserID:      arg.UserID,
		TokenHash:   arg.TokenHash,
		ActiveOrgID: arg.ActiveOrgID,
		ExpiresAt:   arg.ExpiresAt,
		LastSeenAt:  localAuthTS(q.now),
		CreatedAt:   localAuthTS(q.now),
	}
	q.sessions[row.ID] = row
	return row, nil
}

func (q *localAuthMemoryQuerier) GetLocalAuthSession(_ context.Context, id string) (sqlc.GetLocalAuthSessionRow, error) {
	session, ok := q.sessions[id]
	if !ok || !session.ExpiresAt.Valid || !q.now.Before(session.ExpiresAt.Time) {
		return sqlc.GetLocalAuthSessionRow{}, pgx.ErrNoRows
	}
	user := q.users[session.UserID]
	row := sqlc.GetLocalAuthSessionRow{
		ID:          session.ID,
		UserID:      session.UserID,
		TokenHash:   session.TokenHash,
		ActiveOrgID: session.ActiveOrgID,
		ExpiresAt:   session.ExpiresAt,
		LastSeenAt:  session.LastSeenAt,
		CreatedAt:   session.CreatedAt,
		Email:       user.Email,
		FirstName:   user.FirstName,
		LastName:    user.LastName,
	}
	if session.ActiveOrgID != nil {
		for _, membership := range q.memberships {
			if membership.UserID == session.UserID && membership.OrgID == *session.ActiveOrgID {
				role := membership.RoleSlug
				row.RoleSlug = &role
				break
			}
		}
	}
	return row, nil
}

func (q *localAuthMemoryQuerier) TouchLocalAuthSession(_ context.Context, id string) error {
	session, ok := q.sessions[id]
	if !ok {
		return pgx.ErrNoRows
	}
	session.LastSeenAt = localAuthTS(q.now)
	q.sessions[id] = session
	return nil
}

func (q *localAuthMemoryQuerier) UpdateLocalAuthSessionOrg(_ context.Context, arg sqlc.UpdateLocalAuthSessionOrgParams) (sqlc.LocalAuthSession, error) {
	session, ok := q.sessions[arg.ID]
	if !ok {
		return sqlc.LocalAuthSession{}, pgx.ErrNoRows
	}
	session.ActiveOrgID = arg.ActiveOrgID
	session.LastSeenAt = localAuthTS(q.now)
	q.sessions[arg.ID] = session
	return session, nil
}

func (q *localAuthMemoryQuerier) DeleteLocalAuthSession(_ context.Context, id string) error {
	if _, ok := q.sessions[id]; !ok {
		return pgx.ErrNoRows
	}
	delete(q.sessions, id)
	return nil
}

func (q *localAuthMemoryQuerier) DeleteExpiredLocalAuthSessions(_ context.Context) error {
	for id, session := range q.sessions {
		if !session.ExpiresAt.Valid || !q.now.Before(session.ExpiresAt.Time) {
			delete(q.sessions, id)
		}
	}
	return nil
}

func localAuthPostRequest(t *testing.T, s *Service, path string, values url.Values) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	csrfRec := httptest.NewRecorder()
	token, err := s.issueLocalAuthCSRFToken(csrfRec)
	if err != nil {
		t.Fatalf("issue CSRF token: %v", err)
	}
	values.Set("csrf_token", token)
	req := httptest.NewRequest(http.MethodPost, "https://app.example.test"+path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://app.example.test")
	for _, cookie := range csrfRec.Result().Cookies() {
		req.AddCookie(cookie)
	}
	return httptest.NewRecorder(), req
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookieName && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("missing %s cookie in response", SessionCookieName)
	return nil
}

func firstLocalAuthUser(t *testing.T, q *localAuthMemoryQuerier) sqlc.LocalAuthUser {
	t.Helper()
	for _, user := range q.users {
		return user
	}
	t.Fatal("expected a local auth user")
	return sqlc.LocalAuthUser{}
}

func localAuthCreateTestUser(t *testing.T, q *localAuthMemoryQuerier, id, email, first, last string) sqlc.LocalAuthUser {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("password-"+id), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user, err := q.CreateLocalAuthUser(context.Background(), sqlc.CreateLocalAuthUserParams{
		ID:              id,
		Email:           email,
		EmailNormalized: normalizeEmail(email),
		PasswordHash:    hash,
		FirstName:       first,
		LastName:        last,
	})
	if err != nil {
		t.Fatalf("CreateLocalAuthUser: %v", err)
	}
	return user
}

func localAuthMembershipForUserOrg(t *testing.T, q *localAuthMemoryQuerier, userID, orgID string) sqlc.LocalAuthMembership {
	t.Helper()
	membership, err := q.GetLocalAuthMembershipForUserOrg(context.Background(), sqlc.GetLocalAuthMembershipForUserOrgParams{
		UserID: userID,
		OrgID:  orgID,
	})
	if err != nil {
		t.Fatalf("GetLocalAuthMembershipForUserOrg: %v", err)
	}
	return membership
}

func TestLocalAuthPageRendering(t *testing.T) {
	s, _ := newLocalAuthFlowService(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://app.example.test/login", nil)
	s.LoginHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("login content type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "Workspace sign-in") || !strings.Contains(rec.Body.String(), `name="csrf_token"`) {
		t.Fatalf("login page body missing expected content: %q", rec.Body.String())
	}
	foundCSRF := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == localAuthCSRFCookieName {
			foundCSRF = true
			break
		}
	}
	if !foundCSRF {
		t.Fatal("login page missing CSRF cookie")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/signup?invite=bad-token", nil)
	s.SignupHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("signup GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This invitation is invalid or expired.") {
		t.Fatalf("signup page should render invalid invite error: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="invite" value="bad-token"`) {
		t.Fatalf("signup page should preserve invite token: %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "https://app.example.test/login", nil)
	s.LoginHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("login PUT status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestLocalSignupLoginSessionAndPasswordFlow(t *testing.T) {
	s, q := newLocalAuthFlowService(t)

	rec, req := localAuthPostRequest(t, s, "/signup", url.Values{
		"email":      {"Alice@Example.com"},
		"first_name": {"Alice"},
		"last_name":  {"Example"},
		"password":   {"correct-password"},
	})
	s.SignupHandler(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/onboarding" {
		t.Fatalf("signup response = %d %q, want onboarding redirect", rec.Code, rec.Header().Get("Location"))
	}
	user := firstLocalAuthUser(t, q)
	if user.EmailNormalized != "alice@example.com" {
		t.Fatalf("normalized email = %q", user.EmailNormalized)
	}

	orgID, err := s.CreateOrganization(context.Background(), "  Acme  ")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if name, err := s.GetOrganizationName(context.Background(), orgID); err != nil || name != "Acme" {
		t.Fatalf("GetOrganizationName = %q, %v", name, err)
	}
	if err := s.UpdateOrganizationName(context.Background(), orgID, "  Renamed  "); err != nil {
		t.Fatalf("UpdateOrganizationName: %v", err)
	}
	if enabled, err := s.OrganizationHasFeatureFlag(context.Background(), orgID, "anything"); err != nil || enabled {
		t.Fatalf("OrganizationHasFeatureFlag = %v, %v; want false nil", enabled, err)
	}
	if err := s.AddUserToOrganization(context.Background(), user.ID, orgID, "admin"); err != nil {
		t.Fatalf("AddUserToOrganization: %v", err)
	}

	rec, req = localAuthPostRequest(t, s, "/login", url.Values{
		"email":    {"alice@example.com"},
		"password": {"correct-password"},
	})
	s.LoginHandler(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("login response = %d %q, want app redirect", rec.Code, rec.Header().Get("Location"))
	}
	cookie := sessionCookieFrom(t, rec)

	var principal Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		principal, ok = FromContext(r.Context())
		if !ok {
			t.Fatal("expected principal in context")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.Middleware(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("middleware response = %d", rec.Code)
	}
	if principal.UserID != user.ID || principal.OrgID != orgID || principal.Role != "admin" {
		t.Fatalf("principal = %+v, want user %q org %q admin", principal, user.ID, orgID)
	}

	req = httptest.NewRequest(http.MethodPost, "https://app.example.test/switch", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	if err := s.SwitchOrg(rec, req, orgID); err != nil {
		t.Fatalf("SwitchOrg: %v", err)
	}
	if err := s.ChangePassword(context.Background(), user.ID, "wrong-password", "new-password"); !errors.Is(err, ErrCurrentPasswordIncorrect) {
		t.Fatalf("wrong password error = %v, want ErrCurrentPasswordIncorrect", err)
	}
	if err := s.ChangePassword(context.Background(), user.ID, "correct-password", "new-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if bcrypt.CompareHashAndPassword(q.users[user.ID].PasswordHash, []byte("new-password")) != nil {
		t.Fatal("password hash was not updated")
	}

	q.sessions["expired"] = sqlc.LocalAuthSession{ID: "expired", UserID: user.ID, ExpiresAt: localAuthTS(q.now.Add(-time.Minute))}
	if err := s.DeleteExpiredLocalAuthSessions(context.Background()); err != nil {
		t.Fatalf("DeleteExpiredLocalAuthSessions: %v", err)
	}
	if _, ok := q.sessions["expired"]; ok {
		t.Fatal("expired session was not deleted")
	}

	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/logout", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.LogoutHandler(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("logout response = %d %q, want root redirect", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok := q.sessions[principal.SessionID]; ok {
		t.Fatal("logout did not delete current session")
	}
	if err := s.DeleteOrganization(context.Background(), orgID); err != nil {
		t.Fatalf("DeleteOrganization: %v", err)
	}
}

func TestLocalInviteSignupAcceptsInvitation(t *testing.T) {
	s, q := newLocalAuthFlowService(t)
	orgID, err := s.CreateOrganization(context.Background(), "Invite Org")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	token := "invite-token"
	q.invitations["inv_local_test"] = sqlc.LocalAuthInvitation{
		ID:              "inv_local_test",
		OrgID:           orgID,
		Email:           "guest@example.com",
		EmailNormalized: "guest@example.com",
		RoleSlug:        "member",
		TokenHash:       hashLocalToken(token),
		ExpiresAt:       localAuthTS(q.now.Add(localInvitationTTL)),
		CreatedAt:       localAuthTS(q.now),
		UpdatedAt:       localAuthTS(q.now),
	}

	req := httptest.NewRequest(http.MethodGet, "https://app.example.test/signup?invite="+url.QueryEscape(token), nil)
	rec := httptest.NewRecorder()
	s.SignupHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite signup form response = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "guest@example.com") || !strings.Contains(body, "member") {
		t.Fatalf("invite signup form did not include invitation details:\n%s", body)
	}

	rec, req = localAuthPostRequest(t, s, "/signup", url.Values{
		"email":    {"guest@example.com"},
		"password": {"guest-password"},
		"invite":   {token},
	})
	s.SignupHandler(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("invite signup response = %d %q, want app redirect", rec.Code, rec.Header().Get("Location"))
	}
	if !q.invitations["inv_local_test"].AcceptedAt.Valid {
		t.Fatal("invitation was not accepted")
	}
	user := firstLocalAuthUser(t, q)
	membership, err := q.GetLocalAuthMembershipForUserOrg(context.Background(), sqlc.GetLocalAuthMembershipForUserOrgParams{
		UserID: user.ID,
		OrgID:  orgID,
	})
	if err != nil {
		t.Fatalf("GetLocalAuthMembershipForUserOrg: %v", err)
	}
	if membership.RoleSlug != "member" {
		t.Fatalf("membership role = %q, want member", membership.RoleSlug)
	}
}

func TestLocalAuthHandlerValidationBranches(t *testing.T) {
	s, q := newLocalAuthFlowService(t)
	existing := localAuthCreateTestUser(t, q, "user_local_existing", "existing@example.com", "Existing", "User")

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
		body    url.Values
		want    string
	}{
		{
			name:    "signup invalid email",
			handler: s.SignupHandler,
			path:    "/signup",
			body:    url.Values{"email": {"@"}, "password": {"good-password"}},
			want:    "Enter a valid email address.",
		},
		{
			name:    "signup short password",
			handler: s.SignupHandler,
			path:    "/signup",
			body:    url.Values{"email": {"new@example.com"}, "password": {"short"}},
			want:    "Password must be at least 8 characters.",
		},
		{
			name:    "signup duplicate email",
			handler: s.SignupHandler,
			path:    "/signup",
			body:    url.Values{"email": {"existing@example.com"}, "password": {"good-password"}},
			want:    "An account already exists for that email.",
		},
		{
			name:    "signup missing invite",
			handler: s.SignupHandler,
			path:    "/signup",
			body:    url.Values{"email": {"invitee@example.com"}, "password": {"good-password"}, "invite": {"missing"}},
			want:    "This invitation is invalid or expired.",
		},
		{
			name:    "login missing password",
			handler: s.LoginHandler,
			path:    "/login",
			body:    url.Values{"email": {"existing@example.com"}},
			want:    "Email or password is incorrect.",
		},
		{
			name:    "login unknown user",
			handler: s.LoginHandler,
			path:    "/login",
			body:    url.Values{"email": {"missing@example.com"}, "password": {"good-password"}},
			want:    "Email or password is incorrect.",
		},
		{
			name:    "login wrong password",
			handler: s.LoginHandler,
			path:    "/login",
			body:    url.Values{"email": {"existing@example.com"}, "password": {"wrong-password"}},
			want:    "Email or password is incorrect.",
		},
		{
			name:    "login missing invite",
			handler: s.LoginHandler,
			path:    "/login",
			body:    url.Values{"email": {"existing@example.com"}, "password": {"password-user_local_existing"}, "invite": {"missing"}},
			want:    "This invitation is invalid or expired.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, req := localAuthPostRequest(t, s, tc.path, tc.body)
			tc.handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("response = %d, want 200", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("response body missing %q:\n%s", tc.want, rec.Body.String())
			}
		})
	}

	orgID, err := s.CreateOrganization(context.Background(), "Invite Mismatch")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	q.invitations["inv_mismatch"] = sqlc.LocalAuthInvitation{
		ID:              "inv_mismatch",
		OrgID:           orgID,
		Email:           "other@example.com",
		EmailNormalized: "other@example.com",
		RoleSlug:        "member",
		TokenHash:       hashLocalToken("mismatch-token"),
		ExpiresAt:       localAuthTS(q.now.Add(localInvitationTTL)),
	}
	rec, req := localAuthPostRequest(t, s, "/signup", url.Values{
		"email":    {"someone@example.com"},
		"password": {"good-password"},
		"invite":   {"mismatch-token"},
	})
	s.SignupHandler(rec, req)
	if !strings.Contains(rec.Body.String(), "Use the email address that was invited.") {
		t.Fatalf("signup invite mismatch body:\n%s", rec.Body.String())
	}
	rec, req = localAuthPostRequest(t, s, "/login", url.Values{
		"email":    {existing.Email},
		"password": {"password-user_local_existing"},
		"invite":   {"mismatch-token"},
	})
	s.LoginHandler(rec, req)
	if !strings.Contains(rec.Body.String(), "Use the email address that was invited.") {
		t.Fatalf("login invite mismatch body:\n%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/login", nil)
	rec = httptest.NewRecorder()
	s.LoginHandler(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Log in") {
		t.Fatalf("login GET response = %d body %q", rec.Code, rec.Body.String())
	}
	for _, handler := range []http.HandlerFunc{s.LoginHandler, s.SignupHandler} {
		req = httptest.NewRequest(http.MethodPut, "https://app.example.test/login", nil)
		rec = httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("method response = %d, want 405", rec.Code)
		}
	}

	rec, req = localAuthPostRequest(t, s, "/signup", url.Values{"email": {"rate@example.com"}, "password": {"good-password"}})
	key := req.URL.Path + "|" + s.localAuthClientIP(req)
	s.localRate = map[string]localAuthRateEntry{key: {Count: localAuthRateLimit, ResetAt: q.now.Add(localAuthRateWindow), LastSeen: q.now}}
	s.SignupHandler(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited signup response = %d, want 429", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "https://app.example.test/signup", strings.NewReader(url.Values{"email": {"no-csrf@example.com"}}.Encode()))
	req.Header.Set("Origin", "https://app.example.test")
	rec = httptest.NewRecorder()
	s.SignupHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF response = %d, want 403", rec.Code)
	}
}

func TestLocalInvitationEdgeHelpers(t *testing.T) {
	s, q := newLocalAuthFlowService(t)
	orgID, err := s.CreateOrganization(context.Background(), "Edges")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	for _, tc := range []struct {
		id     string
		mutate func(*sqlc.LocalAuthInvitation)
	}{
		{id: "accepted", mutate: func(inv *sqlc.LocalAuthInvitation) {
			inv.AcceptedAt = localAuthTS(q.now)
		}},
		{id: "revoked", mutate: func(inv *sqlc.LocalAuthInvitation) {
			inv.RevokedAt = localAuthTS(q.now)
		}},
		{id: "expired", mutate: func(inv *sqlc.LocalAuthInvitation) {
			inv.ExpiresAt = localAuthTS(q.now.Add(-time.Minute))
		}},
	} {
		inv := sqlc.LocalAuthInvitation{
			ID:              tc.id,
			OrgID:           orgID,
			Email:           tc.id + "@example.com",
			EmailNormalized: tc.id + "@example.com",
			RoleSlug:        "member",
			TokenHash:       hashLocalToken(tc.id + "-token"),
			ExpiresAt:       localAuthTS(q.now.Add(localInvitationTTL)),
		}
		tc.mutate(&inv)
		q.invitations[tc.id] = inv
		if _, ok, err := s.localInvitationFromRequest(context.Background(), tc.id+"-token"); err != nil || ok {
			t.Fatalf("localInvitationFromRequest(%s) = ok %v err %v, want invalid nil", tc.id, ok, err)
		}
	}
	if _, ok, err := s.localInvitationFromRequest(context.Background(), ""); err != nil || ok {
		t.Fatalf("empty invitation token = ok %v err %v", ok, err)
	}
	if err := s.localAcceptInvitationForUser(context.Background(), localInvitation{}, "user"); err == nil {
		t.Fatal("localAcceptInvitationForUser should reject empty invitation")
	}
	if token, err := s.localSendInvitation(context.Background(), "not-an-email", orgID, "member", ""); err == nil || token != "" {
		t.Fatalf("localSendInvitation invalid email = token %q err %v", token, err)
	}
	if _, err := s.RequestPasswordReset(context.Background(), "user@example.com"); err == nil {
		t.Fatal("RequestPasswordReset should fail in local auth mode")
	}
	if _, ok, err := s.FindOrgUserByEmail(context.Background(), orgID, ""); err != nil || ok {
		t.Fatalf("FindOrgUserByEmail empty = ok %v err %v", ok, err)
	}
}

func TestLocalSessionEdgeHelpers(t *testing.T) {
	s, q := newLocalAuthFlowService(t)
	orgID, err := s.CreateOrganization(context.Background(), "Edges")
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if _, ok := s.localPrincipalFromCookie(context.Background(), "malformed"); ok {
		t.Fatal("malformed local session cookie should not authenticate")
	}
	user := localAuthCreateTestUser(t, q, "user_local_edge", "edge@example.com", "Edge", "Case")
	session := sqlc.LocalAuthSession{
		ID:        "sess_local_edge",
		UserID:    user.ID,
		TokenHash: hashLocalToken("real-secret"),
		ExpiresAt: localAuthTS(q.now.Add(localSessionTTL)),
	}
	q.sessions[session.ID] = session
	if _, ok := s.localPrincipalFromCookie(context.Background(), localSessionCookieValue(session.ID, "wrong-secret")); ok {
		t.Fatal("wrong local session secret should not authenticate")
	}

	req := httptest.NewRequest(http.MethodGet, "https://app.example.test/switch", nil)
	rec := httptest.NewRecorder()
	if err := s.localSwitchOrg(rec, req, orgID); err == nil {
		t.Fatal("localSwitchOrg without cookie should fail")
	}
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: localSessionCookieValue(session.ID, "real-secret")})
	if err := s.localSwitchOrg(rec, req, "missing-org"); err == nil {
		t.Fatal("localSwitchOrg without membership should fail")
	}

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		if _, ok := FromContext(r.Context()); ok {
			t.Fatal("unexpected principal for request without valid cookie")
		}
	})
	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/", nil)
	rec = httptest.NewRecorder()
	s.Middleware(next).ServeHTTP(rec, req)
	if !called {
		t.Fatal("middleware did not call next without cookie")
	}
	called = false
	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "bad-cookie"})
	rec = httptest.NewRecorder()
	s.Middleware(next).ServeHTTP(rec, req)
	if !called {
		t.Fatal("middleware did not call next with bad cookie")
	}
	if cleared := rec.Result().Cookies()[0]; cleared.Name != SessionCookieName || cleared.MaxAge >= 0 {
		t.Fatalf("bad cookie was not cleared: %+v", cleared)
	}
	s.localRevokeCurrentSession(httptest.NewRequest(http.MethodGet, "https://app.example.test/logout", nil))

	if err := s.ChangePassword(context.Background(), user.ID, "real-secret", "short"); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short password error = %v", err)
	}
	bypass, err := New(Config{Bypass: true})
	if err != nil {
		t.Fatalf("New bypass: %v", err)
	}
	if err := bypass.ChangePassword(context.Background(), "user", "", "short"); err != nil {
		t.Fatalf("bypass ChangePassword: %v", err)
	}
	workosLike := &Service{cfg: Config{Mode: AuthModeWorkOS}}
	if err := workosLike.DeleteExpiredLocalAuthSessions(context.Background()); err != nil {
		t.Fatalf("workos DeleteExpiredLocalAuthSessions: %v", err)
	}
	if err := workosLike.ChangePassword(context.Background(), "user", "old", "new-password"); err == nil {
		t.Fatal("workos ChangePassword should fail")
	}
}

func TestLocalRateAndSameOriginEdges(t *testing.T) {
	s, q := newLocalAuthFlowService(t)
	s.localRate = map[string]localAuthRateEntry{
		"expired": {ResetAt: q.now.Add(-time.Minute)},
		"active":  {ResetAt: q.now.Add(time.Minute)},
	}
	s.pruneLocalAuthRateEntries(q.now)
	if _, ok := s.localRate["expired"]; ok {
		t.Fatal("expired rate limit entry was not pruned")
	}
	if _, ok := s.localRate["active"]; !ok {
		t.Fatal("active rate limit entry was pruned")
	}

	req := httptest.NewRequest(http.MethodPost, "https://app.example.test/form", nil)
	req.Host = ""
	if err := requireLocalSameOrigin(req); err == nil {
		t.Fatal("missing host should fail same-origin check")
	}
	req = httptest.NewRequest(http.MethodPost, "https://app.example.test/form", nil)
	req.Header.Set("Referer", "https://evil.example.test/form")
	if err := requireLocalSameOrigin(req); err == nil {
		t.Fatal("mismatched Referer should fail same-origin check")
	}
	req = httptest.NewRequest(http.MethodPost, "https://app.example.test/form", nil)
	req.Header.Set("Origin", "://broken")
	if err := requireLocalSameOrigin(req); err == nil {
		t.Fatal("invalid Origin should fail same-origin check")
	}
}

func TestRequireAuthAndOrg(t *testing.T) {
	s, _ := newLocalAuthFlowService(t)
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://app.example.test/settings", nil)
	s.RequireAuth(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" || nextCalled {
		t.Fatalf("RequireAuth unauthenticated = %d %q next=%v", rec.Code, rec.Header().Get("Location"), nextCalled)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/settings", nil)
	req = req.WithContext(withPrincipal(req.Context(), Principal{UserID: "user"}))
	s.RequireOrg(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/onboarding" {
		t.Fatalf("RequireOrg without org = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	nextCalled = false
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "https://app.example.test/settings", nil)
	req = req.WithContext(withPrincipal(req.Context(), Principal{UserID: "user", OrgID: "org"}))
	s.RequireOrg(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !nextCalled {
		t.Fatalf("RequireOrg with org = %d next=%v", rec.Code, nextCalled)
	}
}

func newBypassAuthFlowService(t *testing.T) *Service {
	t.Helper()
	s, err := New(Config{
		Bypass:      true,
		BypassUser:  "user_bypass",
		BypassOrg:   "org_bypass",
		BypassRole:  "admin",
		BypassEmail: "bypass@example.com",
	})
	if err != nil {
		t.Fatalf("New bypass service: %v", err)
	}
	return s
}

func TestBypassAuthOrgMethods(t *testing.T) {
	s := newBypassAuthFlowService(t)
	ctx := context.Background()

	if orgID, err := s.CreateOrganization(ctx, "Bypass"); err != nil || orgID != "org_bypass" {
		t.Fatalf("CreateOrganization = %q, %v", orgID, err)
	}
	if name, err := s.GetOrganizationName(ctx, "org_any"); err != nil || name != "org_any" {
		t.Fatalf("GetOrganizationName = %q, %v", name, err)
	}
	if err := s.UpdateOrganizationName(ctx, "org_any", "New"); err != nil {
		t.Fatalf("UpdateOrganizationName: %v", err)
	}
	if enabled, err := s.OrganizationHasFeatureFlag(ctx, "org_any", "flag"); err != nil || enabled {
		t.Fatalf("OrganizationHasFeatureFlag = %v, %v", enabled, err)
	}
	if err := s.DeleteOrganization(ctx, "org_any"); err != nil {
		t.Fatalf("DeleteOrganization: %v", err)
	}
	if err := s.AddUserToOrganization(ctx, "user_any", "org_any", "member"); err != nil {
		t.Fatalf("AddUserToOrganization: %v", err)
	}
	if err := s.SwitchOrg(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), "org_any"); err != nil {
		t.Fatalf("SwitchOrg: %v", err)
	}
}

func TestBypassAuthProfileAndMemberMethods(t *testing.T) {
	s := newBypassAuthFlowService(t)
	ctx := context.Background()
	profile, err := s.GetProfile(ctx, "user_bypass")
	if err != nil || profile.Email != "bypass@example.com" {
		t.Fatalf("GetProfile = %+v, %v", profile, err)
	}
	if multiple, err := s.UserHasMultipleOrgs(ctx, "user_bypass"); err != nil || multiple {
		t.Fatalf("UserHasMultipleOrgs = %v, %v", multiple, err)
	}
	orgs, err := s.ListUserOrgs(ctx, "user_bypass", "org_bypass")
	if err != nil || len(orgs) != 1 || !orgs[0].Current {
		t.Fatalf("ListUserOrgs = %+v, %v", orgs, err)
	}
	if err := s.UpdateProfile(ctx, "user_bypass", "First", "Last"); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if resetURL, err := s.RequestPasswordReset(ctx, "bypass@example.com"); err != nil || resetURL != "/" {
		t.Fatalf("RequestPasswordReset = %q, %v", resetURL, err)
	}
	members, err := s.ListMembers(ctx, "org_bypass")
	if err != nil || len(members) != 1 || members[0].Status != "active" {
		t.Fatalf("ListMembers = %+v, %v", members, err)
	}
	only, err := s.UsersOnlyInOrganization(ctx, "org_bypass")
	if err != nil || len(only) != 1 || only[0] != "user_bypass" {
		t.Fatalf("UsersOnlyInOrganization = %+v, %v", only, err)
	}
	if err := s.DeleteUsers(ctx, []string{"user_bypass"}); err != nil {
		t.Fatalf("DeleteUsers: %v", err)
	}
	found, ok, err := s.FindOrgUserByEmail(ctx, "org_bypass", "bypass@example.com")
	if err != nil || !ok || found.UserID != "user_bypass" {
		t.Fatalf("FindOrgUserByEmail match = %+v, %v, %v", found, ok, err)
	}
	if _, ok, err := s.FindOrgUserByEmail(ctx, "org_bypass", "missing@example.com"); err != nil || ok {
		t.Fatalf("FindOrgUserByEmail missing = ok %v err %v", ok, err)
	}
}

func TestBypassAuthInvitationAndRoleMethods(t *testing.T) {
	s := newBypassAuthFlowService(t)
	ctx := context.Background()
	if invites, err := s.ListInvitations(ctx, "org_bypass"); err != nil || invites != nil {
		t.Fatalf("ListInvitations = %+v, %v", invites, err)
	}
	if token, err := s.SendInvitation(ctx, "new@example.com", "org_bypass", "member", "user_bypass"); err != nil || token != "" {
		t.Fatalf("SendInvitation = %q, %v", token, err)
	}
	if err := s.RevokeInvitation(ctx, "inv", "org_bypass"); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}
	if err := s.RemoveMember(ctx, "om", "org_bypass", "user_bypass"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if err := s.UpdateMemberRole(ctx, "om", "org_bypass", "user_bypass", "member"); err != nil {
		t.Fatalf("UpdateMemberRole: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	rec := httptest.NewRecorder()
	s.LogoutHandler(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?signed_out=1" {
		t.Fatalf("LogoutHandler bypass = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

type localMembersFixture struct {
	s      *Service
	q      *localAuthMemoryQuerier
	admin  sqlc.LocalAuthUser
	member sqlc.LocalAuthUser
	orgA   string
	orgB   string
}

func newLocalMembersFixture(t *testing.T) localMembersFixture {
	t.Helper()
	s, q := newLocalAuthFlowService(t)
	admin := localAuthCreateTestUser(t, q, "user_local_admin", "admin@example.com", "Ada", "Admin")
	member := localAuthCreateTestUser(t, q, "user_local_member", "member@example.com", "Maya", "Member")

	orgA, err := s.CreateOrganization(context.Background(), "Alpha")
	if err != nil {
		t.Fatalf("create org A: %v", err)
	}
	orgB, err := s.CreateOrganization(context.Background(), "Beta")
	if err != nil {
		t.Fatalf("create org B: %v", err)
	}
	if err := s.AddUserToOrganization(context.Background(), admin.ID, orgA, "admin"); err != nil {
		t.Fatalf("add admin to org A: %v", err)
	}
	if err := s.AddUserToOrganization(context.Background(), admin.ID, orgB, "member"); err != nil {
		t.Fatalf("add admin to org B: %v", err)
	}
	if err := s.AddUserToOrganization(context.Background(), member.ID, orgA, "member"); err != nil {
		t.Fatalf("add member to org A: %v", err)
	}
	return localMembersFixture{s: s, q: q, admin: admin, member: member, orgA: orgA, orgB: orgB}
}

func TestLocalMembersProfileAndOrgListing(t *testing.T) {
	f := newLocalMembersFixture(t)

	profile, err := f.s.GetProfile(context.Background(), f.admin.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.DisplayName() != "Ada Admin" {
		t.Fatalf("profile display name = %q", profile.DisplayName())
	}
	if err := f.s.UpdateProfile(context.Background(), f.admin.ID, "  Grace  ", "  Hopper  "); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if f.q.users[f.admin.ID].FirstName != "Grace" || f.q.users[f.admin.ID].LastName != "Hopper" {
		t.Fatalf("profile was not trimmed and updated: %+v", f.q.users[f.admin.ID])
	}

	multiple, err := f.s.UserHasMultipleOrgs(context.Background(), f.admin.ID)
	if err != nil {
		t.Fatalf("UserHasMultipleOrgs: %v", err)
	}
	if !multiple {
		t.Fatal("admin should have multiple orgs")
	}
	orgs, err := f.s.ListUserOrgs(context.Background(), f.admin.ID, f.orgB)
	if err != nil {
		t.Fatalf("ListUserOrgs: %v", err)
	}
	if len(orgs) != 2 || orgs[0].Name != "Alpha" || !orgs[1].Current {
		t.Fatalf("ListUserOrgs = %+v", orgs)
	}

	members, err := f.s.ListMembers(context.Background(), f.orgA)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("ListMembers len = %d, want 2", len(members))
	}
	only, err := f.s.UsersOnlyInOrganization(context.Background(), f.orgA)
	if err != nil {
		t.Fatalf("UsersOnlyInOrganization: %v", err)
	}
	if len(only) != 1 || only[0] != f.member.ID {
		t.Fatalf("UsersOnlyInOrganization = %+v, want only member", only)
	}
	found, ok, err := f.s.FindOrgUserByEmail(context.Background(), f.orgA, "MEMBER@example.com")
	if err != nil || !ok || found.UserID != f.member.ID {
		t.Fatalf("FindOrgUserByEmail = %+v %v %v", found, ok, err)
	}
}

func TestLocalInvitationsFlow(t *testing.T) {
	f := newLocalMembersFixture(t)

	token, err := f.s.SendInvitation(context.Background(), "invitee@example.com", f.orgA, "member", f.admin.ID)
	if err != nil {
		t.Fatalf("SendInvitation: %v", err)
	}
	if token == "" {
		t.Fatal("SendInvitation returned empty token")
	}
	invites, err := f.s.ListInvitations(context.Background(), f.orgA)
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invites) != 1 || invites[0].Email != "invitee@example.com" || invites[0].ExpiresAt == "" {
		t.Fatalf("ListInvitations = %+v", invites)
	}
	inviteID := invites[0].ID
	if err := f.s.RevokeInvitation(context.Background(), inviteID, f.orgB); !errors.Is(err, ErrCrossOrg) {
		t.Fatalf("cross-org revoke error = %v, want ErrCrossOrg", err)
	}
	if err := f.s.RevokeInvitation(context.Background(), inviteID, f.orgA); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}
}

func TestLocalMemberRoleGuards(t *testing.T) {
	f := newLocalMembersFixture(t)
	adminMembership := localAuthMembershipForUserOrg(t, f.q, f.admin.ID, f.orgA)
	memberMembership := localAuthMembershipForUserOrg(t, f.q, f.member.ID, f.orgA)
	if err := f.s.RemoveMember(context.Background(), adminMembership.ID, f.orgA, f.member.ID); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("expected last-admin remove guard, got %v", err)
	}
	if err := f.s.UpdateMemberRole(context.Background(), adminMembership.ID, f.orgA, f.member.ID, "member"); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("expected last-admin demote guard, got %v", err)
	}
	if err := f.s.UpdateMemberRole(context.Background(), memberMembership.ID, f.orgA, f.admin.ID, "admin"); err != nil {
		t.Fatalf("UpdateMemberRole member to admin: %v", err)
	}
	if err := f.s.RemoveMember(context.Background(), memberMembership.ID, f.orgA, f.admin.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if err := f.s.DeleteUsers(context.Background(), []string{"", f.member.ID}); err != nil {
		t.Fatalf("DeleteUsers: %v", err)
	}
	if _, ok := f.q.users[f.member.ID]; ok {
		t.Fatal("DeleteUsers did not remove member")
	}
}
