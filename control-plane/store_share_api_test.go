package main

// This is not a store test (ADR 0067 / CP-STORE): it exercises the API layer — manager,
// newSessionShareAPI, etagJSON — with the store underneath it. The dependency runs
// main -> store, so the test belongs on the API side of the seam rather than inside it.

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func TestSessionShareProposalListIsNoStoreThroughETagMiddleware(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	identity, _ := st.UpsertIdentity(ctx, "nostore@example.com", "nostore-owner", "")
	membership, _ := st.EnsureMembership(ctx, identity.ID, tenant.ID, "member")
	views, _ := st.ListMemberships(ctx, identity.ID)
	workspace := store.Workspace{ID: store.NewID(), TenantID: tenant.ID, MembershipID: membership.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := store.SharedSessionCatalog{ID: store.NewID(), WorkspaceID: workspace.ID, OwnerMembershipID: membership.ID, Name: "s1", Kind: "codex", LastSeen: store.NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, membership.ID, []store.SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	secretPayload := `{"op":"start","prompt":"decrypted proposal text"}`
	proposal := store.SessionShareProposal{ID: store.NewID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: membership.ID,
		ProposerMembershipID: membership.ID, Action: "turn", Ciphertext: base64.StdEncoding.EncodeToString([]byte(secretPayload)),
		Status: "pending", CreatedAt: store.NowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	api := newSessionShareAPI(&manager{store: st, rts: map[string]cachedRT{}})
	h := etagJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.listProposals(w, r, identity, views[0])
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/session-share-proposals", nil)
	req.Header.Set("If-None-Match", `W/"stale"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("ETag") != "" {
		t.Fatalf("status=%d cache=%q etag=%q body=%s membership=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"), rec.Body.String(), membership.ID)
	}
	if !strings.Contains(rec.Body.String(), "decrypted proposal text") {
		t.Fatalf("decrypted payload missing: %s", rec.Body.String())
	}
}
