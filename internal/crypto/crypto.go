// Package crypto encrypts connection credentials at rest with AES-256-GCM.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
)

type Box struct{ aead cipher.AEAD }

// New builds a cipher from a 32-byte key supplied as 64-char hex or base64.
// An empty key generates an ephemeral one (credentials won't survive a restart).
func New(key string) (*Box, error) {
	var raw []byte
	switch {
	case key == "":
		raw = make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		log.Println("crypto: DBM_ENC_KEY not set using an EPHEMERAL key; stored credentials will be unreadable after restart")
	case len(key) == 64:
		b, err := hex.DecodeString(key)
		if err != nil {
			return nil, fmt.Errorf("DBM_ENC_KEY hex decode: %w", err)
		}
		raw = b
	default:
		b, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			return nil, fmt.Errorf("DBM_ENC_KEY must be 64-char hex or base64 of 32 bytes: %w", err)
		}
		raw = b
	}
	if len(raw) != 32 {
		return nil, errors.New("DBM_ENC_KEY must decode to exactly 32 bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt returns base64(nonce||ciphertext).
func (b *Box) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt reverses Encrypt.
func (b *Box) Decrypt(enc string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(data) < ns {
		return "", errors.New("ciphertext too short")
	}
	pt, err := b.aead.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
