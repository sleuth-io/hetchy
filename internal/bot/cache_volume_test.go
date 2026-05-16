package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestDaytonaCacheVolumeRouteStableSafeAndPooled(t *testing.T) {
	got := daytonaCacheRoute("Hetchy Cache!!", "dev", "org_1")
	if got != daytonaCacheRoute("Hetchy Cache!!", "dev", "org_1") {
		t.Fatal("volume route should be stable for identical inputs")
	}
	if got.Env != "dev" || got.SlotCount != daytonaCacheDevVolumeCount || got.Slot < 0 || got.Slot >= got.SlotCount {
		t.Fatalf("unexpected dev route: %+v", got)
	}
	if !strings.HasPrefix(got.Name, "hetchy-cache-dev-") {
		t.Fatalf("volume name prefix/env were not sanitized as expected: %q", got.Name)
	}
	if len(got.Name) > daytonaCacheVolumeNameMax {
		t.Fatalf("volume name length = %d, want <= %d", len(got.Name), daytonaCacheVolumeNameMax)
	}
	if long := daytonaCacheVolumeName(strings.Repeat("p", 100), "production", 79); len(long) > daytonaCacheVolumeNameMax {
		t.Fatalf("long volume name length = %d, want <= %d: %q", len(long), daytonaCacheVolumeNameMax, long)
	}
	for _, r := range got.Name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Fatalf("volume name contains unsafe rune %q: %q", r, got.Name)
		}
	}
	for env, want := range map[string]int{"dev": 10, "staging": 10, "prod": 80} {
		seen := map[string]bool{}
		for i := range 200 {
			seen[daytonaCacheRoute("cache", env, fmt.Sprintf("org_%d", i)).Name] = true
		}
		if len(seen) > want {
			t.Fatalf("%s used %d volume names, want <= %d", env, len(seen), want)
		}
	}
	if daytonaCacheVolumeCount("dev")+daytonaCacheVolumeCount("stg")+daytonaCacheVolumeCount("prod") != 100 {
		t.Fatal("cache volume pool must stay within Daytona's 100-volume org limit")
	}
}

func TestDaytonaCacheSubpathIsPerOrgAndRepo(t *testing.T) {
	oc := orgcfg.Config{OrgID: "org_1"}
	orgPrefix := "orgs/" + daytonaCacheOrgHash(oc.OrgID) + "/"
	base := daytonaCacheSubpath(oc, repoCtx{InstallID: 10, RepoID: 20})
	if base != orgPrefix+"repos/10/20/default" {
		t.Fatalf("default subpath = %q", base)
	}
	if base == daytonaCacheSubpath(oc, repoCtx{InstallID: 10, RepoID: 21}) {
		t.Fatal("subpath should differ across repo ids")
	}
	if base == daytonaCacheSubpath(orgcfg.Config{OrgID: "org_2"}, repoCtx{InstallID: 10, RepoID: 20}) {
		t.Fatal("subpath should differ across org ids")
	}
}

func TestResolveDaytonaCacheMountCreatesMissingVolume(t *testing.T) {
	vols := &fakeCacheVolumeService{
		get: []fakeCacheVolumeResult{
			{err: sdkerrors.NewDaytonaNotFoundError("missing", nil)},
		},
		create: []fakeCacheVolumeResult{
			{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "pending"}},
		},
		wait: []fakeCacheVolumeResult{
			{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}},
		},
	}
	b := &Bot{
		log:       discardLogger(),
		cfg:       Config{Env: "dev", DaytonaCacheVolumePrefix: "cache"},
		cacheVols: vols,
	}

	mount, ok := b.resolveDaytonaCacheMount(context.Background(),
		orgcfg.Config{OrgID: "org_1"},
		repoCtx{Slug: "owner/repo", InstallID: 11, RepoID: 22})
	if !ok {
		t.Fatal("expected cache mount")
	}
	if mount.VolumeID != "vol-1" || mount.MountPath != daytonaCacheMountPath || mount.Subpath == nil || *mount.Subpath != "orgs/"+daytonaCacheOrgHash("org_1")+"/repos/11/22/default" {
		t.Fatalf("unexpected mount: %+v", mount)
	}
	if strings.Join(vols.calls, ",") != "get,create,wait" {
		t.Fatalf("calls = %v", vols.calls)
	}
	if got := vols.waitTimeouts[0]; got != daytonaCacheVolumeTimeout {
		t.Fatalf("wait timeout = %s, want %s", got, daytonaCacheVolumeTimeout)
	}
	if got, want := vols.getNames[0], daytonaCacheRoute("cache", "dev", "org_1").Name; got != want {
		t.Fatalf("volume get name = %q, want %q", got, want)
	}
}

