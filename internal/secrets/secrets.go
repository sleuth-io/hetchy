// Package secrets provides AES-GCM encryption for storing per-org tokens
// (GitHub PAT, Slack tokens, etc.) at rest in Postgres. The encryption key
// is supplied at process start via SECRETS_ENCRYPTION_KEY — 32 raw bytes
// hex- or base64-encoded.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const keyLen = 32

// ErrEmptyCiphertext signals an attempt to decrypt a zero-length input.
// Treat this as "no value stored" rather than a malformed payload.
var ErrEmptyCiphertext = errors.New("secrets: empty ciphertext")

// Cipher wraps an AES-GCM AEAD with the project's envelope format. Construct
// it once at process start and pass it to anything that needs to read or
// write encrypted columns.
type Cipher struct {
	aead cipher.AEAD
}

// New parses the supplied key and returns a Cipher. The key may be 32 raw
// bytes, a 64-character hex string, or a base64-encoded 32-byte value.
func New(key string) (*Cipher, error) {
	raw, err := decodeKey(key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns nonce||ciphertext||tag. Empty plaintext returns nil so
// "unset" round-trips through the database as NULL rather than an
// indistinguishable encrypted blob.
func (c *Cipher) Encrypt(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	out := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return out, nil
}

// Decrypt reverses Encrypt. A nil or empty input returns "" with no error so
// callers can treat unset columns naturally.
func (c *Cipher) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", ErrEmptyCiphertext
	}
	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	return string(plain), nil
}

func decodeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("secrets: encryption key is empty (set SECRETS_ENCRYPTION_KEY)")
	}
	if len(s) == keyLen {
		return []byte(s), nil
	}
	if raw, err := hex.DecodeString(s); err == nil && len(raw) == keyLen {
		return raw, nil
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil && len(raw) == keyLen {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(raw) == keyLen {
		return raw, nil
	}
	return nil, fmt.Errorf("secrets: encryption key must decode to %d bytes (raw, hex, or base64)", keyLen)
}
