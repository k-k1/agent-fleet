package mcpreg

// What happens to a grant after sign-in (ADR 0119 decision 5): the access token a caller may
// use, refresh, disconnect, and the status the Console reads. The relay (relay.go) calls
// AccessToken / RefreshAccessToken; nothing else hands a token out.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	// ErrMorePermission means the server has a grant that was not made for it as it is now: a
	// shared grant the server joined after the sign-in, or one made for another URL.
	ErrMorePermission = errors.New("mcp oauth: more permission needed")
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

// currentGrant returns a copy of the grant serverID uses (findGrantFor), nil when it has none.
func currentGrant(serverID string) *secrets.MCPOAuthGrant {
	data, _, d, err := grantLoad(serverID)
	if err != nil {
		return nil
	}
	return findGrantFor(data, d)
}

var (
	grantLocksMu sync.Mutex
	grantLocks   = map[string]*sync.Mutex{}
	// rotated holds a refresh response whose write to the store failed, so the rotated refresh
	// token is not lost while the old one is already spent at the AS. The next call writes it
	// before doing anything else. Keyed by the sign-in (grantKey), whose lock its writer held.
	rotatedMu sync.Mutex
	rotated   = map[string]rotation{}
)

type rotation struct {
	oldRefresh string
	tok        tokenResponse
	at         int64
	// resources are what the renewal asked for, which the new access token is for.
	resources []string
}

func rotationKey(g *secrets.MCPOAuthGrant) string { return grantKey(g) }

var (
	spentMu sync.Mutex
	// spent remembers recent renewals by the refresh token they spent (credKey), so a sign-in
	// whose exchange the AS answered with that same refresh token, stored after the renewal,
	// takes the newest one instead of bringing the spent token back.
	spent = map[string]spentRotation{}
)

type spentRotation struct {
	refresh string
	at      time.Time
}

const spentTTL = 10 * time.Minute

func rememberSpent(oldKey, next string) {
	if next == "" {
		return
	}
	spentMu.Lock()
	defer spentMu.Unlock()
	now := time.Now()
	for k, v := range spent {
		if now.Sub(v.at) > spentTTL {
			delete(spent, k)
		}
	}
	spent[oldKey] = spentRotation{next, now}
}

// successorOf is the newest refresh token renewals rotated g's refresh token into, following the
// chain, "" when none did.
func successorOf(g *secrets.MCPOAuthGrant) string {
	spentMu.Lock()
	defer spentMu.Unlock()
	out, cur := "", g.RefreshToken
	for i := 0; i < 16; i++ {
		v, ok := spent[credKey(g, cur)]
		if !ok || time.Since(v.at) > spentTTL || v.refresh == cur {
			break
		}
		out, cur = v.refresh, v.refresh
	}
	return out
}

// serverLockKey is the lock that orders a sign-in's completion against a disconnect of the same
// server (signInLockKey; a shared grant's group has its own). It is separate from the grant's key
// (grantKey), which refreshes hold.
func serverLockKey(serverID string) string { return "server\n" + serverID }

type disconnectOpt struct {
	holdGroup    string
	requireMatch func() (bool, error)
	also         func(*secrets.Data)
}

// lockAll takes the locks of keys in sorted order, each once, and returns their release.
func lockAll(keys []string) func() {
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	var held []*sync.Mutex
	for i, k := range keys {
		if i > 0 && keys[i-1] == k {
			continue
		}
		m := lockFor(k)
		m.Lock()
		held = append(held, m)
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}
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
	if len(r.resources) > 0 {
		g.TokenResources = append([]string(nil), r.resources...)
	}
}

// persistRotation writes a refresh result into the grant it came from, while its refresh token is
// still the one the request spent: a grant replaced by a new sign-in meanwhile is left alone.
// Another grant that holds the same refresh token (an AS that returned it again on another
// sign-in) takes only the rotated refresh token, since the old one is spent; its access token and
// scope are its own sign-in's and stay.
func persistRotation(ref *secrets.MCPOAuthGrant, r rotation) error {
	return updateGrantSecrets(func(s *secrets.Data) error {
		for i := range s.MCPOAuth {
			g := &s.MCPOAuth[i]
			switch {
			case !sameCredential(g, ref) || g.RefreshToken != r.oldRefresh:
			case sameSignIn(g, ref):
				applyRotation(g, r)
			case r.tok.RefreshToken != "":
				g.RefreshToken = r.tok.RefreshToken
			}
		}
		return nil
	})
}

