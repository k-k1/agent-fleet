package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// useHidingPolicy makes the listing endpoints hide by a store that holds p.
func useHidingPolicy(t *testing.T, p routepolicy.Policy) {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	settleKindProbes(t)
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })
}

// apiKeysOnly allows the two vendor API routes and nothing else.
var apiKeysOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI, routes.RouteOpenAIAPI}}

func liveKinds(t *testing.T) map[string]kindEntryWire {
	t.Helper()
	settleKindProbes(t)
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

// Under a restriction a member endpoint is added only on an approved self-hosted host (ADR 0138
// §2), judged on the URL being stored, not on the connection it replaces.
func TestLcppConnectionEntryOnAnApprovedEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	p := apiKeysOnly
	p.Endpoints, p.EndpointRoutes = []string{"llm.corp.example"}, []string{routes.RouteMemberLlamacpp}
	useHidingPolicy(t, p)
	if w := lcppPut("http://LLM.corp.example:8081/v1", ""); w.Code != http.StatusOK {
		t.Fatalf("an approved endpoint was refused: %d %s", w.Code, w.Body.String())
	}
	if w := lcppPut("http://llm.other.example:8081", ""); w.Code != http.StatusForbidden {
		t.Errorf("an endpoint that is not approved, replacing an approved one: %d %s", w.Code, w.Body.String())
	}
	if s, err := secrets.Load(); err != nil || s.Lcpp == nil || routepolicy.HostOf(s.Lcpp.URL) != "LLM.corp.example" {
		t.Errorf("the refused endpoint replaced the approved one: %+v %v", s.Lcpp, err)
	}
	p.EndpointRoutes = nil
	useHidingPolicy(t, p)
	if w := lcppPut("http://llm.corp.example:8081", ""); w.Code != http.StatusForbidden {
		t.Errorf("an approved host on which no layer allows the route: %d %s", w.Code, w.Body.String())
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

// settleKindProbes makes the test wait, when it ends, for the credential probes the live catalog
// started. The catalog stops waiting for them after usableRoutesBudget, and a slow one can still
// write under HOME, which fails t.TempDir's cleanup ("directory not empty"). Call it AFTER the
// temporary directories are made: cleanups run last-in first-out, so this one runs before theirs.
func settleKindProbes(t *testing.T) {
	t.Helper()
	t.Cleanup(awaitKindProbes)
}

// memberHome gives the test a HOME of its own for the member's ui-prefs.
func memberHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	settleKindProbes(t)
}

// writeMemberPrefs replaces the member's ui-prefs with prefs.
func writeMemberPrefs(t *testing.T, prefs map[string]any) {
	t.Helper()
	b, err := json.Marshal(prefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := uiprefs.WriteAtomic(b); err != nil {
		t.Fatal(err)
	}
}

func liveAccess(t *testing.T) memberAccessWire {
	t.Helper()
	settleKindProbes(t)
	rec := httptest.NewRecorder()
	handleAgentKinds(rec, httptest.NewRequest(http.MethodGet, "/agents/kinds", nil))
	var got kindCatalogWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Access == nil {
		t.Fatal("the live catalog carries no access summary")
	}
	return *got.Access
}

// The member's own switches hide a harness from the live catalog like the policy does, and the
// access summary tells the three cases apart without naming a layer or a kind: nothing allowed
// (ask an administrator), everything allowed switched off by the member (offer the reset), and
// some allowed harness switched off. A harness the policy forbids anyway never counts as hidden
// by the member, so the summary cannot reveal it.
func TestKindCatalogMemberSwitchesAndAccess(t *testing.T) {
	memberHome(t)
	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if a := liveAccess(t); !reflect.DeepEqual(a, memberAccessWire{PolicyConfirmed: true}) {
		t.Errorf("no restriction, no switch: %+v", a)
	}

	writeMemberPrefs(t, map[string]any{"agentEnabled": map[string]any{"codex": false, "shell": false}})
	by := liveKinds(t)
	if _, ok := by[session.KindCodex]; ok {
		t.Error("codex, switched off by the member, is listed")
	}
	if _, ok := by[session.KindShell]; !ok {
		t.Error("shell runs no model and cannot be switched off, yet it was hidden")
	}
	if a := liveAccess(t); !reflect.DeepEqual(a, memberAccessWire{SomeOffByOwnSettings: true, PolicyConfirmed: true}) {
		t.Errorf("one harness switched off: %+v", a)
	}

	// Under apiKeysOnly only claude and codex (and opencode, on its provider keys) have an
	// allowed route. Switching off agy, which the policy forbids anyway, is not reported.
	useHidingPolicy(t, apiKeysOnly)
	writeMemberPrefs(t, map[string]any{"agentEnabled": map[string]any{"agy": false}})
	if a := liveAccess(t); !reflect.DeepEqual(a, memberAccessWire{PolicyConfirmed: true}) {
		t.Errorf("only a forbidden harness switched off: %+v", a)
	}
	off := map[string]any{}
	for k := range liveKinds(t) {
		off[k] = false
	}
	writeMemberPrefs(t, map[string]any{"agentEnabled": off})
	if a := liveAccess(t); !reflect.DeepEqual(a, memberAccessWire{AllOffByOwnSettings: true, SomeOffByOwnSettings: true, PolicyConfirmed: true}) {
		t.Errorf("every allowed harness switched off: %+v", a)
	}

	useHidingPolicy(t, routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{"no-such-route"}})
	if a := liveAccess(t); !reflect.DeepEqual(a, memberAccessWire{NoRouteAvailable: true, PolicyConfirmed: true}) {
		t.Errorf("nothing allowed: %+v", a)
	}
}

// The chat flag follows the chat backend's own routes, not the kind's: a kind whose backend has no
// allowed route says false, one with no chat backend carries no flag.
func TestKindCatalogChatFlag(t *testing.T) {
	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	by := liveKinds(t)
	if c := by[session.KindClaude].Chat; c == nil || !*c {
		t.Errorf("claude chat with no restriction: %v", c)
	}
	if c := by[session.KindShell].Chat; c != nil {
		t.Errorf("shell has no chat backend, yet carries %v", *c)
	}
	// claude's chat backend runs on the subscription only: with just the API routes allowed claude
	// stays launchable but its chat is not offered, while codex's chat runs on its API key.
	useHidingPolicy(t, apiKeysOnly)
	by = liveKinds(t)
	if c := by[session.KindClaude].Chat; c == nil || *c {
		t.Errorf("claude chat with the subscription forbidden: %v, want false", c)
	}
	if c := by[session.KindCodex].Chat; c == nil || !*c {
		t.Errorf("codex chat on an allowed API key: %v, want true", c)
	}
}

// A kind the member switched off with agentEnabled has no GET /connections entry, like a kind the
// policy forbids; GitHub's entry, which is git and not a model, stays even with copilot off; a kind
// that is on keeps its entry.
func TestConnectionsHideSwitchedOffHarnesses(t *testing.T) {
	memberHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	get := func() map[string]json.RawMessage {
		w := httptest.NewRecorder()
		handleConnectionsGet(w, httptest.NewRequest(http.MethodGet, "/connections", nil))
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
		return out
	}
	writeMemberPrefs(t, map[string]any{"agentEnabled": map[string]any{"agy": true}})
	before := get()
	for _, k := range []string{session.KindAgy, session.KindCopilot, "github"} {
		if _, ok := before[k]; !ok {
			t.Fatalf("control: %s has no entry with every switch on", k)
		}
	}
	writeMemberPrefs(t, map[string]any{"agentEnabled": map[string]any{"agy": false, "copilot": false}})
	after := get()
	for _, k := range []string{session.KindAgy, session.KindCopilot} {
		if _, ok := after[k]; ok {
			t.Errorf("%s, switched off by the member, still has an entry", k)
		}
	}
	for _, k := range []string{"github", session.KindClaude, session.KindCodex} {
		if _, ok := after[k]; !ok && before[k] != nil {
			t.Errorf("%s's entry was hidden", k)
		}
	}
}

// fakeCredStore is an agents.CredentialStore over a list the test edits.
type fakeCredStore struct {
	creds   *[]agents.StoredCredential
	removed *[]string
}

func (f fakeCredStore) StoredCredentials(context.Context) []agents.StoredCredential {
	return slices.Clone(*f.creds)
}

// RemoveCredential keeps the contract: only the confirmed binding, only while forbidden.
func (f fakeCredStore) RemoveCredential(_ context.Context, id, binding string, forbidden agents.Forbidden) error {
	i := slices.IndexFunc(*f.creds, func(c agents.StoredCredential) bool { return c.ID == id })
	if i < 0 || (*f.creds)[i].Binding != binding || !forbidden((*f.creds)[i].Routes, "") {
		return agents.ErrCredentialChanged
	}
	*f.removed = append(*f.removed, id)
	*f.creds = slices.Delete(*f.creds, i, i+1)
	return nil
}

func useCredSources(t *testing.T, srcs ...credentialSource) {
	t.Helper()
	old := credentialSources
	credentialSources = func() []credentialSource { return srcs }
	t.Cleanup(func() { credentialSources = old })
}

func getUnusable(t *testing.T) (unusableWire, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handleUnusableGet(w, httptest.NewRequest(http.MethodGet, "/connections/unusable", nil))
	var got unusableWire
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return got, w.Body.String()
}

// D14a's line: a count and a token for the stored credentials the policy forbids on every route,
// never naming one; nothing with no restriction; a credential with an allowed route, or whose
// binding cannot be told, is not counted.
func TestUnusableCredentialsCount(t *testing.T) {
	var removed []string
	codexCreds := []agents.StoredCredential{{ID: "login", Routes: []string{routes.RouteChatGPTSubscription}, Binding: "chatgpt-subscription@a1"}}
	ocCreds := []agents.StoredCredential{
		{ID: "key:OPENAI_API_KEY", Routes: []string{routes.RouteOpenAIAPI}, Binding: "OPENAI_API_KEY@k1"},
		{ID: "key:OPENCODE_API_KEY", Routes: []string{routes.RouteOpencodeZen, routes.RouteOpencodeGo}, Binding: "OPENCODE_API_KEY@z1"},
		{ID: "account", Routes: []string{routes.RouteOpencodeAccount}, Binding: ""},
	}
	useCredSources(t, credentialSource{"codex", fakeCredStore{&codexCreds, &removed}}, credentialSource{"opencode", fakeCredStore{&ocCreds, &removed}})

	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if got, _ := getUnusable(t); got != (unusableWire{}) {
		t.Errorf("an unrestricted member is offered %+v", got)
	}
	useHidingPolicy(t, apiKeysOnly)
	got, body := getUnusable(t)
	if got.Count != 2 || got.Token == "" {
		t.Fatalf("under apiKeysOnly: %+v, want the codex login and the Zen key counted", got)
	}
	for _, leak := range []string{"chatgpt", "OPENCODE", "codex", "opencode", "login", "binding"} {
		if strings.Contains(body, leak) {
			t.Errorf("the answer names %q: %s", leak, body)
		}
	}
	if len(removed) != 0 {
		t.Errorf("counting removed %v", removed)
	}
}

// The removal takes exactly what was counted, re-checked: a credential replaced, removed or made
// usable since is skipped, nothing outside the count is touched, a token works once, and an
// unmarked request is refused before anything happens.
func TestUnusableCredentialsRemove(t *testing.T) {
	s := memberorigintest.Install(t)
	mux := buildMux()
	var removed []string
	codexCreds := []agents.StoredCredential{{ID: "login", Routes: []string{routes.RouteChatGPTSubscription}, Binding: "chatgpt-subscription@a1"}}
	ocCreds := []agents.StoredCredential{
		{ID: "key:OPENAI_API_KEY", Routes: []string{routes.RouteOpenAIAPI}, Binding: "OPENAI_API_KEY@k1"},
		{ID: "key:OPENCODE_API_KEY", Routes: []string{routes.RouteOpencodeZen}, Binding: "OPENCODE_API_KEY@z1"},
		{ID: "key:OPENROUTER_API_KEY", Routes: []string{routes.RouteOpenRouterAPI}, Binding: "OPENROUTER_API_KEY@r1"},
	}
	useCredSources(t, credentialSource{"codex", fakeCredStore{&codexCreds, &removed}}, credentialSource{"opencode", fakeCredStore{&ocCreds, &removed}})
	useHidingPolicy(t, apiKeysOnly)

	post := func(token string, sign bool) *httptest.ResponseRecorder {
		body := []byte(`{"token":"` + token + `"}`)
		r := httptest.NewRequest(http.MethodPost, "/connections/unusable/remove", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer agent-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AF-Relay", "cp")
		if sign {
			r.Header.Set(memberorigin.Header, s.Mark(memberorigintest.Claims(r, body)))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	got, _ := getUnusable(t)
	if got.Count != 3 {
		t.Fatalf("counted %d, want 3", got.Count)
	}
	if w := post(got.Token, false); w.Code != http.StatusForbidden || len(removed) != 0 {
		t.Fatalf("unmarked: %d %s, removed %v", w.Code, w.Body, removed)
	}
	// Between the count and the confirmation the OpenRouter key is replaced (another binding) and
	// the Zen key is removed by hand: both are skipped, only the codex login goes.
	ocCreds[2].Binding = "OPENROUTER_API_KEY@r2"
	ocCreds = slices.Delete(ocCreds, 1, 2)
	w := post(got.Token, true)
	var out unusableRemoveWire
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("marked: %d %s", w.Code, w.Body)
	}
	if !slices.Equal(removed, []string{"login"}) || out.Removed != 1 || out.Skipped != 2 || out.Failed != 0 {
		t.Errorf("removed %v, answer %+v, want only the codex login", removed, out)
	}
	if out.Count != 1 || out.Token == "" {
		t.Errorf("recomputed line %+v, want the replacement OpenRouter key counted anew", out.unusableWire)
	}
	if len(ocCreds) != 2 || ocCreds[0].ID != "key:OPENAI_API_KEY" {
		t.Errorf("an allowed or replaced key was touched: %+v", ocCreds)
	}
	if w := post(got.Token, true); w.Code != http.StatusConflict {
		t.Errorf("a used token: %d %s", w.Code, w.Body)
	}

	// Loosened between the count and the confirmation: nothing is removed.
	removed = nil
	next := out.Token
	useHidingPolicy(t, routepolicy.Policy{Revision: 9, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI, routes.RouteOpenRouterAPI}})
	if w := post(next, true); w.Code != http.StatusOK || len(removed) != 0 {
		t.Errorf("after loosening: %d %s, removed %v", w.Code, w.Body, removed)
	}
}

// The sources are the kinds' own stores and the llama.cpp endpoint: never the GitHub connection
// copilot rides on, nor a source the Control Plane or an administrator owns.
func TestCredentialSourcesLeaveOutOtherUses(t *testing.T) {
	for _, s := range credentialSources() {
		if s.owner == session.KindCopilot || s.owner == "github" {
			t.Errorf("%s is a credential source", s.owner)
		}
	}
	if _, ok := sessionx.AgentOf(session.KindCopilot).(agents.CredentialStore); ok {
		t.Error("copilot lists credentials of its own; its only one is the GitHub connection")
	}
}

// The llama.cpp endpoint is listed with its endpoint binding and removed like DELETE
// /connections/lcpp.
func TestLcppEndpointStore(t *testing.T) {
	memberHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	if got := (lcppEndpointStore{}).StoredCredentials(context.Background()); len(got) != 0 {
		t.Fatalf("no endpoint: %+v", got)
	}
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Lcpp = &secrets.LcppConn{URL: "http://llm.lab.invalid:8080", APIKey: "k"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := (lcppEndpointStore{}).StoredCredentials(context.Background())
	if len(got) != 1 || got[0].Binding == "" || !slices.Equal(got[0].Routes, []string{routes.RouteMemberLlamacpp}) {
		t.Fatalf("an endpoint: %+v", got)
	}
	always := func([]string, string) bool { return true }
	if err := (lcppEndpointStore{}).RemoveCredential(context.Background(), "endpoint", "member-llamacpp@other", always); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Fatalf("another binding: %v", err)
	}
	if err := (lcppEndpointStore{}).RemoveCredential(context.Background(), "endpoint", got[0].Binding, func([]string, string) bool { return false }); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Fatalf("no longer forbidden: %v", err)
	}
	if err := (lcppEndpointStore{}).RemoveCredential(context.Background(), "endpoint", got[0].Binding, always); err != nil {
		t.Fatal(err)
	}
	if got := (lcppEndpointStore{}).StoredCredentials(context.Background()); len(got) != 0 {
		t.Errorf("after removal: %+v", got)
	}
}

// The snapshot's check is not the last word: a credential replaced, or a policy loosened, after it
// and before the store removes is left alone, because the store checks again under the lock its
// writers take. Driven through the real llama.cpp endpoint store, whose writer is
// handlePutLcppConn's secrets.Update.
func TestUnusableRemovalRechecksAtTheStore(t *testing.T) {
	memberHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	s := memberorigintest.Install(t)
	mux := buildMux()
	useCredSources(t, credentialSource{"lcpp", lcppEndpointStore{}})
	useHidingPolicy(t, apiKeysOnly)
	setEndpoint := func(url string) {
		t.Helper()
		if err := secrets.Update(func(d *secrets.Data) error { d.Lcpp = &secrets.LcppConn{URL: url}; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := func() string {
		d, err := secrets.Load()
		if err != nil || d.Lcpp == nil {
			return ""
		}
		return d.Lcpp.URL
	}
	remove := func() unusableRemoveWire {
		t.Helper()
		got, _ := getUnusable(t)
		if got.Count != 1 {
			t.Fatalf("counted %d, want the endpoint", got.Count)
		}
		body := []byte(`{"token":"` + got.Token + `"}`)
		r := httptest.NewRequest(http.MethodPost, "/connections/unusable/remove", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer agent-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AF-Relay", "cp")
		r.Header.Set(memberorigin.Header, s.Mark(memberorigintest.Claims(r, body)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out unusableRemoveWire
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out
	}
	hook := func(f func()) {
		old := beforeUnusableRemoval
		beforeUnusableRemoval = func(unusableCred) { f() }
		t.Cleanup(func() { beforeUnusableRemoval = old })
	}

	setEndpoint("http://one.lab.invalid:8080")
	hook(func() { setEndpoint("http://two.lab.invalid:8080") })
	if out := remove(); out.Removed != 0 || out.Skipped != 1 || endpoint() != "http://two.lab.invalid:8080" {
		t.Errorf("replaced after the check: %+v, endpoint %q", out, endpoint())
	}

	hook(func() {
		useHidingPolicy(t, routepolicy.Policy{Revision: 9, Restricted: true, Allowed: []string{routes.RouteMemberLlamacpp}})
	})
	if out := remove(); out.Removed != 0 || out.Skipped != 1 || endpoint() == "" {
		t.Errorf("loosened after the check: %+v, endpoint %q", out, endpoint())
	}

	useHidingPolicy(t, apiKeysOnly)
	hook(func() {})
	if out := remove(); out.Removed != 1 || endpoint() != "" {
		t.Errorf("control, nothing changed: %+v, endpoint %q", out, endpoint())
	}
}

// Removing an endpoint on a host the policy does not approve, under a policy that approves another
// host for member-llamacpp, must not wait on itself: the policy is judged on the host the store's
// transaction holds, never through the endpoint callback, which loads the same store.
func TestLcppRemovalUnderApprovedEndpointsDoesNotDeadlock(t *testing.T) {
	memberHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	useHidingPolicy(t, routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI},
		Endpoints: []string{"approved.lab.invalid"}, EndpointRoutes: []string{routes.RouteMemberLlamacpp}})
	run := func(url string) error {
		t.Helper()
		if err := secrets.Update(func(d *secrets.Data) error { d.Lcpp = &secrets.LcppConn{URL: url}; return nil }); err != nil {
			t.Fatal(err)
		}
		listed := (lcppEndpointStore{}).StoredCredentials(context.Background())
		if len(listed) != 1 {
			t.Fatalf("listed %+v", listed)
		}
		done := make(chan error, 1)
		go func() {
			done <- (lcppEndpointStore{}).RemoveCredential(context.Background(), "endpoint", listed[0].Binding, stillForbidden)
		}()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("the removal deadlocked")
			return nil
		}
	}
	if err := run("http://other.lab.invalid:8080"); err != nil {
		t.Errorf("an endpoint on an unapproved host: %v, want removed", err)
	}
	if err := run("http://approved.lab.invalid:8080"); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Errorf("an endpoint on the approved host: %v, want kept (allowed there)", err)
	}
}

// The catalogue says whether its kinds are filtered by a policy in force: a member's settings pages
// list nothing from it otherwise (ADR 0138 D10a, D12).
func TestKindCatalogSaysWhetherThePolicyIsConfirmed(t *testing.T) {
	confirmed := func() bool {
		rec := httptest.NewRecorder()
		handleAgentKinds(rec, httptest.NewRequest(http.MethodGet, "/agents/kinds", nil))
		var got kindCatalogWire
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Access == nil {
			t.Fatal("the live catalogue has no access summary")
		}
		return got.Access.PolicyConfirmed
	}
	useHidingPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if !confirmed() {
		t.Error("a confirmed unrestricted policy reads unconfirmed")
	}
	useHidingPolicy(t, apiKeysOnly)
	if !confirmed() {
		t.Error("a confirmed restriction reads unconfirmed")
	}
	stale := routepolicy.Open(filepath.Join(t.TempDir(), "stale.json"), "")
	if _, err := stale.Seed(apiKeysOnly, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if stale.Gate() == nil {
		t.Fatal("fixture: the seeded policy is confirmed")
	}
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return stale }
	t.Cleanup(func() { routepolicy.Active = old })
	if confirmed() {
		t.Error("a policy that cannot be confirmed reads confirmed")
	}
}

// An approved self-hosted host makes member-llamacpp addable before any endpoint is stored (ADR
// 0138 §2): the catalogue says so without naming a host, and GET /connections keeps lcpp's entry,
// so the member can enter the endpoint that makes lcpp usable. Once it is stored on the approved
// host, lcpp is listed with the route, and nothing is left to add.
func TestMemberEndpointIsAddableOnAnApprovedHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	access := func() memberAccessWire {
		rec := httptest.NewRecorder()
		handleAgentKinds(rec, httptest.NewRequest(http.MethodGet, "/agents/kinds", nil))
		var got kindCatalogWire
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return *got.Access
	}
	useHidingPolicy(t, apiKeysOnly)
	if a := access(); len(a.AddableRoutes) != 0 {
		t.Errorf("control, no approved host: addable %v", a.AddableRoutes)
	}
	p := apiKeysOnly
	p.Endpoints, p.EndpointRoutes = []string{"llm.corp.example"}, []string{routes.RouteMemberLlamacpp}
	useHidingPolicy(t, p)
	if _, ok := liveKinds(t)[session.KindLcpp]; ok {
		t.Error("lcpp is listed with no endpoint stored")
	}
	if a := access(); !slices.Equal(a.AddableRoutes, []string{routes.RouteMemberLlamacpp}) {
		t.Errorf("addable %v, want member-llamacpp", a.AddableRoutes)
	}
	if body, _ := json.Marshal(access()); strings.Contains(string(body), "corp.example") {
		t.Errorf("the access summary names an approved host: %s", body)
	}
	if _, ok := connectionsMap(t)[session.KindLcpp]; !ok {
		t.Error("GET /connections has no lcpp entry to add the endpoint on")
	}
	if w := lcppPut("http://other.example:8081", ""); w.Code != http.StatusForbidden {
		t.Errorf("an endpoint on a host that is not approved: %d %s", w.Code, w.Body.String())
	}
	if w := lcppPut("http://llm.corp.example:8081", ""); w.Code != http.StatusOK {
		t.Fatalf("an endpoint on the approved host was refused: %d %s", w.Code, w.Body.String())
	}
	lcpp, ok := liveKinds(t)[session.KindLcpp]
	if !ok || !slices.Contains(append(lcpp.Routes, lcpp.FixedRoutes...), routes.RouteMemberLlamacpp) {
		t.Errorf("lcpp after the approved endpoint: %+v listed=%v", lcpp, ok)
	}
	if a := access(); len(a.AddableRoutes) != 0 {
		t.Errorf("addable %v after the endpoint was stored on the approved host", a.AddableRoutes)
	}
}

// connectionsMap is GET /connections' entries by name.
func connectionsMap(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	handleConnectionsGet(w, httptest.NewRequest(http.MethodGet, "/connections", nil))
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return out
}

// GET /connections never carries the member's endpoint while the policy does not allow it: an
// endpoint stored on a host that is not approved is kept but hidden (D14a), so its URL and the
// model last found there are left out of the raw answer. On the approved host they are shown.
func TestConnectionsHideAnEndpointOnAnUnapprovedHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	raw := func() string {
		w := httptest.NewRecorder()
		handleConnectionsGet(w, httptest.NewRequest(http.MethodGet, "/connections", nil))
		return w.Body.String()
	}
	store := func(url string) {
		t.Helper()
		if err := secrets.Update(func(d *secrets.Data) error { d.Lcpp = &secrets.LcppConn{URL: url}; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	p := apiKeysOnly
	p.Endpoints, p.EndpointRoutes = []string{"llm.corp.example"}, []string{routes.RouteMemberLlamacpp}
	useHidingPolicy(t, p)
	store("http://hidden-box.example:8081")
	lcppMemberCacheReset()
	lcppMemberRecordReachable(true)
	lcppMemberRecordModel([]lcppMemberModel{{ID: "hidden-model-name"}})
	t.Cleanup(lcppMemberCacheReset)
	body := raw()
	for _, leak := range []string{"hidden-box.example", "hidden-model-name"} {
		if strings.Contains(body, leak) {
			t.Errorf("GET /connections carries %q for an endpoint the policy does not allow: %s", leak, body)
		}
	}
	if _, ok := connectionsMap(t)[session.KindLcpp]; !ok {
		t.Error("the lcpp entry is gone, so no endpoint can be entered")
	}
	store("http://llm.corp.example:8081")
	if body := raw(); !strings.Contains(body, "llm.corp.example") {
		t.Errorf("control: the approved endpoint is not shown: %s", body)
	}
}

// GET /connections lists the provider keys on their own entry, by name only: claude's anthropic-api
// reads the same slot, so the key stays listed when opencode itself is hidden. A key whose every
// route is forbidden is not listed (D14a), and the opencode.ai key stays on opencode's entry.
func TestProviderKeysEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(d *secrets.Data) error {
		d.Opencode = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret", "OPENAI_API_KEY": "sk-oai-secret", "OPENCODE_API_KEY": "oc-secret"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	envs := func() []string {
		var e struct {
			Envs []string `json:"envs"`
		}
		raw := connectionsMap(t)["provider_keys"]
		if strings.Contains(string(raw), "secret") {
			t.Fatalf("provider_keys carries a key: %s", raw)
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
		return e.Envs
	}
	if got := envs(); !slices.Equal(got, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}) {
		t.Errorf("unrestricted: %v", got)
	}
	if raw := string(connectionsMap(t)[session.KindOpencode]); !strings.Contains(raw, "OPENAI_API_KEY") || !strings.Contains(raw, "OPENCODE_API_KEY") {
		t.Errorf("control, unrestricted: opencode's entry %s", raw)
	}
	useHidingPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}})
	if got := envs(); !slices.Equal(got, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("anthropic-api only: %v", got)
	}
	// opencode's own entry, still listed (it may run on anthropic-api), names the same keys only.
	ocEnvs := func() []string {
		var e struct {
			Envs []string `json:"envs"`
		}
		raw, ok := connectionsMap(t)[session.KindOpencode]
		if !ok {
			t.Fatal("opencode has no entry")
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
		return e.Envs
	}
	if got := ocEnvs(); !slices.Equal(got, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("opencode's entry under anthropic-api only lists %v", got)
	}
	// The member switched opencode off: claude still reads the key, so it stays listed.
	writeMemberPrefs(t, map[string]any{"agentEnabled": map[string]any{session.KindOpencode: false}})
	if _, ok := connectionsMap(t)[session.KindOpencode]; ok {
		t.Error("control: opencode's entry is listed while switched off")
	}
	if got := envs(); !slices.Equal(got, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("with opencode switched off: %v", got)
	}
}

// Approving a custom provider's host does not bring unrelated stored key names back into either
// list: a name the Agent has no route for opens custom-api, and only an outright allowance of that
// route lists it (D14a). Known keys of an allowed route stay.
func TestApprovedCustomEndpointListsNoGenericKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(d *secrets.Data) error {
		d.Opencode = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret", "SOME_OTHER_KEY": "other-secret"}
		d.CustomAPI = &secrets.CustomAPIConn{Provider: "mine", URL: "https://llm.corp.example/v1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI},
		Endpoints: []string{"llm.corp.example"}, EndpointRoutes: []string{routes.RouteCustomAPI}}
	if !p.Allows(routes.RouteCustomAPI) {
		t.Fatal("control: the approved host does not allow custom-api, so the case proves nothing")
	}
	useHidingPolicy(t, p)
	for name, raw := range map[string]json.RawMessage{
		"provider_keys": connectionsMap(t)["provider_keys"],
		"opencode":      connectionsMap(t)[session.KindOpencode],
	} {
		var e struct {
			Envs []string `json:"envs"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%s: %v: %s", name, err, raw)
		}
		if !slices.Equal(e.Envs, []string{"ANTHROPIC_API_KEY"}) {
			t.Errorf("%s lists %v, want only ANTHROPIC_API_KEY", name, e.Envs)
		}
	}
}

// A harness the member switched off offers no models either (the member layer): the picker gets
// nothing, with a reason that names the member's own switch, not the policy. Switched on again it
// offers its menu.
func TestModelMenusHideASwitchedOffHarness(t *testing.T) {
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, perCallLister{Agent: sessionx.AgentOf(session.KindOpencode),
		routeOf: map[string]string{"anthropic/x": routes.RouteAnthropicAPI, "opencode/free": routes.RouteOpencodeZen}}))
	writeUIPrefs(t, `{"hiddenModels":{},"agentEnabled":{"opencode":false}}`)
	ids, reason := agentModels(t, session.KindOpencode)
	if len(ids) != 0 || reason != "switched_off" {
		t.Errorf("switched-off opencode offers %v (reason %q), want nothing, said", ids, reason)
	}
	writeUIPrefs(t, `{"hiddenModels":{},"agentEnabled":{"opencode":true}}`)
	if ids, _ := agentModels(t, session.KindOpencode); len(ids) == 0 {
		t.Error("control: switched back on, opencode offers nothing")
	}
}

// An opencode Bedrock model resolves to no route with no selection (the AWS chain would decide),
// so the launch menu's policy filter used to drop every one of them, bedrock allowed or not. It
// keeps them when the policy allows the bedrock route, and only then.
func TestPolicyKeepsBedrockModelsOnlyWhenTheRouteIsAllowed(t *testing.T) {
	list := []agents.ModelChoice{{ID: "amazon-bedrock/anthropic.claude-sonnet-4-6"}}
	allowed := routepolicy.Policy{Revision: 1, Restricted: true, Allowed: []string{"bedrock", "opencode-zen"}}
	if got := policyVisibleModels("opencode", allowed, list); len(got) != 1 {
		t.Errorf("bedrock allowed: the Bedrock model was dropped: %v", got)
	}
	denied := routepolicy.Policy{Revision: 1, Restricted: true, Allowed: []string{"opencode-zen"}}
	if got := policyVisibleModels("opencode", denied, list); len(got) != 0 {
		t.Errorf("bedrock not allowed: the Bedrock model was kept: %v", got)
	}
}