func TestResolveDaytonaCacheMountCreateConflictRetriesGet(t *testing.T) {
	vols := &fakeCacheVolumeService{
		get: []fakeCacheVolumeResult{
			{err: sdkerrors.NewDaytonaNotFoundError("missing", nil)},
			{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}},
		},
		create: []fakeCacheVolumeResult{
			{err: sdkerrors.NewDaytonaError("already exists", http.StatusConflict, nil)},
		},
		wait: []fakeCacheVolumeResult{
			{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}},
		},
	}
	b := &Bot{log: discardLogger(), cfg: Config{Env: "dev"}, cacheVols: vols}

	if _, ok := b.resolveDaytonaCacheMount(context.Background(),
		orgcfg.Config{OrgID: "org_1"},
		repoCtx{Slug: "owner/repo", InstallID: 11, RepoID: 22}); !ok {
		t.Fatal("expected cache mount after conflict get retry")
	}
	if strings.Join(vols.calls, ",") != "get,create,get,wait" {
		t.Fatalf("calls = %v", vols.calls)
	}
}

func TestResolveDaytonaCacheMountCreateDuplicateNameRetriesGet(t *testing.T) {
	vols := &fakeCacheVolumeService{
		get: []fakeCacheVolumeResult{
			{err: sdkerrors.NewDaytonaNotFoundError("missing", nil)},
			{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}},
		},
		create: []fakeCacheVolumeResult{
			{err: sdkerrors.NewDaytonaError("Volume with name cache-dev-00 already exists", http.StatusBadRequest, nil)},
		},
		wait: []fakeCacheVolumeResult{
			{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}},
		},
	}
	b := &Bot{log: discardLogger(), cfg: Config{Env: "dev"}, cacheVols: vols}

	if _, ok := b.resolveDaytonaCacheMount(context.Background(),
		orgcfg.Config{OrgID: "org_1"},
		repoCtx{Slug: "owner/repo", InstallID: 11, RepoID: 22}); !ok {
		t.Fatal("expected cache mount after duplicate-name get retry")
	}
	if strings.Join(vols.calls, ",") != "get,create,get,wait" {
		t.Fatalf("calls = %v", vols.calls)
	}
}

func TestIsDaytonaConflictRequiresStatusCode(t *testing.T) {
	if !isDaytonaConflict(sdkerrors.NewDaytonaError("already exists", http.StatusConflict, nil)) {
		t.Fatal("expected Daytona 409 to be treated as conflict")
	}
	if !isDaytonaConflict(sdkerrors.NewDaytonaError("Volume with name cache-dev-00 already exists", http.StatusBadRequest, nil)) {
		t.Fatal("expected Daytona duplicate volume 400 to be treated as conflict")
	}
	if isDaytonaConflict(sdkerrors.NewDaytonaError("bad request", http.StatusBadRequest, nil)) {
		t.Fatal("unrelated Daytona 400 should not be treated as conflict")
	}
	if isDaytonaConflict(errors.New("repo already exists")) {
		t.Fatal("plain error text should not be treated as Daytona conflict")
	}
}

func TestResolveDaytonaCacheMountErrorsFallBackToNoMount(t *testing.T) {
	vols := &fakeCacheVolumeService{
		get: []fakeCacheVolumeResult{{err: sdkerrors.NewDaytonaError("boom", http.StatusInternalServerError, nil)}},
	}
	b := &Bot{log: discardLogger(), cfg: Config{Env: "dev"}, cacheVols: vols}

	if mount, ok := b.resolveDaytonaCacheMount(context.Background(),
		orgcfg.Config{OrgID: "org_1"},
		repoCtx{Slug: "owner/repo", InstallID: 11, RepoID: 22}); ok || mount.VolumeID != "" {
		t.Fatalf("expected no mount, got ok=%v mount=%+v", ok, mount)
	}
	if strings.Join(vols.calls, ",") != "get" {
		t.Fatalf("calls = %v", vols.calls)
	}
}

func TestResolveDaytonaCacheMountDisabledMakesNoVolumeCalls(t *testing.T) {
	vols := &fakeCacheVolumeService{unexpected: true}
	b := &Bot{log: discardLogger(), cfg: Config{DaytonaCacheVolumesDisabled: true}, cacheVols: vols}

	if _, ok := b.resolveDaytonaCacheMount(context.Background(),
		orgcfg.Config{OrgID: "org_1"},
		repoCtx{Slug: "owner/repo", InstallID: 11, RepoID: 22}); ok {
		t.Fatal("disabled cache should not mount")
	}
	if len(vols.calls) != 0 {
		t.Fatalf("disabled cache made volume calls: %v", vols.calls)
	}
}

