// relay_routes_test.go checks the CP's relays to the Workspace Agent against the Agent's own
// route table, and checks that every mutating relay leaves an audit row.
//
// A relay is hand-mirrored from workspace/agent/routes.go, and nothing else notices when the
// Agent renames or drops the route behind it, or when a new POST relay is registered without
// an auditActionTarget case: both fail only in front of a member.
//
// Relays are found by behaviour, not by reading routes.go: every /api route of the assembled
// mux whose handler resolves a workspace is driven once against a recording fake Agent, and a
// route whose request arrives there with the same method and the path minus /api is a relay
// (proxy.go rest). A relay registered through any wrapper is seen that way. The weak spot is
// a relay whose CP-side checks refuse the probe's dummy request before it is forwarded: it
// would look CP-owned and drop out silently. cpOwnedResolvedRoutes closes that — every
// workspace-resolving /api route must either relay or be listed there.
//
// The Agent's table is read from workspace/agent/testdata/routes.golden by path; the two Go
// modules stay separate.
package main

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const agentRoutesGoldenPath = "../workspace/agent/testdata/routes.golden"

// minRelays is a floor under the number of relays the probe finds: the `, rest)` registrations
// in routes.go alone were 334 when this was written. A probe that silently stops seeing relays
// must not leave the two checks below passing over an empty list.
const minRelays = 334

// routeGroup is a set of routes that share one reason.
type routeGroup struct {
	reason string
	routes []string
}

// routeReasons flattens groups into route -> reason. A route listed twice is a mistake in the
// list, so it panics rather than letting the later reason win silently.
func routeReasons(groups ...routeGroup) map[string]string {
	out := map[string]string{}
	for _, g := range groups {
		for _, r := range g.routes {
			if _, dup := out[r]; dup {
				panic("route listed twice: " + r)
			}
			out[r] = g.reason
		}
	}
	return out
}

// agentHit is one request the fake Agent received.
type agentHit struct{ method, path string }

// relayProbe is the outcome of driving one CP route once.
type relayProbe struct {
	route    string // "METHOD /api/pattern", as in testdata/routes.golden
	method   string
	pattern  string // the CP pattern without the method
	concrete string // the CP path actually requested
	hits     []agentHit
	audits   []string // audit actions written while the route ran
}

