package mcpreg

// One grant per tenant client and issuer (ADR 0146 decision 4). Pinned here:
//   - a sign-in with the tenant's client asks for every enabled server of that client (each URL a
//     resource, the union of their scopes) and serves all of them;
//   - a server the group gained, one moved to another URL and one given a scope the sign-in lacks
//     read as needing more permission, and only the first two lose the token;
//   - a renewal asks only for the URLs the grant still serves, through a server that still has
//     the client;
//   - a tenant fetch that ends one server leaves the shared grant to the others, and ends it with
//     the group; a disconnect ends it for all;
//   - grants made before sharing keep serving their own server.

import (
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// distributeSet writes defs as the tenant set.
func distributeSet(t *testing.T, defs ...ServerDef) {
	t.Helper()
	kept, dropped := acceptTenant(defs)
	if dropped != 0 {
		t.Fatalf("%d tenant definition(s) dropped", dropped)
	}
	if err := writeTenantCache(kept, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tenantCachePath()) })
}

func grantSetDef(id, u string, client *secrets.MCPTenantClient, scopes ...string) ServerDef {
	return ServerDef{
		ID: id, Name: id, Origin: OriginTenant, Transport: TransportHTTP, URL: u, Auth: AuthOAuth,
		Enabled: true, Targets: Targets{Session: true}, OAuthClient: client, Scopes: scopes,
	}
}

func publicClient(f *asFake) *secrets.MCPTenantClient {
	return &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp-public"}
}

func signInVia(t *testing.T, f *asFake, id string) (StartResult, url.Values) {
	t.Helper()
	req := startReq(nil)
	res, q := mustStart(t, id, req)
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	return res, q
}

func statusOf(t *testing.T, id string) OAuthStatus {
	t.Helper()
	st, err := OAuthStatusOf(id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func grantCount(t *testing.T) int {
	t.Helper()
	data, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	return len(data.MCPOAuth)
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestSharedGrantSignsInEveryServerOfTheClient(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	off := grantSetDef("t-off", f.url("/off/mcp"), c, "s:off")
	distributeSet(t,
		grantSetDef("t-a", f.url("/mcp"), c, "s:a"),
		grantSetDef("t-b", f.url("/b/mcp"), c, "s:b", "s:a"),
		grantSetDef("t-other", f.url("/other/mcp"), &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp-2"}, "s:x"),
		off,
	)
	if err := SetEnabled("t-off", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetEnabled("t-off", true) })

	res, q := signInVia(t, f, "t-a")
	if res.Registration != OAuthRegTenant {
		t.Fatalf("registration = %q", res.Registration)
	}
	// Both URLs as resources, the union of the scopes; not the switched-off server, not another client's.
	if got := sorted(q["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), f.url("/b/mcp")})) {
		t.Fatalf("authorization resources = %v", got)
	}
	if q.Get("scope") != "s:a s:b offline_access" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	if got := sorted(f.tokens[0]["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), f.url("/b/mcp")})) {
		t.Fatalf("token resources = %v", got)
	}
	if grantCount(t) != 1 {
		t.Fatalf("%d grants stored, want one shared grant", grantCount(t))
	}
	for _, id := range []string{"t-a", "t-b"} {
		st := statusOf(t, id)
		if st.State != OAuthStateConnected || st.SetChange {
			t.Errorf("%s: %+v, want connected", id, st)
		}
		if tok, err := AccessToken(context.Background(), id); err != nil || tok != "at-0" {
			t.Errorf("%s: AccessToken = %q, %v", id, tok, err)
		}
	}
	if st := statusOf(t, "t-b"); !reflect.DeepEqual(st.SharedWith, []string{"t-a", "t-off"}) {
		t.Errorf("t-b shared with %v", st.SharedWith)
	}
	if st := statusOf(t, "t-other"); st.State != OAuthStateNotSignedIn {
		t.Errorf("another client's server: %+v", st)
	}
	// The switched-off server was not asked for: turned back on, it needs more permission.
	if st := statusOf(t, "t-off"); st.State != OAuthStateNeedsMoreOAu || !st.SetChange {
		t.Errorf("switched-off server: %+v", st)
	}
	if _, err := AccessToken(context.Background(), "t-other"); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("another client's server got a token: %v", err)
	}

	// A renewal asks for both, whichever server needs it.
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if tok, err := AccessToken(context.Background(), "t-b"); err != nil || tok != "at-1" {
		t.Fatalf("renewal: %q, %v", tok, err)
	}
	if got := sorted(f.tokens[1]["resource"]); f.tokens[1].Get("grant_type") != "refresh_token" ||
		!reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), f.url("/b/mcp")})) {
		t.Fatalf("renewal form %v", f.tokens[1])
	}
	if tok, err := AccessToken(context.Background(), "t-a"); err != nil || tok != "at-1" || f.tokenCount() != 2 {
		t.Fatalf("the other member renewed again: %q, %v, %d token requests", tok, err, f.tokenCount())
	}
}

