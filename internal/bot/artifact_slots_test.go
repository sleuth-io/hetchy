package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/artifacts"
)

type fakeArtifactMinter struct {
	mu       sync.Mutex
	requests []fakeArtifactRequest
	fail     error
}

type fakeArtifactRequest struct {
	prefix string
	req    artifacts.MintRequest
}

func (f *fakeArtifactMinter) MintSlots(_ context.Context, prefix string, req artifacts.MintRequest) ([]artifacts.Slot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, fakeArtifactRequest{prefix: prefix, req: req})
	if f.fail != nil {
		return nil, f.fail
	}
	slots := make([]artifacts.Slot, 0, req.Count)
	for i := range req.Count {
		index := req.StartIndex + i
		slots = append(slots, artifacts.Slot{
			Kind:        req.Kind,
			ContentType: req.ContentType,
			PutURL:      fmt.Sprintf("https://example.test/put/%s/%s/%d", prefix, req.Kind, index),
			GetURL:      fmt.Sprintf("https://example.test/get/%s/%s/%d", prefix, req.Kind, index),
		})
	}
	return slots, nil
}

func TestArtifactSlotBrokerRejectsAndPrunesExpiredToken(t *testing.T) {
	fake := &fakeArtifactMinter{}
	br := newArtifactSlotBroker(fake)
	now := time.Unix(1_700_000_000, 0)
	br.now = func() time.Time { return now }

	_, token, err := br.Start(context.Background(), "org_abc/42/req_1", defaultArtifactSlotRequests, artifactSlotRunOptions{})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	if got := len(br.runs); got != 1 {
		t.Fatalf("runs before expiry = %d, want 1", got)
	}

	now = now.Add(artifactRunTokenExpiry + time.Second)
	_, err = br.Mint(context.Background(), token, artifacts.MintRequest{
		Kind:        artifacts.KindScreenshot,
		ContentType: artifacts.ContentTypePNG,
		Count:       1,
	})
	if !errors.Is(err, errArtifactTokenInvalid) {
		t.Fatalf("Mint expired token error = %v, want %v", err, errArtifactTokenInvalid)
	}
	if got := len(br.runs); got != 0 {
		t.Fatalf("runs after expiry prune = %d, want 0", got)
	}
}

func TestAddGitHubTokenRefreshEnvWithoutArtifactSigner(t *testing.T) {
	b := &Bot{
		log:           discardLogger(),
		cfg:           Config{LogoutReturnTo: "https://app.example.test/"},
		artifactSlots: newArtifactSlotBroker(nil),
		githubTokenMinTTLFn: func(context.Context, int64, []int64, time.Duration) (string, time.Time, error) {
			return "ghs_fresh", time.Now().Add(time.Hour), nil
		},
	}

	env := map[string]string{}
	if err := b.addGitHubTokenRefreshEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{InstallID: 11, RepoID: 22}); err != nil {
		t.Fatalf("add refresh env: %v", err)
	}
	if got, want := env[artifacts.EnvSlotURL], "https://app.example.test"+artifactSlotPath; got != want {
		t.Fatalf("%s = %q, want %q", artifacts.EnvSlotURL, got, want)
	}
	if env[artifacts.EnvSlotToken] == "" {
		t.Fatalf("missing %s", artifacts.EnvSlotToken)
	}
	if _, ok := env[artifacts.EnvSlots]; ok {
		t.Fatalf("%s should not be set for token-only refresh env", artifacts.EnvSlots)
	}

	installationID, repoID, err := b.artifactSlots.GitHubAuth(env[artifacts.EnvSlotToken])
	if err != nil {
		t.Fatalf("GitHubAuth: %v", err)
	}
	if installationID != 11 || repoID != 22 {
		t.Fatalf("GitHubAuth = %d/%d, want 11/22", installationID, repoID)
	}
	_, err = b.artifactSlots.Mint(context.Background(), env[artifacts.EnvSlotToken], artifacts.MintRequest{
		Kind:        artifacts.KindScreenshot,
		ContentType: artifacts.ContentTypePNG,
		Count:       1,
	})
	if !errors.Is(err, errArtifactSlotsDisabled) {
		t.Fatalf("Mint without signer error = %v, want %v", err, errArtifactSlotsDisabled)
	}
}

