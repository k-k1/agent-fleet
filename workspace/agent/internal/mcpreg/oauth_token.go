package mcpreg

// What happens to a grant after sign-in (ADR 0119 decision 5): the access token a caller may
// use, refresh, disconnect, and the status the Console reads. The relay (a later phase) calls
// AccessToken / RefreshAccessToken; nothing else hands a token out.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var (
	// ErrNotSignedIn means the server has no grant.
	ErrNotSignedIn = errors.New("mcp oauth: not signed in")
	// ErrSignInAgain means the grant cannot be renewed (the refresh token was refused, or the
	// access token expired and there is none). Only the member can fix it, in the Console.
	ErrSignInAgain = errors.New("mcp oauth: sign in again")
)

// refreshSkew renews an access token this long before it expires, so a request that starts
// with a valid token does not reach the server with a dead one.
const refreshSkew = 60 * time.Second

// Grant states as the Console shows them.
const (
	OAuthStateNotSignedIn  = "not_signed_in"
	OAuthStateConnected    = "connected"
	OAuthStateSignInAgain  = "sign_in_again"
	OAuthStateNeedsMoreOAu = "needs_more_permission"
)

func currentGrant(serverID string) *secrets.MCPOAuthGrant {
	data, err := secrets.Load()
	if err != nil {
		return nil
	}
	return findGrant(data, serverID)
}

// findGrant returns a copy of the server's grant. Completion keeps one grant per server; the
// newest wins should an older store hold more.
func findGrant(data *secrets.Data, serverID string) *secrets.MCPOAuthGrant {
	var best *secrets.MCPOAuthGrant
	for i := range data.MCPOAuth {
		g := data.MCPOAuth[i]
		if g.ServerID == serverID && (best == nil || g.ConnectedAt > best.ConnectedAt) {
			best = &g
		}
	}
	return best
}

var (
	grantLocksMu sync.Mutex
	grantLocks   = map[string]*sync.Mutex{}
	// rotated holds a refresh response whose write to the store failed, so the rotated refresh
	// token is not lost while the old one is already spent at the AS. The next call writes it
	// before doing anything else.
	rotatedMu sync.Mutex
	rotated   = map[string]rotation{}
)

type rotation struct {
	oldRefresh string
	tok        tokenResponse
	at         int64
}

func lockFor(key string) *sync.Mutex {
	grantLocksMu.Lock()
	defer grantLocksMu.Unlock()
	m, ok := grantLocks[key]
	if !ok {
		m = &sync.Mutex{}
		grantLocks[key] = m
	}
	return m
}

func applyRotation(g *secrets.MCPOAuthGrant, r rotation) {
	g.AccessToken = r.tok.AccessToken
	if r.tok.RefreshToken != "" {
		g.RefreshToken = r.tok.RefreshToken
	}
	g.ExpiresAt = 0
	if r.tok.ExpiresIn > 0 {
		g.ExpiresAt = r.at + int64(r.tok.ExpiresIn)
	}
	if r.tok.Scope != "" {
		g.Scope = r.tok.Scope
	}
	g.RefreshedAt = r.at
	g.SignInAgain = false
}

// persistRotation writes a refresh result into the grant it came from. It only applies when
// the stored refresh token is still the one the request spent: a grant replaced by a new
// sign-in meanwhile is left alone.
func persistRotation(serverID, issuer string, r rotation) error {
	return updateSecrets(func(s *secrets.Data) error {
		for i := range s.MCPOAuth {
			g := &s.MCPOAuth[i]
			if g.ServerID == serverID && g.Issuer == issuer && g.RefreshToken == r.oldRefresh {
				applyRotation(g, r)
			}
		}
		return nil
	})
}

func fresh(g *secrets.MCPOAuthGrant, now time.Time) bool {
	return g.AccessToken != "" && (g.ExpiresAt == 0 || now.Add(refreshSkew).Unix() < g.ExpiresAt)
}

// AccessToken returns an access token for serverID that is valid now, renewing it first when
// it is about to expire.
func AccessToken(ctx context.Context, serverID string) (string, error) {
	return refreshGrant(ctx, serverID, "")
}

// RefreshAccessToken is for a caller whose request was refused 401 with the token rejected: it
// renews unless another caller already has, in which case it returns that newer token.
func RefreshAccessToken(ctx context.Context, serverID, rejected string) (string, error) {
	if rejected == "" {
		return "", errors.New("mcp oauth: no rejected token named")
	}
	return refreshGrant(ctx, serverID, rejected)
}

