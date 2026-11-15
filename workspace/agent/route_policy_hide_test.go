package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// useHidingPolicy makes the listing endpoints hide by a store that holds p.
func useHidingPolicy(t *testing.T, p routepolicy.Policy) {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })
}

// apiKeysOnly allows the two vendor API routes and nothing else.
var apiKeysOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI, routes.RouteOpenAIAPI}}

func liveKinds(t *testing.T) map[string]kindEntryWire {
	t.Helper()
	rec := httptest.NewRecorder()
	handleAgentKinds(rec, httptest.NewRequest(http.MethodGet, "/agents/kinds", nil))
	var got kindCatalogWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	by := map[string]kindEntryWire{}
	for _, e := range got.Kinds {
		by[e.Kind] = e
	}
	return by
}

// The live kind catalog hides what the policy forbids (ADR 0138 D10a): a harness none of whose
// routes is allowed is absent, one with some lists only those, and a kind that runs no model is
// untouched. With no restriction the catalog is unchanged.
func TestKindCatalogHidesWhatThePolicyForbids(t *testing.T) {
	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if all := liveKinds(t); len(all) != len(sessionx.RegisteredKinds()) {
		t.Fatalf("an open policy hid kinds: %d of %d", len(all), len(sessionx.RegisteredKinds()))
	}
	useHidingPolicy(t, apiKeysOnly)
	by := liveKinds(t)
	for _, k := range []string{session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro, session.KindMuse, session.KindLcpp} {
		if _, ok := by[k]; ok {
			t.Errorf("%s is listed although none of its routes is allowed", k)
		}
	}
	for _, k := range []string{session.KindShell, session.KindSSM} {
		if _, ok := by[k]; !ok {
			t.Errorf("%s, which runs no model, was hidden", k)
		}
	}
	if c := by[session.KindClaude]; !slices.Equal(c.Routes, []string{routes.RouteAnthropicAPI}) || len(c.FixedRoutes) != 0 {
		t.Errorf("claude lists %v / fixed %v, want only anthropic-api", c.Routes, c.FixedRoutes)
	}
	if c := by[session.KindCodex]; !slices.Equal(c.FixedRoutes, []string{routes.RouteOpenAIAPI}) {
		t.Errorf("codex lists fixed %v, want only openai-api", c.FixedRoutes)
	}
	for k, e := range by {
		if e.UsableRoutes != nil {
			for _, r := range *e.UsableRoutes {
				if !apiKeysOnly.Allows(r) {
					t.Errorf("%s lists usable route %s, which the policy forbids", k, r)
				}
			}
		}
	}
}

// GET /connections has no entry for a harness the policy forbids altogether; the GitHub entry,
// which also serves git, stays.
func TestConnectionsHideForbiddenHarnesses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	get := func() map[string]json.RawMessage {
		w := httptest.NewRecorder()
		handleConnectionsGet(w, httptest.NewRequest(http.MethodGet, "/connections", nil))
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
		return out
	}
	before := get()
	if _, ok := before[session.KindAgy]; !ok {
		t.Fatal("control: agy has no entry without a policy")
	}
	useHidingPolicy(t, apiKeysOnly)
	after := get()
	if _, ok := after[session.KindAgy]; ok {
		t.Error("agy's entry is listed although agy-login is forbidden")
	}
	for _, k := range []string{"github", session.KindClaude, session.KindCodex} {
		if _, ok := after[k]; !ok && before[k] != nil {
			t.Errorf("%s's entry was hidden", k)
		}
	}
}

// perCallLister is a kind whose route follows its model, with a fixed catalog.
type perCallLister struct {
	agents.Agent
	routeOf map[string]string
}

func (a perCallLister) ListModels(agents.ModelQuery) agents.ModelListing {
	var l agents.ModelListing
	for _, id := range []string{"anthropic/x", "opencode/free", "mystery/y"} {
		l.Models = append(l.Models, agents.ModelChoice{ID: id, Label: id})
	}
	l.Enumerated = len(l.Models)
	return l
}
func (a perCallLister) ResolveLaunch(m session.Meta) routes.Launch {
	return routes.Launch{Route: a.routeOf[m.Model]}
}
func (perCallLister) UsableRoutes() []string { return nil }
func (perCallLister) RoutesPerCall()         {}
func (perCallLister) FixedRoutes() []string {
	return []string{routes.RouteAnthropicAPI, routes.RouteOpencodeZen}
}

func agentModels(t *testing.T, kind string) (ids []string, reason string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/agents/"+kind+"/models", nil)
	req.SetPathValue("kind", kind)
	rec := httptest.NewRecorder()
	handleAgentModels(rec, req)
	var got struct {
		Models []agents.ModelChoice `json:"models"`
		Reason string               `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	for _, m := range got.Models {
		ids = append(ids, m.ID)
	}
	return ids, got.Reason
}

// A model menu offers what the policy lets the kind launch: nothing for a forbidden harness (and
// says why), and for a kind whose route follows its model, the models whose route is allowed.
func TestModelMenusHideWhatThePolicyForbids(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{}}`)
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, perCallLister{Agent: sessionx.AgentOf(session.KindOpencode),
		routeOf: map[string]string{"anthropic/x": routes.RouteAnthropicAPI, "opencode/free": routes.RouteOpencodeZen}}))
	if ids, _ := agentModels(t, session.KindOpencode); len(ids) != 3 {
		t.Fatalf("control: without a policy the menu is %v", ids)
	}
	useHidingPolicy(t, apiKeysOnly)
	if ids, _ := agentModels(t, session.KindOpencode); !slices.Equal(ids, []string{"anthropic/x"}) {
		t.Errorf("opencode under the policy offers %v, want only the allowed provider's model", ids)
	}
	ids, reason := agentModels(t, session.KindAgy)
	if len(ids) != 0 || reason != "route_policy" {
		t.Errorf("agy under the policy offers %v (reason %q), want nothing, said", ids, reason)
	}
}

// A member's own endpoint (member-llamacpp) is not added while the policy forbids it, and nothing
// is written; with no restriction it is added as before.
func TestLcppConnectionEntryUnderThePolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	useHidingPolicy(t, apiKeysOnly)
	if w := lcppPut("http://127.0.0.1:8081", ""); w.Code != http.StatusForbidden {
		t.Fatalf("a forbidden member endpoint: %d %s", w.Code, w.Body.String())
	}
	if s, err := secrets.Load(); err != nil || s.Lcpp != nil {
		t.Errorf("a refused endpoint was stored: %+v %v", s.Lcpp, err)
	}
	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if w := lcppPut("http://127.0.0.1:8081", ""); w.Code != http.StatusOK {
		t.Errorf("control: an open policy refused: %d %s", w.Code, w.Body.String())
	}
}

// Every harness whose login the Agent runs as a flow has its flow store guarded, so a tighter
// policy cancels a login in progress (ADR 0138 D12, credential entry).
func TestEveryLoginFlowIsGuarded(t *testing.T) {
	got := agents.GuardedLoginRoutes()
	for _, r := range []string{routes.RouteAnthropicSubscription, routes.RouteChatGPTSubscription, routes.RouteAgyLogin,
		routes.RouteCursorLogin, routes.RouteKiroLogin, routes.RouteMuseLogin} {
		if !slices.Contains(got, r) {
			t.Errorf("no login flow store guards %s (guarded: %v)", r, got)
		}
	}
}
