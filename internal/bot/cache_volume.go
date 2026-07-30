package bot

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

const (
	daytonaCacheMountPath            = "/home/daytona/.hetchy-cache"
	defaultCacheVolumePrefix         = "hetchy-cache"
	defaultCachePruneDays            = 10
	defaultDaytonaAutoArchiveMinutes = 60
	daytonaCacheVolumeTimeout        = 60 * time.Second
	daytonaCacheVolumeNameMax        = 63

	daytonaCacheDevVolumeCount  = 10
	daytonaCacheStgVolumeCount  = 10
	daytonaCacheProdVolumeCount = 80

	daytonaSandboxLabelEnv           = "hetchy_env"
	daytonaSandboxLabelOrgID         = "hetchy_org_id"
	daytonaSandboxLabelCacheVolumeID = "hetchy_cache_volume_id"
)

type daytonaCacheVolumeService interface {
	Get(context.Context, string) (*types.Volume, error)
	Create(context.Context, string) (*types.Volume, error)
	WaitForReady(context.Context, *types.Volume, time.Duration) (*types.Volume, error)
}

func (b *Bot) resolveDaytonaCacheMount(ctx context.Context, oc orgcfg.Config, repo repoCtx) (types.VolumeMount, bool) {
	if b == nil || b.cfg.DaytonaCacheVolumesDisabled {
		return types.VolumeMount{}, false
	}
	if !repoCacheIdentityAvailable(oc, repo) {
		if b.log != nil {
			b.log.Warn("daytona cache volume skipped: missing org/repo identity",
				"org", oc.OrgID, "install_id", repo.InstallID, "repo_id", repo.RepoID, "repo", repo.Slug)
		}
		return types.VolumeMount{}, false
	}
	if b.cacheVols == nil {
		if b.log != nil {
			b.log.Warn("daytona cache volume skipped: volume service unavailable", "org", oc.OrgID, "repo", repo.Slug)
		}
		return types.VolumeMount{}, false
	}

	route := daytonaCacheRoute(b.cfg.DaytonaCacheVolumePrefix, b.cfg.Env, oc.OrgID)
	name := route.Name
	volume, err := b.getOrCreateDaytonaCacheVolume(ctx, name)
	if err != nil {
		if b.log != nil {
			b.log.Warn("daytona cache volume unavailable",
				"org", oc.OrgID, "repo", repo.Slug, "volume", name,
				"cache_env", route.Env, "cache_slot", route.Slot, "error", err)
		}
		return types.VolumeMount{}, false
	}
	if volume == nil {
		if b.log != nil {
			b.log.Warn("daytona cache volume unavailable: nil volume", "org", oc.OrgID, "repo", repo.Slug, "volume", name)
		}
		return types.VolumeMount{}, false
	}
	if strings.TrimSpace(volume.ID) == "" {
		if b.log != nil {
			b.log.Warn("daytona cache volume unavailable: empty volume id", "org", oc.OrgID, "repo", repo.Slug, "volume", name)
		}
		return types.VolumeMount{}, false
	}

	subpath := daytonaCacheSubpath(oc, repo)
	return types.VolumeMount{
		VolumeID:  volume.ID,
		MountPath: daytonaCacheMountPath,
		Subpath:   &subpath,
	}, true
}

func (b *Bot) getOrCreateDaytonaCacheVolume(ctx context.Context, name string) (*types.Volume, error) {
	volume, err := b.cacheVols.Get(ctx, name)
	switch {
	case err == nil:
	case isDaytonaNotFound(err):
		volume, err = b.cacheVols.Create(ctx, name)
		if err != nil {
			if !isDaytonaConflict(err) {
				return nil, fmt.Errorf("create volume %s: %w", name, err)
			}
			volume, err = b.cacheVols.Get(ctx, name)
			if err != nil {
				return nil, fmt.Errorf("get volume after create conflict %s: %w", name, err)
			}
		}
	default:
		return nil, fmt.Errorf("get volume %s: %w", name, err)
	}
	if volume == nil {
		return nil, fmt.Errorf("volume %s returned empty response", name)
	}

	volume, err = b.cacheVols.WaitForReady(ctx, volume, daytonaCacheVolumeTimeout)
	if err != nil {
		return nil, fmt.Errorf("wait for volume %s: %w", name, err)
	}
	if volume == nil {
		return nil, fmt.Errorf("volume %s returned empty ready response", name)
	}
	return volume, nil
}

func addDaytonaCacheEnv(env map[string]string, cfg Config, oc orgcfg.Config, repo repoCtx, mounted bool) {
	if env == nil {
		return
	}
	env["HETCHY_CACHE_PRUNE_DAYS"] = strconv.Itoa(cachePruneDays(cfg))
	if cfg.DaytonaCacheVolumesDisabled {
		env["HETCHY_CACHE_STATUS"] = "disabled"
		return
	}
	if !repoCacheIdentityAvailable(oc, repo) {
		env["HETCHY_CACHE_STATUS"] = "unavailable"
		return
	}
	if mounted {
		env["HETCHY_CACHE_STATUS"] = "mounted"
		env["HETCHY_CACHE_DIR"] = daytonaCacheMountPath
		return
	}
	env["HETCHY_CACHE_STATUS"] = "unavailable"
}