// sameCredential reports whether a and b renew with the same client at the same issuer.
func sameCredential(a, b *secrets.MCPOAuthGrant) bool {
	return a.Issuer == b.Issuer && a.ClientID == b.ClientID
}

func fresh(g *secrets.MCPOAuthGrant, now time.Time) bool {
	return g.AccessToken != "" && (g.ExpiresAt == 0 || now.Add(refreshSkew).Unix() < g.ExpiresAt)
}

// AccessToken returns an access token for serverID that is valid now, renewing it first when
// it is about to expire.
func AccessToken(ctx context.Context, serverID string) (string, error) {
	tok, _, err := refreshGrantRes(ctx, serverID, "")
	return tok, err
}

// RefreshAccessToken is for a caller whose request was refused 401 with the token rejected: it
// renews unless another caller already has, in which case it returns that newer token.
func RefreshAccessToken(ctx context.Context, serverID, rejected string) (string, error) {
	if rejected == "" {
		return "", errors.New("mcp oauth: no rejected token named")
	}
	tok, _, err := refreshGrantRes(ctx, serverID, rejected)
	return tok, err
}

// refreshGrantRes is the single-flight renewal: one request per grant at a time, and a caller that
// waited behind another re-reads the grant and finds the work done instead of sending a second
// request with a refresh token the first already spent.
// It returns the resource the grant was made for, so a caller that sends the token somewhere can
// check the two belong together from the same snapshot.
func refreshGrantRes(ctx context.Context, serverID, rejected string) (token, resource string, err error) {
	token, resource, _, err = refreshGrantGen(ctx, serverID, rejected)
	return token, resource, err
}

// grantGeneration names one sign-in, not its tokens: a renewal keeps it, signing in again (perhaps
// as another account) changes it. Nothing secret is in it.
func grantGeneration(g *secrets.MCPOAuthGrant) string {
	return g.Issuer + "\n" + g.ClientID + "\n" + strconv.FormatInt(g.ConnectedAt, 10) + "\n" + g.SignInID
}

// newSignInID is a grant's SignInID.
func newSignInID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// refreshGrantGen is refreshGrantRes with the generation of the grant the token belongs to, read
// from the same snapshot. The resource is the one the grant recorded for serverID: a grant that
// does not serve serverID as it is now (grantResource) hands out no token for it.
func refreshGrantGen(ctx context.Context, serverID, rejected string) (token, resource, gen string, err error) {
	// The lock is per stored sign-in, read before it is held; a sign-in that replaced it meanwhile
	// is retried under its own lock, so two renewals of one grant never run at once.
	for attempt := 0; ; attempt++ {
		token, resource, gen, err, retry := refreshGrantOnce(ctx, serverID, rejected)
		if !retry || attempt == 2 {
			return token, resource, gen, err
		}
	}
}

