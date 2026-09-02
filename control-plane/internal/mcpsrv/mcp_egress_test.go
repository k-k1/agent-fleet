package mcpsrv

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The M4 egress MCP tools are super_admin-only, and propose_allowlist_change creates a
// PROPOSED (not effective) entry plus an audit row — enforcing the human-approval loop.
func TestMCPEgressProposeAndGate(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := newMCPAPI(&manager{store: st})
	super := &adminCtx{prin: &mcpPrincipal{patID: "pat1"}, tenant: store.Tenant{ID: "t1"}, isSuper: true, isAdmin: true}
	tenantAdmin := &adminCtx{prin: &mcpPrincipal{patID: "pat2"}, tenant: store.Tenant{ID: "t1"}, isSuper: false, isAdmin: true}

	// A tenant_admin (not super) is rejected from every egress tool.
	if _, err := api.mcpProposeAllowlist(ctx, tenantAdmin, "x.com", "r"); err == nil {
		t.Fatal("propose: expected super_admin gate")
	}
	if _, err := api.mcpEgressStats(ctx, tenantAdmin, 7); err == nil {
		t.Fatal("stats: expected super_admin gate")
	}
	if _, err := api.mcpListAllowlist(ctx, tenantAdmin, ""); err == nil {
		t.Fatal("list: expected super_admin gate")
	}

	// super_admin proposes; entry is normalised, PROPOSED, and returns the approval note.
	out, err := api.mcpProposeAllowlist(ctx, super, "  Paste.EE ", "under review")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !strings.Contains(out, `"state":"proposed"`) || !strings.Contains(out, "approval") {
		t.Fatalf("propose out: %s", out)
	}
	prop, _ := st.ListAllowlist(ctx, "proposed", 10)
	if len(prop) != 1 || prop[0].Entry != "paste.ee" || prop[0].AddedBy != "mcp:pat1" {
		t.Fatalf("proposed row: %+v", prop)
	}
	// A proposed entry is NOT yet effective (needs human approval).
	if eff, _ := st.EffectiveAllowlist(ctx); len(eff) != 0 {
		t.Fatalf("proposed must not be effective: %+v", eff)
	}
	// The proposal is audited (actor_kind=mcp, action=egress.propose).
	al, _ := st.ListAuditByTenant(ctx, "t1", 10)
	found := false
	for _, a := range al {
		if a.Action == "egress.propose" && a.ActorKind == "mcp" && a.Target == "paste.ee" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected egress.propose audit row, got %+v", al)
	}

	// stats works for super.
	if _, err := api.mcpEgressStats(ctx, super, 7); err != nil {
		t.Fatalf("stats super: %v", err)
	}
}
