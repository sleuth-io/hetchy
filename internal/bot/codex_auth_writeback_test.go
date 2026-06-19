package bot

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestPersistOpenAICodexAuthJSONWritesRefreshedAuth(t *testing.T) {
	original := testCodexAuthJSON(t)
	refreshed := mutateCodexAuthJSON(t, original, map[string]any{
		"last_refresh": "2026-05-20T20:00:00Z",
	})
	store := &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:                 "org_1",
		OpenAICodexOAuthToken: original,
		SXKey:                 "sx-key",
		DefaultGitHubOwner:    "sleuth-io",
		DefaultGitHubRepo:     "hetchy",
	}}
	var gotPath string
	b := &Bot{
		log:  discardLogger(),
		orgs: store,
		downloadSandboxFileFn: func(_ context.Context, _ *daytona.Sandbox, path string) ([]byte, error) {
			gotPath = path
			return []byte("\n " + refreshed + " \n"), nil
		},
	}

	if err := b.persistOpenAICodexAuthJSON(context.Background(), &daytona.Sandbox{}, store.getConfig, "req_1"); err != nil {
		t.Fatalf("persistOpenAICodexAuthJSON: %v", err)
	}
	if gotPath != codexAuthJSONPath {
		t.Fatalf("download path = %q, want %q", gotPath, codexAuthJSONPath)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got, err := canonicalOpenAICodexAuthJSON([]byte(store.upserts[0].OpenAICodexOAuthToken))
	if err != nil {
		t.Fatalf("stored auth json invalid: %v", err)
	}
	want, err := canonicalOpenAICodexAuthJSON([]byte(refreshed))
	if err != nil {
		t.Fatalf("refreshed auth json invalid: %v", err)
	}
	if got != want {
		t.Fatalf("stored auth json = %s, want %s", got, want)
	}
	if store.upserts[0].SXKey != "sx-key" || store.upserts[0].DefaultGitHubOwner != "sleuth-io" || store.upserts[0].DefaultGitHubRepo != "hetchy" {
		t.Fatalf("upsert did not preserve org settings: %+v", store.upserts[0])
	}
}

func TestPersistOpenAICodexAuthJSONSkipsUnchangedAuth(t *testing.T) {
	authJSON := testCodexAuthJSON(t)
	store := &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:                 "org_1",
		OpenAICodexOAuthToken: authJSON,
	}}
	b := &Bot{
		log:  discardLogger(),
		orgs: store,
		downloadSandboxFileFn: func(context.Context, *daytona.Sandbox, string) ([]byte, error) {
			return []byte(authJSON), nil
		},
	}

	if err := b.persistOpenAICodexAuthJSON(context.Background(), &daytona.Sandbox{}, store.getConfig, "req_1"); err != nil {
		t.Fatalf("persistOpenAICodexAuthJSON: %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(store.upserts))
	}
}

func TestPersistOpenAICodexAuthJSONSkipsWhenCredentialChanged(t *testing.T) {
	original := testCodexAuthJSON(t)
	refreshed := mutateCodexAuthJSON(t, original, map[string]any{
		"last_refresh": "2026-05-20T20:00:00Z",
	})
	newer := mutateCodexAuthJSON(t, original, map[string]any{
		"last_refresh": "2026-05-21T20:00:00Z",
	})
	store := &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:                 "org_1",
		OpenAICodexOAuthToken: newer,
	}}
	b := &Bot{
		log:  discardLogger(),
		orgs: store,
		downloadSandboxFileFn: func(context.Context, *daytona.Sandbox, string) ([]byte, error) {
			return []byte(refreshed), nil
		},
	}

	oc := orgcfg.Config{OrgID: "org_1", OpenAICodexOAuthToken: original}
	if err := b.persistOpenAICodexAuthJSON(context.Background(), &daytona.Sandbox{}, oc, "req_1"); err != nil {
		t.Fatalf("persistOpenAICodexAuthJSON: %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(store.upserts))
	}
}

func TestPersistOpenAICodexAuthJSONOnlyHandlesAuthJSON(t *testing.T) {
	cases := []struct {
		name string
		oc   orgcfg.Config
	}{
		{name: "api key", oc: orgcfg.Config{OrgID: "org_1", OpenAIAPIKey: "sk-test"}},
		{name: "agent identity", oc: orgcfg.Config{OrgID: "org_1", OpenAICodexOAuthToken: "ey-token"}},
		{name: "empty org", oc: orgcfg.Config{OpenAICodexOAuthToken: testCodexAuthJSON(t)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			b := &Bot{
				log:  discardLogger(),
				orgs: &fakeOrgStore{},
				downloadSandboxFileFn: func(context.Context, *daytona.Sandbox, string) ([]byte, error) {
					called = true
					return nil, errors.New("should not be called")
				},
			}

			if err := b.persistOpenAICodexAuthJSON(context.Background(), &daytona.Sandbox{}, tc.oc, "req_1"); err != nil {
				t.Fatalf("persistOpenAICodexAuthJSON: %v", err)
			}
			if called {
				t.Fatal("download was called")
			}
		})
	}
}

func mutateCodexAuthJSON(t *testing.T, authJSON string, fields map[string]any) string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(authJSON), &v); err != nil {
		t.Fatalf("unmarshal auth json: %v", err)
	}
	maps.Copy(v, fields)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal auth json: %v", err)
	}
	return string(b)
}