func refreshGrantOnce(ctx context.Context, serverID, rejected string) (token, resource, gen string, err error, retry bool) {
	data, reg, d, lerr := grantLoad(serverID)
	if lerr != nil {
		return "", "", "", ErrNotSignedIn, false
	}
	g0 := findGrantFor(data, d)
	if g0 == nil {
		return "", "", "", ErrNotSignedIn, false
	}
	// A token that is usable now is handed out without waiting for any renewal: the lock below
	// is only for the request that spends the refresh token.
	if res, ok := usableToken(g0, d, rejected); ok && !hasPendingRefusal(g0) {
		return g0.AccessToken, res, grantGeneration(g0), nil, false
	}
	key := grantKey(g0)
	mu := lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	if data, reg, d, lerr = grantLoad(serverID); lerr != nil {
		return "", "", "", ErrNotSignedIn, false
	}
	g := findGrantFor(data, d)
	if g == nil {
		return "", "", "", ErrNotSignedIn, false
	}
	if grantKey(g) != key {
		// Renewed (or replaced) while this waited: look again under the new key.
		return "", "", "", errors.New("mcp oauth: the sign-in changed while it was being renewed"), true
	}
	if g.SignInAgain {
		return "", "", "", ErrSignInAgain, false
	}
	res, serves := grantResource(g, d)
	if !serves {
		return "", "", "", ErrMorePermission, false
	}
	rk := rotationKey(g)
	rotatedMu.Lock()
	r, have := rotated[rk]
	rotatedMu.Unlock()
	if have {
		if g.RefreshToken == r.oldRefresh {
			if err := persistRotation(g, r); err != nil {
				return "", "", "", errors.New("mcp oauth: the renewed grant could not be saved yet"), false
			}
			applyRotation(g, r)
		}
		rotatedMu.Lock()
		delete(rotated, rk)
		rotatedMu.Unlock()
	}
	if hasPendingRefusal(g) {
		persistRefusedSet(g)
	}
	if res, ok := usableToken(g, d, rejected); ok {
		return g.AccessToken, res, grantGeneration(g), nil, false
	}
	if g.SetRefused || setRefusedInMemory(key) {
		// The AS will not renew this grant for its set: each member uses the bearer while it
		// lasts and then needs a sign-in of its own; the others keep theirs.
		refuseSet(g, serverID)
		return "", "", "", ErrNotSignedIn, false
	}
	if g.RefreshToken == "" {
		if fresh(g, oauthNow()) && !tokenCovers(g, res) {
			// The token was issued for the grant's other servers and nothing can renew it for
			// this one: this server needs a sign-in, the others keep theirs.
			return "", "", "", ErrMorePermission, false
		}
		markSignInAgain(g, "")
		return "", "", "", ErrSignInAgain, false
	}
	// A shared grant renews for every member it still serves, so the token keeps working for all
	// of them; a grant of its own asks for its one resource, as before.
	resources := servedResources(g, reg)
	tok, terr := renewToken(ctx, serverID, g, resources)
	if cpRefusedSet(terr, g.TokenAuth, resources) {
		// The CP refused the set before calling the AS (a row changed that this workspace has not
		// fetched yet), so the refresh token is unspent: renew for this server alone.
		resources = []string{res}
		tok, terr = renewToken(ctx, serverID, g, resources)
	}
	if terr != nil {
		if terr.code == "invalid_grant" {
			markSignInAgain(g, g.RefreshToken)
			return "", "", "", ErrSignInAgain, false
		}
		if terr.code == "invalid_target" && len(resources) > 1 {
			// The AS renews no grant for several servers, which it may only say now. Whether it
			// spent the refresh token first is unknown, so it is not sent again: the grant is not
			// renewed any more, its bearer serves the members that still accept it until it
			// expires, and the group's next sign-ins ask for one server each.
			markNoShare(g.Group)
			refuseSet(g, serverID)
			return "", "", "", ErrNotSignedIn, false
		}
		return "", "", "", errors.New("mcp oauth: the authorization server did not renew the grant (" + terr.code + ")"), false
	}
	rot := rotation{oldRefresh: g.RefreshToken, tok: tok, at: oauthNow().Unix(), resources: resources}
	rememberSpent(credKey(g, g.RefreshToken), tok.RefreshToken)
	// The rotated refresh token is written before the new access token is used: the old one
	// may already be spent at the AS, so a crash after the response but before this write is
	// the only window in which it can be lost.
	if err := persistRotation(g, rot); err != nil {
		rotatedMu.Lock()
		rotated[rk] = rot
		rotatedMu.Unlock()
		return "", "", "", errors.New("mcp oauth: the renewed grant could not be saved yet"), false
	}
	// The renewal took a round trip: the tenant may have taken this server out of the group or
	// moved it meanwhile, and the token must then not go to it.
	if data, _, d, lerr := grantLoad(serverID); lerr != nil {
		return "", "", "", ErrNotSignedIn, false
	} else if now := findGrantFor(data, d); now == nil || !sameSignIn(now, g) {
		return "", "", "", errors.New("mcp oauth: the sign-in changed while it was being renewed"), false
	} else if r2, ok := grantResource(now, d); !ok || r2 != res {
		return "", "", "", ErrMorePermission, false
	}
	return tok.AccessToken, res, grantGeneration(g), nil, false
}

