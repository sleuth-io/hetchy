package sxsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// CacheStatus is the startup/runtime view of the SX cache root Hetchy gives to
// the SX library.
type CacheStatus struct {
	Configured     bool
	Path           string
	AvailableBytes uint64
	MinFreeBytes   uint64
}

func (m *Manager) CheckCache() (CacheStatus, error) {
	if m == nil {
		return CacheStatus{}, nil
	}
	return checkCache(m.cacheDir, m.cacheMinFreeBytes)
}

func (m *Manager) withGitVaultGuard(ctx context.Context, orgID string, fn func(context.Context) error) error {
	if m == nil {
		return ErrNotConfigured
	}
	if m.gitOperationTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.gitOperationTimeout)
		defer cancel()
	}
	if _, err := checkCache(m.cacheDir, m.cacheMinFreeBytes); err != nil {
		return err
	}
	if err := m.acquireGlobalGitSlot(ctx); err != nil {
		return err
	}
	defer m.releaseGlobalGitSlot()
	if err := m.acquireOrgLock(ctx, orgID); err != nil {
		return err
	}
	defer m.releaseOrgLock(orgID)
	return fn(ctx)
}

func (m *Manager) withGitVaultGuardIfConfigured(ctx context.Context, orgID string, fn func(context.Context) error) error {
	if m == nil {
		return ErrNotConfigured
	}
	if sxKey, err := m.skillsNewKey(ctx, orgID); err != nil {
		return err
	} else if sxKey != "" {
		return fn(ctx)
	}
	gv, err := m.GitVault(ctx, orgID)
	if err != nil {
		return err
	}
	if !gv.Configured {
		return fn(ctx)
	}
	return m.withGitVaultGuard(ctx, orgID, fn)
}

func (m *Manager) acquireGlobalGitSlot(ctx context.Context) error {
	if m == nil || m.gitOps == nil {
		return nil
	}
	select {
	case m.gitOps <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sx git vault operation timed out waiting for global slot: %w", ctx.Err())
	}
}

func (m *Manager) releaseGlobalGitSlot() {
	if m == nil || m.gitOps == nil {
		return
	}
	select {
	case <-m.gitOps:
	default:
	}
}

func (m *Manager) acquireOrgLock(ctx context.Context, orgID string) error {
	lock := m.orgLock(orgID)
	select {
	case lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sx git vault operation timed out waiting for org lock: %w", ctx.Err())
	}
}

func (m *Manager) releaseOrgLock(orgID string) {
	lock := m.orgLock(orgID)
	select {
	case <-lock:
	default:
	}
}

func (m *Manager) orgLock(orgID string) chan struct{} {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		orgID = "_global"
	}
	m.orgLocksMu.Lock()
	defer m.orgLocksMu.Unlock()
	if m.orgLocks == nil {
		m.orgLocks = map[string]chan struct{}{}
	}
	lock := m.orgLocks[orgID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		m.orgLocks[orgID] = lock
	}
	return lock
}

func checkCache(cacheDir string, minFreeBytes uint64) (CacheStatus, error) {
	cacheDir = strings.TrimSpace(cacheDir)
	status := CacheStatus{Path: cacheDir, MinFreeBytes: minFreeBytes}
	if cacheDir == "" {
		return status, nil
	}
	status.Configured = true
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return status, fmt.Errorf("sx cache mkdir %s: %w", cacheDir, err)
	}
	probe := filepath.Join(cacheDir, ".hetchy-write-test")
	if err := os.WriteFile(probe, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
		return status, fmt.Errorf("sx cache not writable %s: %w", cacheDir, err)
	}
	_ = os.Remove(probe)

	available, err := availableBytes(cacheDir)
	if err != nil {
		return status, err
	}
	status.AvailableBytes = available
	if minFreeBytes > 0 && available < minFreeBytes {
		return status, fmt.Errorf("sx cache has %s free at %s; need at least %s", formatBytes(available), cacheDir, formatBytes(minFreeBytes))
	}
	return status, nil
}

func availableBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("sx cache statfs %s: %w", path, err)
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func formatBytes(n uint64) string {
	const mib = 1024 * 1024
	if n < mib {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%d MiB", n/mib)
}