func TestAddArtifactRunEnvInitialAndFollowup(t *testing.T) {
	fake := &fakeArtifactMinter{}
	b := &Bot{
		cfg:           Config{LogoutReturnTo: "https://app.example.test/"},
		artifacts:     fake,
		artifactSlots: newArtifactSlotBroker(fake),
	}

	initialEnv := map[string]string{}
	initialSlots, err := b.addArtifactRunEnv(context.Background(), "org_abc/42/req_1", initialEnv, repoCtx{})
	if err != nil {
		t.Fatalf("initial add env: %v", err)
	}
	followupEnv := map[string]string{}
	followupSlots, err := b.addArtifactRunEnv(context.Background(), "org_abc/42/thread_1/followup-req_2", followupEnv, repoCtx{})
	if err != nil {
		t.Fatalf("followup add env: %v", err)
	}

	for label, env := range map[string]map[string]string{"initial": initialEnv, "followup": followupEnv} {
		if env[artifacts.EnvSlots] == "" {
			t.Fatalf("%s env missing %s", label, artifacts.EnvSlots)
		}
		if got, want := env[artifacts.EnvSlotURL], "https://app.example.test"+artifactSlotPath; got != want {
			t.Fatalf("%s %s = %q, want %q", label, artifacts.EnvSlotURL, got, want)
		}
		if env[artifacts.EnvSlotToken] == "" {
			t.Fatalf("%s env missing %s", label, artifacts.EnvSlotToken)
		}
		if _, ok := env["HETCHY_SCREENSHOT_SLOTS"]; ok {
			t.Fatalf("%s env should not include legacy screenshot slots", label)
		}
	}

	if len(initialSlots) != len(defaultArtifactSlotRequests) || len(followupSlots) != len(defaultArtifactSlotRequests) {
		t.Fatalf("default slot count mismatch: initial=%d followup=%d", len(initialSlots), len(followupSlots))
	}
	if !strings.Contains(initialEnv[artifacts.EnvSlots], `"kind":"screenshot"`) {
		t.Fatalf("initial manifest missing screenshot slot: %s", initialEnv[artifacts.EnvSlots])
	}
	if !strings.Contains(initialEnv[artifacts.EnvSlots], `"content_type":"video/mp4"`) {
		t.Fatalf("initial manifest missing recording slot: %s", initialEnv[artifacts.EnvSlots])
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := len(fake.requests); got != 6 {
		t.Fatalf("mint requests = %d, want 6", got)
	}
	if got := fake.requests[0].prefix; got != "org_abc/42/req_1" {
		t.Fatalf("initial prefix = %q", got)
	}
	if got := fake.requests[3].prefix; got != "org_abc/42/thread_1/followup-req_2" {
		t.Fatalf("followup prefix = %q", got)
	}
}

func TestAddArtifactRunEnvUsesExternalCallbackOrigin(t *testing.T) {
	fake := &fakeArtifactMinter{}
	b := &Bot{
		cfg: Config{
			Env:                   "dev",
			WebPort:               "8080",
			LogoutReturnTo:        "http://localhost:8080/",
			WorkOSRedirectURI:     "https://app.hetchy.ai/callback",
			SlackOAuthRedirectURI: "https://slack-other.example.test/callback",
		},
		artifacts:     fake,
		artifactSlots: newArtifactSlotBroker(fake),
	}

	env := map[string]string{}
	if _, err := b.addArtifactRunEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{}); err != nil {
		t.Fatalf("add env: %v", err)
	}
	if got, want := env[artifacts.EnvSlotURL], "https://app.hetchy.ai"+artifactSlotPath; got != want {
		t.Fatalf("%s = %q, want %q", artifacts.EnvSlotURL, got, want)
	}
}

func TestArtifactSlotsHandlerMintsMoreSlots(t *testing.T) {
	fake := &fakeArtifactMinter{}
	b := &Bot{
		log:           discardLogger(),
		cfg:           Config{LogoutReturnTo: "https://app.example.test/"},
		artifacts:     fake,
		artifactSlots: newArtifactSlotBroker(fake),
	}
	env := map[string]string{}
	if _, err := b.addArtifactRunEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{}); err != nil {
		t.Fatalf("add env: %v", err)
	}

	body := bytes.NewBufferString(`{"kind":"recording","content_type":"video/mp4","count":1}`)
	req := httptest.NewRequest(http.MethodPost, artifactSlotPath, body)
	req.Header.Set("Authorization", "Bearer "+env[artifacts.EnvSlotToken])
	rec := httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	var slots []artifacts.Slot
	if err := json.Unmarshal(rec.Body.Bytes(), &slots); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(slots))
	}
	if slots[0].Kind != artifacts.KindRecording || slots[0].ContentType != artifacts.ContentTypeMP4 {
		t.Fatalf("slot = %+v, want recording/mp4", slots[0])
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	last := fake.requests[len(fake.requests)-1]
	if last.req.StartIndex != 1 {
		t.Fatalf("follow-on recording start index = %d, want 1", last.req.StartIndex)
	}
}