var (
	refusedMu sync.Mutex
	// refused holds the sign-ins (grantKey) the AS refused to renew for their set, for as long as
	// the store has not recorded it (SetRefused): a failed write must not let another member send
	// that refresh token again.
	refused = map[string]map[string]bool{}
)

func setRefusedInMemory(key string) bool {
	refusedMu.Lock()
	defer refusedMu.Unlock()
	return refused[key] != nil
}

// hasPendingRefusal detects departures absent from this store snapshot.
func hasPendingRefusal(g *secrets.MCPOAuthGrant) bool {
	refusedMu.Lock()
	defer refusedMu.Unlock()
	ids := refused[grantKey(g)]
	if len(ids) > 0 && !g.SetRefused {
		return true
	}
	for id := range ids {
		if !containsString(g.Left, id) {
			return true
		}
	}
	return false
}

// refuseSet records that the AS will not renew the sign-in g for its set, and takes serverID, whose
// bearer it could not renew, off it: one write, and a memory mark that stops renewals of g even if
// the write fails (the next use writes it again). A grant of one server is ended instead.
func refuseSet(g *secrets.MCPOAuthGrant, serverID string) {
	key := grantKey(g)
	refusedMu.Lock()
	if refused[key] == nil {
		refused[key] = map[string]bool{}
	}
	refused[key][serverID] = true
	refusedMu.Unlock()
	if g.Group == "" {
		markSignInAgain(g, "")
		return
	}
	err := updateGrantSecrets(func(*secrets.Data) error { return nil })
	if err == nil {
		if f := OnGrantChange; f != nil {
			go f()
		}
	}
}

// updateGrantSecrets carries any failed refusal into the same transaction as the next update.
func updateGrantSecrets(fn func(*secrets.Data) error) error {
	return updateSecrets(func(s *secrets.Data) error {
		if err := fn(s); err != nil {
			return err
		}
		refusedMu.Lock()
		defer refusedMu.Unlock()
		for i := range s.MCPOAuth {
			g := &s.MCPOAuth[i]
			pending := refused[grantKey(g)]
			if len(pending) == 0 || g.Group == "" {
				continue
			}
			g.SetRefused = true
			for id := range pending {
				if !containsString(g.Left, id) {
					g.Left = append(g.Left, id)
				}
			}
		}
		return nil
	})
}

