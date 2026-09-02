package main

import (
	"context"
	"errors"
)

// The tenant-defined IdP registry moved to internal/auth; these two stayed
// behind because they are methods on manager, which the whole Control Plane is
// built on and which this transport does not move (ADR 0067 決定 1). The registry
// receives openTenantSecret as a function value at construction (main.go), so
// nothing else had to change.

// --- secret sealing (docs/log/61 §61.11.4 + 決定 33) ------------------------------

// sealTenantSecret seals a client_secret with the tenant key, exactly as
// mcp_server.sealHeaders does for header values: AES-256-GCM through the custodian,
// with the key reference as AAD so the ciphertext is bound to the tenant.
//
// With no master key configured (dev / a single node without one) the value is
// stored as plaintext with an empty key_ref — the same degradation as everywhere
// else in CP, rather than refusing to work.
func (m *manager) sealTenantSecret(ctx context.Context, tenantID, secret string) (enc, keyRef string, err error) {
	if secret == "" {
		return "", "", nil
	}
	if len(m.master32) == 0 || m.custodian == nil {
		return secret, "", nil
	}
	ct, err := m.custodian.Wrap(ctx, tenantID, []byte(secret))
	if err != nil {
		return "", "", err
	}
	return ct, tenantID, nil
}

// openTenantSecret reverses sealTenantSecret. An unreadable value is an ERROR, never
// an empty secret: a token exchange with an empty client_secret fails at the IdP
// with a message nobody can trace back to a key change (docs/log/61 §61.11.4).
func (m *manager) openTenantSecret(ctx context.Context, enc, keyRef string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if keyRef == "" {
		return enc, nil
	}
	if m.custodian == nil {
		return "", errors.New("the client secret is sealed with a tenant key but no key custodian is configured")
	}
	b, err := m.custodian.Unwrap(ctx, keyRef, enc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
