package routegw

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const (
	provBinding = "src1#0123456789abcdef"
	provModel   = "us.anthropic.claude-opus-5-5"
)

// fakeCP is the Control Plane's check and token endpoints; the route gateway itself is the env's
// upstream server, which Base points at.
type fakeCP struct {
	mu       sync.Mutex
	base     string
	usable   CPUsable
	checkRef *refusal
	mintRef  *refusal
	checks   int
	mints    []string // session|incarnation|binding of each mint
	exp      time.Time
	n        int
}

func (f *fakeCP) Base() string { f.mu.Lock(); defer f.mu.Unlock(); return f.base }

func (f *fakeCP) Check(context.Context, string) (CPUsable, *refusal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.usable, f.checkRef
}

func (f *fakeCP) Mint(_ context.Context, session, incarnation, _, binding string) (CPToken, *refusal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mintRef != nil {
		return CPToken{}, f.mintRef
	}
	f.n++
	f.mints = append(f.mints, session+"|"+incarnation+"|"+binding)
	return CPToken{Token: "afr_cp" + string(rune('0'+f.n)), ExpiresAt: f.exp.Format(time.RFC3339), BasePath: "/route/v1",
		CallHeader: cpCallHeader}, nil
}

func (f *fakeCP) mintCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.mints) }

func newProvEnv(t *testing.T) (*env, *fakeCP) {
	t.Helper()
	e := newEnv(t)
	cp := &fakeCP{base: e.up.URL, usable: CPUsable{Route: RouteBedrockDeployment, Binding: provBinding, Region: "us-east-1",
		Models: []string{provModel}, RemainingMicro: 5_000_000}, exp: e.now.Add(10 * time.Minute)}
	e.g.d.CP = cp
	return e, cp
}

// provSession registers a session on the deployment's route, bound as a create binds it.
func (e *env) provSession(name string) session.Meta {
	e.t.Helper()
	sel, ref := e.g.Bind(routes.Selection{Route: RouteBedrockDeployment})
	if ref != nil {
		e.t.Fatalf("bind: %+v", ref)
	}
	m := session.Meta{Name: name, Dir: "/work/" + name, CreatedAt: "2026-11-05T08:00:00Z", Kind: session.KindClaude,
		Model: provModel, Route: &sel}
	e.mu.Lock()
	e.metas[name] = m
	e.roots[name] = []int{os.Getpid()}
	e.mu.Unlock()
	return m
}

func provPath(model string) string { return "/model/" + model + "/invoke" }