// persistRefusedSet retries the pending refusal without removing another member.
func persistRefusedSet(g *secrets.MCPOAuthGrant) {
	if err := updateGrantSecrets(func(*secrets.Data) error { return nil }); err == nil {
		if f := OnGrantChange; f != nil {
			go f()
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// grantRenewableFor reports whether g is live for res: it can be renewed, or its bearer was
// issued for res (and, once the AS refused to renew the set, is still unexpired).
func grantRenewableFor(g *secrets.MCPOAuthGrant, res string) bool {
	if g.SetRefused || setRefusedInMemory(grantKey(g)) {
		return tokenCovers(g, res) && fresh(g, oauthNow())
	}
	return g.RefreshToken != "" || tokenCovers(g, res)
}

// usableToken is g's access token's resource for d when the token may be handed to d as it is:
// unexpired, not the one the caller saw refused, and requested for d's resource. A token
// requested for other resources (a shared grant renewed while d was moved away) is never sent to
// d; it is renewed for every resource the grant serves now.
func usableToken(g *secrets.MCPOAuthGrant, d ServerDef, rejected string) (string, bool) {
	if g.SignInAgain || !fresh(g, oauthNow()) || (rejected != "" && g.AccessToken == rejected) {
		return "", false
	}
	res, ok := grantResource(g, d)
	if !ok || !tokenCovers(g, res) {
		return "", false
	}
	return res, true
}

// renewToken sends one refresh request for resources. serverID, not the grant's own: the CP bridge
// resolves the client from the server named in its path, and the one the sign-in started from may
// have left the group since.
func renewToken(ctx context.Context, serverID string, g *secrets.MCPOAuthGrant, resources []string) (tokenResponse, *tokenError) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"resource":      resources,
	}
	return postToken(ctx, serverID, g.Issuer, g.TokenEndpoint, form, g.ClientID, g.ClientSecret, g.TokenAuth)
}

// markSignInAgain drops the tokens of a grant that cannot be renewed and flags it. spent, when
// set, guards against a newer sign-in having replaced the grant while the request ran.
func markSignInAgain(ref *secrets.MCPOAuthGrant, spent string) {
	_ = updateGrantSecrets(func(s *secrets.Data) error {
		for i := range s.MCPOAuth {
			g := &s.MCPOAuth[i]
			switch {
			case sameSignIn(g, ref) && (spent == "" || g.RefreshToken == spent):
				g.AccessToken, g.RefreshToken, g.ExpiresAt = "", "", 0
				g.SignInAgain = true
			case spent != "" && sameCredential(g, ref) && g.RefreshToken == spent:
				// Another sign-in that holds the refused refresh token (an AS that returned it
				// again) loses only that token: its bearer is its own and serves until it expires.
				g.RefreshToken = ""
			}
		}
		return nil
	})
	// The configs that name this server's relay path are stale now. A running CLI keeps its
	// copy and gets the relay's error naming the Console; the next one should not see the server.
	if f := OnGrantChange; f != nil {
		go f()
	}
}

// NoteInsufficientScope records that the server refused the grant's token for lack of scope
// (403 insufficient_scope), so the Console can offer a sign-in with the scope asked for. It goes
// on the grant the server uses, which a shared grant's other members read too: they hold the same
// token, which lacks it. A new sign-in clears it (the grant is replaced).
func NoteInsufficientScope(serverID, scope string) error {
	if !validScope(scope) {
		return errors.New("invalid scope")
	}
	g := currentGrant(serverID)
	if g == nil {
		return nil
	}
	return updateGrantSecrets(func(s *secrets.Data) error {
		for i := range s.MCPOAuth {
			if o := &s.MCPOAuth[i]; sameSignIn(o, g) {
				// A shared grant's members may each ask for a different scope, and the next sign-in
				// has to grant all of them: added to what is recorded, unless that would no longer
				// fit one request. A grant of one server keeps the latest, as before.
				// What has to fit is what the next sign-in sends: the granted scopes with all of it.
				next := scope
				if u := unionScope(o.StepUpScope, scope); len(o.Members) > 1 && validScope(unionScope(o.Scope, u)) {
					next = u
				}
				o.StepUpScope = next
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
	// SetChange says why the state is needs_more_permission when it is not a server's challenge:
	// the grant was not made for this server as it is now (it joined a shared sign-in, moved to
	// another URL, or was given scopes the sign-in did not ask for). The missing scopes, if any,
	// are in StepUpScope.
	SetChange bool `json:"setChange,omitempty"`
	// SharedWith names the other servers one sign-in covers with this one (ADR 0146 decision 4):
	// signing in or disconnecting here does it for them too.
	SharedWith []string `json:"sharedWith,omitempty"`
}

// OAuthStatusOf reads the status of serverID.
func OAuthStatusOf(serverID string) (OAuthStatus, error) {
	data, reg, d, err := grantLoad(serverID)
	if err != nil {
		return OAuthStatus{}, err
	}
	st := OAuthStatus{ServerID: serverID, State: OAuthStateNotSignedIn}
	if g := findGrantFor(data, d); g != nil {
		st.SharedWith = sharedNames(reg, d, g)
		st.Issuer, st.Registration = g.Issuer, g.Registration
		st.Scopes = splitScopes(g.Scope)
		st.ExpiresAt, st.ConnectedAt, st.StepUpScope = g.ExpiresAt, g.ConnectedAt, g.StepUpScope
		res, serves := grantResource(g, d)
		// A member whose token was issued for the others, or has run out, and that cannot renew it
		// needs a sign-in.
		serves = serves && grantRenewableFor(g, res)
		missing := missingScopes(g, d)
		switch {
		case g.SignInAgain:
			st.State = OAuthStateSignInAgain
		case !serves || len(missing) > 0:
			st.State, st.SetChange = OAuthStateNeedsMoreOAu, true
			st.StepUpScope = unionScope(g.StepUpScope, strings.Join(missing, " "))
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
	return updateGrantSecrets(func(s *secrets.Data) error {
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

// DisconnectOAuth revokes the grants serverID uses at the authorization server when it offers
// revocation, then deletes them locally whatever the answer. A shared grant ends for every server
// of its group: they hold the same tokens.
var disconnectSelected func() // test seam after selection, before taking the locks

func DisconnectOAuth(ctx context.Context, serverID string) (DisconnectResult, error) {
	data, _, d, err := grantLoad(serverID)
	if err != nil {
		return DisconnectResult{ServerID: serverID}, err
	}
	// What the row shows is what goes: a server that signed in on its own (its set did not fit,
	// or a DCR retry) disconnects that sign-in only, and leaves its group's to the others.
	if g := findGrantFor(data, d); g != nil && g.Group == "" {
		// The group's grant, which this server used before signing in on its own, stays for the
		// others but no longer applies here: a disconnected server gets no bearer. Both changes are
		// one write under the group's lock, so no reader sees the own grant gone and the group's
		// still applying, and no shared sign-in completes in between.
		if disconnectSelected != nil {
			disconnectSelected()
		}
		return disconnectWhere(ctx, serverID, "", func(o *secrets.MCPOAuthGrant) bool {
			_, member := grantMember(o, serverID)
			return member && o.Group == "" && sameSignIn(o, g)
		}, disconnectOpt{holdGroup: grantGroup(d), requireMatch: func() (bool, error) {
			data, _, current, err := grantLoad(serverID)
			if err != nil {
				return false, err
			}
			return findGrantFor(data, current) != nil, nil
		}, also: func(s *secrets.Data) {
			for i := range s.MCPOAuth {
				if o := &s.MCPOAuth[i]; o.Group != "" && grantApplies(o, d) {
					o.Left = append(o.Left, serverID)
				}
			}
		}})
	}
	group := grantGroup(d)
	return disconnectWhere(ctx, serverID, group, func(g *secrets.MCPOAuthGrant) bool { return grantApplies(g, d) })
}

// disconnectWhere revokes and deletes every stored grant match selects, under the sign-in locks of
// serverID and of group, so a sign-in completing now (with its own grant or the group's) either
// stores before this reads the grants (and is removed) or checks the definition after (and
// refuses). Each grant's own lock is held as well, so no renewal of it is in flight. The order is
// server, group, grants; nothing takes them the other way round.
//
// opt, when given, holds a group's sign-in lock without ending that group's sign-ins (holdGroup),
// and changes the store in the same write that deletes the grants (also).
func disconnectWhere(ctx context.Context, serverID, group string, match func(*secrets.MCPOAuthGrant) bool, opt ...disconnectOpt) (DisconnectResult, error) {
	var o disconnectOpt
	if len(opt) > 0 {
		o = opt[0]
	}
	smu := lockFor(serverLockKey(serverID))
	smu.Lock()
	defer smu.Unlock()
	if g := group; g != "" || o.holdGroup != "" {
		if g == "" {
			g = o.holdGroup
		}
		gmu := lockFor(signInLockKey(serverID, g))
		gmu.Lock()
		defer gmu.Unlock()
	}
	res := DisconnectResult{ServerID: serverID}
	pick := func() []secrets.MCPOAuthGrant {
		data, err := secrets.Load()
		if err != nil {
			return nil
		}
		var out []secrets.MCPOAuthGrant
		for _, g := range data.MCPOAuth {
			if match(&g) {
				out = append(out, g)
			}
		}
		return out
	}
	found := pick()
	if len(found) == 0 && o.requireMatch != nil {
		changed, err := o.requireMatch()
		if err != nil {
			return res, err
		}
		if changed {
			return res, errors.New("mcp oauth: the sign-in changed while disconnecting")
		}
	}
	countEnd(serverID, group)
	dropPendingFor(serverID, group)
	if len(found) == 0 {
		return res, nil
	}
	keys := make([]string, 0, len(found))
	for i := range found {
		keys = append(keys, grantKey(&found[i]))
	}
	unlock := lockAll(keys) // not while a refresh of these grants is in flight
	defer unlock()
	// Read again under the grants' locks: a renewal that finished meanwhile rotated the tokens.
	found = pick()
	if len(found) == 0 {
		if o.requireMatch != nil {
			changed, err := o.requireMatch()
			if err != nil {
				return res, err
			}
			if changed {
				return res, errors.New("mcp oauth: the sign-in changed while disconnecting")
			}
		}
		return res, nil
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].ConnectedAt > found[j].ConnectedAt })
	res.HadGrant, res.Issuer, res.Revoked = true, found[0].Issuer, true
	for i := range found {
		g := &found[i]
		revoked := false
		// A renewal whose write failed holds the newest tokens in memory only: those are the
		// live ones to revoke.
		rk := rotationKey(g)
		rotatedMu.Lock()
		if r, ok := rotated[rk]; ok && r.oldRefresh == g.RefreshToken {
			applyRotation(g, r)
		}
		delete(rotated, rk)
		rotatedMu.Unlock()
		if g.RevocationEndpoint != "" {
			token, hint := g.RefreshToken, "refresh_token"
			if token == "" {
				token, hint = g.AccessToken, "access_token"
			}
			if token != "" {
				rctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
				revoked = revokeTokenVia(rctx, *g, serverID, token, hint)
				cancel()
			}
		}
		res.Revoked = res.Revoked && revoked
	}
	// A sign-in still open in a browser tab must not bring the grant back after this.
	dropPendingFor(serverID, group)
	err := updateGrantSecrets(func(s *secrets.Data) error {
		kept := s.MCPOAuth[:0]
		for _, o := range s.MCPOAuth {
			if !match(&o) {
				kept = append(kept, o)
			}
		}
		s.MCPOAuth = kept
		if o.also != nil {
			o.also(s)
		}
		return nil
	})
	return res, err
}

// revokeUnstored revokes, best effort, the tokens of a grant that is not going to be stored, so
// they do not stay live at the AS with nothing on this side able to end them.
func revokeUnstored(ctx context.Context, g secrets.MCPOAuthGrant) {
	token, hint := g.RefreshToken, "refresh_token"
	if token == "" {
		token, hint = g.AccessToken, "access_token"
	}
	if token == "" || (g.RevocationEndpoint == "" && g.TokenAuth != tokenAuthCP) {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	revokeTokenVia(rctx, g, g.ServerID, token, hint)
}

var (
	endsMu sync.Mutex
	// ends counts the disconnects per sign-in lock key (a server's and a group's). A sign-in
	// records the counts when it starts and stores nothing if they moved: a callback already past
	// the pending map when the disconnect ran would otherwise bring the grant back.
	ends = map[string]uint64{}
)

// endCounts are the disconnect counts a sign-in of serverID (for group, when set) is bound to.
func endCounts(serverID, group string) [2]uint64 {
	endsMu.Lock()
	defer endsMu.Unlock()
	out := [2]uint64{ends[serverLockKey(serverID)]}
	if group != "" {
		out[1] = ends[signInLockKey(serverID, group)]
	}
	return out
}

func countEnd(serverID, group string) {
	endsMu.Lock()
	defer endsMu.Unlock()
	ends[serverLockKey(serverID)]++
	if group != "" {
		ends[signInLockKey(serverID, group)]++
	}
}

// dropPendingFor ends the sign-ins in flight for serverID, and for every server of group when it
// is not empty.
func dropPendingFor(serverID, group string) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for k, p := range pendingAuths {
		if p.serverID == serverID || (group != "" && p.group == group) {
			delete(pendingAuths, k)
		}
	}
}

// revokeTokenVia revokes token. via is the server a confidential client's request names to the CP,
// which resolves the client from it: a definition that still has that client, which the one the
// grant was started from may no longer be.
func revokeTokenVia(ctx context.Context, g secrets.MCPOAuthGrant, via, token, hint string) bool {
	if g.TokenAuth == tokenAuthCP {
		return revokeTokenCP(ctx, via, g.Issuer, g.ClientID, token, hint)
	}
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
