package muse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// Login is what muse runs on now (ADR 0138 D2, D5b): META_API_KEY in the Agent's environment
// first, because muse says it "always takes priority over the account login" (envKeySet), then
// auth.json told apart by mechanism. The account login binds to the account's e-mail, which a
// token refresh keeps; a stored key binds to its fingerprint. Nothing signed in resolves to
// nothing.
func Login() routes.Launch {
	if v := strings.TrimSpace(os.Getenv("META_API_KEY")); v != "" {
		return routes.Launch{Route: routes.RouteMetaAPI, Binding: routes.KeyBinding("META_API_KEY", v)}
	}
	b, err := os.ReadFile(authPath())
	if err != nil {
		return routes.Launch{}
	}
	return loginOf(b)
}

// loginOf reads auth.json. The key is fingerprinted from its raw JSON bytes, so no code path
// here holds it as a string (the rule readCredential keeps).
func loginOf(b []byte) routes.Launch {
	var f authFile
	if json.Unmarshal(b, &f) != nil {
		return routes.Launch{}
	}
	meta, ok := f.Providers["meta"]
	if !ok {
		return routes.Launch{}
	}
	str := func(k string) string {
		var s string
		if json.Unmarshal(meta[k], &s) != nil {
			return ""
		}
		return s
	}
	switch mech := str("mechanism"); {
	case mech != "" && mech != mechanismAPIKey:
		return routes.Launch{Route: routes.RouteMuseLogin, Binding: routes.AccountBinding(routes.RouteMuseLogin, str("user_email"))}
	case hasValue(meta["api_key"]):
		h := sha256.Sum256([]byte(strings.TrimSpace(string(meta["api_key"]))))
		return routes.Launch{Route: routes.RouteMetaAPI, Binding: "muse-api-key@" + hex.EncodeToString(h[:])[:16]}
	case hasValue(meta["access_token"]):
		// A token with no mechanism and no key: a shape not measured. It is a login, but which
		// account cannot be told.
		return routes.Launch{Route: routes.RouteMuseLogin}
	}
	return routes.Launch{}
}

// ResolveLaunch implements agents.CredentialResolver.
func (agentImpl) ResolveLaunch(session.Meta) routes.Launch { return Login() }

// UsableRoutes implements agents.CredentialResolver: the one route the current login is on.
func (agentImpl) UsableRoutes() []string {
	if l := Login(); l.Route != "" {
		return []string{l.Route}
	}
	return nil
}