// A call on the deployment's route goes to the CP's route gateway with the CP token, unsigned,
// and its ledger row carries the CP's call id under the provided route.
func TestProvidedCallIsForwardedToTheCP(t *testing.T) {
	e, cp := newProvEnv(t)
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(cpCallHeader, "cpcall-1")
		w.Header().Set("X-Amzn-Bedrock-Input-Token-Count", "11")
		w.Header().Set("X-Amzn-Bedrock-Output-Token-Count", "7")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}]}`))
	}
	m := e.provSession("s1")
	if m.Route.Connection != provBinding {
		t.Fatalf("bound to %q", m.Route.Connection)
	}
	tok := e.issue(m)
	resp, body := e.call(tok, provPath(provModel), `{"max_tokens":10}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"hi"`) {
		t.Fatalf("answer = %d %s", resp.StatusCode, body)
	}
	e.mu.Lock()
	req := e.upReqs[0]
	e.mu.Unlock()
	if req.URL.Path != "/route/v1"+provPath(provModel) || req.Header.Get("Authorization") != "Bearer afr_cp1" ||
		req.Header.Get("X-Amz-Date") != "" || strings.Contains(req.Header.Get("Authorization"), tok) {
		t.Fatalf("forwarded %s %v", req.URL.Path, req.Header)
	}
	if _, credN := e.counts(); credN != 0 {
		t.Fatalf("a provided call resolved %d member credentials", credN)
	}
	if got := cp.mints[0]; got != "s1|"+awsx.Incarnation(m)+"|"+provBinding {
		t.Fatalf("minted for %q", got)
	}
	rows := e.waitRows(1)
	if r := rows[0]; r.Route != usagex.RouteBedrockDeployment || r.Call != "cpcall-1" || r.Billing != usagex.BillingMetered ||
		r.In != 11 || r.Out != 7 || !r.OK {
		t.Fatalf("ledger row = %+v", r)
	}
}

// What the Agent still holds on a provided route: the model pin and the caller's process tree.
// Neither reaches the CP when it refuses.
func TestProvidedCallKeepsTheSessionBinding(t *testing.T) {
	e, cp := newProvEnv(t)
	tok := e.issue(e.provSession("s1"))
	resp, body := e.call(tok, provPath("us.anthropic.claude-sonnet-5-5"), `{}`)
	if resp.StatusCode != 404 || codeOf(t, body) != "route_model_not_selected" {
		t.Fatalf("another model: %d %s", resp.StatusCode, body)
	}
	e.mu.Lock()
	e.roots["s1"] = []int{1}
	e.mu.Unlock()
	if resp, body := e.call(tok, provPath(provModel), `{}`); resp.StatusCode != 401 || codeOf(t, body) != "route_caller_forbidden" {
		t.Fatalf("another tree: %d %s", resp.StatusCode, body)
	}
	if e.upstreamCalls() != 0 || cp.mintCount() != 1 {
		t.Fatalf("a refused call reached the CP (%d calls, %d mints)", e.upstreamCalls(), cp.mintCount())
	}
	// The session moved to another route: the token ends.
	e.mu.Lock()
	m := e.metas["s1"]
	m.Route = &routes.Selection{Route: RouteBedrock, Connection: "p1@"}
	e.metas["s1"] = m
	e.roots["s1"] = []int{os.Getpid()}
	e.mu.Unlock()
	if resp, body := e.call(tok, provPath(provModel), `{}`); resp.StatusCode != 401 || codeOf(t, body) != "route_selection_changed" {
		t.Fatalf("selection changed: %d %s", resp.StatusCode, body)
	}
}

// The CP's budget refusal reaches the harness as the CP wrote it: 503, x-should-retry false, the
// message naming the spend limit. The ledger row is a failed call.
func TestProvidedBudgetRefusalPassesThrough(t *testing.T) {
	e, _ := newProvEnv(t)
	const msg = "you have hit your monthly budget, the spend limit of the deployment's Bedrock route: ≈$5.00 of $5.00 used in 2026-11; it resets on 2026-12-01 00:00 UTC"
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Errortype", "route_budget_exhausted")
		w.Header().Set("X-Should-Retry", "false")
		w.Header().Set("X-Af-Route-Budget-Level", "member_budget")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"` + msg + `","code":"route_budget_exhausted"}`))
	}
	tok := e.issue(e.provSession("s1"))
	resp, body := e.call(tok, provPath(provModel), `{}`)
	if resp.StatusCode != 503 || resp.Header.Get("X-Should-Retry") != "false" || !strings.Contains(body, "spend limit") ||
		resp.Header.Get("X-Af-Route-Budget-Level") != "" {
		t.Fatalf("budget refusal: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	// Never admitted, nothing reserved or billed: no ledger row (#3683). An admitted call that
	// Bedrock refused carries the CP's call id and is recorded as failed.
	e.upFn = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(cpCallHeader, "cpcall-429")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Too many requests"}`))
	}
	if resp, _ := e.call(tok, provPath(provModel), `{}`); resp.StatusCode != 429 {
		t.Fatalf("throttle: %d", resp.StatusCode)
	}
	rows := e.waitRows(1)
	if len(rows) != 1 || rows[0].Call != "cpcall-429" || rows[0].OK || rows[0].Route != usagex.RouteBedrockDeployment {
		t.Fatalf("ledger rows = %+v, want only the admitted call's", rows)
	}
}

