package artifacts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// LocalPath is the unauthenticated signed artifact endpoint served by
	// Hetchy when HETCHY_ARTIFACT_DIR is configured.
	LocalPath = "/api/artifacts/"

	localOpPut = "put"
	localOpGet = "get"

	// MaxLocalArtifactBytes caps one local proof artifact upload.
	MaxLocalArtifactBytes = 100 << 20
)

type localArtifactToken struct {
	Op          string `json:"op"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Expires     int64  `json:"expires"`
}

// LocalStore stores proof artifacts on the local filesystem and serves them
// through signed PUT/GET URLs.
type LocalStore struct {
	root    string
	baseURL string
	secret  []byte
	now     func() time.Time
}

// NewLocal returns a filesystem-backed artifact store. Empty root disables the
// feature with ErrNotConfigured so callers can share the S3 disabled path.
func NewLocal(root, publicBaseURL, secret string) (*LocalStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, ErrNotConfigured
	}
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if base == "" {
		return nil, errors.New("artifacts: HETCHY_PUBLIC_BASE_URL is required for local artifact storage")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("artifacts: invalid public base URL %q", publicBaseURL)
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("artifacts: SECRETS_ENCRYPTION_KEY is required for local artifact storage")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("artifacts: resolve local root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("artifacts: create local root: %w", err)
	}
	return &LocalStore{
		root:    abs,
		baseURL: base,
		secret:  []byte(secret),
		now:     time.Now,
	}, nil
}

func (s *LocalStore) MintSlots(_ context.Context, prefix string, req MintRequest) ([]Slot, error) {
	if s == nil {
		return nil, errors.New("artifacts: nil local store")
	}
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	spec, _ := specFor(req.Kind, req.ContentType)
	slots := make([]Slot, 0, req.Count)
	for i := range req.Count {
		key, err := safeArtifactKey(objectKey(prefix, spec, req.StartIndex+i))
		if err != nil {
			return nil, err
		}
		putURL, err := s.signedURL(localArtifactToken{
			Op:          localOpPut,
			Key:         key,
			ContentType: req.ContentType,
			Expires:     s.now().Add(PutExpiry).Unix(),
		})
		if err != nil {
			return nil, err
		}
		getURL, err := s.signedURL(localArtifactToken{
			Op:          localOpGet,
			Key:         key,
			ContentType: req.ContentType,
			Expires:     s.now().Add(GetExpiry).Unix(),
		})
		if err != nil {
			return nil, err
		}
		slots = append(slots, Slot{
			Kind:        req.Kind,
			ContentType: req.ContentType,
			PutURL:      putURL,
			GetURL:      getURL,
		})
	}
	return slots, nil
}

func (s *LocalStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, LocalPath)
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.handlePut(w, r, token)
	case http.MethodGet, http.MethodHead:
		s.handleGet(w, r, token)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *LocalStore) handlePut(w http.ResponseWriter, r *http.Request, rawToken string) {
	tok, err := s.verifyToken(rawToken, localOpPut)
	if err != nil {
		http.Error(w, "invalid artifact upload token", http.StatusForbidden)
		return
	}
	if got := mediaTypeOnly(r.Header.Get("Content-Type")); got != tok.ContentType {
		http.Error(w, "content type does not match artifact slot", http.StatusBadRequest)
		return
	}
	dst, err := s.pathForKey(tok.Key)
	if err != nil {
		http.Error(w, "invalid artifact path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		http.Error(w, "artifact directory unavailable", http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		http.Error(w, "artifact upload unavailable", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	closed := false
	closeTmp := func() error {
		if closed {
			return nil
		}
		closed = true
		return tmp.Close()
	}
	defer func() {
		_ = closeTmp()
		_ = os.Remove(tmpName)
	}()

	limited := http.MaxBytesReader(w, r.Body, MaxLocalArtifactBytes)
	if _, err := io.Copy(tmp, limited); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "artifact too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "artifact upload failed", http.StatusInternalServerError)
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		http.Error(w, "artifact upload failed", http.StatusInternalServerError)
		return
	}
	if err := closeTmp(); err != nil {
		http.Error(w, "artifact upload failed", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpName, dst); err != nil {
		http.Error(w, "artifact upload failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *LocalStore) handleGet(w http.ResponseWriter, r *http.Request, rawToken string) {
	tok, err := s.verifyToken(rawToken, localOpGet)
	if err != nil {
		http.Error(w, "invalid artifact download token", http.StatusForbidden)
		return
	}
	src, err := s.pathForKey(tok.Key)
	if err != nil {
		http.Error(w, "invalid artifact path", http.StatusBadRequest)
		return
	}
	f, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "artifact unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "artifact unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", tok.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, path.Base(tok.Key), stat.ModTime(), f)
}

func (s *LocalStore) signedURL(tok localArtifactToken) (string, error) {
	raw, err := json.Marshal(tok)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return s.baseURL + LocalPath + payload + "." + sig, nil
}

func (s *LocalStore) verifyToken(raw, op string) (localArtifactToken, error) {
	payload, sig, ok := strings.Cut(raw, ".")
	if !ok || payload == "" || sig == "" {
		return localArtifactToken{}, errors.New("missing signature")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return localArtifactToken{}, errors.New("bad signature")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return localArtifactToken{}, err
	}
	var tok localArtifactToken
	if err := json.Unmarshal(decoded, &tok); err != nil {
		return localArtifactToken{}, err
	}
	if tok.Op != op {
		return localArtifactToken{}, errors.New("wrong operation")
	}
	if tok.Expires <= s.now().Unix() {
		return localArtifactToken{}, errors.New("expired token")
	}
	if _, err := safeArtifactKey(tok.Key); err != nil {
		return localArtifactToken{}, err
	}
	if _, err := specFor(kindFromKey(tok.Key), tok.ContentType); err != nil {
		return localArtifactToken{}, err
	}
	return tok, nil
}

func (s *LocalStore) pathForKey(key string) (string, error) {
	key, err := safeArtifactKey(key)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(s.root, filepath.FromSlash(key))
	rel, err := filepath.Rel(s.root, dst)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", errors.New("artifact path escapes root")
	}
	return dst, nil
}

func safeArtifactKey(key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("%w: invalid artifact key", ErrInvalidRequest)
	}
	clean := path.Clean(key)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("%w: invalid artifact key", ErrInvalidRequest)
	}
	return clean, nil
}

func kindFromKey(key string) string {
	base := path.Base(key)
	switch {
	case strings.HasPrefix(base, "screenshot-"):
		return KindScreenshot
	case strings.HasPrefix(base, "recording-"):
		return KindRecording
	case strings.HasPrefix(base, "diagram-"):
		return KindDiagram
	default:
		return ""
	}
}

func mediaTypeOnly(v string) string {
	if before, _, ok := strings.Cut(v, ";"); ok {
		return strings.TrimSpace(before)
	}
	return strings.TrimSpace(v)
}
