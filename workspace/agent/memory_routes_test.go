package main

// The two tests of the memory family that stay in package main.
//
// The family itself lives in internal/memoryx, but moving these two would leave them measuring
// nothing: neither touches an unexported memoryx symbol (so keeping them here demands no extra
// exports), and what they check can only be checked against the real route table — that the
// ten memory routes do not swallow the existing pattern route `/agents/{kind}/models`. The mux
// memoryx builds for itself (internal/memoryx/mux_test.go) has no `{kind}` route, so the claim
// would disappear on the way over.
//
// The other side — that memoryx's test mux has not drifted from routes.go — is covered by
// TestMemoryRoutesMatchAgentRouteTable in internal/memoryx/mux_test.go.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

func TestMemoryRoutesRegistered(t *testing.T) {
	mux := buildMux()
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/agents/memory/roots", "GET /agents/memory/roots"},
		{"GET", "/agents/memory/snapshots", "GET /agents/memory/snapshots"},
		{"POST", "/agents/memory/snapshots", "POST /agents/memory/snapshots"},
		{"GET", "/agents/memory/diff", "GET /agents/memory/diff"},
		{"GET", "/agents/memory/tree", "GET /agents/memory/tree"},
		{"POST", "/agents/memory/restore", "POST /agents/memory/restore"},
		{"PUT", "/agents/memory/settings", "PUT /agents/memory/settings"},
		{"GET", "/agents/memory/export", "GET /agents/memory/export"},
		{"POST", "/agents/memory/import", "POST /agents/memory/import"},
		{"POST", "/agents/memory/import/apply", "POST /agents/memory/import/apply"},
		// The existing pattern route still coexists: it is not swallowed by {kind}.
		{"GET", "/agents/codex/models", "GET /agents/{kind}/models"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}

func TestMemoryP2RoutesRegistered(t *testing.T) {
	mux := buildMux()
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/agents/memory/tree", "GET /agents/memory/tree"},
		{"POST", "/agents/memory/restore", "POST /agents/memory/restore"},
		{"PUT", "/agents/memory/settings", "PUT /agents/memory/settings"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}

// The member writes are wrapped in the real route table, not only in memoryx's copy: AGENT_TOKEN
// and the relay hint alone are refused before any handler runs (ADR 0113).
func TestMemberWriteRoutesRequireTheMark(t *testing.T) {
	memberorigintest.Install(t)
	mux := buildMux()
	for _, p := range []string{
		"/agents/memory/entries/pin",
		"/agents/memory/entries/revert",
		"/agents/memory/entries/member-save",
		"/agents/memory/entries/purge",
		"/agents/memory/entries/project-move",
		"/agents/memory/snapshots",
		"/agents/memory/import/apply",
		"/agents/memory-review/run",
		"/agents/memory-review/proposals/list",
		"/agents/memory-review/proposals/approve",
		"/agents/memory-review/proposals/reject",
		"/agents/memory-review/proposals/reject-all",
		"/agents/memory-review/runs/revert",
	} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer agent-token")
		req.Header.Set("X-AF-Relay", "cp")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), memberorigin.CodeRequired) {
			t.Errorf("%s without a mark: %d %s", p, w.Code, w.Body)
		}
	}
}

