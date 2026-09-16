package main

// engine_tenant_gate_test.go — ADR 0084 decision 7/8: a tenant denied a role must be refused
// at all three gates (catalog, issueSessionToken, serve), and a tenant nobody has said
// anything about (nil, the stored default) must be let through exactly as before.
//
// Real SQLite store throughout, like engine_ingest_perm_test.go: the gate's whole job is
// reading tenantLimits through a membership, and a stub store would only assert that the code
// calls what it calls.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineTenantGateFixture builds one tenant nobody has restricted (ADR 0084 decision 7: nil
// resolves to allowed) and one explicitly denied both roles, each with one member, plus a
// gateway around a real `llm` engine backed by an httptest upstream that actually answers —
// so the positive control below proves the whole route works, not merely that it fails to
// refuse.
func engineTenantGateFixture(t *testing.T) (g engineGateway, membershipID map[string]string) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	allowed, err := st.CreateTenant(ctx, "allowed", "Allowed")
	if err != nil {
		t.Fatalf("tenant allowed: %v", err)
	}
	denied, err := st.CreateTenant(ctx, "denied", "Denied")
	if err != nil {
		t.Fatalf("tenant denied: %v", err)
	}
	if err := st.SetTenantLimits(ctx, denied.ID,
		`{"allow_engine_llm":false,"allow_engine_image":false}`); err != nil {
		t.Fatalf("deny: %v", err)
	}
	// `allowed` gets no limits row at all — the common case (decision 7's nil, not an
	// explicit true), which is exactly the case a `bool`-typed field would have broken.

	membershipID = map[string]string{}
	person := func(name, email, tenantID string) {
		t.Helper()
		ident, err := st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
		if err != nil {
			t.Fatalf("identity %s: %v", email, err)
		}
		mem, err := st.EnsureMembership(ctx, ident.ID, tenantID, "member")
		if err != nil {
			t.Fatalf("membership %s: %v", email, err)
		}
		membershipID[name] = mem.ID
	}
	person("allowed", "allowed@example.com", allowed.ID)
	person("denied", "denied@example.com", denied.ID)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"hi"}}]}`))
	}))
	t.Cleanup(up.Close)

	if err := st.PutEngineModel(ctx, store.EngineModel{Role: "llm", ID: "m", Kind: "gguf", Enabled: true}); err != nil {
		t.Fatalf("put model: %v", err)
	}
	e := &engineRuntimeState{
		def:     engineDef{Key: "llm", API: engineAPIChat, URL: up.URL, Health: "/health", Provider: "llamacpp"},
		catalog: newEngineCatalog(st, "llm"),
	}
	e.demand = newEngineDemand(nil, engineSettingsFor("llm").demandAt, 5*time.Minute)

	reg := &engineRegistry{
		byKey:   map[string]*engineRuntimeState{"llm": e},
		signKey: engineSignKey([]byte(strings.Repeat("k", 32))),
	}
	g = engineGateway{mgr: &manager{store: st}, reg: reg}
	return g, membershipID
}

// serveLLM drives POST /engine/llm/v1/chat/completions through the real mux handler (not
// g.plain directly), so gate 3's placement in serve() is what is under test.
func serveLLM(g engineGateway, membershipID string) (code int, body string) {
	tok := mintEngineSessionToken(g.reg.signKey, membershipID, "sess-1", "llm", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/engine/llm/v1/chat/completions", strings.NewReader(`{}`))
	r.SetPathValue("key", "llm")
	r.SetPathValue("path", "chat/completions")
	r.Header.Set("Authorization", "Bearer "+tok)
	g.serve(rec, r)
	return rec.Code, rec.Body.String()
}

// TestEngineGatewayServeGateRefusesTheDeniedTenant is gate 3 (ADR 0084 decision 8): the
// session token's own 30-day life means the catalog and token gates alone leave a window, so
// `serve` checks again right after resolving membership.
func TestEngineGatewayServeGateRefusesTheDeniedTenant(t *testing.T) {
	g, mid := engineTenantGateFixture(t)

	// Positive control (AGENTS.md "Verifying your own work"): the same route, same token
	// shape, an allowed tenant — and it must reach the real upstream and answer 200. Without
	// this, a 403 below could just as well mean the whole path is broken as that the gate
	// works.
	if code, body := serveLLM(g, mid["allowed"]); code != http.StatusOK {
		t.Fatalf("positive control: allowed tenant got %d: %s", code, body)
	}

	code, body := serveLLM(g, mid["denied"])
	if code != http.StatusForbidden {
		t.Fatalf("denied tenant got %d: %s, want 403", code, body)
	}
	if !strings.Contains(body, "engine_forbidden") {
		t.Errorf("body = %s, want the engine_forbidden code", body)
	}
}

// issueTokenFor drives POST /internal/engine/token as the given membership.
func issueTokenFor(g engineGateway, membershipID string) (code int, body string) {
	issueTok := mintEngineIssueToken(g.reg.signKey, membershipID)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/internal/engine/token", strings.NewReader(`{"key":"llm"}`))
	r.Header.Set("Authorization", "Bearer "+issueTok)
	g.issueSessionToken(rec, r)
	return rec.Code, rec.Body.String()
}

// TestEngineGatewayIssueTokenGateRefusesTheDeniedTenant is gate 2: the Agent's own catalog
// cache holds for ten minutes, so a tenant denied mid-window must not still be able to buy a
// session token for the role it just lost.
func TestEngineGatewayIssueTokenGateRefusesTheDeniedTenant(t *testing.T) {
	g, mid := engineTenantGateFixture(t)

	// Positive control first, same reasoning as above.
	if code, body := issueTokenFor(g, mid["allowed"]); code != http.StatusOK {
		t.Fatalf("positive control: allowed tenant got %d: %s", code, body)
	}

	code, body := issueTokenFor(g, mid["denied"])
	if code != http.StatusForbidden {
		t.Fatalf("denied tenant got %d: %s, want 403", code, body)
	}
	if !strings.Contains(body, "engine_forbidden") {
		t.Errorf("body = %s, want the engine_forbidden code", body)
	}
}

// catalogFor drives GET /internal/engine/catalog as the given membership and returns the
// engine keys it lists.
func catalogFor(t *testing.T, g engineGateway, membershipID string) []string {
	t.Helper()
	issueTok := mintEngineIssueToken(g.reg.signKey, membershipID)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/internal/engine/catalog", nil)
	r.Header.Set("Authorization", "Bearer "+issueTok)
	g.catalog(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Engines []struct {
			Key string `json:"key"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := make([]string, 0, len(out.Engines))
	for _, e := range out.Engines {
		keys = append(keys, e.Key)
	}
	return keys
}

// TestEngineGatewayCatalogHidesTheDeniedRole is gate 1, decision 8's "main" gate: this alone
// removes the row from what the Agent writes into opencode's config and what the image pane
// offers, exactly like an engine an admin switched off (decision 5's "do not offer and then
// refuse" rule).
func TestEngineGatewayCatalogHidesTheDeniedRole(t *testing.T) {
	g, mid := engineTenantGateFixture(t)

	// Positive control: the allowed tenant sees the row. A catalog call that always answers
	// empty would pass the denied case below for the wrong reason.
	if keys := catalogFor(t, g, mid["allowed"]); len(keys) != 1 || keys[0] != "llm" {
		t.Fatalf("positive control: allowed tenant's catalog = %v, want [llm]", keys)
	}

	if keys := catalogFor(t, g, mid["denied"]); len(keys) != 0 {
		t.Errorf("denied tenant's catalog = %v, want none", keys)
	}
}
