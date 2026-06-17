package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

const (
	AuthModeWorkOS = "workos"
	AuthModeLocal  = "local"

	localSessionTTL     = 7 * 24 * time.Hour
	localInvitationTTL  = 7 * 24 * time.Hour
	localPasswordMinLen = 8

	localAuthCSRFCookieName = "hetchy_local_auth_csrf"
	localAuthCSRFTokenTTL   = 15 * time.Minute
	localAuthRateLimit      = 20
	localAuthRateWindow     = time.Minute
	localAuthRateMaxEntries = 4096
)

// ErrCurrentPasswordIncorrect is safe to surface to an end user. Other
// ChangePassword errors may wrap database or bcrypt internals and should be
// mapped to a generic message by handlers.
var ErrCurrentPasswordIncorrect = errors.New("current password is incorrect")

type localAuthStore struct {
	q sqlc.Querier
}

func newLocalAuthStore(q sqlc.Querier) *localAuthStore {
	return &localAuthStore{q: q}
}

func normalizeAuthMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", AuthModeWorkOS:
		return AuthModeWorkOS, nil
	case AuthModeLocal:
		return AuthModeLocal, nil
	default:
		return "", fmt.Errorf("auth: unsupported mode %q", raw)
	}
}

type localInvitation struct {
	row   sqlc.LocalAuthInvitation
	token string
}

func (s *Service) localInvitationFromRequest(ctx context.Context, token string) (localInvitation, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return localInvitation{}, false, nil
	}
	row, err := s.local.q.GetLocalAuthInvitationByTokenHash(ctx, hashLocalToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return localInvitation{}, false, nil
	}
	if err != nil {
		return localInvitation{}, false, err
	}
	if row.AcceptedAt.Valid || row.RevokedAt.Valid || !row.ExpiresAt.Valid || !s.nowFn().Before(row.ExpiresAt.Time) {
		return localInvitation{}, false, nil
	}
	return localInvitation{row: row, token: token}, true, nil
}

func (s *Service) localLoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data := localAuthPageData{
			Mode:          "login",
			Title:         "Log in",
			Eyebrow:       "Workspace sign-in",
			SubmitLabel:   "Log in",
			AlternateText: "Need an account?",
			AlternateURL:  "/signup",
			AlternateLink: "Sign up",
			InviteToken:   strings.TrimSpace(r.URL.Query().Get("invite")),
		}
		s.decorateInvitePageData(r.Context(), &data)
		s.renderLocalAuthPage(w, data)
	case http.MethodPost:
		s.handleLocalLoginPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) localSignupHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data := localAuthPageData{
			Mode:          "signup",
			Title:         "Create account",
			Eyebrow:       "Open signup",
			SubmitLabel:   "Sign up",
			AlternateText: "Already have an account?",
			AlternateURL:  "/login",
			AlternateLink: "Log in",
			InviteToken:   strings.TrimSpace(r.URL.Query().Get("invite")),
		}
		s.decorateInvitePageData(r.Context(), &data)
		s.renderLocalAuthPage(w, data)
	case http.MethodPost:
		s.handleLocalSignupPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) decorateInvitePageData(ctx context.Context, data *localAuthPageData) {
	if data.InviteToken == "" {
		return
	}
	inv, ok, err := s.localInvitationFromRequest(ctx, data.InviteToken)
	if err != nil {
		data.Error = "Could not load invitation. Please try again."
		return
	}
	if !ok {
		data.Error = "This invitation is invalid or expired."
		return
	}
	data.Email = inv.row.Email
	data.InviteEmail = inv.row.Email
	data.InviteRole = inv.row.RoleSlug
}

