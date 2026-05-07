// Package screenshots mints AWS S3 pre-signed URLs the bootstrap
// validation prompt hands off to the in-sandbox agent. The agent uses
// the PUT URLs to upload screenshots without ever holding AWS
// credentials, then embeds the matching GET URLs in the PR body it
// drafts via `gh pr create`. The bot itself never fetches or rewrites
// the images — the only thing it does is sign at request-launch time.
//
// SigV4 caps signed URL lifetime at 7 days, so GET URLs expire well
// before the 90-day bucket lifecycle removes the underlying object.
// PRs reviewed within a week render normally; older ones show the
// markdown alt text in place of the image. A future enhancement could
// add a bot-side proxy endpoint that re-signs on demand to preserve
// rendering for the full object lifetime, but that's deferred — the
// 7-day window covers the practical PR-review timeframe.
package screenshots

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	// PutExpiry is how long the upload URLs stay valid. The agent's
	// task budget is well under this; we don't want stale URLs hanging
	// around in env vars after the run completes.
	PutExpiry = 30 * time.Minute

	// GetExpiry is the maximum SigV4 will sign for. The bucket's
	// lifecycle policy keeps the object on disk for 90 days, but
	// signed-URL holders can only render it during this window.
	GetExpiry = 7 * 24 * time.Hour

	// MaxSlots caps how many slots a single agent run can request.
	// One PR usually needs 1-3 screenshots; the cap prevents an
	// out-of-control prompt from issuing thousands of presigns.
	MaxSlots = 20
)

// Slot is one upload slot the agent can fill. PutURL accepts a single
// PUT for ~30 min; GetURL renders the resulting object for ~7 days.
type Slot struct {
	PutURL string `json:"put_url"`
	GetURL string `json:"get_url"`
}

// presigner is the subset of *s3.PresignClient we need. Defining it as
// an interface lets unit tests substitute a deterministic fake without
// dragging the AWS SDK transport layer into the test binary.
type presigner interface {
	PresignPutObject(ctx context.Context, input *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error)
	PresignGetObject(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error)
}

// signedRequest is a tiny shim over the AWS SDK's PresignedHTTPRequest
// so the presigner interface above doesn't import the SDK type
// directly (which would defeat the purpose of having the interface).
type signedRequest struct{ URL string }

// awsPresigner adapts the real *s3.PresignClient to the presigner
// interface. The wrapping is one method each — kept here rather than
// in a separate file so the package surface stays tight.
type awsPresigner struct{ inner *s3.PresignClient }

func (a *awsPresigner) PresignPutObject(ctx context.Context, input *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error) {
	r, err := a.inner.PresignPutObject(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return &signedRequest{URL: r.URL}, nil
}

func (a *awsPresigner) PresignGetObject(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error) {
	r, err := a.inner.PresignGetObject(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return &signedRequest{URL: r.URL}, nil
}

// Signer mints presigned URLs for one S3 bucket. Construct one at
// startup and reuse it across requests — the underlying *s3.Client is
// safe for concurrent use.
type Signer struct {
	bucket    string
	presigner presigner
}

// ErrNotConfigured is returned by New when bucket or region is empty.
// Callers treat this as "feature disabled" rather than a hard failure
// — the bot logs and continues; the validation prompt then falls back
// to telling the agent to skip embedded screenshots.
var ErrNotConfigured = errors.New("screenshots: HETCHY_S3_BUCKET / HETCHY_S3_REGION not set")

// New loads AWS credentials from the default chain (env vars, shared
// config, IAM role) and returns a Signer pinned to bucket+region.
// Returns ErrNotConfigured when bucket or region is empty.
func New(ctx context.Context, bucket, region string) (*Signer, error) {
	if bucket == "" || region == "" {
		return nil, ErrNotConfigured
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("screenshots: load aws config: %w", err)
	}
	s3client := s3.NewFromConfig(cfg)
	return &Signer{
		bucket:    bucket,
		presigner: &awsPresigner{inner: s3.NewPresignClient(s3client)},
	}, nil
}

// MintSlots returns n upload slots under prefix. Each slot points at
// `prefix/screenshot-NNN.png` (zero-padded so a sorted listing
// matches the agent's emit order). The PUT URL accepts one upload;
// the GET URL renders the resulting object until GetExpiry.
//
// Errors from the presigner are wrapped and returned, leaving partial
// slots on the floor — callers should treat any error as "screenshot
// hosting is unavailable for this run" rather than retrying mid-list.
func (s *Signer) MintSlots(ctx context.Context, prefix string, n int) ([]Slot, error) {
	if s == nil {
		return nil, errors.New("screenshots: nil signer")
	}
	if n <= 0 {
		return nil, fmt.Errorf("screenshots: slot count %d must be positive", n)
	}
	if n > MaxSlots {
		return nil, fmt.Errorf("screenshots: slot count %d exceeds max %d", n, MaxSlots)
	}
	slots := make([]Slot, 0, n)
	for i := range n {
		key := fmt.Sprintf("%s/screenshot-%03d.png", prefix, i)
		// ContentType is baked into the SigV4 signature, so the agent
		// MUST send `-H "Content-Type: image/png"` on its PUT — the
		// presign rejects mismatching content types. Without this
		// constraint a forgotten header lets S3 store the object as
		// application/octet-stream and GitHub refuses to render it
		// inline in the PR body.
		putReq, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(s.bucket),
			Key:         aws.String(key),
			ContentType: aws.String("image/png"),
		}, s3.WithPresignExpires(PutExpiry))
		if err != nil {
			return nil, fmt.Errorf("screenshots: presign put %s: %w", key, err)
		}
		getReq, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		}, s3.WithPresignExpires(GetExpiry))
		if err != nil {
			return nil, fmt.Errorf("screenshots: presign get %s: %w", key, err)
		}
		slots = append(slots, Slot{PutURL: putReq.URL, GetURL: getReq.URL})
	}
	return slots, nil
}
