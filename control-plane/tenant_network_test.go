package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// --- who is calling (docs/log/66 §66.3) -----------------------------------------

// The whole feature rests on this function, and there is exactly one way to get it
// wrong: read the LEFT of X-Forwarded-For. Proxies append the peer they received
// from, so a client that sends its own X-Forwarded-For lands to the LEFT of the
// entries the trusted hops added — counting from the right ignores it for free.
func TestResolveClientIPCountsFromTheRight(t *testing.T) {
	for _, c := range []struct {
		name   string
		hops   int
		remote string
		xff    []string
		want   string // "" = must not name a client
	}{
		{"no proxy: the peer is the client", 0, "203.0.113.9:51234", nil, "203.0.113.9"},
		{"no proxy: a forged header is not read", 0, "203.0.113.9:51234", []string{"198.51.100.1"}, "203.0.113.9"},
		{"one proxy: rightmost entry", 1, "10.20.10.5:443", []string{"203.0.113.9"}, "203.0.113.9"},
		{"one proxy: a forged prefix is ignored", 1, "10.20.10.5:443", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"one proxy: many forged entries change nothing", 1, "10.20.10.5:443", []string{"1.1.1.1", "2.2.2.2, 203.0.113.9"}, "203.0.113.9"},
		{"two proxies: second from the right", 2, "10.20.10.5:443", []string{"1.2.3.4, 203.0.113.9, 198.51.100.7"}, "203.0.113.9"},
		{"chain shorter than declared: unknown, not a guess", 2, "10.20.10.5:443", []string{"203.0.113.9"}, ""},
		{"no header at all with a proxy declared: unknown", 1, "10.20.10.5:443", nil, ""},
		{"port on the forwarded entry", 1, "10.20.10.5:443", []string{"203.0.113.9:44321"}, "203.0.113.9"},
		{"ipv6", 1, "10.20.10.5:443", []string{"2001:db8::1"}, "2001:db8::1"},
		{"ipv4-mapped ipv6 is unmapped", 1, "10.20.10.5:443", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
		r.RemoteAddr = c.remote
		for _, h := range c.xff {
			r.Header.Add("X-Forwarded-For", h)
		}
		info := resolveClientIP(r, c.hops)
		got := ""
		if info.OK {
			got = info.IP.String()
		}
		if got != c.want {
			t.Errorf("%s: client = %q, want %q", c.name, got, c.want)
		}
	}
}

// The "we cannot name the caller" flag has to survive to the save endpoint, because
// that is where the dangerous misconfiguration is caught: hops=0 behind a proxy makes
// every request look like it comes from the load balancer.
func TestResolveClientIPReportsAForwardingHeaderEvenWhenIgnoringIt(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.20.10.5:443"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	info := resolveClientIP(r, 0)
	if !info.Forwarded || !info.OK || info.IP.String() != "10.20.10.5" {
		t.Fatalf("info = %+v, want the peer address plus Forwarded=true", info)
	}
}

// --- what an administrator may type -----------------------------------------

func TestParseCIDRList(t *testing.T) {
	prefixes, text, aerr := parseCIDRList(" 203.0.113.9 , 198.51.100.0/24, 192.0.2.7/24 , 2001:db8::/32 ")
	if aerr != nil {
		t.Fatalf("parse: %v", aerr)
	}
	// A bare address becomes a single host; a prefix with host bits set is MASKED and
	// the normalized text is what gets stored (and shown back).
	if want := []string{"203.0.113.9/32", "198.51.100.0/24", "192.0.2.0/24", "2001:db8::/32"}; strings.Join(text, ",") != strings.Join(want, ",") {
		t.Fatalf("normalized = %v, want %v", text, want)
	}
	if !ipInAny(netip.MustParseAddr("192.0.2.7"), prefixes) {
		t.Error("the masked prefix must still contain the address it was written from")
	}
	if ipInAny(netip.MustParseAddr("203.0.113.10"), prefixes) {
		t.Error("a single-host entry must not match its neighbours")
	}
	if _, _, aerr := parseCIDRList("192.0.2.0/24, office"); aerr == nil {
		t.Error("a typo must be refused at save time; it is silent afterwards")
	}
}

// --- the gate ---------------------------------------------------------------

// networkFixture: one tenant with a member and a tenant_admin, plus a super_admin.
func networkFixture(t *testing.T) (*sqlStore, *manager, Tenant, MembershipView, Identity) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	member, _ := st.UpsertIdentity(ctx, "yamada@acme.co.jp", "yamada-acme-co-jp", "")
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "tenant_admin")
	if _, err := st.EnsureMembership(ctx, member.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("admin membership: %v", err)
	}
	ms, err := st.ListMemberships(ctx, member.ID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("memberships: %v %+v", err, ms)
	}
	return st, mgr, tn, ms[0], member
}