func TestSharedGrantSetChangesNeedMorePermission(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	a := grantSetDef("t-a", f.url("/mcp"), c, "s:a")
	b := grantSetDef("t-b", f.url("/b/mcp"), c, "s:b")
	distributeSet(t, a, b)
	signInVia(t, f, "t-a")

	// The tenant adds a server, gives one a scope and moves the other.
	added := grantSetDef("t-new", f.url("/new/mcp"), c, "s:new")
	a2 := a
	a2.Scopes = []string{"s:a", "s:more"}
	b2 := b
	b2.URL = f.url("/b2/mcp")
	distributeSet(t, a2, b2, added)

	for id, wantScope := range map[string]string{"t-new": "s:new", "t-a": "s:more", "t-b": ""} {
		st := statusOf(t, id)
		if st.State != OAuthStateNeedsMoreOAu || !st.SetChange || st.StepUpScope != wantScope {
			t.Errorf("%s: %+v, want needs more permission asking for %q", id, st, wantScope)
		}
	}
	// A new scope leaves the URL served; a joined or moved server gets no token.
	g := currentGrant("t-a")
	if _, ok := grantResource(g, a2); !ok {
		t.Error("a scope change took the token from its server")
	}
	for _, d := range []ServerDef{added, b2} {
		if _, ok := grantResource(g, d); ok {
			t.Errorf("%s is served by a grant not made for it", d.ID)
		}
		if _, err := AccessToken(context.Background(), d.ID); !errors.Is(err, ErrMorePermission) {
			t.Errorf("%s: AccessToken err = %v", d.ID, err)
		}
	}
	// The renewal asks only for what the grant still serves: not the moved server's old URL.
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := AccessToken(context.Background(), "t-a"); err != nil {
		t.Fatal(err)
	}
	if got := f.tokens[len(f.tokens)-1]["resource"]; !reflect.DeepEqual(got, []string{f.url("/mcp")}) {
		t.Fatalf("renewal resources = %v", got)
	}

	// Signing in again asks for all three and serves all three.
	_, q := signInVia(t, f, "t-a")
	if got := sorted(q["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), f.url("/b2/mcp"), f.url("/new/mcp")})) {
		t.Fatalf("resources = %v", got)
	}
	if got := sorted(splitScopes(q.Get("scope"))); !reflect.DeepEqual(got, sorted([]string{"s:a", "s:b", "s:more", "s:new", "offline_access"})) {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	if grantCount(t) != 1 {
		t.Fatalf("%d grants stored", grantCount(t))
	}
	for _, id := range []string{"t-a", "t-b", "t-new"} {
		if st := statusOf(t, id); st.State != OAuthStateConnected {
			t.Errorf("%s: %+v", id, st)
		}
	}
}

// The step-up button sends the granted scopes plus what is missing; the definitions' scopes are
// added to it, so a member is never left short by a sign-in started from another.
func TestSharedGrantStepUpKeepsEveryDefinitionScope(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c, "s:a"), grantSetDef("t-b", f.url("/b/mcp"), c, "s:b"))
	_, q := mustStart(t, "t-a", startReq(func(r *StartRequest) { r.Scope = "s:extra" }))
	if got := sorted(splitScopes(q.Get("scope"))); !reflect.DeepEqual(got, []string{"s:a", "s:b", "s:extra"}) {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
}

// A server added to the group while the sign-in was open is not stored as a member; one that
// moved meanwhile is dropped from it.
func TestSharedGrantStoresOnlyMembersStillInTheGroup(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	a := grantSetDef("t-a", f.url("/mcp"), c, "s:a")
	b := grantSetDef("t-b", f.url("/b/mcp"), c, "s:b")
	distributeSet(t, a, b)
	req := startReq(nil)
	mustStart(t, "t-a", req)
	b.URL = f.url("/b2/mcp")
	distributeSet(t, a, b, grantSetDef("t-new", f.url("/new/mcp"), c))
	if _, err := CompleteOAuth(context.Background(), "t-a", CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	g := currentGrant("t-a")
	if len(g.Members) != 1 || g.Members[0].ServerID != "t-a" {
		t.Fatalf("members = %+v", g.Members)
	}
	if st := statusOf(t, "t-b"); st.State != OAuthStateNeedsMoreOAu {
		t.Errorf("moved server: %+v", st)
	}
}

func TestSharedGrantThroughTheCPNamesAServerThatHasTheClient(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	cp := newCPFake(t)
	c := &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp", Confidential: true}
	a := grantSetDef("t-a", f.url("/mcp"), c, "s:a")
	b := grantSetDef("t-b", f.url("/b/mcp"), c, "s:b")
	distributeSet(t, a, b)
	signInVia(t, f, "t-a")
	if cp.paths[0] != "/internal/mcp-servers/t-a/oauth/token" || len(cp.forms[0]["resource"]) != 2 {
		t.Fatalf("exchange: %v %v", cp.paths, cp.forms[0])
	}
	// The server the sign-in started from leaves the group: renewals go through one that stays.
	a.OAuthClient = &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp-2", Confidential: true}
	distributeSet(t, a, b)
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if tok, err := AccessToken(context.Background(), "t-b"); err != nil || tok != "cp-at-1" {
		t.Fatalf("renewal: %q, %v", tok, err)
	}
	if cp.paths[1] != "/internal/mcp-servers/t-b/oauth/token" || !reflect.DeepEqual(cp.forms[1]["resource"], []string{f.url("/b/mcp")}) {
		t.Fatalf("renewal went to %s with %v", cp.paths[1], cp.forms[1])
	}
	if st := statusOf(t, "t-a"); st.State != OAuthStateNotSignedIn {
		t.Errorf("the server that left: %+v", st)
	}
}

func TestTenantFetchEndsASharedGrantOnlyWithItsGroup(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	a := grantSetDef("t-a", f.url("/mcp"), c, "s:a")
	b := grantSetDef("t-b", f.url("/b/mcp"), c, "s:b")
	distributeSet(t, a, b)
	signInVia(t, f, "t-a")

	ended := endedOAuth([]ServerDef{a, b}, []ServerDef{b})
	if len(ended) != 1 || ended[0].ID != "t-a" || ended[0].Group == "" || ended[0].GroupGone {
		t.Fatalf("ended = %+v", ended)
	}
	distributeSet(t, b)
	if err := EndTenantSignIn(context.Background(), ended[0]); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, "t-b"); st.State != OAuthStateConnected || len(f.revokes) != 0 {
		t.Fatalf("the remaining server lost its sign-in: %+v, revokes %v", st, f.revokes)
	}

	ended = endedOAuth([]ServerDef{b}, nil)
	if len(ended) != 1 || !ended[0].GroupGone {
		t.Fatalf("ended = %+v", ended)
	}
	distributeSet(t)
	if err := EndTenantSignIn(context.Background(), ended[0]); err != nil {
		t.Fatal(err)
	}
	if grantCount(t) != 0 || len(f.revokes) != 1 || f.revokes[0].Get("token") != "rt-0" {
		t.Fatalf("the group's grant was not ended: %d grants, revokes %v", grantCount(t), f.revokes)
	}
}

func TestDisconnectEndsTheSharedGrantForEveryServer(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c), grantSetDef("t-b", f.url("/b/mcp"), c))
	signInVia(t, f, "t-a")
	res, err := DisconnectOAuth(context.Background(), "t-b")
	if err != nil || !res.HadGrant || !res.Revoked || len(f.revokes) != 1 {
		t.Fatalf("disconnect %+v %v, revokes %v", res, err, f.revokes)
	}
	for _, id := range []string{"t-a", "t-b"} {
		if st := statusOf(t, id); st.State != OAuthStateNotSignedIn {
			t.Errorf("%s: %+v", id, st)
		}
	}
}

