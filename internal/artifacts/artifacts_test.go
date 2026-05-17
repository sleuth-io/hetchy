package artifacts

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakePresigner struct {
	puts       []*s3.PutObjectInput
	gets       []*s3.GetObjectInput
	putExpires []time.Duration
	getExpires []time.Duration
	failPut    bool
	failGet    bool
	urlIndex   int
}

func (f *fakePresigner) PresignPutObject(_ context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error) {
	if f.failPut {
		return nil, errors.New("forced put failure")
	}
	f.puts = append(f.puts, in)
	f.putExpires = append(f.putExpires, presignExpires(opts))
	url := fmt.Sprintf("https://example.test/put/%d", f.urlIndex)
	f.urlIndex++
	return &signedRequest{URL: url}, nil
}

func (f *fakePresigner) PresignGetObject(_ context.Context, in *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*signedRequest, error) {
	if f.failGet {
		return nil, errors.New("forced get failure")
	}
	f.gets = append(f.gets, in)
	f.getExpires = append(f.getExpires, presignExpires(opts))
	url := fmt.Sprintf("https://example.test/get/%d", f.urlIndex)
	f.urlIndex++
	return &signedRequest{URL: url}, nil
}

func presignExpires(opts []func(*s3.PresignOptions)) time.Duration {
	var po s3.PresignOptions
	for _, opt := range opts {
		opt(&po)
	}
	return po.Expires
}

func TestMintSlots_ArtifactMappings(t *testing.T) {
	cases := []struct {
		name        string
		req         MintRequest
		wantKey     string
		wantContent string
	}{
		{
			name:        "screenshot png",
			req:         MintRequest{Kind: KindScreenshot, ContentType: ContentTypePNG, Count: 1},
			wantKey:     "org_abc/42/req_xyz/screenshot-000.png",
			wantContent: ContentTypePNG,
		},
		{
			name:        "recording mp4",
			req:         MintRequest{Kind: KindRecording, ContentType: ContentTypeMP4, Count: 1, StartIndex: 2},
			wantKey:     "org_abc/42/req_xyz/recording-002.mp4",
			wantContent: ContentTypeMP4,
		},
		{
			name:        "diagram png",
			req:         MintRequest{Kind: KindDiagram, ContentType: ContentTypePNG, Count: 1},
			wantKey:     "org_abc/42/req_xyz/diagram-000.png",
			wantContent: ContentTypePNG,
		},
		{
			name:        "diagram svg",
			req:         MintRequest{Kind: KindDiagram, ContentType: ContentTypeSVG, Count: 1},
			wantKey:     "org_abc/42/req_xyz/diagram-000.svg",
			wantContent: ContentTypeSVG,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakePresigner{}
			s := &Signer{bucket: "test-bucket", presigner: fp}

			slots, err := s.MintSlots(context.Background(), "org_abc/42/req_xyz", tc.req)
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			if len(slots) != 1 {
				t.Fatalf("want 1 slot, got %d", len(slots))
			}
			if got := slots[0].Kind; got != tc.req.Kind {
				t.Fatalf("slot kind = %q, want %q", got, tc.req.Kind)
			}
			if got := slots[0].ContentType; got != tc.req.ContentType {
				t.Fatalf("slot content_type = %q, want %q", got, tc.req.ContentType)
			}
			if got := *fp.puts[0].Key; got != tc.wantKey {
				t.Fatalf("put key = %q, want %q", got, tc.wantKey)
			}
			if got := *fp.gets[0].Key; got != tc.wantKey {
				t.Fatalf("get key = %q, want %q", got, tc.wantKey)
			}
			if got := *fp.puts[0].ContentType; got != tc.wantContent {
				t.Fatalf("content type = %q, want %q", got, tc.wantContent)
			}
			if got := *fp.puts[0].Bucket; got != "test-bucket" {
				t.Fatalf("bucket = %q, want test-bucket", got)
			}
			if got := fp.putExpires[0]; got != PutExpiry {
				t.Fatalf("put expiry = %v, want %v", got, PutExpiry)
			}
			if got := fp.getExpires[0]; got != GetExpiry {
				t.Fatalf("get expiry = %v, want %v", got, GetExpiry)
			}
		})
	}
}

func TestMintSlots_RejectsInvalidRequests(t *testing.T) {
	s := &Signer{bucket: "x", presigner: &fakePresigner{}}
	cases := []MintRequest{
		{Kind: KindScreenshot, ContentType: ContentTypeMP4, Count: 1},
		{Kind: KindRecording, ContentType: ContentTypePNG, Count: 1},
		{Kind: KindDiagram, ContentType: "text/plain", Count: 1},
		{Kind: KindScreenshot, ContentType: ContentTypePNG, Count: 0},
		{Kind: KindScreenshot, ContentType: ContentTypePNG, Count: -1},
		{Kind: KindScreenshot, ContentType: ContentTypePNG, Count: MaxSlots + 1},
		{Kind: KindScreenshot, ContentType: ContentTypePNG, Count: 1, StartIndex: -1},
	}
	for _, req := range cases {
		if _, err := s.MintSlots(context.Background(), "p", req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("MintSlots(%+v) error = %v, want ErrInvalidRequest", req, err)
		}
	}
}

func TestMintSlots_PresignFailuresAbort(t *testing.T) {
	for _, fp := range []*fakePresigner{{failPut: true}, {failGet: true}} {
		s := &Signer{bucket: "x", presigner: fp}
		if _, err := s.MintSlots(context.Background(), "p", MintRequest{
			Kind:        KindScreenshot,
			ContentType: ContentTypePNG,
			Count:       3,
		}); err == nil {
			t.Error("expected error when presign fails")
		}
	}
}

func TestNew_MissingConfigReturnsSentinel(t *testing.T) {
	for _, c := range []struct{ bucket, region string }{
		{"", "us-west-2"},
		{"b", ""},
		{"", ""},
	} {
		s, err := New(context.Background(), c.bucket, c.region)
		if !errors.Is(err, ErrNotConfigured) {
			t.Errorf("New(%q,%q) error = %v, want ErrNotConfigured", c.bucket, c.region, err)
		}
		if s != nil {
			t.Errorf("New(%q,%q) signer = %v, want nil", c.bucket, c.region, s)
		}
	}
}

func TestMintSlots_NilSignerErrors(t *testing.T) {
	var s *Signer
	if _, err := s.MintSlots(context.Background(), "p", MintRequest{
		Kind:        KindScreenshot,
		ContentType: ContentTypePNG,
		Count:       1,
	}); err == nil {
		t.Error("nil receiver should error")
	}
}