func TestHandleRequestFreshRunAttachesCacheVolumeAndEnv(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.cfg = Config{Env: "dev", Snapshot: "snap", DaytonaCacheVolumePrefix: "cache", DaytonaCachePruneDays: 14}
	b.cacheVols = &fakeCacheVolumeService{
		get:  []fakeCacheVolumeResult{{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}}},
		wait: []fakeCacheVolumeResult{{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}}},
	}
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token", InstallID: 11, RepoID: 22}, nil
	}
	var params types.SnapshotParams
	b.createFn = func(_ context.Context, raw any) (*daytona.Sandbox, error) {
		var ok bool
		params, ok = raw.(types.SnapshotParams)
		if !ok {
			t.Fatalf("create params type = %T", raw)
		}
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
		return "https://github.com/hetchyhq/hetchy/pull/2", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, ClaudeModelOpus, newCaptureEmitter())

	if len(params.Volumes) != 1 {
		t.Fatalf("SnapshotParams.Volumes len = %d, want 1 (%+v)", len(params.Volumes), params.Volumes)
	}
	if got := params.Volumes[0]; got.VolumeID != "vol-1" || got.MountPath != daytonaCacheMountPath || got.Subpath == nil || *got.Subpath != "orgs/"+daytonaCacheOrgHash("org_test")+"/repos/11/22/default" {
		t.Fatalf("cache volume mount = %+v", got)
	}
	if params.EnvVars["HETCHY_CACHE_DIR"] != daytonaCacheMountPath {
		t.Fatalf("HETCHY_CACHE_DIR = %q", params.EnvVars["HETCHY_CACHE_DIR"])
	}
	if params.EnvVars["HETCHY_CACHE_STATUS"] != "mounted" {
		t.Fatalf("HETCHY_CACHE_STATUS = %q", params.EnvVars["HETCHY_CACHE_STATUS"])
	}
	if params.EnvVars["HETCHY_CACHE_PRUNE_DAYS"] != "14" {
		t.Fatalf("HETCHY_CACHE_PRUNE_DAYS = %q", params.EnvVars["HETCHY_CACHE_PRUNE_DAYS"])
	}
	if params.Labels[daytonaSandboxLabelEnv] != "dev" {
		t.Fatalf("env label = %q", params.Labels[daytonaSandboxLabelEnv])
	}
	if params.Labels[daytonaSandboxLabelOrgID] != "org_test" {
		t.Fatalf("org label = %q", params.Labels[daytonaSandboxLabelOrgID])
	}
	if params.Labels[daytonaSandboxLabelCacheVolumeID] != "vol-1" {
		t.Fatalf("cache volume label = %q", params.Labels[daytonaSandboxLabelCacheVolumeID])
	}
}

func TestHandleRequestFreshRunMarksCacheUnavailableWhenVolumeResolveFails(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.cfg = Config{Env: "dev", Snapshot: "snap"}
	b.cacheVols = &fakeCacheVolumeService{
		get: []fakeCacheVolumeResult{{err: sdkerrors.NewDaytonaError("boom", http.StatusInternalServerError, nil)}},
	}
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token", InstallID: 11, RepoID: 22}, nil
	}
	var params types.SnapshotParams
	b.createFn = func(_ context.Context, raw any) (*daytona.Sandbox, error) {
		params = raw.(types.SnapshotParams)
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, repo repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
		if repo.CacheMounted {
			t.Fatal("repo.CacheMounted should be false when volume resolution fails")
		}
		return "https://github.com/hetchyhq/hetchy/pull/2", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, ClaudeModelOpus, newCaptureEmitter())

	if len(params.Volumes) != 0 {
		t.Fatalf("expected no cache volumes, got %+v", params.Volumes)
	}
	if _, ok := params.EnvVars["HETCHY_CACHE_DIR"]; ok {
		t.Fatalf("HETCHY_CACHE_DIR should not be set after resolve failure: %+v", params.EnvVars)
	}
	if params.EnvVars["HETCHY_CACHE_STATUS"] != "unavailable" {
		t.Fatalf("HETCHY_CACHE_STATUS = %q", params.EnvVars["HETCHY_CACHE_STATUS"])
	}
	if _, ok := params.Labels[daytonaSandboxLabelCacheVolumeID]; ok {
		t.Fatalf("cache volume label should not be set when no volume is mounted: %+v", params.Labels)
	}
}

