package main

// Member-facing egress check / propose (docs/log/48 §9). What these pin:
//   - the verdict is the SAME policy the proxy enforces (defaults ∪ active entries),
//   - `configured` follows the proxy wiring, not the token — a deployment with no proxy
//     must not have the Console warning about a restriction it does not have,
//   - a member's write can only ever land as `proposed` and never becomes effective,
//   - a duplicate request is collapsed instead of filling the approval queue.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newEgressMemberFixture(t *testing.T) (*sqlStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st, ctx
}

type checkBody struct {
	Configured bool   `json:"configured"`
	Mode       string `json:"mode"`
	Enforce    bool   `json:"enforce"`
	Hosts      map[string]struct {
		Host     string `json:"host"`
		Allowed  bool   `json:"allowed"`
		Proposed bool   `json:"proposed"`
	} `json:"hosts"`
}

func runCheck(t *testing.T, eg egressAPI, query string) checkBody {
	t.Helper()
	w := httptest.NewRecorder()
	eg.checkHosts(w, httptest.NewRequest("GET", "/api/egress/check?"+query, nil), Identity{}, MembershipView{})
	if w.Code != 200 {
		t.Fatalf("check: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	var out checkBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("check body: %v (%s)", err, w.Body.String())
	}
	return out
}

func TestEgressCheckVerdicts(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	_ = st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: "mcp.internal", State: "active", AddedAt: nowTS()})
	_ = st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: ".wanted.example", State: "proposed", AddedAt: nowTS()})
	// A retired entry is neither allowed nor pending — it must not read as either.
	_ = st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: "old.example", State: "retired", AddedAt: nowTS()})
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", nil)

	got := runCheck(t, eg, "host=mcp.internal&host=api.anthropic.com&host=srv.wanted.example&host=old.example&host=nope.example")
	if !got.Configured || got.Enforce || got.Mode != "log-only" {
		t.Fatalf("deployment state: %+v", got)
	}
	if !got.Hosts["mcp.internal"].Allowed {
		t.Fatalf("db active entry should be allowed: %+v", got.Hosts["mcp.internal"])
	}
	if !got.Hosts["api.anthropic.com"].Allowed {
		t.Fatalf("product default should be allowed: %+v", got.Hosts["api.anthropic.com"])
	}
	// A pending ".suffix" covers its subdomains, matched with the proxy's own rules.
	if v := got.Hosts["srv.wanted.example"]; v.Allowed || !v.Proposed {
		t.Fatalf("pending suffix: %+v", v)
	}
	if v := got.Hosts["old.example"]; v.Allowed || v.Proposed {
		t.Fatalf("retired entry: %+v", v)
	}
	if v := got.Hosts["nope.example"]; v.Allowed || v.Proposed {
		t.Fatalf("unknown host: %+v", v)
	}

	// enforce flips only the wording the Console picks — never the verdict.
	_ = st.SetSetting(ctx, "egress_mode", "enforce")
	got2 := runCheck(t, eg, "host=nope.example")
	if !got2.Enforce || got2.Mode != "enforce" || got2.Hosts["nope.example"].Allowed {
		t.Fatalf("enforce mode: %+v", got2)
	}
}

// The answer is keyed by the string the caller sent, and a URL / host:port reduces to
// the host the proxy would match on.
func TestEgressCheckNormalizesInput(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	_ = st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: "mcp.internal", State: "active", AddedAt: nowTS()})
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", nil)

	got := runCheck(t, eg, "host="+"https%3A%2F%2FMCP.Internal%3A8443%2Fmcp")
	v, ok := got.Hosts["https://MCP.Internal:8443/mcp"]
	if !ok {
		t.Fatalf("verdict must be keyed by the caller's own string: %+v", got.Hosts)
	}
	if v.Host != "mcp.internal" || !v.Allowed {
		t.Fatalf("normalized verdict: %+v", v)
	}
}

// No forward proxy configured => nothing constrains the workspace, and the Console is
// told so rather than warning about a policy this deployment does not apply.
func TestEgressCheckUnconfiguredDeployment(t *testing.T) {
	st, _ := newEgressMemberFixture(t)
	eg := newEgressAPI(&manager{store: st}, "tok", "", nil)
	if got := runCheck(t, eg, "host=nope.example"); got.Configured {
		t.Fatalf("no AF_EGRESS_PROXY_ADDR must report configured=false: %+v", got)
	}
}