// Bind, check and launch ask the CP; what it refuses is refused here, a binding it has moved on
// from is never followed, and a model off the source's list is refused at create.
func TestProvidedBindCheckAndIssue(t *testing.T) {
	e, cp := newProvEnv(t)
	if _, ref := e.g.Bind(routes.Selection{Route: RouteBedrockDeployment, Connection: "src1#ffffffffffffffff"}); ref == nil ||
		ref.Code != "route_connection_changed" {
		t.Fatalf("stale binding bound: %+v", ref)
	}
	sel := agents.RouteSelection{Route: RouteBedrockDeployment, Connection: provBinding}
	if ref := e.g.CheckRoute(context.Background(), sel, "us.anthropic.claude-haiku-5-5"); ref == nil || ref.Code != routes.CodeRouteModel {
		t.Fatalf("model off the list: %+v", ref)
	}
	if ref := e.g.CheckRoute(context.Background(), sel, provModel); ref != nil {
		t.Fatalf("usable: %+v", ref)
	}
	m := e.provSession("s1")
	cp.mu.Lock()
	cp.checkRef = &refusal{http.StatusForbidden, "route_not_granted", "this tenant is not granted the deployment's Bedrock route"}
	cp.mu.Unlock()
	if _, ref := e.g.Issue(m); ref == nil || ref.Code != "route_not_granted" || ref.Status != http.StatusForbidden {
		t.Fatalf("launch after the grant went: %+v", ref)
	}
	if ref := e.g.CheckRoute(context.Background(), sel, provModel); ref == nil || ref.Code != "route_not_granted" {
		t.Fatalf("check after the grant went: %+v", ref)
	}
	// The offer reads the cached answer, then asks again once it is stale.
	n := cp.checks
	if e.g.Offered(RouteBedrockDeployment) || cp.checks != n {
		t.Fatalf("offered on a refusal (%d checks, was %d)", cp.checks, n)
	}
	cp.mu.Lock()
	cp.checkRef = nil
	cp.mu.Unlock()
	e.advance(checkFresh)
	if !e.g.Offered(RouteBedrockDeployment) {
		t.Fatal("not offered once the CP allows it again")
	}
	e.g.d.CP = nil
	if _, ref := e.g.Bind(routes.Selection{Route: RouteBedrockDeployment}); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Fatalf("no CP: %+v", ref)
	}
}

// The sweep renews a running session's CP token before it runs out, and forgets the token of a
// session whose gateway token is gone. A call on an expired one mints a new one.
func TestProvidedTokenRenewal(t *testing.T) {
	e, cp := newProvEnv(t)
	m := e.provSession("s1")
	tok := e.issue(m)
	e.run(4 * time.Minute)
	if cp.mintCount() != 1 {
		t.Fatalf("renewed early: %d mints", cp.mintCount())
	}
	cp.mu.Lock()
	cp.exp = e.now.Add(20 * time.Minute)
	cp.mu.Unlock()
	e.run(2 * time.Minute)
	if cp.mintCount() != 2 {
		t.Fatalf("not renewed inside %v of its expiry: %d mints", cpRenewBefore, cp.mintCount())
	}
	if resp, _ := e.call(tok, provPath(provModel), `{}`); resp.StatusCode != 200 {
		t.Fatalf("call after renewal: %d", resp.StatusCode)
	}
	e.mu.Lock()
	got := e.upReqs[0].Header.Get("Authorization")
	e.mu.Unlock()
	if got != "Bearer afr_cp2" {
		t.Fatalf("the call used %q, not the renewed token", got)
	}
	// Ended: the sweep drops the gateway token and then the CP token.
	e.mu.Lock()
	delete(e.metas, "s1")
	e.mu.Unlock()
	e.g.Sweep()
	e.g.prov.mu.Lock()
	left := len(e.g.prov.toks)
	e.g.prov.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d CP tokens kept for an ended session", left)
	}
	// A held token past its time is replaced at the call.
	m2 := e.provSession("s2")
	tok2 := e.issue(m2)
	cp.mu.Lock()
	cp.exp = e.now.Add(10 * time.Minute)
	cp.mu.Unlock()
	e.g.prov.mu.Lock()
	h := e.g.prov.toks["s2"]
	h.tok.ExpiresAt = e.now.Add(30 * time.Second).Format(time.RFC3339)
	e.g.prov.toks["s2"] = h
	e.g.prov.mu.Unlock()
	before := cp.mintCount()
	if resp, _ := e.call(tok2, provPath(provModel), `{}`); resp.StatusCode != 200 || cp.mintCount() != before+1 {
		t.Fatalf("expiring token not replaced at the call: %d, mints %d→%d", resp.StatusCode, before, cp.mintCount())
	}
}

// record never writes an empty call id: spool dedup keys on it, so two such rows would collapse.
// forward records only answers that carry the CP's id; record holds without that guard too.
func TestProvidedRecordAlwaysHasACallID(t *testing.T) {
	e, _ := newProvEnv(t)
	c := &call{b: Binding{Session: "s1"}, provided: true}
	for _, id := range []string{"", "not an id!"} {
		resp := &http.Response{StatusCode: 200, Header: http.Header{cpCallHeader: {id}}}
		e.g.record(c, provModel, "invoke", resp, newUsageObserver("invoke"), true, e.now)
	}
	rows := e.waitRows(2)
	if len(rows) != 2 || rows[0].Call == "" || rows[1].Call == "" || rows[0].Call == rows[1].Call || rows[0].Key == rows[1].Key {
		t.Fatalf("rows = %+v", rows)
	}
}
