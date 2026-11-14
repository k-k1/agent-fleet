package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// apiKeySlot names codex's own copy of an OpenAI key in a binding, so it never shares one with
// the shared OPENAI_API_KEY opencode receives although both are openai-api (ADR 0138 D2).
const apiKeySlot = "codex-api-key"

// Login is what codex's auth.json says it runs on: chatgpt-subscription or openai-api, from
// auth_mode, and the binding of that credential (ADR 0138 D2, D5b). The ChatGPT login binds to
// its account id, which a token refresh keeps; the API-key login to the key's keyed digest. A
// missing or unreadable file resolves to nothing. Every caller that runs codex (sessions, the
// chat backend, the image provider) reads it when its call starts.
func Login() routes.Launch { return LoginAt(filepath.Join(paths.HomeDir(), ".codex")) }

// LoginAt is Login for a codex run whose home is home (a $CODEX_HOME), whose auth.json is what
// that run uses.
func LoginAt(home string) routes.Launch {
	b, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return routes.Launch{}
	}
	return loginOf(b)
}

func loginOf(b []byte) routes.Launch {
	var a struct {
		AuthMode string  `json:"auth_mode"`
		APIKey   *string `json:"OPENAI_API_KEY"`
		Tokens   *struct {
			IDToken   string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &a) != nil {
		return routes.Launch{}
	}
	key := ""
	if a.APIKey != nil {
		key = strings.TrimSpace(*a.APIKey)
	}
	mode := strings.ToLower(a.AuthMode)
	if mode == "" {
		// A file written before auth_mode existed: the shape tells the modes apart.
		switch {
		case a.Tokens != nil:
			mode = "chatgpt"
		case key != "":
			mode = "apikey"
		}
	}
	switch mode {
	case "chatgpt":
		id := ""
		if a.Tokens != nil {
			id = a.Tokens.AccountID
			if id == "" {
				id = idTokenAccount(a.Tokens.IDToken)
			}
		}
		// A file whose tokens name no account binds to the login's generation (D5b's fallback).
		return routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: credbind.AccountOrGeneration(routes.RouteChatGPTSubscription, id)}
	case "apikey":
		return routes.Launch{Route: routes.RouteOpenAIAPI, Binding: credbind.Key(apiKeySlot, key)}
	}
	return routes.Launch{}
}

// idTokenAccount is the ChatGPT account id from the id_token's claims, the e-mail when there is
// none. Unverified on purpose: it is codex's own stored token, read for identity only.
func idTokenAccount(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	if c.Auth.AccountID != "" {
		return c.Auth.AccountID
	}
	return c.Email
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
