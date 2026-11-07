package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// Package auth holds the CP-native sign-in machinery: the provider abstraction
// every IdP adapter implements, the adapters themselves (generic OIDC and the
// GitHub adapter), the runtime registry of tenant-defined providers, and the
// localized login-page vocabulary.
//
// What is deliberately NOT here is the HTTP layer — the login/callback/link
// handlers, the session and state cookies, the tenant login-rule cache. Those
// hang off the root package's config / manager, which the whole Control Plane is
// built on, so bringing them along would mean moving those two types with them
// (ADR 0067 decision 1: a family that reaches back into the original package is out of
// scope for the transport). They stay in control-plane/oauth*.go and reach in
// here directly (the alias_auth.go layer was reclaimed in RECLAIM-B).

// googleProviderID is also the transitional default for sessions and state
// cookies minted before providers existed (they carry no provider id).
const GoogleProviderID = "google"

// Principal is what a provider proves about the person who just signed in.
// Verified means the provider's declared `trust` rule (§61.4) was satisfied —
// not merely that an email claim was present.
type Principal struct {
	Provider string
	Subject  string // the IdP's stable subject id (unlike email, it never changes)
	// Realm is the authority that verified Subject: the issuer URL for OIDC, the
	// fixed https://github.com for the GitHub adapter. Two providers with one realm
	// are two buttons onto the same IdP, which is what lets the deployment's GitHub
	// and a tenant's own GitHub resolve to one person (docs/log/61 §61.15, rule 1.5).
	// It is filled in by the callback from the provider itself — never from a
	// tenant-supplied field — so a tenant cannot name somebody else's realm.
	Realm string
	// RealmClaim / RealmSubject are the optional SECOND key rule 1.5 may match on:
	// which stable claim the adapter was told to read, and what it carried (docs/log/61
	// §61.15.10). Both are filled by the adapter out of the token it just exchanged,
	// never from configuration — a tenant names the claim, never the value.
	RealmClaim   string
	RealmSubject string
	Email        string
	Verified     bool
}

// LoginProvider is one sign-in button: an IdP the deployment enabled. Every
// provider shares the single redirect_uri (/oauth2/callback) — which provider a
// callback belongs to is carried in the signed state cookie, so the operator
// registers exactly one URI per IdP no matter how many are configured (decision 8).
type LoginProvider interface {
	ID() string
	Label(lang string) string // login page button text
	// AuthorizeURL may hit the network (OIDC discovery is lazy), hence ctx+error.
	AuthorizeURL(ctx context.Context, state, redirectURI string) (string, error)
	Exchange(ctx context.Context, code, redirectURI string) (Principal, error)
	// Allowed re-checks authorization. It is called at login AND on every
	// request (authGate) — removing someone from the allowlist is the
	// offboarding path and must not wait for the session cookie to expire.
	Allowed(ctx context.Context, p Principal) (bool, error)
}

// FlowParams is the per-sign-in material that binds an authorization code to the browser
// that asked for it: a PKCE verifier (RFC 7636, S256) and an OIDC nonce. The Control Plane
// keeps both in the signed state cookie, so a code that leaks to another browser cannot be
// redeemed there. Zero values mean "not used".
type FlowParams struct {
	CodeVerifier string
	Nonce        string
}

// FlowProvider is implemented by the built-in providers that take part in PKCE / nonce.
// It is an optional interface, like EntryVerdictor, so a test fake or a provider that
// predates it keeps the plain AuthorizeURL / Exchange pair.
type FlowProvider interface {
	// NewFlow returns fresh FlowParams for one sign-in; its fields are empty for what the
	// provider has opted out of (see OIDCProvider.NoPKCE).
	NewFlow() FlowParams
	AuthorizeURLFlow(ctx context.Context, state, redirectURI string, f FlowParams) (string, error)
	ExchangeFlow(ctx context.Context, code, redirectURI string, f FlowParams) (Principal, error)
}

// ErrNonceMismatch is an ID token whose nonce is not the one this sign-in sent.
var ErrNonceMismatch = errors.New("id_token nonce does not match this sign-in")

// newVerifier is a 256-bit random PKCE code verifier (43 characters of base64url).
func newVerifier() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newNonce is a random OIDC nonce.
func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// PKCEChallenge is the S256 code_challenge for a verifier.
func PKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// EntryVerdict is a judgment about whether somebody may still enter, with "could not tell" kept
// apart from "no". Allowed's bool folds the two together (a GitHub outage past its grace window
// answers false), which is fine for turning one request away and wrong for revoking credentials.
type EntryVerdict int

const (
	EntryUnknown EntryVerdict = iota // could not be checked: judge nothing
	EntryAllowed
	EntryDenied // a definitive refusal
)

// EntryVerdictor is implemented by the built-in providers. Callers must have established that
// the inputs behind the email axis (allowlist file, roster lookup) are readable; the verdict only
// speaks for the provider's own checks.
type EntryVerdictor interface {
	EntryVerdict(ctx context.Context, p Principal) EntryVerdict
}

// ProviderIssuer is implemented by the built-in provider types so the admin list
// can name the identity source without widening the LoginProvider interface —
// which every test fake would then have to grow a method for.
//
// The method is EXPORTED because the adapters live here while the admin list
// that asserts against it lives in the root package: an interface whose method
// name is unexported can only ever be satisfied inside the package that declares
// it, and the assertion would compile and silently evaluate to false (measured).
// A disappearing issuer column is not the kind of thing a build catches.
type ProviderIssuer interface{ IssuerURL() string }

// ProviderRealm answers "where would this provider prove someone", reusing the
// optional interface the admin provider list already relies on (login_provider_api.go).
// A provider that does not implement it simply takes no part in rule 1.5.
func ProviderRealm(p LoginProvider) string {
	if pi, ok := p.(ProviderIssuer); ok {
		return pi.IssuerURL()
	}
	return ""
}

// ProviderAllowlister is implemented by providers that can carry an allowlist of
// their own (an email list for OIDC, the org list for GitHub).
type ProviderAllowlister interface{ HasOwnAllowlist() bool }

// AnyProviderAllowlist reports whether at least one provider carries its own
// allowlist — with none, and no deployment-wide list, every login is denied.
func AnyProviderAllowlist(ps []LoginProvider) bool {
	for _, p := range ps {
		if a, ok := p.(ProviderAllowlister); ok && a.HasOwnAllowlist() {
			return true
		}
	}
	return false
}
