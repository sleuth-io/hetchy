package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type localAuthRateEntry struct {
	Count    int
	ResetAt  time.Time
	LastSeen time.Time
}

func (s *Service) issueLocalAuthCSRFToken(w http.ResponseWriter) (string, error) {
	token, _, err := localToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     localAuthCSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.cfg.CookieSecure,
		Expires:  s.nowFn().Add(localAuthCSRFTokenTTL),
		MaxAge:   int(localAuthCSRFTokenTTL.Seconds()),
	})
	return token, nil
}

func (s *Service) requireLocalAuthCSRF(r *http.Request) error {
	cookie, err := r.Cookie(localAuthCSRFCookieName)
	if err != nil || cookie.Value == "" {
		return errors.New("missing form token")
	}
	token := strings.TrimSpace(r.FormValue("csrf_token"))
	if token == "" {
		return errors.New("missing form token")
	}
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
		return errors.New("invalid form token")
	}
	return nil
}

func (s *Service) allowLocalAuthAttempt(r *http.Request) bool {
	key := r.URL.Path + "|" + s.localAuthClientIP(r)
	now := s.nowFn()
	s.localRateMu.Lock()
	defer s.localRateMu.Unlock()
	if s.localRate == nil {
		s.localRate = map[string]localAuthRateEntry{}
	}
	if len(s.localRate) > localAuthRateMaxEntries {
		s.pruneLocalAuthRateEntries(now)
	}
	entry := s.localRate[key]
	if entry.ResetAt.IsZero() || !now.Before(entry.ResetAt) {
		entry = localAuthRateEntry{ResetAt: now.Add(localAuthRateWindow)}
	}
	entry.Count++
	entry.LastSeen = now
	s.localRate[key] = entry
	return entry.Count <= localAuthRateLimit
}

func (s *Service) pruneLocalAuthRateEntries(now time.Time) {
	for key, entry := range s.localRate {
		if !now.Before(entry.ResetAt) {
			delete(s.localRate, key)
		}
	}
}

func (s *Service) localAuthClientIP(r *http.Request) string {
	if s.cfg.TrustedProxy {
		if ip := forwardedHeaderIP(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
		if ip := singleHeaderIP(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	return remoteAddrIP(r)
}

func forwardedHeaderIP(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return singleHeaderIP(first)
}

func singleHeaderIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func remoteAddrIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if remote := strings.TrimSpace(r.RemoteAddr); remote != "" {
		return remote
	}
	return "unknown"
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
	return nil
}
