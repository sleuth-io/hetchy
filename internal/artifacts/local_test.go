package artifacts

import (
	"bytes"
	"context"
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

func TestNewLocalMissingRootReturnsSentinel(t *testing.T) {
	store, err := NewLocal("", "https://app.example.test", strings.Repeat("s", 32))
	if err != ErrNotConfigured {
		t.Fatalf("NewLocal error = %v, want ErrNotConfigured", err)
	}
	if store != nil {
		t.Fatalf("store = %#v, want nil", store)
	}
}