func repoCacheIdentityAvailable(oc orgcfg.Config, repo repoCtx) bool {
	return strings.TrimSpace(oc.OrgID) != "" && repo.InstallID != 0 && repo.RepoID != 0
}

func cachePruneDays(cfg Config) int {
	if cfg.DaytonaCachePruneDays > 0 {
		return cfg.DaytonaCachePruneDays
	}
	return defaultCachePruneDays
}

type daytonaCacheVolumeRoute struct {
	Name      string
	Env       string
	Slot      int
	SlotCount int
}

// daytonaCacheRoute maps each Hetchy org into a fixed per-environment
// volume pool. The pool size totals 100 volumes across dev/stg/prod so a
// single shared Daytona org stays inside Daytona's hard volume limit.
func daytonaCacheRoute(prefix, env, orgID string) daytonaCacheVolumeRoute {
	cacheEnv := daytonaSandboxEnv(env)
	slotCount := daytonaCacheVolumeCount(cacheEnv)
	slot := daytonaCacheVolumeSlot(orgID, slotCount)
	return daytonaCacheVolumeRoute{
		Name:      daytonaCacheVolumeName(prefix, cacheEnv, slot),
		Env:       cacheEnv,
		Slot:      slot,
		SlotCount: slotCount,
	}
}

func daytonaCacheVolumeName(prefix, env string, slot int) string {
	env = daytonaSandboxEnv(env)
	slotSuffix := fmt.Sprintf("%02d", max(slot, 0))

	prefix = sanitizeDaytonaNamePart(prefix)
	if prefix == "" {
		prefix = defaultCacheVolumePrefix
	}
	maxPrefixLen := max(daytonaCacheVolumeNameMax-len(env)-len(slotSuffix)-2, 1)
	if len(prefix) > maxPrefixLen {
		prefix = strings.Trim(prefix[:maxPrefixLen], "-")
		if prefix == "" {
			prefix = "cache"
		}
	}
	return prefix + "-" + env + "-" + slotSuffix
}

func daytonaSandboxEnv(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "dev", "development":
		return "dev"
	case "stg", "stage", "staging":
		return "stg"
	default:
		return "prod"
	}
}

func daytonaCacheVolumeCount(env string) int {
	switch daytonaSandboxEnv(env) {
	case "dev":
		return daytonaCacheDevVolumeCount
	case "stg":
		return daytonaCacheStgVolumeCount
	default:
		return daytonaCacheProdVolumeCount
	}
}

func daytonaCacheVolumeSlot(orgID string, slotCount int) int {
	if slotCount <= 1 {
		return 0
	}
	hash := sha256.Sum256([]byte(orgID))
	n := binary.BigEndian.Uint64(hash[:8])
	return int(n % uint64(slotCount))
}

// daytonaCacheSubpath is the tenant boundary inside a pooled Daytona
// volume. The org hash must stay first so two Hetchy orgs can never see
// each other's repo caches even if they route to the same physical volume.
func daytonaCacheSubpath(oc orgcfg.Config, repo repoCtx) string {
	return fmt.Sprintf("orgs/%s/repos/%d/%d/default", daytonaCacheOrgHash(oc.OrgID), repo.InstallID, repo.RepoID)
}

func daytonaCacheOrgHash(orgID string) string {
	hash := sha256.Sum256([]byte(orgID))
	return hex.EncodeToString(hash[:])[:16]
}

func daytonaSandboxLabels(cfg Config, oc orgcfg.Config, cacheVolumeID string) map[string]string {
	labels := map[string]string{
		daytonaSandboxLabelEnv: daytonaSandboxEnv(cfg.Env),
	}
	if strings.TrimSpace(oc.OrgID) != "" {
		labels[daytonaSandboxLabelOrgID] = oc.OrgID
	}
	if strings.TrimSpace(cacheVolumeID) != "" {
		labels[daytonaSandboxLabelCacheVolumeID] = cacheVolumeID
	}
	return labels
}

func sanitizeDaytonaNamePart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastHyphen := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func isDaytonaNotFound(err error) bool {
	if _, ok := errors.AsType[*sdkerrors.DaytonaNotFoundError](err); ok {
		return true
	}
	var daytonaErr *sdkerrors.DaytonaError
	return errors.As(err, &daytonaErr) && daytonaErr.StatusCode == http.StatusNotFound
}

func isDaytonaConflict(err error) bool {
	var daytonaErr *sdkerrors.DaytonaError
	if !errors.As(err, &daytonaErr) {
		return false
	}
	if daytonaErr.StatusCode == http.StatusConflict {
		return true
	}
	// Daytona returns 400, not 409, for duplicate volume names.
	msg := strings.ToLower(daytonaErr.Message)
	return daytonaErr.StatusCode == http.StatusBadRequest &&
		strings.Contains(msg, "volume") &&
		strings.Contains(msg, "name") &&
		strings.Contains(msg, "already exists")
}
