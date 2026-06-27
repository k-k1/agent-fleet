package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// KeyCustodian wraps/unwraps per-workspace data encryption keys (DEKs) with a
// per-tenant key-encryption key (KEK). It is the envelope-encryption seam
// (docs/15 P3-3): the on-prem default is localCustodian; Vault transit / AWS KMS
// implement the same interface so a tenant's KEK can be disabled for true
// crypto-shred. keyRef selects the tenant key (localCustodian uses the tenant id).
type KeyCustodian interface {
	Wrap(ctx context.Context, keyRef string, dek []byte) (string, error)
	Unwrap(ctx context.Context, keyRef, ciphertext string) ([]byte, error)
}

// localCustodian derives a per-tenant KEK from the deployment master key via HMAC
// and wraps DEKs with AES-256-GCM. The KEK never leaves the process. NOTE
// (docs/15 §15.2): because the KEK is master-derived, holding the master unwraps
// every DEK — strength equals the single-master model. True per-tenant disable
// requires the Vault/KMS adapters; this lays the seam for them.
type localCustodian struct{ master32 []byte }

func newLocalCustodian(master32 []byte) *localCustodian { return &localCustodian{master32: master32} }

func (c *localCustodian) gcm(keyRef string) (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, c.master32)
	mac.Write([]byte("af-kek:" + keyRef))
	block, err := aes.NewCipher(mac.Sum(nil)) // 32-byte KEK -> AES-256
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *localCustodian) Wrap(_ context.Context, keyRef string, dek []byte) (string, error) {
	g, err := c.gcm(keyRef)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	// AAD = keyRef binds a wrapped DEK to its tenant.
	ct := g.Seal(nonce, nonce, dek, []byte(keyRef))
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (c *localCustodian) Unwrap(_ context.Context, keyRef, ciphertext string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	g, err := c.gcm(keyRef)
	if err != nil {
		return nil, err
	}
	if len(raw) < g.NonceSize() {
		return nil, errors.New("wrapped dek too short")
	}
	nonce, ct := raw[:g.NonceSize()], raw[g.NonceSize():]
	return g.Open(nil, nonce, ct, []byte(keyRef))
}
