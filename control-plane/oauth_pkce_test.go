package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
)

func stateOf(t *testing.T, cfg config, ck *http.Cookie) oauthState {
	t.Helper()
	payload, ok := cfg.verifyCookie(cookiePurposeState, ck.Value)
	if !ok {
		t.Fatal("state cookie does not verify")
	}
	var st oauthState
	if err := json.Unmarshal(payload, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func okClaims() map[string]any {
	return map[string]any{"sub": "g1", "email": "yamada@acme.co.jp", "email_verified": true}
}

// The authorize request carries the S256 challenge of the verifier kept in the signed state
// cookie, a nonce, and the token request carries the verifier back.
func TestLoginUsesPKCEAndNonceForOIDC(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{idTokenClaims: okClaims(), userinfoClaims: okClaims()})
	cfg := oauthTestConfig(t, stubProvider("corp", idp, auth.TrustEmailVerified))

	ck, au := startLogin(t, cfg, "?provider=corp")
	st := stateOf(t, cfg, ck)
	if st.Ver == "" || st.ONonce == "" {
		t.Fatalf("state carries no verifier / nonce: %+v", st)
	}
	sum := sha256.Sum256([]byte(st.Ver))
	if got, want := au.Query().Get("code_challenge"), base64.RawURLEncoding.EncodeToString(sum[:]); got != want {
		t.Fatalf("code_challenge = %q, want %q", got, want)
	}
	if au.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("method = %q", au.Query().Get("code_challenge_method"))
	}
	if au.Query().Get("nonce") != st.ONonce {
		t.Fatalf("nonce = %q, state has %q", au.Query().Get("nonce"), st.ONonce)
	}
	idp.idTokenClaims["nonce"] = au.Query().Get("nonce")
	w := callback(t, cfg, ck, "code", au.Query().Get("state"))
	if sessionCookieOf(t, w) == nil {
		t.Fatalf("no session (error=%q)", loginErrorOf(t, w))
	}
	if idp.tokenForm.Get("code_verifier") != st.Ver {
		t.Fatalf("token request code_verifier = %q, want the state's", idp.tokenForm.Get("code_verifier"))
	}
}

// An ID token that carries a different nonce is refused; one that carries none is accepted.
func TestCallbackNonceIsComparedWhenTheTokenCarriesOne(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{idTokenClaims: okClaims(), userinfoClaims: okClaims()})
	cfg := oauthTestConfig(t, stubProvider("corp", idp, auth.TrustEmailVerified))

	ck, au := startLogin(t, cfg, "?provider=corp")
	idp.idTokenClaims["nonce"] = "somebody-elses-nonce"
	w := callback(t, cfg, ck, "code", au.Query().Get("state"))
	if sessionCookieOf(t, w) != nil || loginErrorOf(t, w) != "exchange" {
		t.Fatalf("a foreign nonce must end in the exchange error, got session=%v", sessionCookieOf(t, w))
	}

	idp.idTokenClaims = okClaims() // no nonce claim at all
	ck, au = startLogin(t, cfg, "?provider=corp")
	if w := callback(t, cfg, ck, "code", au.Query().Get("state")); sessionCookieOf(t, w) == nil {
		t.Fatalf("a token without a nonce must still sign in (error=%q)", loginErrorOf(t, w))
	}
}

// The per-provider opt-out sends no challenge and expects no verifier.
func TestLoginPKCEOptOut(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{idTokenClaims: okClaims(), userinfoClaims: okClaims()})
	p := stubProvider("legacy", idp, auth.TrustEmailVerified)
	p.NoPKCE = true
	cfg := oauthTestConfig(t, p)

	ck, au := startLogin(t, cfg, "?provider=legacy")
	if au.Query().Has("code_challenge") || stateOf(t, cfg, ck).Ver != "" {
		t.Fatal("opted-out provider must not use PKCE")
	}
	if w := callback(t, cfg, ck, "code", au.Query().Get("state")); sessionCookieOf(t, w) == nil {
		t.Fatalf("opt-out sign-in failed (error=%q)", loginErrorOf(t, w))
	}
	if idp.tokenForm.Has("code_verifier") {
		t.Fatal("opted-out provider must not send a code_verifier")
	}
}

