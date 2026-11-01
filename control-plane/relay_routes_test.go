// relay_routes_test.go checks the CP's relays to the Workspace Agent against the Agent's own
// route table, and checks that every mutating relay leaves an audit row.
//
// A relay is hand-mirrored from workspace/agent/routes.go, and nothing else notices when the
// Agent renames or drops the route behind it, or when a new POST relay is registered without
// an audit on its relay row: both fail only in front of a member.
//
// Relays are found by behaviour, not by reading routes.go or handler names: every /api route
// of the assembled mux is driven once against a recording fake Agent, and a route whose
// request arrives there with the same method and the path minus /api is a relay (proxy.go
// rest). A relay registered through any wrapper is seen that way. The weak spot is a relay
// whose CP-side checks refuse the probe's dummy request before it is forwarded: it would look
// CP-owned and drop out silently. cpOwnedResolvedRoutes closes that — a route that resolved
// a workspace (the runtime cache is emptied before each route, so resolving one builds a
// runtime through the factory) must either relay or be listed there.
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
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const agentRoutesGoldenPath = "../workspace/agent/testdata/routes.golden"

// minRelays is a floor under the number of relays the probe finds: the relay table's 321 plain
// rows (relays_*.go) and the 13 browser relays alone make 334. A probe that silently stops seeing
// relays must not leave the checks below passing over an empty list.
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

// wrappedRelays are relays registered through something other than a plain rest handler, one
// per wrapper shape. The probe must find each: it is the positive control for the enumeration,
// which a filter on handler names would silently narrow (it loses jsonSameOriginOnly(rest)).
var wrappedRelays = []string{
	"POST /api/sessions/{name}/plan-reviewer",                     // jsonSameOriginOnly(rest)
	"PUT /api/connections/git/{host}",                             // bbReset closure around rest
	"POST /api/chat/conversations/{id}/stream",                    // stream
	"PUT /api/fs/file",                                            // fsFilePut, body checked before the relay
	"POST /api/agents/memory/import",                              // hashed multipart upload
	"POST /api/gcp-login/profiles/{name}/attempts/{attempt}/code", // gcpLoginCode
	"POST /api/browser/pages",                                     // browserAPI.rest
	"POST /api/sessions/{name}/fork",                              // CP-owned handler that forwards unchanged
}

// agentHit is one request the fake Agent received.
type agentHit struct{ method, path string }

// relayProbe is the outcome of driving one CP route once.
type relayProbe struct {
	route    string // "METHOD /api/pattern", as in testdata/routes.golden
	method   string
	pattern  string // the CP pattern without the method
	concrete string // the CP path actually requested
	resolved bool   // a workspace runtime was built while the route ran
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

// countingRuntimeFactory builds stub runtimes pointing at the fake Agent and counts the builds
// made while serving a request. A build from a goroutine a handler left behind (the engine
// admin's catalog notice) is not counted: it can land in the next route's window, and would
// make that route look workspace-resolving at random.
type countingRuntimeFactory struct {
	endpoint string
	built    *atomic.Int64
}

func (f countingRuntimeFactory) New(runtime.Workspace, runtime.SecretKeys, []string) runtime.Runtime {
	buf := make([]byte, 64<<10)
	if bytes.Contains(buf[:goruntime.Stack(buf, false)], []byte("net/http.(*conn).serve")) {
		f.built.Add(1)
	}
	return stubRuntime{endpoint: f.endpoint, token: "tok"}
}

// agentProbeAnswers is what the fake Agent answers for a route instead of 200 {}, where the CP
// audits only an answer that reports a change.
var agentProbeAnswers = map[string]string{
	// oauth_mcp_client.go audits the disconnect only when the Agent says a grant was dropped.
	"DELETE /mcp-servers/x1/oauth": `{"serverId":"x1","issuer":"https://issuer.invalid","hadGrant":true,"revoked":true}`,
	// A sign-in step is audited only when the Agent says it stored the credential.
	"POST /connections/agy/complete":        `{"connected":true}`,
	"POST /connections/claude/complete":     `{"connected":true}`,
	"POST /connections/codex/device/poll":   `{"connected":true}`,
	"POST /connections/cursor/poll":         `{"connected":true}`,
	"POST /connections/kiro/poll":           `{"connected":true}`,
	"POST /connections/muse/poll":           `{"connected":true}`,
	"POST /connections/opencode/oauth/poll": `{"connected":true,"status":"complete"}`,
}

// probeRelays drives every /api route of the full route table once. A workspace resolves to a
// runtime that points at a recording fake Agent, which answers 200 {} unless
// agentProbeAnswers says otherwise.
func probeRelays(t *testing.T) []relayProbe {
	t.Helper()
	return probeRelaysWith(t, nil, agentProbeAnswers)
}

// probeRelaysWith probes the routes only accepts (all when nil), with the fake Agent answering
// from answers.
func probeRelaysWith(t *testing.T, only func(route string) bool, answers map[string]string) []relayProbe {
	t.Helper()
	cfg, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	mgr := cfg.mgr

	var mu sync.Mutex
	var hits []agentHit
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, agentHit{r.Method, r.URL.Path})
		mu.Unlock()
		answer, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			answer = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
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
	var built atomic.Int64
	mgr.rtFactory = countingRuntimeFactory{endpoint: agent.URL, built: &built}
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
		if method == "ANY" || !strings.HasPrefix(pattern, "/api/") || (only != nil && !only(line)) {
			continue
		}
		p := relayProbe{route: line, method: method, pattern: pattern, concrete: concretePath(pattern)}
		// Emptied per route, so a route that resolves the workspace has to build its runtime.
		mgr.mu.Lock()
		delete(mgr.rts, mv.MembershipID)
		mgr.mu.Unlock()
		built.Store(0)
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
		p.resolved = built.Load() > 0
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
	resolving := 0
	for _, p := range probes {
		if p.resolved {
			resolving++
		}
	}
	t.Logf("%d /api routes probed, %d resolved a workspace, %d relays", len(probes), resolving, len(relays))

	t.Run("wrapped relays are found", func(t *testing.T) {
		relayed := map[string]bool{}
		for _, p := range relays {
			relayed[p.route] = true
		}
		for _, r := range wrappedRelays {
			if !relayed[r] {
				t.Errorf("the probe did not find the relay %s", r)
			}
		}
	})

	// The plain relays are registered from the table, so they have a row by construction; a
	// wrapped row is a declaration only, and this is what keeps it from naming a route that does
	// not relay (a typo, or a route that stopped relaying, keeps an audit rule nothing consults).
	t.Run("every table row is a relay", func(t *testing.T) {
		relayed := map[string]bool{}
		for _, p := range relays {
			relayed[p.route] = true
		}
		for route := range relayIndex {
			if !relayed[route] {
				t.Errorf("the relay table lists %s, which the probe did not see relayed", route)
			}
		}
	})

	t.Run("classified", func(t *testing.T) {
		resolved := map[string]bool{}
		for _, p := range probes {
			if !p.resolved {
				continue
			}
			resolved[p.route] = true
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
			if !resolved[r] {
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
				t.Errorf("%s is a mutating relay that writes no audit row: give its relays_*.go row an audit,"+
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
