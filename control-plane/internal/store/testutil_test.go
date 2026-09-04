package store

// Copies of main-side helpers, used only by tests. They are kept apart from util.go (the
// seam for production code) because there is no reason to add them to store's production
// surface.
//
// The same temporary duplication as util.go: when these are folded into a shared helper
// at the wave boundary, clean this file up with it.

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// gib mirrors main.gib (mem.go). Untyped, so it flexes at each use site.
const gib = 1024 * 1024 * 1024

var userInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUser mirrors main.sanitizeUser (resolver.go): an email (or any id)
// turned into a container-name-safe key.
func sanitizeUser(s string) string {
	s = userInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// Copies of main-side constants. The values themselves are the tests' premise (the
// provider id is the string that goes into the row, trust is a tenant_idp column, lease
// is the lease-expiry formula), so a drift at the definition site ought to turn this red
// — today it drifts silently. Fold into one definition at the wave boundary.
const (
	googleProviderID = "google"        // oauth.go
	trustIssuer      = "issuer"        // oauth_oidc.go
	shareOwnerLease  = 2 * time.Minute // session_share.go
)

// countRows mirrors the helper in main's identity_link_test.go.
func countRows(t *testing.T, st *SQL, table string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// linkOf mirrors the helper in main's identity_link_test.go: one ordinary login, with no
// realm.
func linkOf(provider, subject, email string, emailJoin bool) IdentityLink {
	return IdentityLink{
		Provider: provider, Subject: subject, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: emailJoin,
	}
}