// relayed reports whether the CP forwarded the request as it came: same method, and the path
// with /api stripped.
func (p relayProbe) relayed() bool {
	want := strings.TrimPrefix(p.concrete, "/api")
	for _, h := range p.hits {
		if h.method == p.method && h.path == want {
			return true
		}
	}
	return false
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

var wildcardRE = regexp.MustCompile(`\{([^}]*)\}`)

// concretePath fills a pattern's wildcards with values the CP's own pre-relay checks accept
// (a git host, a port), so a relay is not mistaken for a CP-owned route.
func concretePath(pattern string) string {
	return wildcardRE.ReplaceAllStringFunc(pattern, func(w string) string {
		name := strings.Trim(w, "{}")
		switch {
		case name == "$":
			return ""
		case strings.HasSuffix(name, "..."):
			return "a/b"
		case name == "host":
			return "github.com"
		case name == "port":
			return "3000"
		default:
			return "x1"
		}
	})
}

// shapeOf replaces every wildcard name with {}, so CP and Agent patterns that differ only in
// what they call a segment compare equal.
func shapeOf(pattern string) string {
	return wildcardRE.ReplaceAllStringFunc(pattern, func(w string) string {
		if strings.HasSuffix(w, "...}") {
			return "{...}"
		}
		return "{}"
	})
}

// relayProbeRequest builds the request a route is probed with. A few relays check the body
// before forwarding it; they get one that passes.
func relayProbeRequest(t *testing.T, ctx context.Context, base, method, pattern, concrete string) *http.Request {
	t.Helper()
	body, ctype := []byte("{}"), "application/json"
	if method == http.MethodGet || method == http.MethodHead {
		body = nil
	}
	switch method + " " + pattern {
	case "PUT /api/fs/file":
		body = []byte(cpPutBody("repos/app/a.txt", "x\n"))
	case "POST /api/agents/memory/import":
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "bundle.tar.gz")
		_, _ = fw.Write([]byte("x"))
		_ = mw.Close()
		body, ctype = b.Bytes(), mw.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, method, base+concrete, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("probe request for %s %s: %v", method, pattern, err)
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set(memberOriginConsoleHeader, "1")
	return req
}

// handlerResolvesWorkspace reports whether a route's handler is a withResolved closure: a
// relay needs the workspace's runtime, so no other route can be one.
func handlerResolvesWorkspace(h http.Handler) bool {
	return strings.Contains(runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name(), ".withResolved")
}

// probeRelays drives every workspace-resolving /api route of the full route table once, with
// a resolved workspace whose runtime points at a recording fake Agent that answers 200 {}.
func probeRelays(t *testing.T) []relayProbe {
	t.Helper()
	cfg, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	mgr := cfg.mgr

	var mu sync.Mutex
	var hits []agentHit
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, agentHit{r.Method, r.URL.Path})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(agent.Close)

	ctx := context.Background()
	id := mgr.resolveIdentity(httptest.NewRequest(http.MethodGet, "/", nil))
	_, mv, aerr := mgr.resolveMembership(ctx, id.key, id.email, "")
	if aerr != nil {
		t.Fatalf("resolve membership: %v", aerr)
	}
	ws := store.Workspace{ID: "ws-relay", TenantID: mv.TenantID, MembershipID: mv.MembershipID,
		ContainerName: "relay", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "running", CreatedAt: store.NowTS()}
	if err := mgr.store.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	auditRows := func() map[string]string {
		rows, err := mgr.store.ListAuditByTenant(ctx, mv.TenantID, 100000)
		if err != nil {
			t.Fatalf("list audit: %v", err)
		}
		out := make(map[string]string, len(rows))
		for _, r := range rows {
			out[r.ID] = r.Action
		}
		return out
	}

	// Served over a real connection: the memory upload relay sets read deadlines, which a
	// ResponseRecorder refuses.
	cp := httptest.NewServer(mux)
	t.Cleanup(cp.Close)

	var out []relayProbe
	for _, line := range muxRoutes(t, mux) {
		method, pattern, _ := strings.Cut(line, " ")
		if method == "ANY" || !strings.HasPrefix(pattern, "/api/") {
			continue
		}
		p := relayProbe{route: line, method: method, pattern: pattern, concrete: concretePath(pattern)}
		if h, _ := mux.Handler(httptest.NewRequest(method, p.concrete, nil)); !handlerResolvesWorkspace(h) {
			continue
		}
		// Re-seeded per route: a CP-owned handler may evict the runtime cache, and a miss would
		// reach for a container factory the smoke manager has none of.
		mgr.mu.Lock()
		mgr.rts[mv.MembershipID] = cachedRT{rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: ws}
		mgr.mu.Unlock()
		mu.Lock()
		hits = nil
		mu.Unlock()
		before := auditRows()

		// The bound is for CP-owned streams (GET /api/events), which hold the request open.
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if resp, err := cp.Client().Do(relayProbeRequest(t, rctx, cp.URL, method, pattern, p.concrete)); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		cancel()

		mu.Lock()
		p.hits = append([]agentHit(nil), hits...)
		mu.Unlock()
		for id, action := range auditRows() {
			if _, seen := before[id]; !seen {
				p.audits = append(p.audits, action)
			}
		}
		out = append(out, p)
	}
	return out
}

// agentRouteMux builds a ServeMux from the Agent's route golden, so a relayed request is
// matched by the same rules the Agent applies.
func agentRouteMux(t *testing.T, lines []string) *http.ServeMux {
	t.Helper()
	m := http.NewServeMux()
	nop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, ln := range lines {
		method, path, ok := strings.Cut(ln, " ")
		if !ok {
			t.Fatalf("agent golden line %q is not METHOD PATH", ln)
		}
		if method == "ANY" {
			m.Handle(path, nop)
		} else {
			m.Handle(method+" "+path, nop)
		}
	}
	return m
}

// relaysMissingFromAgent returns, for each relay, why the Agent would not serve it: no Agent
// route matches the forwarded request, or the one that matches has another shape than the CP
// pattern (the relay then lands on a different handler than the one it mirrors).
func relaysMissingFromAgent(t *testing.T, probes []relayProbe, agentLines []string) []string {
	t.Helper()
	am := agentRouteMux(t, agentLines)
	var bad []string
	for _, p := range probes {
		if !p.relayed() {
			continue
		}
		agentPath := strings.TrimPrefix(p.concrete, "/api")
		_, got := am.Handler(httptest.NewRequest(p.method, agentPath, nil))
		want := p.method + " " + strings.TrimPrefix(p.pattern, "/api")
		switch {
		case got == "":
			bad = append(bad, p.route+": no Agent route serves "+p.method+" "+agentPath)
		case shapeOf(got) != shapeOf(want):
			bad = append(bad, p.route+": the Agent serves it as "+got)
		}
	}
	return bad
}

