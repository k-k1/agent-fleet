// dek.go — envelope encryption for the workspace DEK (P3-3).
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
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

// resolveDEK returns the credential-store keys to inject for a workspace's start
// (resolveStartKeys).
func (m *manager) resolveDEK(ctx context.Context, ws store.Workspace, userKey string) (runtime.SecretKeys, error) {
	keys, _, err := m.resolveStartKeys(ctx, ws, userKey)
	return keys, err
}

// resolveStartKeys returns the keys a start injects, and the home's home_dek row when it has
// one. Zero in dev (no master/custodian) so the Agent stores secrets in plaintext as before.
//
//   - no home key: Key is the derived DEK stored wrapped by the tenant KEK (docs/15 P3-3).
//   - 'migrating': Key is the derived DEK and Next the home's key; the Agent re-seals the
//     store under Next and the CP confirms it from /healthz (home_dek_confirm.go).
//   - 'random': Key is the home's key and there is no Next. The derived key is not handed
//     out any more; the confirmed store does not need it.
func (m *manager) resolveStartKeys(ctx context.Context, ws store.Workspace, userKey string) (runtime.SecretKeys, *store.HomeDEK, error) {
	if len(m.master32) == 0 || m.custodian == nil {
		return runtime.SecretKeys{}, nil, nil
	}
	home, homeKey, err := m.resolveHomeDEK(ctx, ws)
	if err != nil {
		return runtime.SecretKeys{}, nil, err
	}
	if home != nil && home.Scheme == store.HomeDEKRandom {
		return runtime.SecretKeys{Key: homeKey}, home, nil
	}
	key, err := m.resolveWrappedDEK(ctx, ws, userKey)
	if err != nil {
		return runtime.SecretKeys{}, nil, err
	}
	return runtime.SecretKeys{Key: key, Next: homeKey}, home, nil
}

// resolveWrappedDEK returns the hex derived DEK. On first use it mints the legacy DEK, wraps
// it via the custodian, and persists it.
func (m *manager) resolveWrappedDEK(ctx context.Context, ws store.Workspace, userKey string) (string, error) {
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

// resolveHomeDEK returns the home_dek row of the workspace's home and its hex key (ADR 0005
// addendum 2026-10-10), minting it on the first start after AF_WORKSPACE_DEK=random; nil and
// "" for a home without one. A home that has
// one keeps using it whatever the flag says now, because its Agent may already have re-sealed
// the store under it. Any custodian error fails the start: there is no fallback to the
// derived key alone, which would leave a re-sealed store unopenable without saying why.
func (m *manager) resolveHomeDEK(ctx context.Context, ws store.Workspace) (*store.HomeDEK, string, error) {
	if ws.MembershipID == "" {
		return nil, "", nil
	}
	d, ok, err := m.store.GetHomeDEK(ctx, ws.MembershipID)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		if !m.homeDEKRandom {
			return nil, "", nil
		}
		if d, err = m.mintHomeDEK(ctx, ws); err != nil {
			return nil, "", err
		}
	}
	key, err := m.custodian.Unwrap(ctx, d.KeyRef, d.Ciphertext)
	if err != nil {
		return nil, "", fmt.Errorf("open the home's credential-store key: %w", err)
	}
	defer clear(key)
	if len(key) != 32 {
		return nil, "", errors.New("the home's credential-store key is malformed")
	}
	return &d, hex.EncodeToString(key), nil
}

// mintHomeDEK seals a fresh random key for the home and stores it, or returns the one a
// concurrent start stored first.
func (m *manager) mintHomeDEK(ctx context.Context, ws store.Workspace) (store.HomeDEK, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return store.HomeDEK{}, err
	}
	defer clear(key)
	ct, err := m.custodian.Wrap(ctx, ws.TenantID, key)
	if err != nil {
		return store.HomeDEK{}, fmt.Errorf("seal the home's credential-store key: %w", err)
	}
	return m.store.InsertHomeDEK(ctx, store.HomeDEK{MembershipID: ws.MembershipID, Ciphertext: ct, KeyRef: ws.TenantID})
}

// homeDEKModeRandom parses AF_WORKSPACE_DEK. "random" is refused unless the custodian is kms:
// under the local custodian the random key is wrapped by a master-derived KEK, so it would
// add a migration and buy no crypto-shredding.
func homeDEKModeRandom(mode, custodianKind string) (bool, error) {
	switch strings.TrimSpace(mode) {
	case "", "derived":
		return false, nil
	case "random":
		if custodianKind != "kms" {
			return false, fmt.Errorf("AF_WORKSPACE_DEK=random needs AF_KEY_CUSTODIAN=kms (it is %q)", custodianKind)
		}
		return true, nil
	default:
		return false, fmt.Errorf("AF_WORKSPACE_DEK=%q: want derived or random", mode)
	}
}
