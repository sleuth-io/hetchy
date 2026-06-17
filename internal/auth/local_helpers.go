package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validEmail(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" || strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return false
	}
	local, domain, ok := strings.Cut(addr.Address, "@")
	if !ok || local == "" {
		return false
	}
	domainParts := strings.Split(domain, ".")
	return len(domainParts) >= 2 && len(domainParts[len(domainParts)-1]) >= 2
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
