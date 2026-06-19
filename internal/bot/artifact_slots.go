package bot

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sleuth-io/hetchy/internal/artifacts"
)

const artifactSlotPath = "/api/artifact-slots"
const artifactSlotKindGitHubToken = "github_token"

// 8 h covers the longest expected agent run with multiple GitHub token
// refreshes. Broker tokens are scoped to one repo and live only in memory.
const artifactRunTokenExpiry = 8 * time.Hour

var defaultArtifactSlotRequests = []artifacts.MintRequest{
	{Kind: artifacts.KindScreenshot, ContentType: artifacts.ContentTypePNG, Count: 1},
	{Kind: artifacts.KindRecording, ContentType: artifacts.ContentTypeMP4, Count: 1},
	{Kind: artifacts.KindDiagram, ContentType: artifacts.ContentTypePNG, Count: 1},
}

var (
	errArtifactSlotsDisabled = errors.New("artifact slots disabled")
	errArtifactTokenInvalid  = errors.New("artifact slot token invalid")
	errArtifactSlotLimit     = errors.New("artifact slot limit exceeded")
)

type artifactMinter interface {
	MintSlots(context.Context, string, artifacts.MintRequest) ([]artifacts.Slot, error)
}

type artifactSlotBroker struct {
	mu     sync.Mutex
	signer artifactMinter
	now    func() time.Time
	runs   map[string]*artifactSlotRun
}

type artifactSlotRun struct {
	prefix               string
	expires              time.Time
	issued               int
	next                 map[artifactSlotKey]int
	githubInstallationID int64
	githubRepoID         int64
}

type artifactSlotKey struct {
	kind        string
	contentType string
}

func newArtifactSlotBroker(signer artifactMinter) *artifactSlotBroker {
	return &artifactSlotBroker{
		signer: signer,
		now:    time.Now,
		runs:   make(map[string]*artifactSlotRun),
	}
}

type artifactSlotRunOptions struct {
	githubInstallationID int64
	githubRepoID         int64
}

func (b *Bot) startArtifactRun(ctx context.Context, prefix string, repo repoCtx) ([]artifacts.Slot, string, error) {
	if b.artifacts == nil {
		return nil, "", errArtifactSlotsDisabled
	}
	return b.artifactSlots.Start(ctx, prefix, defaultArtifactSlotRequests, artifactSlotRunOptions{
		githubInstallationID: repo.InstallID,
		githubRepoID:         repo.RepoID,
	})
}

func (b *Bot) addGitHubTokenRefreshEnv(ctx context.Context, prefix string, env map[string]string, repo repoCtx) error {
	if b == nil || b.artifactSlots == nil || (b.githubTokenSource() == nil && b.githubTokenMinTTLFn == nil) || repo.InstallID == 0 || repo.RepoID == 0 {
		return errArtifactSlotsDisabled
	}
	_, token, err := b.artifactSlots.Start(ctx, prefix, nil, artifactSlotRunOptions{
		githubInstallationID: repo.InstallID,
		githubRepoID:         repo.RepoID,
	})
	if err != nil {
		return err
	}
	env[artifacts.EnvSlotURL] = b.cfg.PublicBaseURL() + artifactSlotPath
	env[artifacts.EnvSlotToken] = token
	return nil
}

func (b *Bot) addSandboxGitHubAuthEnv(ctx context.Context, prefix string, env map[string]string, repo repoCtx, requestID, phase string, withArtifactSlots bool) int {
	if repo.RepoID == 0 {
		return 0
	}
	artifactSlotCount := 0
	if withArtifactSlots {
		slots, err := b.addArtifactRunEnv(ctx, prefix, env, repo)
		switch {
		case err == nil:
			artifactSlotCount = len(slots)
		case errors.Is(err, errArtifactSlotsDisabled):
			// No S3 upload path configured; validation prompting will
			// ask the agent to mark proof artifacts incomplete.
		default:
			b.log.Warn("artifact slot minting failed",
				"phase", phase, "request_id", requestID, "error", err)
		}
	}
	if env[artifacts.EnvSlotToken] == "" {
		if err := b.addGitHubTokenRefreshEnv(ctx, prefix, env, repo); err != nil && !errors.Is(err, errArtifactSlotsDisabled) {
			b.log.Warn("github token refresh env failed",
				"phase", phase, "request_id", requestID, "error", err)
		}
	}
	return artifactSlotCount
}

func (b *Bot) addArtifactRunEnv(ctx context.Context, prefix string, env map[string]string, repo repoCtx) ([]artifacts.Slot, error) {
	slots, token, err := b.startArtifactRun(ctx, prefix, repo)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(slots)
	if err != nil {
		return nil, fmt.Errorf("artifact slot marshal: %w", err)
	}
	env[artifacts.EnvSlots] = string(raw)
	env[artifacts.EnvSlotURL] = b.cfg.PublicBaseURL() + artifactSlotPath
	env[artifacts.EnvSlotToken] = token
	return slots, nil
}

func (br *artifactSlotBroker) Start(ctx context.Context, prefix string, reqs []artifacts.MintRequest, opts artifactSlotRunOptions) ([]artifacts.Slot, string, error) {
	if br == nil || (br.signer == nil && len(reqs) > 0) {
		return nil, "", errArtifactSlotsDisabled
	}
	token, err := randomArtifactToken()
	if err != nil {
		return nil, "", err
	}

	br.mu.Lock()
	defer br.mu.Unlock()
	br.pruneLocked()

	run := &artifactSlotRun{
		prefix:               prefix,
		expires:              br.now().Add(artifactRunTokenExpiry),
		next:                 make(map[artifactSlotKey]int),
		githubInstallationID: opts.githubInstallationID,
		githubRepoID:         opts.githubRepoID,
	}
	var all []artifacts.Slot
	for _, req := range reqs {
		slots, err := br.mintLocked(ctx, run, req)
		if err != nil {
			return nil, "", err
		}
		all = append(all, slots...)
	}
	br.runs[token] = run
	return all, token, nil
}

