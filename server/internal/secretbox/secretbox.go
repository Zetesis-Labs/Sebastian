// Package secretbox encrypts the secrets the server keeps at rest (provider
// API keys) with AES-256-GCM under SEBASTIAN_SECRETS_KEY. The associated data
// binds a value to its owner, so a ciphertext copied to another row fails.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

const KeySize = 32

var ErrKey = errors.New("SEBASTIAN_SECRETS_KEY must be 32 bytes, hex (64 characters) or base64 (44)")

type Box struct {
	aead cipher.AEAD
}

func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, ErrKey
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

func ParseKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if key, err := hex.DecodeString(encoded); err == nil && len(key) == KeySize {
		return key, nil
	}
	if key, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(key) == KeySize {
		return key, nil
	}
	return nil, ErrKey
}

// Seal returns nonce || ciphertext.
func (b *Box) Seal(plaintext, associated []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		panic("secretbox: no randomness: " + err.Error())
	}
	return b.aead.Seal(nonce, nonce, plaintext, associated)
}

func (b *Box) Open(sealed, associated []byte) ([]byte, error) {
	size := b.aead.NonceSize()
	if len(sealed) < size+b.aead.Overhead() {
		return nil, errors.New("secretbox: sealed value too short")
	}
	return b.aead.Open(nil, sealed[:size], sealed[size:], associated)
}