func TestHandleRequestFreshRunSkipsCacheMountWithoutRepoIdentity(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.cfg = Config{Env: "dev", Snapshot: "snap"}
	b.cacheVols = &fakeCacheVolumeService{unexpected: true}
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	var params types.SnapshotParams
	b.createFn = func(_ context.Context, raw any) (*daytona.Sandbox, error) {
		params = raw.(types.SnapshotParams)
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
		return "https://github.com/hetchyhq/hetchy/pull/2", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, ClaudeModelOpus, newCaptureEmitter())

	if len(params.Volumes) != 0 {
		t.Fatalf("expected no cache volumes, got %+v", params.Volumes)
	}
	if _, ok := params.EnvVars["HETCHY_CACHE_DIR"]; ok {
		t.Fatalf("HETCHY_CACHE_DIR should not be set without repo identity: %+v", params.EnvVars)
	}
	if params.EnvVars["HETCHY_CACHE_STATUS"] != "unavailable" {
		t.Fatalf("HETCHY_CACHE_STATUS = %q", params.EnvVars["HETCHY_CACHE_STATUS"])
	}
}

func TestRunAgentPassesCacheEnvAndFollowUpDoesNotOverride(t *testing.T) {
	restoreAgent := stubPRLookup(t, "acme/repo", "feature/sf-req-1", "main", "https://github.com/acme/repo/pull/7")
	defer restoreAgent()

	var captured capturedScriptRun
	b := &Bot{
		log: discardLogger(),
		cfg: Config{DaytonaCachePruneDays: 9},
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "https://github.com/acme/repo/pull/7", nil
		},
	}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "token", InstallID: 11, RepoID: 22, CacheMounted: true}
	oc := orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}
	if _, err := b.runAgent(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, agents.Profile{}, "ship", "req-1", "feature/sf-req-1", chatTaskOptions{ValidateChanges: false}, ClaudeModelSonnet, newCaptureEmitter()); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if captured.env["HETCHY_CACHE_DIR"] != daytonaCacheMountPath || captured.env["HETCHY_CACHE_STATUS"] != "mounted" || captured.env["HETCHY_CACHE_PRUNE_DAYS"] != "9" {
		t.Fatalf("agent cache env = %+v", captured.env)
	}

	restoreFollowup := stubPRLookup(t, "acme/repo", "feature/sf-req-1", "", "https://github.com/acme/repo/pull/8")
	defer restoreFollowup()
	if _, err := b.runFollowUp(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, convstore.Record{Branch: "feature/sf-req-1", PRURL: "https://github.com/acme/repo/pull/7"}, agents.Profile{}, "tighten", "req-2", chatTaskOptions{}, ClaudeModelSonnet, newCaptureEmitter()); err != nil {
		t.Fatalf("runFollowUp: %v", err)
	}
	if _, ok := captured.env["HETCHY_CACHE_DIR"]; ok {
		t.Fatalf("follow-up should inherit sandbox cache env instead of overriding it: %+v", captured.env)
	}
	if _, ok := captured.env["HETCHY_CACHE_STATUS"]; ok {
		t.Fatalf("follow-up should inherit sandbox cache status instead of overriding it: %+v", captured.env)
	}
}

type fakeCacheVolumeResult struct {
	vol *types.Volume
	err error
}

type fakeCacheVolumeService struct {
	get          []fakeCacheVolumeResult
	create       []fakeCacheVolumeResult
	wait         []fakeCacheVolumeResult
	calls        []string
	getNames     []string
	createNames  []string
	waitTimeouts []time.Duration
	unexpected   bool
}

func (f *fakeCacheVolumeService) Get(_ context.Context, name string) (*types.Volume, error) {
	f.calls = append(f.calls, "get")
	f.getNames = append(f.getNames, name)
	if f.unexpected {
		return nil, errors.New("unexpected get")
	}
	return f.next(&f.get)
}

func (f *fakeCacheVolumeService) Create(_ context.Context, name string) (*types.Volume, error) {
	f.calls = append(f.calls, "create")
	f.createNames = append(f.createNames, name)
	if f.unexpected {
		return nil, errors.New("unexpected create")
	}
	return f.next(&f.create)
}

func (f *fakeCacheVolumeService) WaitForReady(_ context.Context, vol *types.Volume, timeout time.Duration) (*types.Volume, error) {
	f.calls = append(f.calls, "wait")
	f.waitTimeouts = append(f.waitTimeouts, timeout)
	if f.unexpected {
		return nil, errors.New("unexpected wait")
	}
	if len(f.wait) == 0 {
		return vol, nil
	}
	return f.next(&f.wait)
}

func (f *fakeCacheVolumeService) next(results *[]fakeCacheVolumeResult) (*types.Volume, error) {
	if len(*results) == 0 {
		return nil, errors.New("no fake volume result configured")
	}
	result := (*results)[0]
	*results = (*results)[1:]
	return result.vol, result.err
}