func (br *artifactSlotBroker) GitHubAuth(token string) (int64, int64, error) {
	if br == nil {
		return 0, 0, errArtifactSlotsDisabled
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	br.pruneLocked()

	var run *artifactSlotRun
	for candidate, r := range br.runs {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			run = r
			break
		}
	}
	if run == nil {
		return 0, 0, errArtifactTokenInvalid
	}
	// pruneLocked above guarantees run is not expired.
	if run.githubInstallationID == 0 || run.githubRepoID == 0 {
		return 0, 0, errors.New("github token unavailable for this run")
	}
	return run.githubInstallationID, run.githubRepoID, nil
}

func (br *artifactSlotBroker) Mint(ctx context.Context, token string, req artifacts.MintRequest) ([]artifacts.Slot, error) {
	if br == nil || br.signer == nil {
		return nil, errArtifactSlotsDisabled
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	br.pruneLocked()

	var run *artifactSlotRun
	for candidate, r := range br.runs {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			run = r
			break
		}
	}
	if run == nil {
		return nil, errArtifactTokenInvalid
	}
	if br.now().After(run.expires) {
		delete(br.runs, token)
		return nil, errArtifactTokenInvalid
	}
	return br.mintLocked(ctx, run, req)
}

func (br *artifactSlotBroker) mintLocked(ctx context.Context, run *artifactSlotRun, req artifacts.MintRequest) ([]artifacts.Slot, error) {
	if br.signer == nil {
		return nil, errArtifactSlotsDisabled
	}
	if err := artifacts.ValidateRequest(req); err != nil {
		return nil, err
	}
	if run.issued+req.Count > artifacts.MaxSlots {
		return nil, fmt.Errorf("%w: requested %d with %d already issued, max %d",
			errArtifactSlotLimit, req.Count, run.issued, artifacts.MaxSlots)
	}
	key := artifactSlotKey{kind: req.Kind, contentType: req.ContentType}
	req.StartIndex = run.next[key]
	slots, err := br.signer.MintSlots(ctx, run.prefix, req)
	if err != nil {
		return nil, err
	}
	run.next[key] += req.Count
	run.issued += req.Count
	return slots, nil
}

func (br *artifactSlotBroker) pruneLocked() {
	now := br.now()
	for token, run := range br.runs {
		if now.After(run.expires) {
			delete(br.runs, token)
		}
	}
}

func randomArtifactToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("artifact token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

type artifactSlotRequest struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Count       int    `json:"count"`
}

type githubTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func (b *Bot) artifactSlotsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var body artifactSlotRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Kind == artifactSlotKindGitHubToken {
		b.artifactGitHubTokenHandler(w, r, token)
		return
	}
	req := artifacts.MintRequest{
		Kind:        body.Kind,
		ContentType: body.ContentType,
		Count:       body.Count,
	}
	slots, err := b.artifactSlots.Mint(r.Context(), token, req)
	switch {
	case err == nil:
		writeJSON(w, slots)
	case errors.Is(err, errArtifactTokenInvalid):
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
	case errors.Is(err, artifacts.ErrInvalidRequest), errors.Is(err, errArtifactSlotLimit):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errArtifactSlotsDisabled):
		http.Error(w, "artifact upload disabled", http.StatusServiceUnavailable)
	default:
		b.log.Warn("artifact slot minting failed", "error", err)
		http.Error(w, "artifact slot minting failed", http.StatusInternalServerError)
	}
}

func (b *Bot) artifactGitHubTokenHandler(w http.ResponseWriter, r *http.Request, token string) {
	if b == nil || b.artifactSlots == nil || (b.githubTokenSource() == nil && b.githubTokenMinTTLFn == nil) {
		http.Error(w, "github token refresh unavailable", http.StatusServiceUnavailable)
		return
	}
	installationID, repoID, err := b.artifactSlots.GitHubAuth(token)
	switch {
	case err == nil:
	case errors.Is(err, errArtifactTokenInvalid):
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	case errors.Is(err, errArtifactSlotsDisabled):
		http.Error(w, "artifact upload disabled", http.StatusServiceUnavailable)
		return
	default:
		b.log.Warn("github token refresh lookup error", "error", err)
		http.Error(w, "github token refresh unavailable", http.StatusInternalServerError)
		return
	}
	ghToken, exp, err := b.githubTokenMinTTL(r.Context(), installationID, []int64{repoID}, sandboxGitHubTokenMinTTL)
	if err != nil {
		b.log.Warn("github token refresh failed", "installation_id", installationID, "repo_id", repoID, "error", err)
		http.Error(w, "github token refresh failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, githubTokenResponse{
		Token:     ghToken,
		ExpiresAt: exp.UTC().Format(time.RFC3339),
	})
}

func (b *Bot) githubTokenMinTTL(ctx context.Context, installationID int64, repoIDs []int64, minTTL time.Duration) (string, time.Time, error) {
	if b.githubTokenMinTTLFn != nil {
		return b.githubTokenMinTTLFn(ctx, installationID, repoIDs, minTTL)
	}
	src := b.githubTokenSource()
	if src == nil {
		return "", time.Time{}, errors.New("github is not configured")
	}
	return src.InstallationTokenMinTTL(ctx, installationID, repoIDs, minTTL)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
