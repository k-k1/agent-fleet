package main

import (
	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
)

// The sign-in machinery moved to internal/auth (ADR 0067 / ウェーブ B track=CP-AUTH):
// the provider abstraction, the OIDC and GitHub adapters, the runtime registry of
// tenant-defined providers, and the login page vocabulary. What stayed here is the
// HTTP layer — the login / callback / link handlers, the session and state cookies,
// the tenant login-rule cache — because those are methods on config and manager,
// which the whole Control Plane is built on and which 決定 1 keeps out of this wave.
//
// This file re-binds the moved names so that not one caller had to change. The
// aliases are the transport's debt, and the wave-boundary reclaim session pays it
// off; until then, `grep -rn '= auth\.[A-Z]' control-plane` finds every one of
// them — they are all in this file.
//
// ★ Almost every entry below names a type, a func or a const on the far side, so
// the alias cannot become a stale COPY of somebody's variable — the trap that bit
// #295 F-2 and #297 F-1, where a test swapped the original and the code under test
// went on reading the untouched copy. The `var` aliases here take function values,
// and a func declaration is not reassignable, so there is nothing to swap.
//
// Two far-side VARIABLES are aliased and both are deliberate:
//   - errNotAllowed / errNeedsReauth are sentinel errors. The copy holds the same
//     pointer, so errors.Is keeps matching, and nothing anywhere assigns to them.
//   - LoginText is a map and is NOT aliased — see loginTextFor below.

// --- the provider abstraction ------------------------------------------------

type (
	principal     = auth.Principal
	loginProvider = auth.LoginProvider
	loginStrings  = auth.LoginStrings
	oidcProvider  = auth.OIDCProvider
	// githubProvider is referenced by the tests that build a provider list for the
	// admin API; the login flow itself only ever sees it as a loginProvider.
	githubProvider = auth.GitHubProvider
)

const googleProviderID = auth.GoogleProviderID

var (
	errNotAllowed  = auth.ErrNotAllowed
	errNeedsReauth = auth.ErrNeedsReauth
)

var (
	providerRealm        = auth.ProviderRealm
	anyProviderAllowlist = auth.AnyProviderAllowlist
	validIssuerURL       = auth.ValidIssuerURL
	validProviderID      = auth.ValidProviderID
	multiTenantIssuer    = auth.MultiTenantIssuer
)

const (
	trustEmailVerified = auth.TrustEmailVerified
	trustIssuer        = auth.TrustIssuer
	trustAPI           = auth.TrustAPI
	githubWebBase      = auth.GithubWebBase
	githubProviderID   = auth.GithubProviderID
)

// --- the login page vocabulary -----------------------------------------------

// loginText is NOT aliased. Go has no const map, so `var loginText = auth.LoginText`
// would be a second variable pointing at the same table — harmless while nothing
// reassigns it, but exactly the shape the reclaim notes warn about. Callers read it
// through this function instead, which always goes to the far side.
func loginTextFor(lang string) loginStrings { return auth.LoginText[lang] }

const loginPageHTML = auth.LoginPageHTML

var (
	preferredUILang      = auth.PreferredUILang
	loginErrorBlock      = auth.LoginErrorBlock
	providerIcon         = auth.ProviderIcon
	providerInList       = auth.ProviderInList
	defaultProviderLabel = auth.DefaultProviderLabel
)

// --- tenant-defined providers ------------------------------------------------

type (
	tenantIdPRegistry  = auth.TenantIdPRegistry
	tenantIdPStoreView = auth.TenantIdPStoreView
)

const (
	tenantProviderPrefix = auth.TenantProviderPrefix
	defaultTenantSlug    = auth.DefaultTenantSlug
	tenantIdPKindOIDC    = auth.TenantIdPKindOIDC
	tenantIdPKindGitHub  = auth.TenantIdPKindGitHub
	// tenantRuleTTL is shared with the registry, which now owns the value.
	tenantRuleTTL = auth.TenantRuleTTL
)

var (
	newTenantIdPRegistry  = auth.NewTenantIdPRegistry
	tenantProviderID      = auth.TenantProviderID
	parseTenantProviderID = auth.ParseTenantProviderID
	isTenantProviderID    = auth.IsTenantProviderID
	validTenantIdPName    = auth.ValidTenantIdPName
	buildTenantProvider   = auth.BuildTenantProvider
	tenantLinkClaimList   = auth.TenantLinkClaimList
	tenantLabelSuffix     = auth.TenantLabelSuffix
)

// tenantLinkClaims is a map (hence a variable), so it is reached through a
// predicate rather than copied over — see the note at the top of this file.
var tenantLinkClaimAllowed = auth.TenantLinkClaimAllowed

const (
	githubDefaultTTL   = auth.GithubDefaultTTL
	githubDefaultGrace = auth.GithubDefaultGrace
	githubAPIBase      = auth.GithubAPIBase
)

// The remaining names below are reached only by the adapter tests, which still
// live in package main: an internal step of the flow (ExchangeCode), the GitHub
// endpoints and its membership cache, and the two Google URLs the Google path is
// asserted against. They are exported because internal/auth is not an API — the
// reclaim session should pull those tests in beside the adapters and hide these
// again.
const (
	githubScopes       = auth.GithubScopes
	googleAuthorizeURL = auth.GoogleAuthorizeURL
	googleTokenURL     = auth.GoogleTokenURL
)

var newGitHubProvider = auth.NewGitHubProvider
