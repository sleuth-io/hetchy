package artifacts

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalStoreMintPutAndGet(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root, "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(slots))
	}
	if !strings.HasPrefix(slots[0].PutURL, "https://app.example.test"+LocalPath) {
		t.Fatalf("put URL = %q", slots[0].PutURL)
	}

	putURL, err := url.Parse(slots[0].PutURL)
	if err != nil {
		t.Fatalf("parse put URL: %v", err)
	}
	putReq := httptest.NewRequest(http.MethodPut, putURL.Path, bytes.NewReader([]byte("png bytes")))
	putReq.Header.Set("Content-Type", ContentTypePNG)
	putRec := httptest.NewRecorder()
	store.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d body=%q", putRec.Code, putRec.Body.String())
	}

	stored := filepath.Join(root, "org_1", "42", "req_1", "screenshot-000.png")
	raw, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("read stored artifact: %v", err)
	}
	if string(raw) != "png bytes" {
		t.Fatalf("stored bytes = %q", raw)
	}

	getURL, err := url.Parse(slots[0].GetURL)
	if err != nil {
		t.Fatalf("parse get URL: %v", err)
	}
	getReq := httptest.NewRequest(http.MethodGet, getURL.Path, nil)
	getRec := httptest.NewRecorder()
	store.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", getRec.Code, getRec.Body.String())
	}
	if got := getRec.Header().Get("Content-Type"); got != ContentTypePNG {
		t.Fatalf("GET content type = %q, want %q", got, ContentTypePNG)
	}
	if got := getRec.Body.String(); got != "png bytes" {
		t.Fatalf("GET body = %q", got)
	}

	headReq := httptest.NewRequest(http.MethodHead, getURL.Path, nil)
	headRec := httptest.NewRecorder()
	store.ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d body=%q", headRec.Code, headRec.Body.String())
	}
	if got := headRec.Body.String(); got != "" {
		t.Fatalf("HEAD body = %q, want empty", got)
	}
}

func TestLocalStoreRejectsWrongContentType(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	putURL, _ := url.Parse(slots[0].PutURL)
	req := httptest.NewRequest(http.MethodPut, putURL.Path, bytes.NewReader([]byte("not png")))
	req.Header.Set("Content-Type", ContentTypeMP4)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", rec.Code)
	}
}

func TestLocalStoreAcceptsContentTypeParameters(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	putURL, _ := url.Parse(slots[0].PutURL)
	req := httptest.NewRequest(http.MethodPut, putURL.Path, bytes.NewReader([]byte("png")))
	req.Header.Set("Content-Type", ContentTypePNG+"; charset=binary")
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d body=%q, want 201", rec.Code, rec.Body.String())
	}
}

func TestLocalStoreSignedTokensAreOperationSpecific(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}

	getURL, _ := url.Parse(slots[0].GetURL)
	putWithGetToken := httptest.NewRequest(http.MethodPut, getURL.Path, bytes.NewReader([]byte("png")))
	putWithGetToken.Header.Set("Content-Type", ContentTypePNG)
	putRec := httptest.NewRecorder()
	store.ServeHTTP(putRec, putWithGetToken)
	if putRec.Code != http.StatusForbidden {
		t.Fatalf("PUT with GET token status = %d body=%q, want 403", putRec.Code, putRec.Body.String())
	}

	putURL, _ := url.Parse(slots[0].PutURL)
	getWithPutToken := httptest.NewRequest(http.MethodGet, putURL.Path, nil)
	getRec := httptest.NewRecorder()
	store.ServeHTTP(getRec, getWithPutToken)
	if getRec.Code != http.StatusForbidden {
		t.Fatalf("GET with PUT token status = %d body=%q, want 403", getRec.Code, getRec.Body.String())
	}
}

func TestLocalStoreRejectsTamperedSignedToken(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	getURL, _ := url.Parse(slots[0].GetURL)
	tampered := getURL.Path
	if prefix, ok := strings.CutSuffix(tampered, "A"); ok {
		tampered = prefix + "B"
	} else {
		tampered += "A"
	}
	req := httptest.NewRequest(http.MethodGet, tampered, nil)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered GET status = %d body=%q, want 403", rec.Code, rec.Body.String())
	}
}

func TestLocalStoreUploadCopyErrorStatus(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	putURL, _ := url.Parse(slots[0].PutURL)

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "too large", err: &http.MaxBytesError{Limit: MaxLocalArtifactBytes}, want: http.StatusRequestEntityTooLarge},
		{name: "read failed", err: errors.New("read failed"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, putURL.Path, errReader{err: tc.err})
			req.Header.Set("Content-Type", ContentTypePNG)
			rec := httptest.NewRecorder()
			store.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("PUT status = %d body=%q, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestLocalStoreRejectsExpiredToken(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	store.now = func() time.Time { return now.Add(PutExpiry + time.Second) }
	putURL, _ := url.Parse(slots[0].PutURL)
	req := httptest.NewRequest(http.MethodPut, putURL.Path, bytes.NewReader([]byte("late")))
	req.Header.Set("Content-Type", ContentTypePNG)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired PUT status = %d, want 403", rec.Code)
	}
}

