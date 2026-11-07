package main

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const prefixCB = "https://claude.ai/api/mcp/auth_callback"

func TestMCPOAuthRedirectMatchesURLPrefixOnParsedURL(t *testing.T) {
	e := newMCPOAuthEnv(t)
	trusted := trustedHosts{prefixCB: true, "chatgpt.com": true, "https://mcp.corp.example/oauth/": true}
	for raw, want := range map[string]redirectClass{
		prefixCB:                                               redirectHosted,
		prefixCB + "/":                                         redirectHosted,
		prefixCB + "/sub/seg":                                  redirectHosted,
		prefixCB + "?x=1":                                      redirectHosted,
		"https://chatgpt.com/any/path":                         redirectHosted, // a bare host keeps meaning "any path"
		"https://chatgpt.com/":                                 redirectHosted,
		"https://mcp.corp.example/oauth/cb":                    redirectHosted,
		"http://127.0.0.1:9/cb":                                redirectLoopback,
		"https://claude.ai/other":                              redirectRefused,
		"https://claude.ai/":                                   redirectRefused,
		"https://claude.ai":                                    redirectRefused,
		prefixCB + "x":                                         redirectRefused, // string prefix, not a path segment
		prefixCB + "_evil":                                     redirectRefused,
		"https://claude.ai/api/mcp/auth_callbac":               redirectRefused,
		"https://claude.ai/api/mcp/../x":                       redirectRefused,
		"https://claude.ai/api/mcp/auth_callback/../../x":      redirectRefused,
		"https://claude.ai/api/mcp/./auth_callback":            redirectRefused,
		"https://claude.ai/api/mcp//auth_callback":             redirectRefused,
		"https://claude.ai/api/mcp/auth%5Fcallback":            redirectRefused, // encoded characters
		"https://claude.ai/api%2Fmcp/auth_callback":            redirectRefused,
		"https://claude.ai/api/mcp%2fauth_callback":            redirectRefused,
		prefixCB + "%2f..%2fx":                                 redirectRefused,
		"https://claude.ai:443/api/mcp/auth_callback":          redirectRefused,
		"https://claude.ai:8443/api/mcp/auth_callback":         redirectRefused,
		"https://user@claude.ai/api/mcp/auth_callback":         redirectRefused,
		"https://claude.ai@evil.example/api/mcp/auth_callback": redirectRefused,
		"https://claude.ai.evil.example/api/mcp/auth_callback": redirectRefused,
		"https://evil.example/api/mcp/auth_callback":           redirectRefused,
		"http://claude.ai/api/mcp/auth_callback":               redirectRefused,
		prefixCB + "#frag":                                     redirectRefused,
		"https://mcp.corp.example/oauth":                       redirectRefused, // entry ends in "/": below it only
		"https://mcp.corp.example/oauthx/cb":                   redirectRefused,
	} {
		if got := e.api.classifyRedirect(trusted, raw); got != want {
			t.Errorf("classifyRedirect(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestMCPOAuthTrustedEntryValidation(t *testing.T) {
	for _, ok := range []string{"claude.ai", prefixCB, "https://Claude.ai/Api/x/", "https://a.example/x/y"} {
		if _, err := parseTrustedEntry(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://claude.ai/x", "https://claude.ai", "https://claude.ai/", "https://claude.ai:443/x", "https://u@claude.ai/x",
		"https://claude.ai/x?y=1", "https://claude.ai/x#f", "https://claude.ai/a/../b", "https://claude.ai/a//b",
		"https://claude.ai/a%2fb", "https://*.claude.ai/x", "https://10.0.0.1/x", "https://claude.ai/x/./y", "claude.ai/x",
	} {
		if _, err := parseTrustedEntry(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got, err := validateTrustedClientHosts([]string{"https://Claude.AI/Api/x", "claude.ai", "claude.ai"})
	if err != nil || len(got) != 2 || got[0] != "claude.ai" || got[1] != "https://claude.ai/api/x" {
		t.Errorf("canonical form: %v %v", got, err)
	}
	// The lenient reader keeps prefix entries and drops what the validator refuses.
	p := parseTrustedClientHosts("https://claude.ai/cb,claude.com,https://bad.example,*.x")
	if len(p) != 2 || !p["https://claude.ai/cb"] || !p["claude.com"] {
		t.Errorf("lenient parse: %v", p)
	}
}

// The built-in default pins claude.ai to its callback path and the shipped defaults for the other
// two hosts stay host-only.
func TestMCPOAuthDefaultsPinClaudeAI(t *testing.T) {
	e := newMCPOAuthEnv(t)
	tr := mustTrusted(e.api)
	if got := e.api.classifyRedirect(tr, "https://claude.ai/something/else"); got != redirectRefused {
		t.Errorf("claude.ai other path = %d", got)
	}
	for _, ok := range []string{prefixCB, "https://claude.com/api/mcp/auth_callback", "https://chatgpt.com/connector_platform_oauth_redirect"} {
		if e.api.classifyRedirect(tr, ok) != redirectHosted {
			t.Errorf("%s refused by the default list", ok)
		}
	}
}

func TestMCPOAuthRegisterAndAuthorizeRefuseOutsidePrefix(t *testing.T) {
	e := newMCPOAuthEnv(t)
	body := `{"redirect_uris":["https://claude.ai/other/path"]}`
	rec := e.do(http.MethodPost, mcpOAuthRegister, body, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest || jsonField(t, rec, "error") != "invalid_redirect_uri" {
		t.Fatalf("register outside the prefix: %d %s", rec.Code, rec.Body)
	}
	// A registration made while the list was host-only is vetted again at authorize time.
	if err := e.mgr.store.SetSetting(context.Background(), mcpOAuthTrustedSetting, `["claude.ai"]`); err != nil {
		t.Fatal(err)
	}
	id := e.registerClient("https://claude.ai/other/path", prefixCB)
	_, challenge := pkcePair()
	if rec := e.authorize(e.authQuery(id, "https://claude.ai/other/path", challenge)); !strings.Contains(rec.Body.String(), `name="ticket"`) {
		t.Fatalf("host-only entry should admit it: %d", rec.Code)
	}
	if err := e.mgr.store.SetSetting(context.Background(), mcpOAuthTrustedSetting, `["`+prefixCB+`"]`); err != nil {
		t.Fatal(err)
	}
	rec = e.authorize(e.authQuery(id, "https://claude.ai/other/path", challenge))
	if strings.Contains(rec.Body.String(), `name="ticket"`) {
		t.Errorf("authorize accepted a redirect outside the configured prefix: %d", rec.Code)
	}
	if rec := e.authorize(e.authQuery(id, prefixCB, challenge)); !strings.Contains(rec.Body.String(), `name="ticket"`) {
		t.Errorf("authorize refused the in-prefix redirect: %d %s", rec.Code, rec.Body)
	}
}

func TestMCPOAuthPrefixOnlyListStillFetchesCIMDAndRefreshesGrants(t *testing.T) {
	e := newMCPOAuthEnv(t)
	tr := trustedHosts{prefixCB: true}
	if !tr.hostTrusted("claude.ai") || tr.hostTrusted("claude.com") {
		t.Error("hostTrusted")
	}
	if !e.api.grantHostAllowed(tr, "claude.ai") || e.api.grantHostAllowed(tr, "evil.example") || !e.api.grantHostAllowed(nil, "127.0.0.1:55") {
		t.Error("grantHostAllowed")
	}
	const doc = "https://claude.ai/oauth/client.json"
	if _, ok := e.api.cimdURLAllowed(tr, doc); !ok {
		t.Error("a CIMD document on a host named by a prefix entry must be fetchable")
	}
}

func TestMCPOAuthGrantAuditRecordsPathAndRegistration(t *testing.T) {
	e := newMCPOAuthEnv(t)
	id := e.registerClient(prefixCB)
	_ = e.connect(id, prefixCB, "read")
	audit, err := e.mgr.store.ListAuditByTenant(context.Background(), e.tenA.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range audit {
		if a.Action == "mcp.oauth.grant" && strings.HasPrefix(a.Detail, "created") {
			if !strings.Contains(a.Detail, `redirect_path="/api/mcp/auth_callback"`) || !strings.Contains(a.Detail, "via=dcr") ||
				!strings.Contains(a.Detail, `redirect_host="claude.ai"`) {
				t.Errorf("grant audit lacks the path / registration kind: %s", a.Detail)
			}
			return
		}
	}
	t.Fatal("no grant audit row")
}

func TestMCPOAuthConsentShowsFullRedirectAndRegistrationKind(t *testing.T) {
	e := newMCPOAuthEnv(t)
	_, challenge := pkcePair()
	id := e.registerClient(prefixCB)
	hdr := map[string]string{"Accept-Language": "en"}
	rec := e.do(http.MethodGet, mcpOAuthAuthorize+"?"+e.authQuery(id, prefixCB, challenge).Encode(), "", hdr)
	body := rec.Body.String()
	if !strings.Contains(body, prefixCB) || !strings.Contains(body, "dynamic client registration (DCR)") || strings.Contains(body, "(CIMD)") {
		t.Errorf("DCR consent page: %s", body)
	}
	const doc = "https://chatgpt.com/oauth/client.json"
	e.docs[doc] = `{"client_id":"` + doc + `","client_name":"ChatGPT","redirect_uris":["https://chatgpt.com/connector_platform_oauth_redirect"],"token_endpoint_auth_method":"none"}`
	rec = e.do(http.MethodGet, mcpOAuthAuthorize+"?"+e.authQuery(doc, "https://chatgpt.com/connector_platform_oauth_redirect", challenge).Encode(), "", hdr)
	body = rec.Body.String()
	if !strings.Contains(body, "https://chatgpt.com/connector_platform_oauth_redirect") || !strings.Contains(body, "(CIMD)") || strings.Contains(body, "(DCR)") {
		t.Errorf("CIMD consent page: %d %s", rec.Code, body)
	}
}

func TestMCPOAuthRegisterSourceGroupsIPv6By64(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ffff", true},
		{"2001:db8:1:2::1", "2001:db8:1:3::1", false},
		{"192.0.2.1", "192.0.2.2", false},
		{"::ffff:192.0.2.1", "192.0.2.1", true},
	} {
		got := registerSourceKey(netip.MustParseAddr(c.a)) == registerSourceKey(netip.MustParseAddr(c.b))
		if got != c.same {
			t.Errorf("%s vs %s: same=%v, want %v", c.a, c.b, got, c.same)
		}
	}
	// End to end: the limit applies across one /64.
	rl := newRateLimiter(2, time.Hour, 100)
	now := time.Now()
	ok := 0
	for i := 0; i < 6; i++ {
		if rl.allow(registerSourceKey(netip.MustParseAddr("2001:db8:9:9::"+strconv.Itoa(i+1))), now) {
			ok++
		}
	}
	if ok != 2 {
		t.Errorf("%d registrations allowed across one /64, want 2", ok)
	}
}

func TestMCPOAuthRegistrationCapEvictsOldestGrantlessAndWarns(t *testing.T) {
	e := newMCPOAuthEnv(t)
	ctx := context.Background()
	st := e.mgr.store
	if w := e.mgr.deploymentWarnings(); len(w) != 0 {
		t.Fatalf("warnings before the cap: %v", w)
	}
	// Fill the table: one registration with a grant (the oldest), then grant-less ones.
	now := e.now().Unix()
	mk := func(i int) string {
		id := "fill-" + strconv.Itoa(i)
		if _, err := st.CreateMCPOAuthClient(ctx, store.MCPOAuthClient{ClientID: id, RedirectURIs: "[]", CreatedAt: now - 1000 + int64(i)}, mcpOAuthMaxClients); err != nil {
			t.Fatal(err)
		}
		return id
	}
	granted := mk(0)
	if err := st.CreateMCPOAuthGrant(ctx, store.MCPOAuthGrant{ID: "g-fill", ClientID: granted, IdentityID: e.ident.ID, MembershipID: "m",
		Scope: "read", Resource: "r", IdleExpiresAt: now + 9999, AbsExpiresAt: now + 9999}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < mcpOAuthMaxClients; i++ {
		mk(i)
	}
	id := e.registerClient(loopbackCB)
	if _, ok, _ := st.GetMCPOAuthClient(ctx, id); !ok {
		t.Fatal("the new registration is missing")
	}
	if _, ok, _ := st.GetMCPOAuthClient(ctx, "fill-1"); ok {
		t.Error("the oldest grant-less registration survived")
	}
	if _, ok, _ := st.GetMCPOAuthClient(ctx, granted); !ok {
		t.Error("a registration with a grant was evicted")
	}
	if w := e.mgr.deploymentWarnings(); len(w) != 1 || w[0] != deploymentWarnMCPOAuthRegCap {
		t.Errorf("warnings after the cap was hit: %v", w)
	}
}

func TestMCPOAuthGrantlessRegistrationExpiresAfterADay(t *testing.T) {
	e := newMCPOAuthEnv(t)
	if mcpOAuthClientTTL > 24*time.Hour {
		t.Fatalf("grant-less registration lifetime %v", mcpOAuthClientTTL)
	}
	stale := e.registerClient(loopbackCB)
	kept := e.registerClient(loopbackCB)
	e.connect(kept, loopbackCB, "read")
	e.advance(mcpOAuthClientTTL + time.Hour)
	_ = e.registerClient(loopbackCB) // prunes
	if _, ok, _ := e.mgr.store.GetMCPOAuthClient(context.Background(), stale); ok {
		t.Error("an unused registration outlived the TTL")
	}
	if _, ok, _ := e.mgr.store.GetMCPOAuthClient(context.Background(), kept); !ok {
		t.Error("a registration with a grant was pruned")
	}
}
