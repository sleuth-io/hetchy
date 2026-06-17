package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"
)

const (
	AuthModeWorkOS = "workos"
	AuthModeLocal  = "local"

	localSessionTTL     = 7 * 24 * time.Hour
	localInvitationTTL  = 7 * 24 * time.Hour
	localPasswordMinLen = 8
)

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

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validEmail(email string) bool {
	email = strings.TrimSpace(email)
	return email != "" && strings.Contains(email, "@")
}

func localID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func localToken() (string, []byte, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func hashLocalToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func localSessionCookieValue(sessionID, secret string) string {
	return sessionID + "." + secret
}

func parseLocalSessionCookieValue(value string) (sessionID, secret string, ok bool) {
	sessionID, secret, ok = strings.Cut(value, ".")
	if !ok || sessionID == "" || secret == "" {
		return "", "", false
	}
	return sessionID, secret, true
}

func pgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func stringPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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
		if err := s.localAcceptInvitationForUser(r.Context(), inviteToken, user.ID); err != nil {
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
		if err := s.localAcceptInvitationForUser(r.Context(), inviteToken, user.ID); err != nil {
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

func (s *Service) localAcceptInvitationForUser(ctx context.Context, token, userID string) error {
	inv, ok, err := s.localInvitationFromRequest(ctx, token)
	if err != nil {
		return err
	}
	if !ok {
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
		return err
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
		return errors.New("current password is incorrect")
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

func requireLocalSameOrigin(r *http.Request) error {
	host := r.Host
	if host == "" {
		return errors.New("missing host header")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return fmt.Errorf("invalid origin: %w", err)
		}
		if u.Host != host {
			return fmt.Errorf("origin %q does not match host %q", u.Host, host)
		}
		return nil
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		u, err := url.Parse(referer)
		if err != nil {
			return fmt.Errorf("invalid referer: %w", err)
		}
		if u.Host != host {
			return fmt.Errorf("referer %q does not match host %q", u.Host, host)
		}
		return nil
	}
	return errors.New("missing Origin and Referer headers")
}

type localAuthPageData struct {
	Mode          string
	Title         string
	Eyebrow       string
	SubmitLabel   string
	AlternateText string
	AlternateURL  string
	AlternateLink string
	Email         string
	FirstName     string
	LastName      string
	InviteToken   string
	InviteEmail   string
	InviteRole    string
	Error         string
}

func (s *Service) renderLocalAuthPage(w http.ResponseWriter, data localAuthPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := localAuthTemplate.Execute(w, data); err != nil {
		http.Error(w, "render auth page: "+err.Error(), http.StatusInternalServerError)
	}
}

var localAuthTemplate = template.Must(template.New("local-auth").Parse(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - Hetchy</title>
<script src="/assets/theme_bootstrap.js"></script>
<link rel="stylesheet" href="/assets/landing.css">
<style>
.auth-form{display:grid;gap:.8rem;margin-top:1.1rem}
.auth-form label{display:grid;gap:.3rem;color:#4f5a68;font-size:.84rem;font-weight:600}
.auth-form input{width:100%;min-height:2.55rem;border:1px solid #cad5e3;border-radius:8px;padding:.65rem .75rem;background:#fff;color:#111418;font:inherit}
.auth-form input:focus{outline:none;border-color:#0b93f6;box-shadow:0 0 0 3px rgba(11,147,246,.14)}
.auth-form .name-row{display:grid;grid-template-columns:1fr 1fr;gap:.75rem}
.auth-form button{min-height:2.75rem;border:0;border-radius:8px;background:#0b93f6;color:#fff;font:inherit;font-weight:650;cursor:pointer}
.auth-form button:hover{background:#087fd8}
.auth-note{margin:.9rem 0 0;color:#5e6878;font-size:.9rem;line-height:1.45}
.auth-note a{color:#0b5ec0;font-weight:650}
.auth-alert{margin:1rem 0 0;padding:.7rem .8rem;border-radius:8px;background:#fff1f1;color:#9f1d1d;border:1px solid #ffd0d0;font-size:.9rem}
.invite-banner{margin:1rem 0 0;padding:.7rem .8rem;border-radius:8px;background:#eff8ff;color:#164b78;border:1px solid #cfe8ff;font-size:.9rem}
html.is-dark .auth-form label{color:#c6c6cb}
html.is-dark .auth-form input{background:#1c1c1f;color:#f5f5f7;border-color:#383842}
html.is-dark .auth-form input:focus{border-color:#7dc4ff;box-shadow:0 0 0 3px rgba(125,196,255,.12)}
html.is-dark .auth-form button{background:#7dc4ff;color:#101317}
html.is-dark .auth-form button:hover{background:#9fd3ff}
html.is-dark .auth-note{color:#b8b8bc}
html.is-dark .auth-note a{color:#7dc4ff}
html.is-dark .auth-alert{background:#331717;color:#ffb4b4;border-color:#5b2828}
html.is-dark .invite-banner{background:#16273a;color:#cde8ff;border-color:#24445f}
@media (max-width:480px){.auth-form .name-row{grid-template-columns:1fr}}
</style>
</head>
<body>
<main class="entry-shell">
  <section class="entry-panel" aria-labelledby="auth-title">
    <a class="brand" href="/" aria-label="Hetchy home">
      <img class="brand-mark" src="data:image/svg+xml;utf8,%3Csvg%20xmlns%3D%27http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%27%20viewBox%3D%270%200%2032%2032%27%3E%3Crect%20width%3D%2732%27%20height%3D%2732%27%20rx%3D%276%27%20fill%3D%27%230d1117%27%2F%3E%3Cg%20fill%3D%27%237dc4ff%27%3E%3Crect%20x%3D%276%27%20y%3D%276%27%20width%3D%276%27%20height%3D%2720%27%2F%3E%3Crect%20x%3D%2720%27%20y%3D%276%27%20width%3D%276%27%20height%3D%2720%27%2F%3E%3Crect%20x%3D%276%27%20y%3D%2714%27%20width%3D%2720%27%20height%3D%274%27%2F%3E%3C%2Fg%3E%3C%2Fsvg%3E" alt="">
      <span>Hetchy</span>
    </a>
    <div class="entry-copy">
      <p class="eyebrow">{{.Eyebrow}}</p>
      <h1 id="auth-title">{{.Title}}</h1>
      <p class="lede">Use your email and password to access Hetchy.</p>
    </div>
    {{if .Error}}<div class="auth-alert">{{.Error}}</div>{{end}}
    {{if .InviteEmail}}<div class="invite-banner">Invitation for {{.InviteEmail}} as {{.InviteRole}}.</div>{{end}}
    <form method="POST" class="auth-form" action="/{{.Mode}}">
      {{if .InviteToken}}<input type="hidden" name="invite" value="{{.InviteToken}}">{{end}}
      {{if eq .Mode "signup"}}
      <div class="name-row">
        <label>First name
          <input type="text" name="first_name" value="{{.FirstName}}" autocomplete="given-name">
        </label>
        <label>Last name
          <input type="text" name="last_name" value="{{.LastName}}" autocomplete="family-name">
        </label>
      </div>
      {{end}}
      <label>Email
        <input type="email" name="email" value="{{.Email}}" autocomplete="email" required>
      </label>
      <label>Password
        <input type="password" name="password" autocomplete="{{if eq .Mode "signup"}}new-password{{else}}current-password{{end}}" required minlength="8">
      </label>
      <button type="submit">{{.SubmitLabel}}</button>
    </form>
    <p class="auth-note">{{.AlternateText}} <a href="{{.AlternateURL}}{{if .InviteToken}}?invite={{.InviteToken}}{{end}}">{{.AlternateLink}}</a>.</p>
  </section>
</main>
</body>
</html>`))