// refreshGrant is the single-flight renewal: one request per grant at a time, and a caller that
// waited behind another re-reads the grant and finds the work done instead of sending a second
// request with a refresh token the first already spent.
func refreshGrant(ctx context.Context, serverID, rejected string) (string, error) {
	g0 := currentGrant(serverID)
	if g0 == nil {
		return "", ErrNotSignedIn
	}
	key := serverID + "\n" + g0.Issuer
	mu := lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	g := currentGrant(serverID)
	if g == nil {
		return "", ErrNotSignedIn
	}
	if g.SignInAgain {
		return "", ErrSignInAgain
	}
	rotatedMu.Lock()
	r, have := rotated[key]
	rotatedMu.Unlock()
	if have {
		if g.RefreshToken == r.oldRefresh {
			if err := persistRotation(g.ServerID, g.Issuer, r); err != nil {
				return "", errors.New("mcp oauth: the renewed grant could not be saved yet")
			}
			applyRotation(g, r)
		}
		rotatedMu.Lock()
		delete(rotated, key)
		rotatedMu.Unlock()
	}
	if fresh(g, oauthNow()) && (rejected == "" || g.AccessToken != rejected) {
		return g.AccessToken, nil
	}
	if g.RefreshToken == "" {
		markSignInAgain(g.ServerID, g.Issuer, "")
		return "", ErrSignInAgain
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"resource":      {g.Resource},
	}
	tok, terr := postToken(ctx, g.TokenEndpoint, form, g.ClientID, g.ClientSecret, g.TokenAuth)
	if terr != nil {
		if terr.code == "invalid_grant" {
			markSignInAgain(g.ServerID, g.Issuer, g.RefreshToken)
			return "", ErrSignInAgain
		}
		return "", errors.New("mcp oauth: the authorization server did not renew the grant (" + terr.code + ")")
	}
	rot := rotation{oldRefresh: g.RefreshToken, tok: tok, at: oauthNow().Unix()}
	// The rotated refresh token is written before the new access token is used: the old one
	// may already be spent at the AS, so a crash after the response but before this write is
	// the only window in which it can be lost.
	if err := persistRotation(g.ServerID, g.Issuer, rot); err != nil {
		rotatedMu.Lock()
		rotated[key] = rot
		rotatedMu.Unlock()
		return "", errors.New("mcp oauth: the renewed grant could not be saved yet")
	}
	return tok.AccessToken, nil
}

// markSignInAgain drops the tokens of a grant that cannot be renewed and flags it. spent, when
// set, guards against a newer sign-in having replaced the grant while the request ran.
func markSignInAgain(serverID, issuer, spent string) {
	_ = updateSecrets(func(s *secrets.Data) error {
		for i := range s.MCPOAuth {
			g := &s.MCPOAuth[i]
			if g.ServerID == serverID && g.Issuer == issuer && (spent == "" || g.RefreshToken == spent) {
				g.AccessToken, g.RefreshToken, g.ExpiresAt = "", "", 0
				g.SignInAgain = true
			}
		}
		return nil
	})
}

// NoteInsufficientScope records that the server refused the grant's token for lack of scope
// (403 insufficient_scope), so the Console can offer a sign-in with the scope asked for.
func NoteInsufficientScope(serverID, scope string) error {
	if !validScope(scope) {
		return errors.New("invalid scope")
	}
	return updateSecrets(func(s *secrets.Data) error {
		for i := range s.MCPOAuth {
			if s.MCPOAuth[i].ServerID == serverID {
				s.MCPOAuth[i].StepUpScope = scope
			}
		}
		return nil
	})
}