func ctxWithIP(addr string, forwarded bool) context.Context {
	info := clientIPInfo{Forwarded: forwarded}
	if addr != "" {
		info.IP, info.OK = netip.MustParseAddr(addr), true
	}
	return context.WithValue(context.Background(), clientIPKey{}, info)
}

func TestCheckTenantIP(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, member := networkFixture(t)

	// No rule: every address passes, which is how a tenant that never set one — and
	// every existing deployment — is unaffected.
	if aerr := mgr.checkTenantIP(ctxWithIP("198.51.100.7", true), member, mv); aerr != nil {
		t.Fatalf("no rule must admit everyone: %v", aerr)
	}

	if err := st.SetTenantAllowedCIDRs(ctx, tn.ID, "203.0.113.0/24"); err != nil {
		t.Fatalf("set: %v", err)
	}
	mgr.tenantLogin.invalidate()

	if aerr := mgr.checkTenantIP(ctxWithIP("203.0.113.9", true), member, mv); aerr != nil {
		t.Errorf("an address inside the rule must pass: %v", aerr)
	}
	aerr := mgr.checkTenantIP(ctxWithIP("198.51.100.7", true), member, mv)
	if aerr == nil || aerr.code != "ip_not_allowed" || aerr.status != http.StatusForbidden {
		t.Errorf("an address outside the rule = %+v, want 403 ip_not_allowed", aerr)
	}
	// ⚠️ An address the CP could not work out is a DENIAL. A restriction nobody can
	// evaluate must not be one everybody passes.
	if aerr := mgr.checkTenantIP(ctxWithIP("", true), member, mv); aerr == nil {
		t.Error("an unknown source address must be refused, not admitted")
	}
	// The operator's escape hatch: a tenant that locked itself out is fixable.
	super := Identity{ID: member.ID, Role: "super_admin"}
	if aerr := mgr.checkTenantIP(ctxWithIP("198.51.100.7", true), super, mv); aerr != nil {
		t.Errorf("super_admin must not be subject to a tenant's own rule: %v", aerr)
	}
}

// ⚠️ The PAT path must stay OUT of the gate. Its source address is the caller's own
// Workspace container — MCP and the internal git provider both call from inside it —
// so enforcing here would mean a tenant that allowlists its office silently blocks
// every agent running in its own workspaces (ADR 0047 決定 3).
func TestPATPathIsNotSubjectToTheNetworkRule(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn, mv, member := networkFixture(t)
	mgr.rtFactory = stateOnlyFactory{state: "running"}
	if err := st.SetTenantAllowedCIDRs(ctx, tn.ID, "203.0.113.0/24"); err != nil {
		t.Fatalf("set: %v", err)
	}
	mgr.tenantLogin.invalidate()

	// 10.x is the workspace ENI, nothing like the office range above.
	res, aerr := mgr.resolveByMembership(ctxWithIP("10.20.11.42", false), member.ID, mv.MembershipID)
	if aerr != nil || res == nil {
		t.Fatalf("the PAT path must resolve regardless of the tenant's network rule: %v", aerr)
	}
}

// --- saving the rule: every refusal here is a lockout that would otherwise happen ---

func callNetwork(mgr *manager, method, body string, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/tenants/sales/network", strings.NewReader(body))
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "sales")
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		newAdminAPI(mgr).tenantNetwork(w, r)
	} else {
		newAdminAPI(mgr).setTenantNetwork(w, r)
	}
	return w
}