func (s *Service) handleLocalSignupPost(w http.ResponseWriter, r *http.Request) {
	if err := requireLocalSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.requireLocalAuthCSRF(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if !s.allowLocalAuthAttempt(r) {
		http.Error(w, "too many attempts; wait a minute and try again", http.StatusTooManyRequests)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	first := strings.TrimSpace(r.FormValue("first_name"))
	last := strings.TrimSpace(r.FormValue("last_name"))
	password := r.FormValue("password")
	inviteToken := strings.TrimSpace(r.FormValue("invite"))
	data := localAuthPageData{
		Mode:          "signup",
		Title:         "Create account",
		Eyebrow:       "Open signup",
		SubmitLabel:   "Sign up",
		AlternateText: "Already have an account?",
		AlternateURL:  "/login",
		AlternateLink: "Log in",
		Email:         email,
		FirstName:     first,
		LastName:      last,
		InviteToken:   inviteToken,
	}
	if !validEmail(email) {
		data.Error = "Enter a valid email address."
		s.renderLocalAuthPage(w, data)
		return
	}
	if len(password) < localPasswordMinLen {
		data.Error = fmt.Sprintf("Password must be at least %d characters.", localPasswordMinLen)
		s.renderLocalAuthPage(w, data)
		return
	}

	var activeOrgID string
	var invite localInvitation
	if inviteToken != "" {
		inv, ok, err := s.localInvitationFromRequest(r.Context(), inviteToken)
		if err != nil {
			http.Error(w, "load invitation: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			data.Error = "This invitation is invalid or expired."
			s.renderLocalAuthPage(w, data)
			return
		}
		data.InviteEmail = inv.row.Email
		data.InviteRole = inv.row.RoleSlug
		if normalizeEmail(email) != inv.row.EmailNormalized {
			data.Error = "Use the email address that was invited."
			s.renderLocalAuthPage(w, data)
			return
		}
		invite = inv
		activeOrgID = inv.row.OrgID
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash password: "+err.Error(), http.StatusInternalServerError)
		return
	}
	userID, err := localID("user_local_")
	if err != nil {
		http.Error(w, "generate user id: "+err.Error(), http.StatusInternalServerError)
		return
	}
	user, err := s.local.q.CreateLocalAuthUser(r.Context(), sqlc.CreateLocalAuthUserParams{
		ID:              userID,
		Email:           email,
		EmailNormalized: normalizeEmail(email),
		PasswordHash:    hash,
		FirstName:       first,
		LastName:        last,
	})
	if isUniqueViolation(err) {
		data.Error = "An account already exists for that email. Log in instead."
		s.renderLocalAuthPage(w, data)
		return
	}
	if err != nil {
		http.Error(w, "create user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if inviteToken != "" {
		if err := s.localAcceptInvitationForUser(r.Context(), invite, user.ID); err != nil {
			http.Error(w, "accept invitation: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := s.localCreateSession(w, r, user.ID, activeOrgID); err != nil {
		http.Error(w, "create session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dest := "/onboarding"
	if activeOrgID != "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (s *Service) handleLocalLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := requireLocalSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.requireLocalAuthCSRF(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if !s.allowLocalAuthAttempt(r) {
		http.Error(w, "too many attempts; wait a minute and try again", http.StatusTooManyRequests)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	inviteToken := strings.TrimSpace(r.FormValue("invite"))
	data := localAuthPageData{
		Mode:          "login",
		Title:         "Log in",
		Eyebrow:       "Workspace sign-in",
		SubmitLabel:   "Log in",
		AlternateText: "Need an account?",
		AlternateURL:  "/signup",
		AlternateLink: "Sign up",
		Email:         email,
		InviteToken:   inviteToken,
	}
	if !validEmail(email) || password == "" {
		data.Error = "Email or password is incorrect."
		s.renderLocalAuthPage(w, data)
		return
	}
	user, err := s.local.q.GetLocalAuthUserByEmail(r.Context(), normalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		data.Error = "Email or password is incorrect."
		s.renderLocalAuthPage(w, data)
		return
	}
	if err != nil {
		http.Error(w, "load user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password)) != nil {
		data.Error = "Email or password is incorrect."
		s.renderLocalAuthPage(w, data)
		return
	}

	activeOrgID := ""
	if inviteToken != "" {
		inv, ok, err := s.localInvitationFromRequest(r.Context(), inviteToken)
		if err != nil {
			http.Error(w, "load invitation: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			data.Error = "This invitation is invalid or expired."
			s.renderLocalAuthPage(w, data)
			return
		}
		data.InviteEmail = inv.row.Email
		data.InviteRole = inv.row.RoleSlug
		if user.EmailNormalized != inv.row.EmailNormalized {
			data.Error = "Use the email address that was invited."
			s.renderLocalAuthPage(w, data)
			return
		}
		if err := s.localAcceptInvitationForUser(r.Context(), inv, user.ID); err != nil {
			http.Error(w, "accept invitation: "+err.Error(), http.StatusInternalServerError)
			return
		}
		activeOrgID = inv.row.OrgID
	} else {
		orgs, err := s.local.q.ListLocalAuthUserOrgs(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "list orgs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if len(orgs) > 0 {
			activeOrgID = orgs[0].OrgID
		}
	}
	if err := s.localCreateSession(w, r, user.ID, activeOrgID); err != nil {
		http.Error(w, "create session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dest := "/onboarding"
	if activeOrgID != "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (s *Service) localAcceptInvitationForUser(ctx context.Context, inv localInvitation, userID string) error {
	if inv.row.ID == "" {
		return errors.New("invitation is invalid or expired")
	}
	membershipID, err := localID("om_local_")
	if err != nil {
		return err
	}
	if _, err := s.local.q.CreateLocalAuthMembership(ctx, sqlc.CreateLocalAuthMembershipParams{
		ID:       membershipID,
		UserID:   userID,
		OrgID:    inv.row.OrgID,
		RoleSlug: inv.row.RoleSlug,
	}); err != nil {
		if !isUniqueViolation(err) {
			return err
		}
	}
	return s.local.q.AcceptLocalAuthInvitation(ctx, inv.row.ID)
}

func (s *Service) localCreateSession(w http.ResponseWriter, r *http.Request, userID, activeOrgID string) error {
	sessionID, err := localID("sess_local_")
	if err != nil {
		return err
	}
	secret, hash, err := localToken()
	if err != nil {
		return err
	}
	expires := s.nowFn().Add(localSessionTTL)
	if _, err := s.local.q.CreateLocalAuthSession(r.Context(), sqlc.CreateLocalAuthSessionParams{
		ID:          sessionID,
		UserID:      userID,
		TokenHash:   hash,
		ActiveOrgID: stringPtrIfNotEmpty(activeOrgID),
		ExpiresAt:   pgTimestamp(expires),
	}); err != nil {
		return err
	}
	s.setSessionCookie(w, localSessionCookieValue(sessionID, secret))
	return nil
}

func (s *Service) localMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, ok := s.localPrincipalFromCookie(r.Context(), cookie.Value)
		if !ok {
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		_ = s.local.q.TouchLocalAuthSession(r.Context(), p.SessionID)
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

func (s *Service) localPrincipalFromCookie(ctx context.Context, value string) (Principal, bool) {
	sessionID, secret, ok := parseLocalSessionCookieValue(value)
	if !ok {
		return Principal{}, false
	}
	row, err := s.local.q.GetLocalAuthSession(ctx, sessionID)
	if err != nil {
		return Principal{}, false
	}
	hash := hashLocalToken(secret)
	if subtle.ConstantTimeCompare(hash, row.TokenHash) != 1 {
		return Principal{}, false
	}
	p := Principal{
		UserID:    row.UserID,
		Email:     row.Email,
		SessionID: row.ID,
	}
	if row.ActiveOrgID != nil && *row.ActiveOrgID != "" && row.RoleSlug != nil && *row.RoleSlug != "" {
		p.OrgID = *row.ActiveOrgID
		p.Role = *row.RoleSlug
	}
	return p, true
}

func (s *Service) localRevokeCurrentSession(r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	sessionID, _, ok := parseLocalSessionCookieValue(cookie.Value)
	if !ok {
		return
	}
	if err := s.local.q.DeleteLocalAuthSession(r.Context(), sessionID); err != nil {
		// Logout should still complete if the DB row has already expired.
		return
	}
}

// DeleteExpiredLocalAuthSessions removes server-side local sessions whose TTL
// has elapsed. WorkOS mode has no local session table, so callers can invoke
// this unconditionally from process-level cleanup loops.
func (s *Service) DeleteExpiredLocalAuthSessions(ctx context.Context) error {
	if s.cfg.Bypass || !s.IsLocalMode() {
		return nil
	}
	return s.local.q.DeleteExpiredLocalAuthSessions(ctx)
}

func (s *Service) localCreateOrganization(ctx context.Context, name string) (string, error) {
	id, err := localID("org_local_")
	if err != nil {
		return "", err
	}
	org, err := s.local.q.CreateLocalAuthOrg(ctx, sqlc.CreateLocalAuthOrgParams{ID: id, Name: strings.TrimSpace(name)})
	if err != nil {
		return "", err
	}
	return org.ID, nil
}

func (s *Service) localGetOrganizationName(ctx context.Context, orgID string) (string, error) {
	org, err := s.local.q.GetLocalAuthOrg(ctx, orgID)
	if err != nil {
		return "", err
	}
	return org.Name, nil
}

func (s *Service) localUpdateOrganizationName(ctx context.Context, orgID, name string) error {
	_, err := s.local.q.UpdateLocalAuthOrgName(ctx, sqlc.UpdateLocalAuthOrgNameParams{
		ID:   orgID,
		Name: strings.TrimSpace(name),
	})
	return err
}

func (s *Service) localDeleteOrganization(ctx context.Context, orgID string) error {
	return s.local.q.DeleteLocalAuthOrg(ctx, orgID)
}

func (s *Service) localAddUserToOrganization(ctx context.Context, userID, orgID, roleSlug string) error {
	id, err := localID("om_local_")
	if err != nil {
		return err
	}
	_, err = s.local.q.CreateLocalAuthMembership(ctx, sqlc.CreateLocalAuthMembershipParams{
		ID:       id,
		UserID:   userID,
		OrgID:    orgID,
		RoleSlug: roleSlug,
	})
	return err
}

func (s *Service) localSendInvitation(ctx context.Context, email, orgID, roleSlug, inviterUserID string) (string, error) {
	email = strings.TrimSpace(email)
	if !validEmail(email) {
		return "", errors.New("valid email required")
	}
	id, err := localID("inv_local_")
	if err != nil {
		return "", err
	}
	token, tokenHash, err := localToken()
	if err != nil {
		return "", err
	}
	_, err = s.local.q.CreateLocalAuthInvitation(ctx, sqlc.CreateLocalAuthInvitationParams{
		ID:              id,
		OrgID:           orgID,
		Email:           email,
		EmailNormalized: normalizeEmail(email),
		RoleSlug:        roleSlug,
		TokenHash:       tokenHash,
		ExpiresAt:       pgTimestamp(s.nowFn().Add(localInvitationTTL)),
		CreatedBy:       stringPtrIfNotEmpty(inviterUserID),
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) localSwitchOrg(w http.ResponseWriter, r *http.Request, orgID string) error {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return err
	}
	p, ok := s.localPrincipalFromCookie(r.Context(), cookie.Value)
	if !ok {
		return errors.New("invalid local session")
	}
	if _, err := s.local.q.GetLocalAuthMembershipForUserOrg(r.Context(), sqlc.GetLocalAuthMembershipForUserOrgParams{
		UserID: p.UserID,
		OrgID:  orgID,
	}); err != nil {
		return err
	}
	if _, err := s.local.q.UpdateLocalAuthSessionOrg(r.Context(), sqlc.UpdateLocalAuthSessionOrgParams{
		ID:          p.SessionID,
		ActiveOrgID: &orgID,
	}); err != nil {
		return err
	}
	s.setSessionCookie(w, cookie.Value)
	return nil
}

func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if s.cfg.Bypass {
		return nil
	}
	if !s.IsLocalMode() {
		return errors.New("auth: local password changes are only available in local auth mode")
	}
	if len(newPassword) < localPasswordMinLen {
		return fmt.Errorf("password must be at least %d characters", localPasswordMinLen)
	}
	user, err := s.local.q.GetLocalAuthUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(currentPassword)) != nil {
		return ErrCurrentPasswordIncorrect
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.local.q.UpdateLocalAuthUserPassword(ctx, sqlc.UpdateLocalAuthUserPasswordParams{
		ID:           userID,
		PasswordHash: hash,
	})
}
