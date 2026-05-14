package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

const (
	daytonaCacheMountPath     = "/home/daytona/.hetchy-cache"
	defaultCacheVolumePrefix  = "hetchy-cache"
	defaultCachePruneDays     = 30
	daytonaCacheVolumeTimeout = 60 * time.Second
	daytonaCacheVolumeNameMax = 63
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

	name := daytonaCacheVolumeName(b.cfg.DaytonaCacheVolumePrefix, b.cfg.Env, oc.OrgID)
	volume, err := b.getOrCreateDaytonaCacheVolume(ctx, name)
	if err != nil {
		if b.log != nil {
			b.log.Warn("daytona cache volume unavailable", "org", oc.OrgID, "repo", repo.Slug, "volume", name, "error", err)
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

	subpath := daytonaCacheSubpath(repo)
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

func addDaytonaCacheEnv(env map[string]string, cfg Config, oc orgcfg.Config, repo repoCtx) {
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
	env["HETCHY_CACHE_STATUS"] = "enabled"
	env["HETCHY_CACHE_DIR"] = daytonaCacheMountPath
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

func daytonaCacheVolumeName(prefix, env, orgID string) string {
	hash := sha256.Sum256([]byte(orgID))
	suffix := hex.EncodeToString(hash[:])[:16]

	prefix = sanitizeDaytonaNamePart(prefix)
	if prefix == "" {
		prefix = defaultCacheVolumePrefix
	}
	env = sanitizeDaytonaNamePart(env)
	if env == "" {
		env = "prod"
	}
	maxEnvLen := daytonaCacheVolumeNameMax - len(suffix) - 1 - 2
	if len(env) > maxEnvLen {
		env = strings.Trim(env[:maxEnvLen], "-")
		if env == "" {
			env = "env"
		}
	}
	maxPrefixLen := daytonaCacheVolumeNameMax - len(env) - len(suffix) - 2
	if maxPrefixLen < 1 {
		maxPrefixLen = 1
	}
	if len(prefix) > maxPrefixLen {
		prefix = strings.Trim(prefix[:maxPrefixLen], "-")
		if prefix == "" {
			prefix = "cache"
		}
	}
	return prefix + "-" + env + "-" + suffix
}

func daytonaCacheSubpath(repo repoCtx) string {
	pathPart := "default"
	if p := normalizeRepoCachePath(repo.Path); p != "" {
		hash := sha256.Sum256([]byte(p))
		pathPart = "path-" + hex.EncodeToString(hash[:])[:16]
	}
	return fmt.Sprintf("repos/%d/%d/%s", repo.InstallID, repo.RepoID, pathPart)
}

func normalizeRepoCachePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(pathpkg.Clean("/"+p), "/")
	if p == "." {
		return ""
	}
	return p
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
	var notFound *sdkerrors.DaytonaNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var daytonaErr *sdkerrors.DaytonaError
	return errors.As(err, &daytonaErr) && daytonaErr.StatusCode == http.StatusNotFound
}

func isDaytonaConflict(err error) bool {
	var daytonaErr *sdkerrors.DaytonaError
	if errors.As(err, &daytonaErr) && daytonaErr.StatusCode == http.StatusConflict {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "conflict")
}
