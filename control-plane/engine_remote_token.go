package main

// engine_remote_token.go — the credential a borrowed request presents (ADR 0079 decisions 3, 4
// and 8).
//
// The far deployment's own two-tier model is reused exactly as the Workspace uses it: one
// per-membership ISSUING token buys a per-session, per-engine SESSION token, and only the second
// one ever travels on the data path. What is new here is who does the buying — the borrowing
// Control Plane rather than an Agent — and whose session name goes on it.
//
// 🔴 The name stated is the LOCAL session's. The far side takes it as given and does not verify it
// (engine_gateway.go's issueSessionToken says why: the token is worth exactly what the issuing
// token that bought it is worth, so a wrong name costs a mislabelled usage row and no access). That
// name is the only way an operator over there can tell one borrower's spending from another's —
// 🔥 for the llm role only, because no usage row is written for a non-chat engine at all
// (engine_usage.go).
//
// 🔴 And this is why a remote row's bearer cannot live in engineRuntimeState.apiKey the way the
// other two lifecycles' does: apiKey is one string fixed when the row is built, and this one is per
// session.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// engineRemoteTokenReq is what /internal/engine/token is asked.
type engineRemoteTokenReq struct {
	Session string `json:"session"`
	Key     string `json:"key"`
}

// engineRemoteTokenResp is what it answers. `base_url` is the far side's own path for this engine,
// and reading it is the whole of decision 4's "the far gateway owns that rewrite": composing
// `/engine/<key>/v1` here would be this deployment asserting the other one's route layout.
type engineRemoteTokenResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	BaseURL   string `json:"base_url"`
}

// sessionToken is the far session token for one local session, minted on first use and reused
// until it is close to expiring.
//
// The cache is per (engine key, session) because that is what the far side mints: a token carries
// one engine and one session, and presenting one engine's token on another's route is a 401 the far
// gateway logs as exactly that.
func (e *engineRemote) sessionToken(ctx context.Context, session string) (string, error) {
	if e == nil {
		return "", fmt.Errorf("no remote engine handle")
	}
	e.mu.Lock()
	if t, ok := e.tokens[session]; ok && time.Until(t.expires) > engineRemoteTokenRenewAhead {
		e.mu.Unlock()
		return t.token, nil
	}
	e.mu.Unlock()

	// Minted outside the lock: it is a round trip to another deployment, and holding the mutex
	// across it would queue every other session's request behind this one. Two sessions racing here
	// cost one extra mint and nothing else — the far side's tokens are stateless, so a discarded one
	// is not a leak of anything.
	var resp engineRemoteTokenResp
	err := e.parent.call(ctx, http.MethodPost, "/internal/engine/token",
		engineRemoteTokenReq{Session: session, Key: e.key}, &resp)
	if err != nil {
		return "", fmt.Errorf("buying a %s token from %s: %w", e.key, e.parent.base, err)
	}
	if strings.TrimSpace(resp.Token) == "" {
		return "", fmt.Errorf("%s answered no token for %s", e.parent.base, e.key)
	}
	exp := engineRemoteTokenExpiry(resp.ExpiresAt)

	e.mu.Lock()
	e.tokens[session] = engineRemoteToken{token: resp.Token, expires: exp}
	// The far side's own prefix for this role, kept for dial. It travels on the token answer rather
	// than on the catalogue, so this is the call that learns it.
	if b := strings.TrimSpace(resp.BaseURL); b != "" {
		e.basePath = "/" + strings.Trim(b, "/")
	}
	e.mu.Unlock()
	return resp.Token, nil
}

// engineRemoteTokenExpiry reads the far side's stated expiry.
//
// An unreadable or absent one is treated as SHORT rather than long: the consequence of guessing too
// long is a 401 in the middle of somebody's work that only a restart clears, and the consequence of
// guessing too short is one extra round trip to a Control Plane. The floor is above the renew-ahead
// window so that a token is still usable once before it is replaced.
func engineRemoteTokenExpiry(v string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
		return t
	}
	return time.Now().Add(engineRemoteTokenRenewAhead + time.Hour)
}
