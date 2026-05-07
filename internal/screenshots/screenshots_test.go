package screenshots

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakePresigner returns deterministic URLs and records every input
// the Signer passes through. Tests assert on the recorded keys to pin
// the per-slot naming convention without spinning up an HTTP fake or
// hitting AWS — `s3.PresignClient` constructs URLs locally, so a real
// client with bogus creds would also work, but the recorder lets us
// inspect intent precisely (e.g. did the Signer pass the right
// expiry, in the right order, for the right bucket).
type fakePresigner struct {
	puts     []*s3.PutObjectInput
	gets     []*s3.GetObjectInput
	failPut  bool
	failGet  bool
	urlIndex int
}

func (f *fakePresigner) PresignPutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*signedRequest, error) {
	if f.failPut {
		return nil, errors.New("forced put failure")
	}
	f.puts = append(f.puts, in)
	url := fmt.Sprintf("https://example.test/put/%d", f.urlIndex)
	f.urlIndex++
	return &signedRequest{URL: url}, nil
}

func (f *fakePresigner) PresignGetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*signedRequest, error) {
	if f.failGet {
		return nil, errors.New("forced get failure")
	}
	f.gets = append(f.gets, in)
	url := fmt.Sprintf("https://example.test/get/%d", f.urlIndex)
	f.urlIndex++
	return &signedRequest{URL: url}, nil
}

func TestMintSlots_HappyPath(t *testing.T) {
	fp := &fakePresigner{}
	s := &Signer{bucket: "test-bucket", presigner: fp}

	slots, err := s.MintSlots(context.Background(), "org_abc/42/req_xyz", 3)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(slots) != 3 {
		t.Fatalf("want 3 slots, got %d", len(slots))
	}

	for i, slot := range slots {
		if slot.PutURL == "" || slot.GetURL == "" {
			t.Errorf("slot[%d] missing url(s): %+v", i, slot)
		}
		if slot.PutURL == slot.GetURL {
			t.Errorf("slot[%d] put and get urls collide: %s", i, slot.PutURL)
		}
	}

	if len(fp.puts) != 3 || len(fp.gets) != 3 {
		t.Fatalf("want 3 puts + 3 gets, got %d/%d", len(fp.puts), len(fp.gets))
	}

	// Keys must be zero-padded so a sorted listing matches the
	// emit order — agents that upload in slot order see their
	// screenshots in the right sequence in S3.
	wantKeys := []string{
		"org_abc/42/req_xyz/screenshot-000.png",
		"org_abc/42/req_xyz/screenshot-001.png",
		"org_abc/42/req_xyz/screenshot-002.png",
	}
	for i, want := range wantKeys {
		if got := *fp.puts[i].Key; got != want {
			t.Errorf("put[%d] key = %q, want %q", i, got, want)
		}
		if got := *fp.gets[i].Key; got != want {
			t.Errorf("get[%d] key = %q, want %q", i, got, want)
		}
		if got := *fp.puts[i].Bucket; got != "test-bucket" {
			t.Errorf("put[%d] bucket = %q, want test-bucket", i, got)
		}
	}
}

func TestMintSlots_RejectsOutOfRange(t *testing.T) {
	s := &Signer{bucket: "x", presigner: &fakePresigner{}}
	cases := []int{0, -1, MaxSlots + 1, 1000}
	for _, n := range cases {
		if _, err := s.MintSlots(context.Background(), "p", n); err == nil {
			t.Errorf("MintSlots(n=%d) should error, got nil", n)
		}
	}
}

func TestMintSlots_PutFailureAborts(t *testing.T) {
	fp := &fakePresigner{failPut: true}
	s := &Signer{bucket: "x", presigner: fp}
	if _, err := s.MintSlots(context.Background(), "p", 3); err == nil {
		t.Error("expected error when presign put fails")
	}
}

func TestMintSlots_GetFailureAborts(t *testing.T) {
	fp := &fakePresigner{failGet: true}
	s := &Signer{bucket: "x", presigner: fp}
	if _, err := s.MintSlots(context.Background(), "p", 3); err == nil {
		t.Error("expected error when presign get fails")
	}
}

func TestNew_MissingConfigReturnsSentinel(t *testing.T) {
	// Empty bucket OR empty region must yield ErrNotConfigured so
	// callers can treat "not configured" as a no-op (errors.Is)
	// instead of a hard failure that blocks bot startup.
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
	if _, err := s.MintSlots(context.Background(), "p", 1); err == nil {
		t.Error("nil receiver should error")
	}
}