// A sign-in started before PKCE existed has a state without a verifier. It fails with the
// login page's session error, the same as an expired state, and never reaches the IdP.
func TestCallbackWithoutVerifierFailsCleanly(t *testing.T) {
	idp := newStubIdP(t, &stubIdP{idTokenClaims: okClaims(), userinfoClaims: okClaims()})
	cfg := oauthTestConfig(t, stubProvider("corp", idp, auth.TrustEmailVerified))
	b, _ := json.Marshal(oauthState{Nonce: "n1", Next: "/", Exp: time.Now().Add(time.Minute).Unix(), Prov: "corp"})
	w := callback(t, cfg, &http.Cookie{Name: stateCookie, Value: cfg.signCookie(cookiePurposeState, b)}, "code", "n1")
	if sessionCookieOf(t, w) != nil || loginErrorOf(t, w) != "session" {
		t.Fatalf("want the session error without a session, got error=%q", loginErrorOf(t, w))
	}
	if idp.tokenHits != 0 {
		t.Fatalf("the token endpoint was called %d time(s)", idp.tokenHits)
	}
}

// The GitHub adapter: same mechanism, no nonce.
func TestGitHubLoginUsesPKCE(t *testing.T) {
	gh := &auth.GitHubProvider{ProviderID: auth.GithubProviderID, ClientID: "gh", ClientSecret: "s", AllowedOrgs: []string{"acme"}}
	cfg := oauthTestConfig(t, gh)
	ck, au := startLogin(t, cfg, "?provider="+auth.GithubProviderID)
	st := stateOf(t, cfg, ck)
	if st.Ver == "" || st.ONonce != "" {
		t.Fatalf("github state: %+v", st)
	}
	if au.Query().Get("code_challenge") != auth.PKCEChallenge(st.Ver) || au.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize query = %v", au.Query())
	}
	gh.NoPKCE = true
	ck, au = startLogin(t, cfg, "?provider="+auth.GithubProviderID)
	if au.Query().Has("code_challenge") || stateOf(t, cfg, ck).Ver != "" {
		t.Fatal("github opt-out must not use PKCE")
	}
}

// AF_OIDC_<ID>_PKCE=off and AF_GITHUB_PKCE=off are the env spelling of the opt-out; unset or
// any other value keeps PKCE on.
func TestPKCEOptOutFromEnv(t *testing.T) {
	t.Setenv("AF_OIDC_PROVIDERS", "old,new")
	t.Setenv("AF_OIDC_OLD_ISSUER", "https://idp-old.example.org")
	t.Setenv("AF_OIDC_OLD_CLIENT_ID", "c")
	t.Setenv("AF_OIDC_OLD_CLIENT_SECRET", "s")
	t.Setenv("AF_OIDC_OLD_TRUST", "email_verified")
	t.Setenv("AF_OIDC_OLD_PKCE", "off")
	t.Setenv("AF_OIDC_NEW_ISSUER", "https://idp-new.example.org")
	t.Setenv("AF_OIDC_NEW_CLIENT_ID", "c")
	t.Setenv("AF_OIDC_NEW_CLIENT_SECRET", "s")
	t.Setenv("AF_OIDC_NEW_TRUST", "email_verified")
	t.Setenv("AF_OIDC_NEW_PKCE", "on")
	t.Setenv("AF_GITHUB_ALLOWED_ORGS", "acme")
	t.Setenv("AF_GITHUB_LOGIN_CLIENT_ID", "gh")
	t.Setenv("AF_GITHUB_LOGIN_CLIENT_SECRET", "s")
	t.Setenv("AF_GITHUB_PKCE", "false")
	ps, err := auth.BuildLoginProviders(auth.Deployment{})
	if err != nil {
		t.Fatal(err)
	}
	flow := map[string]bool{}
	for _, p := range ps {
		flow[p.ID()] = p.(auth.FlowProvider).NewFlow().CodeVerifier != ""
	}
	if flow["old"] || !flow["new"] || flow[auth.GithubProviderID] || len(flow) != 3 {
		t.Fatalf("PKCE per provider = %v", flow)
	}
}
