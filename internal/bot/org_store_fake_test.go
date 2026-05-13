package bot

import (
	"context"
	"sync"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

type fakeOrgStore struct {
	mu sync.Mutex

	getConfig orgcfg.Config
	getErr    error

	getBySlackConfig orgcfg.Config
	getBySlackErr    error

	listConfigs []orgcfg.Config
	listErr     error

	upserts   []orgcfg.Config
	upsertErr error
}

func (f *fakeOrgStore) Get(context.Context, string) (orgcfg.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getConfig, f.getErr
}

func (f *fakeOrgStore) GetBySlackTeamID(context.Context, string) (orgcfg.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getBySlackConfig, f.getBySlackErr
}

func (f *fakeOrgStore) ListWithSlack(context.Context) ([]orgcfg.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]orgcfg.Config(nil), f.listConfigs...), f.listErr
}

func (f *fakeOrgStore) Upsert(_ context.Context, cfg orgcfg.Config) (orgcfg.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return orgcfg.Config{}, f.upsertErr
	}
	f.upserts = append(f.upserts, cfg)
	return cfg, nil
}