// Grants made before sharing (no members) each keep serving their own server.
func TestGrantsFromBeforeSharingKeepServingTheirServer(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c, "s:a"), grantSetDef("t-b", f.url("/b/mcp"), c, "s:b"))
	old := func(id, res, at string, connected int64) secrets.MCPOAuthGrant {
		return secrets.MCPOAuthGrant{
			ServerID: id, Issuer: f.url(""), Registration: OAuthRegTenant, ClientID: "corp-public", TokenAuth: "none",
			TokenEndpoint: f.url("/token"), Resource: res, AccessToken: at, RefreshToken: "rt-" + at,
			ExpiresAt: time.Now().Add(time.Hour).Unix(), ConnectedAt: connected,
		}
	}
	if err := secrets.Update(func(s *secrets.Data) error {
		s.MCPOAuth = []secrets.MCPOAuthGrant{old("t-a", f.url("/mcp"), "old-a", 100), old("t-b", f.url("/b/mcp"), "old-b", 200)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"t-a": "old-a", "t-b": "old-b"} {
		if st := statusOf(t, id); st.State != OAuthStateConnected {
			t.Errorf("%s: %+v", id, st)
		}
		if tok, err := AccessToken(context.Background(), id); err != nil || tok != want {
			t.Errorf("%s: AccessToken = %q, %v", id, tok, err)
		}
	}
}

// A definition with its own grant (no tenant client) that is given a scope by the tenant reads as
// needing more permission, and a sign-in from there asks for it.
func TestScopeAddedToAnOwnGrantNeedsMorePermission(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	d := grantSetDef("t-a", f.url("/mcp"), nil, "s:a")
	distributeSet(t, d)
	signInVia(t, f, "t-a")
	if st := statusOf(t, "t-a"); st.State != OAuthStateConnected || st.SharedWith != nil {
		t.Fatalf("%+v", st)
	}
	d.Scopes = []string{"s:a", "s:b"}
	distributeSet(t, d)
	st := statusOf(t, "t-a")
	if st.State != OAuthStateNeedsMoreOAu || st.StepUpScope != "s:b" {
		t.Fatalf("%+v", st)
	}
	if tok, err := AccessToken(context.Background(), "t-a"); err != nil || tok == "" {
		t.Errorf("a scope change took the token: %q, %v", tok, err)
	}
}

// A token renewed for fewer servers (one was moved away meanwhile) is never handed to a server
// outside them: when that server comes back, it gets a token renewed for it.
func TestSharedGrantNarrowedTokenIsRenewedForARestoredMember(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	a := grantSetDef("t-a", f.url("/mcp"), c)
	b := grantSetDef("t-b", f.url("/b/mcp"), c)
	distributeSet(t, a, b)
	signInVia(t, f, "t-a")
	moved := b
	moved.URL = f.url("/b2/mcp")
	distributeSet(t, a, moved)
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if tok, err := AccessToken(context.Background(), "t-a"); err != nil || tok != "at-1" {
		t.Fatalf("renewal for A: %q, %v", tok, err)
	}
	distributeSet(t, a, b)
	tok, err := AccessToken(context.Background(), "t-b")
	if err != nil || tok != "at-2" {
		t.Fatalf("B got %q, %v; want a token renewed for it", tok, err)
	}
	if got := sorted(f.tokens[2]["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), f.url("/b/mcp")})) {
		t.Fatalf("renewal resources = %v", got)
	}
}

// A renewal that finds the server out of the group, or moved, when it returns hands out nothing.
func TestSharedGrantRenewalRechecksTheServerAfterTheRoundTrip(t *testing.T) {
	for name, change := range map[string]func(f *asFake, b *ServerDef){
		"left the group": func(f *asFake, b *ServerDef) {
			b.OAuthClient = &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp-2"}
		},
		"moved": func(f *asFake, b *ServerDef) { b.URL = f.url("/b2/mcp") },
	} {
		t.Run(name, func(t *testing.T) {
			resetOAuthState(t)
			f := newASFake(t, nil)
			c := publicClient(f)
			a := grantSetDef("t-a", f.url("/mcp"), c)
			b := grantSetDef("t-b", f.url("/b/mcp"), c)
			distributeSet(t, a, b)
			signInVia(t, f, "t-a")
			oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
			f.mu.Lock()
			f.tokenDelay = 400 * time.Millisecond
			f.mu.Unlock()
			done := make(chan error, 1)
			go func() {
				_, err := AccessToken(context.Background(), "t-b")
				done <- err
			}()
			for f.tokenCount() < 2 {
				time.Sleep(10 * time.Millisecond)
			}
			change(f, &b)
			distributeSet(t, a, b)
			if err := <-done; err == nil {
				t.Fatal("the server was handed the token after it changed during the renewal")
			}
		})
	}
}

// A disconnect that runs while a callback is exchanging its code ends that sign-in too.
func TestDisconnectDuringTheCodeExchangeEndsTheSignIn(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c), grantSetDef("t-b", f.url("/b/mcp"), c))
	req := startReq(nil)
	mustStart(t, "t-a", req)
	f.mu.Lock()
	f.tokenDelay = 400 * time.Millisecond
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := CompleteOAuth(context.Background(), "t-a", CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
		done <- err
	}()
	for f.tokenCount() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := DisconnectOAuth(context.Background(), "t-b"); err != nil {
		t.Fatal(err)
	}
	wantCode(t, <-done, OAuthErrStateMismatch)
	if grantCount(t) != 0 {
		t.Fatal("the disconnected sign-in was stored")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.revokes) != 1 || f.revokes[0].Get("token") != "rt-0" {
		t.Errorf("the unstored tokens were not revoked: %v", f.revokes)
	}
}

