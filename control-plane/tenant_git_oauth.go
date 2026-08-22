// tenant_git_oauth.go — the OAuth app a tenant registers for a git provider
// (docs/71 + ADR0052).
//
// The "connect with OAuth" buttons on the member's git tab used to run on a
// DEPLOYMENT-wide app: GITHUB_OAUTH_CLIENT_ID for GitHub's device flow (read by the
// Agent, from container env the CP injected) and BITBUCKET_OAUTH_KEY/SECRET for
// Bitbucket's code grant (read by the CP). Neither could differ per tenant, and both
// put the operator in the loop for something that belongs to the tenant: the OAuth
// app lives in THEIR GitHub org / Bitbucket workspace.
//
// Since docs/71 the row is the only source. env is not consulted at all — not even as
// a fallback for the default tenant (決定 2). A fallback would mean two places to look
// when a button is missing, and the one that wins would depend on which tenant you
// happen to be in.
//
// ★ Why this file holds no cache, unlike tenant_idp.go: the login registry is read on
// every request through sessionAllowed, so a database round trip there is a hot path.
// These rows are read when somebody presses "connect" and at each poll of a device
// flow — a handful of times per member per year. A cache would only add a window in
// which a just-saved client_id still fails.
package main

import (
	"context"
	"errors"
	"strings"
)

// The provider slugs. They are the same strings the connection routes already use
// (/api/connections/git/{github,bitbucket}/oauth/...), on purpose: a second spelling
// for the same host is how a row gets written that nothing ever reads.
const (
	gitOAuthGitHub    = "github"
	gitOAuthBitbucket = "bitbucket"
)

// gitOAuthProviders is the closed set, in display order.
var gitOAuthProviders = []string{gitOAuthGitHub, gitOAuthBitbucket}

// gitOAuthNeedsSecret reports whether the provider's flow needs a client_secret.
//
// GitHub's is the Device Authorization Grant (RFC 8628), which authenticates with the
// client_id alone — there is no secret to store, and asking for one would make every
// GitHub row look half-finished. Bitbucket's is the authorization code grant, whose
// token exchange is Basic-authenticated with key:secret.
func gitOAuthNeedsSecret(provider string) bool { return provider == gitOAuthBitbucket }

// validGitOAuthProvider keeps the path segment inside the closed set.
func validGitOAuthProvider(p string) bool {
	for _, k := range gitOAuthProviders {
		if k == p {
			return true
		}
	}
	return false
}

// gitOAuthApp resolves a tenant's app for one provider, with the secret unsealed.
//
// ok=false means "this tenant has not registered an app", which is a normal state and
// what the member's UI shows as "OAuth is not configured — ask your tenant admin". An
// ERROR is different and is never flattened into ok=false: a row whose secret cannot
// be unsealed (the master key changed) would otherwise reach Bitbucket as an empty
// client_secret and come back as an opaque invalid_client that nobody can trace to a
// key change — the same reasoning as openTenantSecret's.
func (m *manager) gitOAuthApp(ctx context.Context, tenantID, provider string) (clientID, secret string, ok bool, err error) {
	if tenantID == "" || !validGitOAuthProvider(provider) {
		return "", "", false, nil
	}
	row, found, err := m.store.GetTenantGitOAuth(ctx, tenantID, provider)
	if err != nil {
		return "", "", false, err
	}
	if !found || strings.TrimSpace(row.ClientID) == "" {
		return "", "", false, nil
	}
	secret, err = m.openTenantSecret(ctx, row.SecretEnc, row.KeyRef)
	if err != nil {
		return "", "", false, err
	}
	if gitOAuthNeedsSecret(provider) && secret == "" {
		return "", "", false, errors.New("the stored client secret is empty")
	}
	return row.ClientID, secret, true, nil
}

// gitOAuthConfigured answers only "is there a usable app", for the member-facing
// availability endpoint. A row that cannot be unsealed counts as NOT configured here:
// the question the screen is asking is whether pressing the button would work.
func (m *manager) gitOAuthConfigured(ctx context.Context, tenantID, provider string) bool {
	_, _, ok, err := m.gitOAuthApp(ctx, tenantID, provider)
	return ok && err == nil
}