func TestCPRelaysExistOnAgentAndMutationsAreAudited(t *testing.T) {
	probes := probeRelays(t)
	agentLines := readGoldenLines(t, agentRoutesGoldenPath)

	var relays []relayProbe
	for _, p := range probes {
		if p.relayed() {
			relays = append(relays, p)
		}
	}
	if len(relays) < minRelays {
		t.Fatalf("the probe found %d relays, fewer than the floor of %d: the probe broke, or relays"+
			" were removed on purpose (then lower minRelays)", len(relays), minRelays)
	}
	t.Logf("%d workspace-resolving /api routes probed, %d relays", len(probes), len(relays))

	t.Run("classified", func(t *testing.T) {
		probed := map[string]bool{}
		for _, p := range probes {
			probed[p.route] = true
			_, listed := cpOwnedResolvedRoutes[p.route]
			switch {
			case p.relayed() && listed:
				t.Errorf("%s relays to the Agent but is listed in cpOwnedResolvedRoutes: remove it", p.route)
			case !p.relayed() && !listed:
				t.Errorf("%s resolves a workspace but was not relayed (Agent saw %v): if it is a relay, make"+
					" relayProbeRequest pass its pre-relay checks; if the CP owns it, list it in"+
					" cpOwnedResolvedRoutes", p.route, p.hits)
			}
		}
		for r := range cpOwnedResolvedRoutes {
			if !probed[r] {
				t.Errorf("cpOwnedResolvedRoutes lists %s, which is not a workspace-resolving /api route", r)
			}
		}
	})

	t.Run("agent route exists", func(t *testing.T) {
		for _, b := range relaysMissingFromAgent(t, probes, agentLines) {
			t.Error(b)
		}
		// Every other call a route made to the Agent is checked too (a CP-owned handler's own
		// calls), as far as the probe's dummy request reaches them.
		am := agentRouteMux(t, agentLines)
		for _, p := range probes {
			for _, h := range p.hits {
				if h.method == p.method && h.path == strings.TrimPrefix(p.concrete, "/api") {
					continue // the relay itself, reported above
				}
				if _, got := am.Handler(httptest.NewRequest(h.method, h.path, nil)); got == "" {
					t.Errorf("%s called the Agent on %s %s, which no Agent route serves", p.route, h.method, h.path)
				}
			}
		}
	})

	t.Run("mutating relays are audited", func(t *testing.T) {
		relayed := map[string]bool{}
		for _, p := range relays {
			relayed[p.route] = true
			if !isMutating(p.method) {
				continue
			}
			_, exempt := mutatingRelayAuditExempt[p.route]
			switch {
			case len(p.audits) == 0 && !exempt:
				t.Errorf("%s is a mutating relay that writes no audit row: add an auditActionTarget case,"+
					" or list it in mutatingRelayAuditExempt with the reason", p.route)
			case len(p.audits) > 0 && exempt:
				t.Errorf("%s is audited (%v) but listed in mutatingRelayAuditExempt: remove it", p.route, p.audits)
			}
		}
		for r := range mutatingRelayAuditExempt {
			if m, _, _ := strings.Cut(r, " "); !relayed[r] || !isMutating(m) {
				t.Errorf("mutatingRelayAuditExempt lists %s, which is not a mutating relay", r)
			}
		}
	})
}

// TestRelaysMissingFromAgentSeesADroppedRoute is the check's own positive control: with one
// relayed route deleted from a copy of the Agent golden, that relay is reported.
func TestRelaysMissingFromAgentSeesADroppedRoute(t *testing.T) {
	agentLines := readGoldenLines(t, agentRoutesGoldenPath)
	probes := []relayProbe{{
		route: "POST /api/sessions/{name}/stop", method: http.MethodPost,
		pattern: "/api/sessions/{name}/stop", concrete: "/api/sessions/x1/stop",
		hits: []agentHit{{http.MethodPost, "/sessions/x1/stop"}},
	}}
	if bad := relaysMissingFromAgent(t, probes, agentLines); len(bad) != 0 {
		t.Fatalf("with the full golden: %v", bad)
	}
	var dropped []string
	for _, ln := range agentLines {
		if ln != "POST /sessions/{name}/stop" {
			dropped = append(dropped, ln)
		}
	}
	if len(dropped) != len(agentLines)-1 {
		t.Fatalf("the Agent golden has no POST /sessions/{name}/stop line to drop")
	}
	if bad := relaysMissingFromAgent(t, probes, dropped); len(bad) != 1 {
		t.Fatalf("with the route dropped, got %v, want the relay reported", bad)
	}
}
