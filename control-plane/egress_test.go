package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestEgressPolicy(t *testing.T) {
	p := newEgressPolicy(defaultEgressAllowlist)
	allow := []string{
		"api.anthropic.com",         // exact
		"claude.ai",                 // exact
		"console.anthropic.com",     // suffix .anthropic.com
		"github.com",                // exact + suffix
		"raw.githubusercontent.com", // suffix
		"codeload.github.com",       // suffix .github.com
		"registry.npmjs.org",        // exact
		"files.pythonhosted.org",    // exact
		"API.ANTHROPIC.COM",         // case-insensitive
		"api.anthropic.com.",        // trailing FQDN dot
	}
	for _, h := range allow {
		if !p.allows(h) {
			t.Errorf("expected allow: %s", h)
		}
	}
	deny := []string{"paste.ee", "evil.com", "anthropic.com.attacker.net", "notgithub.com", ""}
	for _, h := range deny {
		if p.allows(h) {
			t.Errorf("expected deny: %s", h)
		}
	}

	// A custom allowlist with a wildcard entry.
	c := newEgressPolicy([]string{"*.internal.example", "one.host", "# comment", ""})
	for _, h := range []string{"a.internal.example", "internal.example", "one.host"} {
		if !c.allows(h) {
			t.Errorf("custom expected allow: %s", h)
		}
	}
	if c.allows("two.host") {
		t.Errorf("custom expected deny: two.host")
	}
}

func TestEgressBatcher(t *testing.T) {
	b := newEgressBatcher("", "")
	b.add("api.anthropic.com", true)
	b.add("api.anthropic.com", true)
	b.add("paste.ee", false)
	evs := b.drain()
	got := map[string]egressEvent{}
	for _, e := range evs {
		got[e.Host] = e
	}
	if got["api.anthropic.com"].Count != 2 || !got["api.anthropic.com"].Allowed {
		t.Fatalf("anthropic bucket: %+v", got["api.anthropic.com"])
	}
	if got["paste.ee"].Count != 1 || got["paste.ee"].Allowed {
		t.Fatalf("paste.ee bucket: %+v", got["paste.ee"])
	}
	if b.drain() != nil {
		t.Fatalf("drain should be empty after first drain")
	}
}