func TestArtifactSlotsHandlerMintsGitHubToken(t *testing.T) {
	fake := &fakeArtifactMinter{}
	expiresAt := time.Unix(1_800_000_000, 0).UTC()
	b := &Bot{
		log:           discardLogger(),
		app:           freshGithubAppForTest(t, "wh-secret"),
		artifactSlots: newArtifactSlotBroker(fake),
		githubTokenMinTTLFn: func(_ context.Context, installationID int64, repoIDs []int64, minTTL time.Duration) (string, time.Time, error) {
			if installationID != 11 {
				t.Fatalf("installationID = %d, want 11", installationID)
			}
			if len(repoIDs) != 1 || repoIDs[0] != 22 {
				t.Fatalf("repoIDs = %#v, want [22]", repoIDs)
			}
			if minTTL != sandboxGitHubTokenMinTTL {
				t.Fatalf("minTTL = %s, want %s", minTTL, sandboxGitHubTokenMinTTL)
			}
			return "ghs_fresh", expiresAt, nil
		},
	}
	env := map[string]string{}
	if err := b.addGitHubTokenRefreshEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{InstallID: 11, RepoID: 22}); err != nil {
		t.Fatalf("add refresh env: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, artifactSlotPath, strings.NewReader(`{"kind":"github_token"}`))
	req.Header.Set("Authorization", "Bearer "+env[artifacts.EnvSlotToken])
	rec := httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body githubTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Token != "ghs_fresh" || body.ExpiresAt != expiresAt.Format(time.RFC3339) {
		t.Fatalf("response = %+v", body)
	}
}

func TestArtifactSlotsHandlerGitHubTokenErrors(t *testing.T) {
	fake := &fakeArtifactMinter{}
	b := &Bot{
		log:           discardLogger(),
		app:           freshGithubAppForTest(t, "wh-secret"),
		artifactSlots: newArtifactSlotBroker(fake),
		githubTokenMinTTLFn: func(context.Context, int64, []int64, time.Duration) (string, time.Time, error) {
			return "ghs_fresh", time.Now().Add(time.Hour), nil
		},
	}
	env := map[string]string{}
	if err := b.addGitHubTokenRefreshEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{InstallID: 11, RepoID: 22}); err != nil {
		t.Fatalf("add refresh env: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, artifactSlotPath, strings.NewReader(`{"kind":"github_token"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401; body=%q", rec.Code, rec.Body.String())
	}

	_, tokenWithoutGitHub, err := b.artifactSlots.Start(context.Background(), "org_abc/42/req_2", nil, artifactSlotRunOptions{})
	if err != nil {
		t.Fatalf("start token-only run: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, artifactSlotPath, strings.NewReader(`{"kind":"github_token"}`))
	req.Header.Set("Authorization", "Bearer "+tokenWithoutGitHub)
	rec = httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing github auth status = %d, want 500; body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "for this run") {
		t.Fatalf("response leaked internal lookup error: %q", rec.Body.String())
	}

	b.app = nil
	b.githubTokenMinTTLFn = nil
	req = httptest.NewRequest(http.MethodPost, artifactSlotPath, strings.NewReader(`{"kind":"github_token"}`))
	req.Header.Set("Authorization", "Bearer "+env[artifacts.EnvSlotToken])
	rec = httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing app status = %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
}

func TestArtifactSlotBrokerStoresGitHubAuthForRun(t *testing.T) {
	fake := &fakeArtifactMinter{}
	b := &Bot{
		log:           discardLogger(),
		artifacts:     fake,
		artifactSlots: newArtifactSlotBroker(fake),
	}
	env := map[string]string{}
	if _, err := b.addArtifactRunEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{InstallID: 11, RepoID: 22}); err != nil {
		t.Fatalf("add env: %v", err)
	}
	installationID, repoID, err := b.artifactSlots.GitHubAuth(env[artifacts.EnvSlotToken])
	if err != nil {
		t.Fatalf("GitHubAuth: %v", err)
	}
	if installationID != 11 || repoID != 22 {
		t.Fatalf("GitHubAuth = %d/%d, want 11/22", installationID, repoID)
	}
}

func TestArtifactSlotsHandlerRejectsBadTokenAndLimit(t *testing.T) {
	fake := &fakeArtifactMinter{}
	b := &Bot{
		log:           discardLogger(),
		artifacts:     fake,
		artifactSlots: newArtifactSlotBroker(fake),
	}
	env := map[string]string{}
	if _, err := b.addArtifactRunEnv(context.Background(), "org_abc/42/req_1", env, repoCtx{}); err != nil {
		t.Fatalf("add env: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, artifactSlotPath, strings.NewReader(`{"kind":"screenshot","content_type":"image/png","count":1}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", rec.Code)
	}

	tooMany := fmt.Sprintf(`{"kind":"screenshot","content_type":"image/png","count":%d}`, artifacts.MaxSlots)
	req = httptest.NewRequest(http.MethodPost, artifactSlotPath, strings.NewReader(tooMany))
	req.Header.Set("Authorization", "Bearer "+env[artifacts.EnvSlotToken])
	rec = httptest.NewRecorder()
	b.artifactSlotsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over limit status = %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}
