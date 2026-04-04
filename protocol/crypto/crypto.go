// Package crypto provides X25519 key exchange and AES-256-GCM encryption
// for end-to-end encrypted communication between SDK clients and guest agents.
//
// The daemon never sees plaintext payloads — it forwards opaque encrypted
// blobs while still being able to read JSON-RPC method names for routing.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// KeyPair holds an X25519 private/public key pair.
type KeyPair struct {
	Private *ecdh.PrivateKey
	Public  *ecdh.PublicKey
}

// GenerateKeyPair generates a new X25519 key pair.
func GenerateKeyPair() (*KeyPair, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate x25519 key: %w", err)
	}
	return &KeyPair{
		Private: priv,
		Public:  priv.PublicKey(),
	}, nil
}

// PublicKeyFromBytes parses a raw 32-byte X25519 public key.
func PublicKeyFromBytes(b []byte) (*ecdh.PublicKey, error) {
	curve := ecdh.X25519()
	pub, err := curve.NewPublicKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse x25519 public key: %w", err)
	}
	return pub, nil
}

// DeriveSharedSecret performs X25519 ECDH and returns the 32-byte shared secret.
func DeriveSharedSecret(priv *ecdh.PrivateKey, peerPub *ecdh.PublicKey) ([]byte, error) {
	secret, err := priv.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	return secret, nil
}

// Session holds a negotiated encryption session with AES-256-GCM.
type Session struct {
	gcm cipher.AEAD
}

// NewSession creates an encryption session from a 32-byte shared secret.
func NewSession(sharedSecret []byte) (*Session, error) {
	if len(sharedSecret) != 32 {
		return nil, fmt.Errorf("shared secret must be 32 bytes, got %d", len(sharedSecret))
	}
	block, err := aes.NewCipher(sharedSecret)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: %w", err)
	}
	return &Session{gcm: gcm}, nil
}

// Seal encrypts plaintext with AES-256-GCM and returns nonce+ciphertext.
func (s *Session) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return s.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts nonce+ciphertext produced by Seal.
func (s *Session) Open(ciphertext []byte) ([]byte, error) {
	nonceSize := s.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce := ciphertext[:nonceSize]
	return s.gcm.Open(nil, nonce, ciphertext[nonceSize:], nil)
}

// SealBase64 encrypts plaintext and returns it as a base64 string
// suitable for embedding in JSON-RPC params/results.
func (s *Session) SealBase64(plaintext []byte) (string, error) {
	ct, err := s.Seal(plaintext)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// OpenBase64 decrypts a base64-encoded ciphertext.
func (s *Session) OpenBase64(encoded string) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	return s.Open(ct)
}
