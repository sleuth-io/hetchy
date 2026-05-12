// Package artifacts mints AWS S3 pre-signed URLs for validation proof
// artifacts the in-sandbox coding agent attaches to PR markdown.
//
// The agent receives short-lived PUT URLs, uploads screenshots,
// recordings, or diagrams without AWS credentials, then links the
// matching GET URLs in the PR. SigV4 caps signed URL lifetime at 7
// days, so old PRs may stop rendering the linked artifacts before the
// bucket lifecycle removes the objects.
package artifacts

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
	KindScreenshot = "screenshot"
	KindRecording  = "recording"
	KindDiagram    = "diagram"

	ContentTypePNG = "image/png"
	ContentTypeMP4 = "video/mp4"
	ContentTypeSVG = "image/svg+xml"

	EnvSlots     = "HETCHY_ARTIFACT_SLOTS"
	EnvSlotURL   = "HETCHY_ARTIFACT_SLOT_URL"
	EnvSlotToken = "HETCHY_ARTIFACT_SLOT_TOKEN"

	// PutExpiry is how long upload URLs stay valid. The agent's task
	// budget is well under this; stale URLs should not linger after a
	// run completes.
	PutExpiry = 30 * time.Minute

	// GetExpiry is the maximum SigV4 will sign for. The bucket
	// lifecycle can retain objects longer, but signed URL holders can
	// only render them during this window.
	GetExpiry = 7 * 24 * time.Hour

	// MaxSlots caps the total number of artifact upload slots a single
	// agent run can consume.
	MaxSlots = 20
)

// Slot is one upload slot the agent can fill. PutURL accepts one upload
// for ~30 min; GetURL renders the resulting object for ~7 days.
type Slot struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	PutURL      string `json:"put_url"`
	GetURL      string `json:"get_url"`
}

// MintRequest describes one homogeneous batch of slots to mint.
// StartIndex lets the bot request follow-on batches without S3 key
// collisions under the same run prefix.
type MintRequest struct {
	Kind        string
	ContentType string
	Count       int
	StartIndex  int
}

type artifactSpec struct {
	stem string
	ext  string
}

var artifactSpecs = map[string]artifactSpec{
	KindScreenshot + "\x00" + ContentTypePNG: {stem: "screenshot", ext: ".png"},
	KindRecording + "\x00" + ContentTypeMP4:  {stem: "recording", ext: ".mp4"},
	KindDiagram + "\x00" + ContentTypePNG:    {stem: "diagram", ext: ".png"},
	KindDiagram + "\x00" + ContentTypeSVG:    {stem: "diagram", ext: ".svg"},
}

var (
	// ErrNotConfigured is returned by New when bucket or region is
	// empty. Callers treat this as "artifact upload disabled".
	ErrNotConfigured = errors.New("artifacts: HETCHY_S3_BUCKET / HETCHY_S3_REGION not set")

	// ErrInvalidRequest marks invalid kind/content_type/count inputs.
	ErrInvalidRequest = errors.New("artifacts: invalid slot request")
)

func specFor(kind, contentType string) (artifactSpec, error) {
	spec, ok := artifactSpecs[kind+"\x00"+contentType]
	if !ok {
		return artifactSpec{}, fmt.Errorf("%w: unsupported kind/content_type %q/%q", ErrInvalidRequest, kind, contentType)
	}
	return spec, nil
}

// ValidateRequest verifies that req names a supported artifact type and
// asks for a sane number of slots. It does not validate StartIndex.
func ValidateRequest(req MintRequest) error {
	if _, err := specFor(req.Kind, req.ContentType); err != nil {
		return err
	}
	if req.Count <= 0 {
		return fmt.Errorf("%w: slot count %d must be positive", ErrInvalidRequest, req.Count)
	}
	if req.Count > MaxSlots {
		return fmt.Errorf("%w: slot count %d exceeds max %d", ErrInvalidRequest, req.Count, MaxSlots)
	}
	if req.StartIndex < 0 {
		return fmt.Errorf("%w: start index %d must be non-negative", ErrInvalidRequest, req.StartIndex)
	}
	return nil
}

// presigner is the subset of *s3.PresignClient we need. Defining it as
// an interface lets unit tests substitute a deterministic fake.
type presigner interface {
	PresignPutObject(ctx context.Context, input *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error)
	PresignGetObject(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error)
}

type signedRequest struct{ URL string }

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
// startup and reuse it across requests; the underlying S3 client is
// safe for concurrent use.
type Signer struct {
	bucket    string
	presigner presigner
}

// New loads AWS credentials from the default chain and returns a Signer
// pinned to bucket+region. Returns ErrNotConfigured when either input
// is empty.
func New(ctx context.Context, bucket, region string) (*Signer, error) {
	if bucket == "" || region == "" {
		return nil, ErrNotConfigured
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("artifacts: load aws config: %w", err)
	}
	s3client := s3.NewFromConfig(cfg)
	return &Signer{
		bucket:    bucket,
		presigner: &awsPresigner{inner: s3.NewPresignClient(s3client)},
	}, nil
}

// MintSlots returns req.Count upload slots under prefix. Keys are named
// from the artifact kind and content type, for example:
//
//   - prefix/screenshot-000.png
//   - prefix/recording-000.mp4
//   - prefix/diagram-000.svg
//
// Errors from the presigner are wrapped and returned, leaving partial
// slots on the floor. Callers should treat any error as "artifact
// hosting is unavailable for this run" rather than retrying mid-list.
func (s *Signer) MintSlots(ctx context.Context, prefix string, req MintRequest) ([]Slot, error) {
	if s == nil {
		return nil, errors.New("artifacts: nil signer")
	}
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	spec, _ := specFor(req.Kind, req.ContentType)
	slots := make([]Slot, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		index := req.StartIndex + i
		key := fmt.Sprintf("%s/%s-%03d%s", prefix, spec.stem, index, spec.ext)
		// ContentType is baked into the SigV4 signature, so the agent
		// must send the matching Content-Type header on PUT. Without
		// this, S3 may store an object as application/octet-stream and
		// GitHub may refuse to render it inline.
		putReq, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(s.bucket),
			Key:         aws.String(key),
			ContentType: aws.String(req.ContentType),
		}, s3.WithPresignExpires(PutExpiry))
		if err != nil {
			return nil, fmt.Errorf("artifacts: presign put %s: %w", key, err)
		}
		getReq, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		}, s3.WithPresignExpires(GetExpiry))
		if err != nil {
			return nil, fmt.Errorf("artifacts: presign get %s: %w", key, err)
		}
		slots = append(slots, Slot{
			Kind:        req.Kind,
			ContentType: req.ContentType,
			PutURL:      putReq.URL,
			GetURL:      getReq.URL,
		})
	}
	return slots, nil
}
