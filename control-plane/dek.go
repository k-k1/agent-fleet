// dek.go — ワークスペース DEK のエンベロープ暗号（P3-3）。
// manager.go からの機械的分割（docs/log/23 P2-W2）。
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// legacyDEK returns the raw DEK the Phase 2 / pre-P3-3 path derived as
// HMAC(master, userKey). It's used as the *first* DEK for a workspace so any
// existing secrets.enc (encrypted with this exact key) keeps decrypting after the
// move to envelope storage — no re-encryption.
func (m *manager) legacyDEK(userKey string) []byte {
	mac := hmac.New(sha256.New, m.master32)
	mac.Write([]byte(userKey))
	return mac.Sum(nil)
}

// resolveDEK returns the hex DEK to inject as AF_SECRET_KEY for a workspace,
// stored wrapped by the tenant KEK (docs/15 P3-3). On first use it mints the
// legacy DEK, wraps it via the custodian, and persists it. Returns "" in dev
// (no master/custodian) so the Agent stores secrets in plaintext as before.
func (m *manager) resolveDEK(ctx context.Context, ws store.Workspace, userKey string) (string, error) {
	if len(m.master32) == 0 || m.custodian == nil {
		return "", nil
	}
	keyRef := ws.TenantID
	ct, kr, ok, err := m.store.GetWrappedDEK(ctx, ws.ID)
	if err != nil {
		return "", err
	}
	var dek []byte
	if ok {
		if dek, err = m.custodian.Unwrap(ctx, kr, ct); err != nil {
			return "", err
		}
	} else {
		dek = m.legacyDEK(userKey) // preserve existing secrets.enc
		if ct, err = m.custodian.Wrap(ctx, keyRef, dek); err != nil {
			return "", err
		}
		if err := m.store.PutWrappedDEK(ctx, ws.ID, ct, keyRef); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(dek), nil
}
