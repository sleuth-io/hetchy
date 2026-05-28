package sxsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerCheckCacheUsesConfiguredDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sx-cache")
	t.Setenv("SX_CACHE_DIR", dir)
	m := NewManagerWithOptions(nil, nil, nil, nil, Options{
		CacheDir:          dir,
		CacheMinFreeBytes: 1,
	})
	if got := os.Getenv("SX_CACHE_DIR"); got != dir {
		t.Fatalf("test SX_CACHE_DIR = %q, want %q", got, dir)
	}
	status, err := m.CheckCache()
	if err != nil {
		t.Fatalf("CheckCache: %v", err)
	}
	if !status.Configured || status.Path != dir || status.AvailableBytes == 0 {
		t.Fatalf("cache status = %+v", status)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("cache dir not created: %v", err)
	}
}

func TestManagerCheckCacheRejectsLowFreeSpace(t *testing.T) {
	dir := t.TempDir()
	m := NewManagerWithOptions(nil, nil, nil, nil, Options{
		CacheDir:          dir,
		CacheMinFreeBytes: ^uint64(0),
	})
	if _, err := m.CheckCache(); err == nil || !strings.Contains(err.Error(), "need at least") {
		t.Fatalf("CheckCache err = %v, want low-space error", err)
	}
}

func TestGitVaultGuardTimesOutWaitingForOrgLock(t *testing.T) {
	m := NewManagerWithOptions(nil, nil, nil, nil, Options{
		CacheDir:            t.TempDir(),
		GitOperationTimeout: 10 * time.Millisecond,
		MaxConcurrentGitOps: 1,
	})
	if err := m.acquireOrgLock(context.Background(), "org_1"); err != nil {
		t.Fatalf("acquireOrgLock: %v", err)
	}
	defer m.releaseOrgLock("org_1")

	err := m.withGitVaultGuard(context.Background(), "org_1", func(context.Context) error {
		return errors.New("should not run")
	})
	if err == nil || !strings.Contains(err.Error(), "org lock") {
		t.Fatalf("withGitVaultGuard err = %v, want org lock timeout", err)
	}
}