func propose(t *testing.T, eg egressAPI, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	eg.propose(w, httptest.NewRequest("POST", "/api/egress/propose", strings.NewReader(body)),
		Identity{ID: "u1", Email: "member@x"}, MembershipView{TenantID: "t1"})
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The whole point of the member face: it can ask, it cannot allow.
func TestEgressProposeNeverBecomesEffective(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", nil)

	code, out := propose(t, eg, `{"entry":"MCP.Example.COM.","reason":"wiki MCP server"}`)
	if code != 200 || out["state"] != "proposed" || out["entry"] != "mcp.example.com" {
		t.Fatalf("propose: %d %+v", code, out)
	}
	eff, _ := st.EffectiveAllowlist(ctx)
	for _, e := range eff {
		if e == "mcp.example.com" {
			t.Fatalf("a member proposal must NOT be effective: %+v", eff)
		}
	}
	rows, _ := st.ListAllowlist(ctx, "proposed", 100)
	if len(rows) != 1 || rows[0].AddedBy != "member@x" || rows[0].Reason != "wiki MCP server" {
		t.Fatalf("proposed row: %+v", rows)
	}
	// The requester's tenant rides on the audit entry, not on the row (approval is
	// deployment-wide, so a tenant on the row would promise scoping that does not exist).
	if rows[0].TenantID != "" {
		t.Fatalf("row must be deployment-global, got tenant %q", rows[0].TenantID)
	}
	al, _ := st.ListAuditByTenant(ctx, "t1", 100)
	n := 0
	for _, a := range al {
		if a.Action == "egress.propose" && a.Target == "mcp.example.com" && a.ActorKind == "user" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one egress.propose audit row for the tenant, got %d (%+v)", n, al)
	}

	// The check now reports it as pending, which is what suppresses a second request.
	if v := runCheck(t, eg, "host=mcp.example.com").Hosts["mcp.example.com"]; v.Allowed || !v.Proposed {
		t.Fatalf("after propose: %+v", v)
	}
}

func TestEgressProposeCollapsesDuplicates(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", nil)

	if code, _ := propose(t, eg, `{"entry":"dup.example"}`); code != 200 {
		t.Fatalf("first: %d", code)
	}
	code, out := propose(t, eg, `{"entry":"dup.example","reason":"again"}`)
	if code != 200 || out["already"] != true || out["state"] != "proposed" {
		t.Fatalf("duplicate: %d %+v", code, out)
	}
	if rows, _ := st.ListAllowlist(ctx, "proposed", 100); len(rows) != 1 {
		t.Fatalf("duplicate must not queue a second row: %+v", rows)
	}

	// Already active: nothing to ask for, and still no new row.
	_ = st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: "live.example", State: "active", AddedAt: nowTS()})
	code, out = propose(t, eg, `{"entry":"live.example"}`)
	if code != 200 || out["state"] != "active" || out["already"] != true {
		t.Fatalf("active entry: %d %+v", code, out)
	}
	if rows, _ := st.ListAllowlist(ctx, "proposed", 100); len(rows) != 1 {
		t.Fatalf("active entry must not queue a request: %+v", rows)
	}

	// A previously rejected (retired) entry may be asked for again — that is the point of
	// asking twice, with a new reason.
	_ = st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: "back.example", State: "retired", AddedAt: nowTS()})
	if code, _ := propose(t, eg, `{"entry":"back.example","reason":"needed after all"}`); code != 200 {
		t.Fatalf("retired re-request: %d", code)
	}
	if rows, _ := st.ListAllowlist(ctx, "proposed", 100); len(rows) != 2 {
		t.Fatalf("retired re-request should queue: %+v", rows)
	}
}

func TestEgressProposeRejectsBadEntries(t *testing.T) {
	st, ctx := newEgressMemberFixture(t)
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", nil)

	for _, tc := range []struct{ entry, code string }{
		{"https://mcp.example.com/mcp", codeEgressEntryInvalid}, // a URL never matches as an entry
		{"mcp.example.com:8443", codeEgressEntryInvalid},        // the proxy strips the port before matching
		{"mcp.example.com/path", codeEgressEntryInvalid},
		{"has space.example", codeEgressEntryInvalid},
		{"a..b", codeEgressEntryInvalid},
		{"", codeEgressEntryInvalid},
		{".com", codeEgressEntryBroad}, // a whole TLD for every workspace
		{"*.com", codeEgressEntryBroad},
	} {
		w := httptest.NewRecorder()
		eg.propose(w, httptest.NewRequest("POST", "/api/egress/propose",
			strings.NewReader(`{"entry":`+quoteJSON(tc.entry)+`}`)), Identity{Email: "m@x"}, MembershipView{})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%q: want 400 got %d (%s)", tc.entry, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%q: want code %s, got %s", tc.entry, tc.code, w.Body.String())
		}
	}
	if rows, _ := st.ListAllowlist(ctx, "", 100); len(rows) != 0 {
		t.Fatalf("no bad entry may be stored: %+v", rows)
	}

	// "*.x" is the wildcard spelling of the suffix form and IS accepted, normalized to
	// the ".x" the policy stores.
	if code, out := propose(t, eg, `{"entry":"*.example.com"}`); code != 200 || out["entry"] != ".example.com" {
		t.Fatalf("wildcard suffix: %d %+v", code, out)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
