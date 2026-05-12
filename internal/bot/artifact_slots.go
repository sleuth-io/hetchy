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

	"github.com/hetchyhq/hetchy/internal/artifacts"
)

const artifactSlotPath = "/api/artifact-slots"

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
	prefix  string
	expires time.Time
	issued  int
	next    map[artifactSlotKey]int
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

func (b *Bot) startArtifactRun(ctx context.Context, prefix string) ([]artifacts.Slot, string, error) {
	if b.artifacts == nil {
		return nil, "", errArtifactSlotsDisabled
	}
	return b.artifactSlots.Start(ctx, prefix, defaultArtifactSlotRequests)
}

func (b *Bot) addArtifactRunEnv(ctx context.Context, prefix string, env map[string]string) ([]artifacts.Slot, error) {
	slots, token, err := b.startArtifactRun(ctx, prefix)
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

func (br *artifactSlotBroker) Start(ctx context.Context, prefix string, reqs []artifacts.MintRequest) ([]artifacts.Slot, string, error) {
	if br == nil || br.signer == nil {
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
		prefix:  prefix,
		expires: br.now().Add(artifacts.PutExpiry),
		next:    make(map[artifactSlotKey]int),
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

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