func TestEgressStore(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	day := "2026-07-05"
	mustRec := func(host string, allowed bool, n int) {
		if err := st.RecordEgress(ctx, day, host, allowed, n); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	mustRec("api.anthropic.com", true, 3)
	mustRec("api.anthropic.com", true, 2) // upsert += -> 5
	mustRec("paste.ee", false, 4)

	rows, err := st.ListEgress(ctx, "2026-07-01", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	by := map[string]store.EgressStat{}
	for _, e := range rows {
		by[e.Host] = e
	}
	if by["api.anthropic.com"].Allowed != 5 || by["api.anthropic.com"].Blocked != 0 {
		t.Fatalf("anthropic stat: %+v", by["api.anthropic.com"])
	}
	if by["paste.ee"].Blocked != 4 || by["paste.ee"].Allowed != 0 {
		t.Fatalf("paste.ee stat: %+v", by["paste.ee"])
	}
	// Window excludes older days.
	if rows2, _ := st.ListEgress(ctx, "2026-08-01", 100); len(rows2) != 0 {
		t.Fatalf("future window should be empty, got %d", len(rows2))
	}
}

// egressAPI.ingest end-to-end: auth gate, aggregation, and a single deduped
// would-block audit row per host.
func TestEgressIngestHandler(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", &egressAuditDedup{})
	body := `{"events":[{"host":"api.anthropic.com","allowed":true,"count":3},` +
		`{"host":"paste.ee","allowed":false,"count":2},{"host":"paste.ee","allowed":false,"count":1}]}`

	w0 := httptest.NewRecorder()
	eg.ingest(w0, httptest.NewRequest("POST", "/internal/egress", strings.NewReader(body)))
	if w0.Code != 401 {
		t.Fatalf("unauthorized: want 401 got %d", w0.Code)
	}

	r := httptest.NewRequest("POST", "/internal/egress", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	eg.ingest(w, r)
	if w.Code != 204 {
		t.Fatalf("authorized: want 204 got %d", w.Code)
	}
	stats, _ := st.ListEgress(ctx, "2000-01-01", 100)
	by := map[string]store.EgressStat{}
	for _, e := range stats {
		by[e.Host] = e
	}
	if by["api.anthropic.com"].Allowed != 3 {
		t.Fatalf("anthropic allowed: %+v", by["api.anthropic.com"])
	}
	if by["paste.ee"].Blocked != 3 {
		t.Fatalf("paste.ee blocked: %+v", by["paste.ee"])
	}
	al, _ := st.ListAuditByTenant(ctx, "", 100)
	n := 0
	for _, a := range al {
		if a.Action == "egress.observe" && a.Target == "paste.ee" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 deduped egress.observe for paste.ee, got %d", n)
	}
}

func TestAllowlistStore(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	add := func(entry, state string) string {
		id := store.NewID()
		if err := st.AddAllowlist(ctx, store.AllowlistEntry{
			ID: id, Entry: entry, State: state, AddedBy: "op@x", AddedAt: store.NowTS(),
		}); err != nil {
			t.Fatalf("add: %v", err)
		}
		return id
	}
	add("registry.internal", "active")
	pid := add("proposed.host", "proposed")

	// EffectiveAllowlist returns only active entries.
	eff, _ := st.EffectiveAllowlist(ctx)
	if len(eff) != 1 || eff[0] != "registry.internal" {
		t.Fatalf("effective: %+v", eff)
	}
	// Approve the proposed one -> now effective.
	if err := st.SetAllowlistState(ctx, pid, "active"); err != nil {
		t.Fatalf("setstate: %v", err)
	}
	eff2, _ := st.EffectiveAllowlist(ctx)
	if len(eff2) != 2 {
		t.Fatalf("effective after approve: %+v", eff2)
	}
	// List filtered by state.
	prop, _ := st.ListAllowlist(ctx, "proposed", 100)
	if len(prop) != 0 {
		t.Fatalf("no proposed should remain, got %d", len(prop))
	}

	// Settings kv.
	if v, _ := st.GetSetting(ctx, "egress_mode"); v != "" {
		t.Fatalf("unset should be empty, got %q", v)
	}
	if err := st.SetSetting(ctx, "egress_mode", "enforce"); err != nil {
		t.Fatalf("setsetting: %v", err)
	}
	if v, _ := st.GetSetting(ctx, "egress_mode"); v != "enforce" {
		t.Fatalf("get after set: %q", v)
	}
	if err := st.SetSetting(ctx, "egress_mode", "log-only"); err != nil { // upsert
		t.Fatalf("upsert: %v", err)
	}
	if v, _ := st.GetSetting(ctx, "egress_mode"); v != "log-only" {
		t.Fatalf("upsert value: %q", v)
	}
}

func TestEffectivePolicyEndpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = st.AddAllowlist(ctx, store.AllowlistEntry{ID: store.NewID(), Entry: "extra.internal", State: "active", AddedAt: store.NowTS()})
	_ = st.SetSetting(ctx, "egress_mode", "enforce")
	eg := newEgressAPI(&manager{store: st}, "tok", "proxy:3128", nil)

	entries, enforce := eg.effectivePolicy(ctx)
	if !enforce {
		t.Fatalf("enforce should be true")
	}
	seen := false
	for _, e := range entries {
		if e == "extra.internal" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("effective policy missing db entry: %+v", entries)
	}
	if len(entries) <= len(defaultEgressAllowlist) {
		t.Fatalf("effective should include defaults + db entry")
	}

	// The proxy-facing endpoint requires the token.
	w0 := httptest.NewRecorder()
	eg.policy(w0, httptest.NewRequest("GET", "/internal/egress/policy", nil))
	if w0.Code != 401 {
		t.Fatalf("no token: want 401 got %d", w0.Code)
	}
	r := httptest.NewRequest("GET", "/internal/egress/policy", nil)
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	eg.policy(w, r)
	if w.Code != 200 {
		t.Fatalf("with token: want 200 got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "extra.internal") || !strings.Contains(w.Body.String(), `"enforce":true`) {
		t.Fatalf("policy body: %s", w.Body.String())
	}
}