// The other half: a request the CP marked gets through the real route table to the handler. Without
// memberorigin.Require on a route the handler's own check refuses it, so the unmarked test above
// stays green; this one does not. An empty body is a validation error past the mark.
func TestMemberWriteRoutesAdmitAMarkedRequest(t *testing.T) {
	s := memberorigintest.Install(t)
	mux := buildMux()
	for _, p := range []string{
		"/agents/memory/entries/pin",
		"/agents/memory/entries/revert",
		"/agents/memory/entries/member-save",
		"/agents/memory/entries/purge",
		"/agents/memory/entries/project-move",
		"/agents/memory/import/apply",
		"/agents/memory/snapshots",
		"/agents/memory-review/proposals/list",
		"/agents/memory-review/proposals/approve",
		"/agents/memory-review/proposals/reject",
		"/agents/memory-review/proposals/reject-all",
		"/agents/memory-review/runs/revert",
	} {
		// reject-all with an empty body would reject the whole queue; a malformed run id is
		// the validation error that proves the request got past the mark without touching it.
		body := `{}`
		if p == "/agents/memory-review/proposals/reject-all" {
			body = `{"run":"nope"}`
		}
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer agent-token")
		req.Header.Set("Content-Type", "application/json")
		s.Sign(req, []byte(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		// A snapshot of an empty body is no validation error: it may succeed or fail on the
		// store, but never on the mark.
		if p == "/agents/memory/snapshots" {
			if w.Code == http.StatusForbidden || strings.Contains(w.Body.String(), "member_origin") {
				t.Errorf("%s with a mark: %d %s", p, w.Code, w.Body)
			}
			continue
		}
		// The queue list takes no input: it answers 200 past the mark.
		want := http.StatusBadRequest
		if p == "/agents/memory-review/proposals/list" {
			want = http.StatusOK
		}
		if w.Code != want || strings.Contains(w.Body.String(), "member_origin") {
			t.Errorf("%s with a mark: %d %s", p, w.Code, w.Body)
		}
	}
}

// The CP signs exactly the routes the Agent requires a mark on. A route the Agent requires but the CP
// does not sign locks the member out; one the CP signs but the Agent leaves open signs for nothing.
// The two modules cannot import each other, so this reads the CP's table from its parsed source and finds
// the Agent's by asking the real mux: a POST with an unparsable body gets member_origin_required
// from a wrapped route and a 400 from the handler of an open one, without touching the store.
func TestMemberOriginRoutesAgreeWithTheControlPlane(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../control-plane/member_origin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Read the map literal's real elements: a commented-out or false entry is not signed.
	var cp []string
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "memberOriginRoutes" || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, _ := kv.Key.(*ast.BasicLit)
			v, _ := kv.Value.(*ast.Ident)
			if k == nil || v == nil || v.Name != "true" {
				continue
			}
			route, err := strconv.Unquote(k.Value)
			if err != nil {
				t.Fatal(err)
			}
			cp = append(cp, strings.Replace(route, " /api/", " /", 1))
		}
		return false
	})
	if len(cp) == 0 {
		t.Fatal("no route parsed from memberOriginRoutes")
	}

	golden, err := os.ReadFile("testdata/routes.golden")
	if err != nil {
		t.Fatal(err)
	}
	memberorigintest.Install(t)
	mux := buildMux()
	var agent []string
	for _, line := range strings.Split(string(golden), "\n") {
		method, path, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || method != http.MethodPost || !(strings.HasPrefix(path, "/agents/memory/") || strings.HasPrefix(path, "/agents/memory-review/")) {
			continue
		}
		req := httptest.NewRequest(method, path, strings.NewReader("not json"))
		req.Header.Set("Authorization", "Bearer agent-token")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if strings.Contains(w.Body.String(), memberorigin.CodeRequired) {
			agent = append(agent, method+" "+path)
		}
	}
	// A conditional requirement is not a route that always wants a mark: the settings write asks
	// for one only when it arms the memory review (TestAPrefsWriteThatArmsTheMemoryReview...).
	cp = slices.DeleteFunc(cp, func(r string) bool { return r == "PUT /env/ui-prefs" })
	sort.Strings(cp)
	sort.Strings(agent)
	if strings.Join(cp, "\n") != strings.Join(agent, "\n") {
		t.Fatalf("the CP signs\n%s\nthe Agent requires a mark on\n%s", strings.Join(cp, "\n"), strings.Join(agent, "\n"))
	}
}

// import/apply names every selected slug, so its body can pass the save limit. Through the real
// route table a marked body above that limit and within the CP's 2 MiB cap must reach the handler,
// which refuses the unknown import with a 400.
func TestImportApplyTakesALargeMarkedSelection(t *testing.T) {
	s := memberorigintest.Install(t)
	slugs := make([]string, 5300)
	for i := range slugs {
		slugs[i] = strings.Repeat("a", 255)
	}
	list, _ := json.Marshal(slugs)
	body := `{"importId":"x","scope":{"projects":` + string(list) + `}}`
	if len(body) <= memoryx.MemberWriteMaxRequest || len(body) > 2<<20 {
		t.Fatalf("body %d is not between the save limit and 2 MiB", len(body))
	}
	req := httptest.NewRequest(http.MethodPost, "/agents/memory/import/apply", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer agent-token")
	req.Header.Set("Content-Type", "application/json")
	s.Sign(req, []byte(body))
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "member_origin") {
		t.Fatalf("large marked apply: %d %.200s, want the handler's 400", w.Code, w.Body)
	}
}
