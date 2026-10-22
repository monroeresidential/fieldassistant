// Package auth holds authentication primitives: token encryption, sessions
// and the Procore OAuth broker.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// Vault encrypts secrets with AES-256-GCM. Blobs are nonce || ciphertext+tag.
type Vault struct {
	aead cipher.AEAD
}

// NewVault returns a Vault for a 32-byte key.
func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("vault key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault gcm: %w", err)
	}
	return &Vault{aead: aead}, nil
}

// Encrypt seals plaintext under a fresh random nonce and returns nonce || ciphertext.
func (v *Vault) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault nonce: %w", err)
	}
	return v.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt opens a blob produced by Encrypt.
func (v *Vault) Decrypt(blob []byte) (string, error) {
	n := v.aead.NonceSize()
	if len(blob) < n {
		return "", errors.New("vault blob too short")
	}
	pt, err := v.aead.Open(nil, blob[:n], blob[n:], nil)
	if err != nil {
		return "", fmt.Errorf("vault decrypt: %w", err)
	}
	return string(pt), nil
}