func TestLocalStoreGetMissingArtifactReturnsNotFound(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	getURL, _ := url.Parse(slots[0].GetURL)
	req := httptest.NewRequest(http.MethodGet, getURL.Path, nil)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d body=%q, want 404", rec.Code, rec.Body.String())
	}
}

func TestLocalStoreRejectsUnsupportedMethod(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	slots, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	})
	if err != nil {
		t.Fatalf("MintSlots: %v", err)
	}
	putURL, _ := url.Parse(slots[0].PutURL)
	req := httptest.NewRequest(http.MethodPost, putURL.Path, nil)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d body=%q, want 405", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD, PUT" {
		t.Fatalf("Allow = %q", got)
	}
}

func TestNilLocalStoreReturnsNotFound(t *testing.T) {
	var store *LocalStore
	req := httptest.NewRequest(http.MethodGet, LocalPath, nil)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d body=%q, want 404", rec.Code, rec.Body.String())
	}
}

func TestLocalStoreRejectsMalformedRequests(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	for _, tc := range []struct {
		name   string
		path   string
		method string
		want   int
	}{
		{name: "missing token", path: LocalPath, method: http.MethodGet, want: http.StatusNotFound},
		{name: "nested token", path: LocalPath + "bad/token", method: http.MethodGet, want: http.StatusNotFound},
		{name: "invalid token", path: LocalPath + "not-a-token", method: http.MethodGet, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			store.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s status = %d body=%q, want %d", tc.method, rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSafeArtifactKeyRejectsTraversal(t *testing.T) {
	for _, key := range []string{
		"",
		"../etc/passwd",
		"../../root",
		"/etc/shadow",
		"a/../../b",
		`a\..\..\b`,
	} {
		t.Run(key, func(t *testing.T) {
			if got, err := safeArtifactKey(key); err == nil {
				t.Fatalf("safeArtifactKey(%q) = %q, want error", key, got)
			}
		})
	}
}

func TestLocalStoreRejectsSignedTraversalToken(t *testing.T) {
	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	store.now = func() time.Time { return time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC) }
	putURL, err := store.signedURL(localArtifactToken{
		Op:          localOpPut,
		Key:         "../outside.png",
		ContentType: ContentTypePNG,
		Expires:     store.now().Add(PutExpiry).Unix(),
	})
	if err != nil {
		t.Fatalf("signedURL: %v", err)
	}
	parsed, _ := url.Parse(putURL)
	req := httptest.NewRequest(http.MethodPut, parsed.Path, bytes.NewReader([]byte("png")))
	req.Header.Set("Content-Type", ContentTypePNG)
	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT status = %d body=%q, want 403", rec.Code, rec.Body.String())
	}
}

func TestNewLocalValidatesConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		root    string
		baseURL string
		secret  string
	}{
		{name: "missing base URL", root: t.TempDir(), secret: strings.Repeat("s", 32)},
		{name: "invalid base URL", root: t.TempDir(), baseURL: "ftp://app.example.test", secret: strings.Repeat("s", 32)},
		{name: "missing secret", root: t.TempDir(), baseURL: "https://app.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewLocal(tc.root, tc.baseURL, tc.secret)
			if err == nil {
				t.Fatalf("NewLocal returned store %#v, want error", store)
			}
		})
	}
}

func TestLocalStoreMintSlotsValidation(t *testing.T) {
	if _, err := (*LocalStore)(nil).MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	}); err == nil {
		t.Fatalf("nil store MintSlots error = nil")
	}

	store, err := NewLocal(t.TempDir(), "https://app.example.test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if _, err := store.MintSlots(context.Background(), "../org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	}); err == nil {
		t.Fatalf("unsafe prefix MintSlots error = nil")
	}
	if _, err := store.MintSlots(context.Background(), "org_1/42/req_1", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
	}); err == nil {
		t.Fatalf("invalid request MintSlots error = nil")
	}
}

func TestNewLocalMissingRootReturnsSentinel(t *testing.T) {
	store, err := NewLocal("", "https://app.example.test", strings.Repeat("s", 32))
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NewLocal error = %v, want ErrNotConfigured", err)
	}
	if store != nil {
		t.Fatalf("store = %#v, want nil", store)
	}
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}