// OAuthStatus is the Console's read of one server's grant. No token appears in it.
type OAuthStatus struct {
	ServerID     string   `json:"serverId"`
	State        string   `json:"state"`
	Issuer       string   `json:"issuer,omitempty"`
	Registration string   `json:"registration,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	// ExpiresAt is the access token's expiry; a connected grant renews itself, so it is shown
	// for information. 0 when the AS did not say.
	ExpiresAt   int64  `json:"expiresAt,omitempty"`
	ConnectedAt int64  `json:"connectedAt,omitempty"`
	StepUpScope string `json:"stepUpScope,omitempty"`
	// PendingRegistration is the registration path of a sign-in started and not yet finished
	// (within its 10 minutes). The Console offers a DCR retry when it is "cimd" and the member
	// is not connected: an AS that rejects a CIMD client usually never redirects back.
	PendingRegistration string `json:"pendingRegistration,omitempty"`
	// PreregisteredIssuers lists the authorization servers a client id was entered for (never
	// the id or secret themselves).
	PreregisteredIssuers []string `json:"preregisteredIssuers,omitempty"`
}

// OAuthStatusOf reads the status of serverID.
func OAuthStatusOf(serverID string) (OAuthStatus, error) {
	data, err := secrets.Load()
	if err != nil {
		return OAuthStatus{}, err
	}
	st := OAuthStatus{ServerID: serverID, State: OAuthStateNotSignedIn}
	if g := findGrant(data, serverID); g != nil {
		st.Issuer, st.Registration = g.Issuer, g.Registration
		st.Scopes = strings.Fields(g.Scope)
		st.ExpiresAt, st.ConnectedAt, st.StepUpScope = g.ExpiresAt, g.ConnectedAt, g.StepUpScope
		switch {
		case g.SignInAgain:
			st.State = OAuthStateSignInAgain
		case g.StepUpScope != "":
			st.State = OAuthStateNeedsMoreOAu
		default:
			st.State = OAuthStateConnected
		}
	}
	for _, c := range data.MCPOAuthClients {
		if c.Kind == OAuthRegPreregistered && c.ServerID == serverID {
			st.PreregisteredIssuers = append(st.PreregisteredIssuers, c.Issuer)
		}
	}
	sort.Strings(st.PreregisteredIssuers)
	pendingMu.Lock()
	for _, p := range pendingAuths {
		if p.serverID == serverID && oauthNow().Before(p.expires) {
			st.PendingRegistration = p.registration
		}
	}
	pendingMu.Unlock()
	return st, nil
}

// SetPreregisteredClient stores (or, with an empty clientID, removes) the client id and secret
// the member entered for serverID at one authorization server.
func SetPreregisteredClient(serverID, issuer, clientID, clientSecret string) error {
	if _, ok := issuerUsable(issuer); !ok {
		return errors.New("invalid issuer")
	}
	if _, err := Get(serverID); err != nil {
		return err
	}
	return updateSecrets(func(s *secrets.Data) error {
		kept := s.MCPOAuthClients[:0]
		for _, c := range s.MCPOAuthClients {
			if !(c.Kind == OAuthRegPreregistered && c.ServerID == serverID && c.Issuer == issuer) {
				kept = append(kept, c)
			}
		}
		s.MCPOAuthClients = kept
		if clientID != "" {
			s.MCPOAuthClients = append(s.MCPOAuthClients, secrets.MCPOAuthClient{
				Issuer: issuer, ServerID: serverID, Kind: OAuthRegPreregistered,
				ClientID: clientID, ClientSecret: clientSecret, CreatedAt: oauthNow().Unix(),
			})
		}
		return nil
	})
}

// DisconnectResult says what Disconnect did.
type DisconnectResult struct {
	ServerID string `json:"serverId"`
	Issuer   string `json:"issuer,omitempty"`
	// HadGrant is false when there was nothing to remove.
	HadGrant bool `json:"hadGrant"`
	// Revoked is true when the authorization server confirmed the RFC 7009 revocation. The
	// local grant is deleted either way.
	Revoked bool `json:"revoked"`
}

// DisconnectOAuth revokes the grant at the authorization server when it offers revocation,
// then deletes it locally whatever the answer.
func DisconnectOAuth(ctx context.Context, serverID string) (DisconnectResult, error) {
	res := DisconnectResult{ServerID: serverID}
	dropPendingFor(serverID)
	g := currentGrant(serverID)
	if g == nil {
		return res, nil
	}
	res.HadGrant, res.Issuer = true, g.Issuer
	key := serverID + "\n" + g.Issuer
	mu := lockFor(key) // not while a refresh of this grant is in flight
	mu.Lock()
	defer mu.Unlock()
	if g = currentGrant(serverID); g == nil {
		return res, nil
	}
	if g.RevocationEndpoint != "" {
		token, hint := g.RefreshToken, "refresh_token"
		if token == "" {
			token, hint = g.AccessToken, "access_token"
		}
		if token != "" {
			rctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
			res.Revoked = revokeToken(rctx, *g, token, hint)
			cancel()
		}
	}
	rotatedMu.Lock()
	delete(rotated, key)
	rotatedMu.Unlock()
	// A sign-in still open in a browser tab must not bring the grant back after this.
	dropPendingFor(serverID)
	err := updateSecrets(func(s *secrets.Data) error {
		kept := s.MCPOAuth[:0]
		for _, o := range s.MCPOAuth {
			if o.ServerID != serverID {
				kept = append(kept, o)
			}
		}
		s.MCPOAuth = kept
		return nil
	})
	return res, err
}

func dropPendingFor(serverID string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for k, p := range pendingAuths {
		if p.serverID == serverID {
			delete(pendingAuths, k)
		}
	}
}

func revokeToken(ctx context.Context, g secrets.MCPOAuthGrant, token, hint string) bool {
	if !endpointUsable(g.RevocationEndpoint) {
		return false
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}}
	switch g.TokenAuth {
	case "post":
		form.Set("client_id", g.ClientID)
		form.Set("client_secret", g.ClientSecret)
	case "basic":
	default:
		form.Set("client_id", g.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if g.TokenAuth == "basic" {
		req.SetBasicAuth(url.QueryEscape(g.ClientID), url.QueryEscape(g.ClientSecret)) // form-encoded, RFC 6749 §2.3.1
	}
	resp, err := oauthHTTP().Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// purgeOAuthLocked deletes every grant and pre-registered client of a server that is being deleted,
// locally only. The caller revokes first when it wants to.
func purgeOAuthLocked(s *secrets.Data, serverID string) {
	kept := s.MCPOAuth[:0]
	for _, g := range s.MCPOAuth {
		if g.ServerID != serverID {
			kept = append(kept, g)
		}
	}
	s.MCPOAuth = kept
	keptC := s.MCPOAuthClients[:0]
	for _, c := range s.MCPOAuthClients {
		if !(c.Kind == OAuthRegPreregistered && c.ServerID == serverID) {
			keptC = append(keptC, c)
		}
	}
	s.MCPOAuthClients = keptC
}