func TestGrantGroupKeepsPathIssuersApart(t *testing.T) {
	d := func(iss string) ServerDef {
		return grantSetDef("x", "https://m.example.com/mcp", &secrets.MCPTenantClient{Issuer: iss, ClientID: "corp"})
	}
	if grantGroup(d("https://as.example.com")) != grantGroup(d("https://as.example.com/")) {
		t.Error("a root \"/\" made another group")
	}
	if grantGroup(d("https://as.example.com/realm")) == grantGroup(d("https://as.example.com/realm/")) {
		t.Error("two path issuers share a group")
	}
}

// A URL is one resource whatever it holds; a plain-http peer and a group too large for one request
// are not shared.
func TestSharedSignInResourcesAndWhoJoins(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	comma := f.url("/b/mcp?x=1,2")
	distributeSet(t,
		grantSetDef("t-a", f.url("/mcp"), c),
		grantSetDef("t-b", comma, c),
		grantSetDef("t-plain", "http://plain.example.com/mcp", c),
	)
	_, q := signInVia(t, f, "t-a")
	if got := sorted(q["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), comma})) {
		t.Fatalf("authorization resources = %v", got)
	}
	if got := sorted(f.tokens[0]["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), comma})) {
		t.Fatalf("token resources = %v", got)
	}
	if st := statusOf(t, "t-plain"); st.State == OAuthStateConnected {
		t.Error("a plain-http peer was signed in")
	}

	resetOAuthState(t)
	defs := []ServerDef{grantSetDef("t-a", f.url("/mcp"), c)}
	for i := 0; i < maxSharedResources; i++ {
		defs = append(defs, grantSetDef("t-p"+string(rune('a'+i%26))+string(rune('a'+i/26)), f.url("/p/mcp")+"?n="+string(rune('a'+i%26))+string(rune('a'+i/26)), c))
	}
	distributeSet(t, defs...)
	_, q = signInVia(t, f, "t-a")
	if got := q["resource"]; len(got) != 1 || got[0] != f.url("/mcp") {
		t.Fatalf("a group of %d asked for %v", len(defs), got)
	}
	if g := currentGrant("t-a"); g.Group != "" || len(g.Members) != 1 {
		t.Fatalf("grant = %+v", g)
	}
}

// A tenant client's grant made before groups existed ends with its own server, as before, even
// while the group goes on.
func TestOldTenantGrantEndsWithItsServer(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	a := grantSetDef("t-a", f.url("/mcp"), c)
	b := grantSetDef("t-b", f.url("/b/mcp"), c)
	distributeSet(t, a, b)
	old := func(id, res string) secrets.MCPOAuthGrant {
		return secrets.MCPOAuthGrant{
			ServerID: id, Issuer: f.url(""), Registration: OAuthRegTenant, ClientID: "corp-public", TokenAuth: "none",
			TokenEndpoint: f.url("/token"), RevocationEndpoint: f.url("/revoke"), Resource: res,
			AccessToken: "at-" + id, RefreshToken: "rt-" + id, ExpiresAt: time.Now().Add(time.Hour).Unix(), ConnectedAt: 100,
		}
	}
	if err := secrets.Update(func(s *secrets.Data) error {
		s.MCPOAuth = []secrets.MCPOAuthGrant{old("t-a", f.url("/mcp")), old("t-b", f.url("/b/mcp"))}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	moved := a
	moved.URL = f.url("/v2/mcp")
	ended := endedOAuth([]ServerDef{a, b}, []ServerDef{moved, b})
	distributeSet(t, moved, b)
	if len(ended) != 1 || ended[0].GroupGone {
		t.Fatalf("ended = %+v", ended)
	}
	if err := EndTenantSignIn(context.Background(), ended[0]); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, "t-a"); st.State != OAuthStateNotSignedIn {
		t.Errorf("t-a: %+v", st)
	}
	if st := statusOf(t, "t-b"); st.State != OAuthStateConnected {
		t.Errorf("t-b: %+v", st)
	}
	if len(f.revokes) != 1 || f.revokes[0].Get("token") != "rt-t-a" {
		t.Errorf("revokes = %v", f.revokes)
	}
}

// A member that signs in on its own (a DCR retry) leaves its group's grant to the others.
func TestOwnSignInLeavesTheGroupsGrant(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c), grantSetDef("t-b", f.url("/b/mcp"), c))
	signInVia(t, f, "t-a")
	req := startReq(func(r *StartRequest) { r.Registration = OAuthRegDCR })
	if res, _ := mustStart(t, "t-a", req); res.Registration != OAuthRegDCR {
		t.Fatalf("registration = %q", res.Registration)
	}
	if _, err := CompleteOAuth(context.Background(), "t-a", CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	if grantCount(t) != 2 {
		t.Fatalf("%d grants", grantCount(t))
	}
	if tok, err := AccessToken(context.Background(), "t-b"); err != nil || tok != "at-0" {
		t.Errorf("t-b lost the group's sign-in: %q, %v", tok, err)
	}
	if st := statusOf(t, "t-a"); st.State != OAuthStateConnected || st.Registration != OAuthRegDCR {
		t.Errorf("t-a: %+v", st)
	}
}

// A server without scopes of its own still asks for its challenge's scope next to the others'.
func TestSharedSignInKeepsTheSignersChallengeScope(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c), grantSetDef("t-b", f.url("/b/mcp"), c, "s:b"))
	_, q := mustStart(t, "t-a", startReq(nil))
	if got := sorted(splitScopes(q.Get("scope"))); !reflect.DeepEqual(got, []string{"mcp:read", "offline_access", "s:b"}) {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
}

// Two members of a shared grant that each step up keep both scopes for the next sign-in.
func TestSharedGrantStepUpsAccumulate(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c), grantSetDef("t-b", f.url("/b/mcp"), c))
	signInVia(t, f, "t-a")
	if err := NoteInsufficientScope("t-a", "x"); err != nil {
		t.Fatal(err)
	}
	if err := NoteInsufficientScope("t-b", "y"); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, "t-a"); st.StepUpScope != "x y" {
		t.Fatalf("step-up = %q", st.StepUpScope)
	}
}

