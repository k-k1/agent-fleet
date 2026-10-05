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
	"strings"
)

// KeyCustodian wraps/unwraps per-workspace data encryption keys (DEKs) with a
// per-tenant key-encryption key (KEK). It is the envelope-encryption seam
// (ADR 0005): the on-prem default is localCustodian, kmsCustodian (custodian_kms.go)
// is the AWS one, selected by AF_KEY_CUSTODIAN. keyRef selects the tenant key (the
// tenant id, or "deployment" for deployment-wide values).
//
// internal/mcpsrv declares a copy of this interface; mcp_wiring.go's cpDeps.Custodian
// returns this one as that one, so a method the copy gains that this one lacks is a
// compile error.
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
	// Without this a deployment switched back from kms reads as a base64 error.
	if strings.HasPrefix(ciphertext, kmsSealPrefix) {
		return nil, errors.New("value was sealed by the KMS key custodian; set AF_KEY_CUSTODIAN=kms and AF_KMS_KEY_ID to open it")
	}
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