func TestSetTenantNetworkRefusesToLockTheEditorOut(t *testing.T) {
	_, mgr, _, _, _ := networkFixture(t)

	// forwarded=false: no proxy in the picture, so the only thing wrong is the list.
	w := callNetwork(mgr, http.MethodPut, `{"allowed_cidrs":"203.0.113.0/24"}`, ctxWithIP("198.51.100.7", false))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "would_lock_out") {
		t.Fatalf("saving a rule that excludes the editor = %d %s, want 400 would_lock_out", w.Code, w.Body.String())
	}
	// The refusal has to name the address the CP sees — otherwise the administrator
	// cannot tell a typo from a proxy problem.
	if !strings.Contains(w.Body.String(), "198.51.100.7") {
		t.Errorf("the refusal must say which address was observed: %s", w.Body.String())
	}
}

// The dangerous misconfiguration: a proxy in front, but the deployment never said so.
// Every caller then looks like the load balancer, and allowlisting the address the
// screen shows would admit the whole internet while reading as "restricted".
func TestSetTenantNetworkRefusesWhenTheProxyIsNotDeclared(t *testing.T) {
	_, mgr, _, _, _ := networkFixture(t)
	// hops=0 (the default in tests) AND a forwarding header present.
	w := callNetwork(mgr, http.MethodPut, `{"allowed_cidrs":"10.20.10.5/32"}`, ctxWithIP("10.20.10.5", true))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "proxy_not_configured") {
		t.Fatalf("= %d %s, want 400 proxy_not_configured", w.Code, w.Body.String())
	}
}

// Clearing is the way back, and it must work from anywhere — including from outside
// the range the tenant just locked itself into.
func TestSetTenantNetworkAlwaysAllowsClearing(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	if err := st.SetTenantAllowedCIDRs(context.Background(), tn.ID, "203.0.113.0/24"); err != nil {
		t.Fatalf("set: %v", err)
	}
	w := callNetwork(mgr, http.MethodPut, `{"allowed_cidrs":""}`, ctxWithIP("198.51.100.7", false))
	if w.Code != http.StatusOK {
		t.Fatalf("clearing = %d %s, want 200", w.Code, w.Body.String())
	}
	got, _ := st.GetTenantAllowedCIDRs(context.Background(), tn.ID)
	if got != "" {
		t.Errorf("stored = %q, want empty", got)
	}
}

// What the editing screen is given. "your_ip" is the address a RULE would be matched
// against, not what the browser believes about itself — that distinction is the whole
// reason the screen exists.
func TestTenantNetworkReportsWhatTheCPSees(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	if err := st.SetTenantAllowedCIDRs(context.Background(), tn.ID, "203.0.113.0/24"); err != nil {
		t.Fatalf("set: %v", err)
	}
	w := callNetwork(mgr, http.MethodGet, "", ctxWithIP("203.0.113.9", false))
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out["your_ip"] != "203.0.113.9" || out["allowed_cidrs"] != "203.0.113.0/24" {
		t.Errorf("out = %+v", out)
	}
	if out["editable"] != true || out["reason"] != "" {
		t.Errorf("a deployment that can name its callers must be editable: %+v", out)
	}

	// Behind an undeclared proxy the screen must say so rather than offering a control.
	w = callNetwork(mgr, http.MethodGet, "", ctxWithIP("10.20.10.5", true))
	out = map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["editable"] != false || out["reason"] != "proxy_not_configured" {
		t.Errorf("undeclared proxy must be reported, not hidden: %+v", out)
	}
}

// Saving normalizes, and the stored text is what comes back — an editor told nothing
// would keep believing the rule says what they typed.
func TestSetTenantNetworkStoresTheNormalizedText(t *testing.T) {
	st, mgr, tn, _, _ := networkFixture(t)
	w := callNetwork(mgr, http.MethodPut, `{"allowed_cidrs":"203.0.113.9/24, 198.51.100.7"}`, ctxWithIP("203.0.113.9", false))
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	got, _ := st.GetTenantAllowedCIDRs(context.Background(), tn.ID)
	if got != "203.0.113.0/24,198.51.100.7/32" {
		t.Fatalf("stored = %q, want the masked prefix and the single host", got)
	}
	if !strings.Contains(w.Body.String(), "203.0.113.0/24") {
		t.Errorf("the response must show what was actually stored: %s", w.Body.String())
	}
}
