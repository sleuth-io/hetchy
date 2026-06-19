package secrets_test

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/secrets"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	key := strings.Repeat("k", 32)
	c, err := secrets.New(key)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	cases := []string{
		"",
		"short",
		"xoxb-1234567890-abcdefghijklmnopqrstuvwx",
		strings.Repeat("a", 4096),
	}
	for _, tc := range cases {
		ct, err := c.Encrypt(tc)
		if err != nil {
			t.Fatalf("encrypt %q: %v", tc, err)
		}
		if tc == "" && ct != nil {
			t.Fatalf("empty plaintext should produce nil ciphertext, got %d bytes", len(ct))
		}
		plain, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt %q: %v", tc, err)
		}
		if plain != tc {
			t.Fatalf("round trip mismatch: got %q want %q", plain, tc)
		}
	}
}

func TestKeyEncodings(t *testing.T) {
	t.Parallel()

	raw := []byte(strings.Repeat("k", 32))
	encodings := []string{
		string(raw),
		hex.EncodeToString(raw),
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
	}
	for _, enc := range encodings {
		if _, err := secrets.New(enc); err != nil {
			t.Fatalf("encoding %q: %v", enc, err)
		}
	}
}

func TestRejectsBadKey(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "short", strings.Repeat("k", 33)} {
		if _, err := secrets.New(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestNonceUniqueness(t *testing.T) {
	t.Parallel()

	c, err := secrets.New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if string(a) == string(b) {
		t.Fatalf("two encryptions of the same plaintext produced identical ciphertext")
	}
}
