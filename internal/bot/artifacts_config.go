package bot

import (
	"context"
	"net/http"

	"github.com/hetchyhq/hetchy/internal/artifacts"
)

type artifactHTTPServer interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}

func newArtifactMinter(ctx context.Context, cfg Config) (artifactMinter, string, error) {
	if cfg.ArtifactDir != "" {
		store, err := artifacts.NewLocal(cfg.ArtifactDir, cfg.PublicBaseURL(), cfg.SecretsEncryptionKey)
		return store, "local", err
	}
	signer, err := artifacts.New(ctx, cfg.S3Bucket, cfg.S3Region)
	return signer, "s3", err
}

func (b *Bot) localArtifactHandler(w http.ResponseWriter, r *http.Request) {
	h, ok := b.artifacts.(artifactHTTPServer)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}