// Through the CP: a peer the tenant moved is not asked for, and a set the CP refuses (a change this
// workspace has not fetched yet) falls back to the server signing in, for the exchange and for a
// renewal alike.
func TestSharedGrantFallsBackWhenTheCPRefusesTheSet(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	cp := newCPFake(t)
	c := &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp", Confidential: true}
	a := grantSetDef("t-a", f.url("/mcp"), c)
	b := grantSetDef("t-b", f.url("/b/mcp"), c)
	x := grantSetDef("t-x", f.url("/x/mcp"), c)
	distributeSet(t, a, b, x)
	req := startReq(nil)
	mustStart(t, "t-a", req)
	moved := x
	moved.URL = f.url("/x2/mcp")
	distributeSet(t, a, b, moved)
	cp.reply = func(n int, form url.Values) (int, string) {
		if len(form["resource"]) > 1 {
			return 400, `{"error":"invalid_target"}`
		}
		return 200, `{"access_token":"cp-at-` + string(rune('0'+n)) + `","token_type":"Bearer","expires_in":3600,"refresh_token":"cp-rt-` + string(rune('0'+n)) + `"}`
	}
	if _, err := CompleteOAuth(context.Background(), "t-a", CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	if got := sorted(cp.forms[0]["resource"]); !reflect.DeepEqual(got, sorted([]string{f.url("/mcp"), f.url("/b/mcp")})) {
		t.Fatalf("first exchange asked for %v", got)
	}
	if got := cp.forms[1]["resource"]; !reflect.DeepEqual(got, []string{f.url("/mcp")}) || cp.forms[1].Get("code") != "c" {
		t.Fatalf("fallback exchange: %v", cp.forms[1])
	}
	g := currentGrant("t-a")
	if !reflect.DeepEqual(g.TokenResources, []string{f.url("/mcp")}) || len(g.Members) != 2 {
		t.Fatalf("grant = %+v", g)
	}
	// B is a member but the token was not issued for it: it is renewed for B before B gets one.
	tok, err := AccessToken(context.Background(), "t-b")
	if err != nil || tok != "cp-at-3" {
		t.Fatalf("t-b: %q, %v", tok, err)
	}
	if got := cp.forms[3]["resource"]; !reflect.DeepEqual(got, []string{f.url("/b/mcp")}) || cp.paths[3] != "/internal/mcp-servers/t-b/oauth/token" {
		t.Fatalf("t-b renewal: %s %v", cp.paths[3], cp.forms[3])
	}
}

// Two members renewing at once spend the refresh token once.
func TestSharedGrantRenewsOnceForConcurrentMembers(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	c := publicClient(f)
	distributeSet(t, grantSetDef("t-a", f.url("/mcp"), c), grantSetDef("t-b", f.url("/b/mcp"), c))
	signInVia(t, f, "t-a")
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	f.mu.Lock()
	f.tokenDelay = 200 * time.Millisecond
	f.mu.Unlock()
	errs := make(chan error, 2)
	for _, id := range []string{"t-a", "t-b"} {
		go func(id string) {
			_, err := AccessToken(context.Background(), id)
			errs <- err
		}(id)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := f.tokenCount(); n != 2 {
		t.Fatalf("%d token requests, want the sign-in and one renewal", n)
	}
}
