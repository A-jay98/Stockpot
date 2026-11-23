// Package secretbox encrypts small secrets at rest (API keys and tokens kept
// in the database) with AES-256-GCM under a key derived from the process's
// secret-key credential.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// version prefixes every ciphertext, so the format or key derivation can
// change later without guessing which one a stored value used.
const version byte = 1

// ErrDecrypt means the ciphertext is corrupt, was sealed under another key,
// or was moved to a different context (associated data).
var ErrDecrypt = errors.New("secretbox: cannot decrypt")

// Box seals and opens secrets. Safe for concurrent use.
type Box struct {
	aead cipher.AEAD
}

// New derives a 256-bit key for purpose from master with HKDF-SHA256.
// Different purposes get independent keys from the same master.
func New(master []byte, purpose string) (*Box, error) {
	if len(master) == 0 {
		return nil, errors.New("secretbox: empty master key")
	}
	key, err := hkdf.Key(sha256.New, master, nil, "stockpot/"+purpose, 32)
	if err != nil {
		return nil, fmt.Errorf("secretbox: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. ad binds the ciphertext to its context (e.g. the
// setting key), so a value copied to another row fails to open.
// Output: version || nonce || ciphertext+tag.
func (b *Box) Seal(plaintext, ad []byte) []byte {
	ns := b.aead.NonceSize()
	out := make([]byte, 1+ns, 1+ns+len(plaintext)+b.aead.Overhead())
	out[0] = version
	_, _ = rand.Read(out[1:])                           // crypto/rand.Read never fails (Go 1.24+)
	return b.aead.Seal(out, out[1:1+ns], plaintext, ad) //nolint:gosec // G407 false positive: the nonce is random, filled just above
}

// Open decrypts what Seal produced with the same ad.
func (b *Box) Open(sealed, ad []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < 1+ns+b.aead.Overhead() || sealed[0] != version {
		return nil, ErrDecrypt
	}
	pt, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
