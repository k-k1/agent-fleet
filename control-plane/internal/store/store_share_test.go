package store

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testShareLeaseUntil() string {
	return time.Now().Add(shareOwnerLease).UTC().Format(time.RFC3339)
}

func TestSessionShareStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ownerID, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	recipientID, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerID.ID, tn.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientID.ID, tn.ID, "member")
	ws := Workspace{ID: NewID(), TenantID: tn.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	share := SessionShare{ID: NewID(), TenantID: tn.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "worktree", ScopeKey: "wc_1",
		Permission: "ro", CreatedAt: NowTS(), UpdatedAt: NowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	share.Permission = "rw"
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetSessionShare(ctx, share.ID)
	if err != nil || !ok || got.Permission != "rw" {
		t.Fatalf("share=%+v ok=%v err=%v", got, ok, err)
	}

	cat := SharedSessionCatalog{ID: NewID(), WorkspaceID: ws.ID, OwnerMembershipID: owner.ID,
		Name: "s1", Kind: "codex", Dir: "/repos/app@wt", Repo: "app@wt", WorkingCopyID: "wc_1",
		Title: "Review", CreatedAt: NowTS(), State: "stopped", Archived: true, LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, ws.ID, owner.ID, []SharedSessionCatalog{cat}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListSharedSessionCatalogByOwner(ctx, owner.ID)
	if err != nil || len(rows) != 1 || !rows[0].Archived || rows[0].WorkingCopyID != "wc_1" {
		t.Fatalf("catalog=%+v err=%v", rows, err)
	}

	p := SessionShareProposal{ID: NewID(), TenantID: tn.ID, CatalogID: rows[0].ID,
		OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn",
		Ciphertext: "opaque", Status: "pending", CreatedAt: NowTS(), ExpiresAt: NowTS()}
	if err := st.CreateSessionShareProposal(ctx, p); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountPendingSessionShareProposals(ctx, rows[0].ID); err != nil || n != 1 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
	if changed, err := st.TransitionSessionShareProposal(ctx, p.ID, "pending", "approved", ownerID.ID, NowTS(), true); err != nil || !changed {
		t.Fatalf("decide changed=%v err=%v", changed, err)
	}
	decided, ok, err := st.GetSessionShareProposal(ctx, p.ID)
	if err != nil || !ok || decided.Status != "approved" || decided.Ciphertext != "" {
		t.Fatalf("proposal=%+v ok=%v err=%v", decided, ok, err)
	}
	expired := p
	expired.ID = NewID()
	expired.Status = "pending"
	expired.Ciphertext = "opaque"
	expired.ExpiresAt = "2000-01-01T00:00:00Z"
	if err := st.CreateSessionShareProposal(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireSessionShareProposals(ctx, owner.ID, NowTS()); err != nil {
		t.Fatal(err)
	}
	gotExpired, ok, err := st.GetSessionShareProposal(ctx, expired.ID)
	if err != nil || !ok || gotExpired.Status != "expired" || gotExpired.Ciphertext != "" {
		t.Fatalf("expired proposal=%+v ok=%v err=%v", gotExpired, ok, err)
	}
	processing := expired
	processing.ID = NewID()
	processing.Status = "processing"
	processing.Ciphertext = "possibly-applied"
	if err := st.CreateSessionShareProposal(ctx, processing); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireSessionShareProposals(ctx, owner.ID, NowTS()); err != nil {
		t.Fatal(err)
	}
	gotProcessing, ok, err := st.GetSessionShareProposal(ctx, processing.ID)
	if err != nil || !ok || gotProcessing.Status != "expired" || gotProcessing.Ciphertext != "" {
		t.Fatalf("processing expiry=%+v ok=%v err=%v", gotProcessing, ok, err)
	}
}

func TestSessionShareClaimRechecksRWAndNeverRepeatsUnknown(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner@example.com", "claim-owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient@example.com", "claim-recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	workspace := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: NewID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s1", Kind: "codex", WorkingCopyID: "wc-1", LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: NewID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID, RecipientMembershipID: recipient.ID, ScopeType: "worktree", ScopeKey: "wc-1", Permission: "rw", CreatedAt: NowTS(), UpdatedAt: NowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	proposal := SessionShareProposal{ID: NewID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque", Status: "pending", CreatedAt: NowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}

	claimAt := time.Now().UTC()
	leaseUntil := claimAt.Add(shareOwnerLease)
	_, _, state, claimErr := st.ClaimSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID,
		claimAt.Format(time.RFC3339), leaseUntil.Format(time.RFC3339))
	if claimErr != nil || state != "claimed" {
		t.Fatalf("first state=%q err=%v", state, claimErr)
	}
	_, _, state, claimErr = st.ClaimSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, NowTS(), testShareLeaseUntil())
	if claimErr != nil || state != "processing" {
		t.Fatalf("retry state=%q err=%v", state, claimErr)
	}

	second := proposal
	second.ID = NewID()
	if err := st.CreateSessionShareProposal(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseSessionShareOwnerLease(ctx, owner.ID, proposal.ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.UpdateSessionSharePermission(ctx, share.ID, owner.ID, "ro", NowTS()); err != nil || !changed {
		t.Fatalf("downgrade changed=%v err=%v", changed, err)
	}
	_, _, state, claimErr = st.ClaimSessionShareProposal(ctx, second.ID, owner.ID, ownerIdentity.ID, NowTS(), testShareLeaseUntil())
	if claimErr != nil || state != "expired" {
		t.Fatalf("downgraded state=%q err=%v", state, claimErr)
	}
	unknown, ok, err := st.GetSessionShareProposal(ctx, proposal.ID)
	if err != nil || !ok || unknown.Status != "expired" || unknown.Ciphertext != "" {
		t.Fatalf("unknown after downgrade=%+v ok=%v err=%v", unknown, ok, err)
	}
}

func TestSessionShareClaimReleasesDBBeforeAgentIO(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner2@example.com", "race-owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient2@example.com", "race-recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	workspace := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: NewID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s1", Kind: "codex", WorkingCopyID: "wc-race", LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: NewID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID, RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "s1", Permission: "rw", CreatedAt: NowTS(), UpdatedAt: NowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	proposal := SessionShareProposal{ID: NewID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque", Status: "pending", CreatedAt: NowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}

	_, _, state, err := st.ClaimSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, NowTS(), testShareLeaseUntil())
	if err != nil || state != "claimed" {
		t.Fatalf("claim state=%q err=%v", state, err)
	}
	// The Agent request happens after Claim returns. An unrelated write must not
	// wait for that external I/O window on SQLite's global write lock.
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- st.InsertAudit(ctx, AuditLog{ID: NewID(), TenantID: tenant.ID, ActorKind: "system", ActorID: "test", Action: "share.claim.concurrent-write", At: NowTS()})
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated DB write remained blocked after proposal claim")
	}
	changed, err := st.FinalizeSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, NowTS())
	if err != nil || !changed {
		t.Fatalf("finalize changed=%v err=%v", changed, err)
	}
	got, ok, err := st.GetSessionShareProposal(ctx, proposal.ID)
	if err != nil || !ok || got.Status != "approved" {
		t.Fatalf("proposal=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestSessionShareExpiryPreservesLeasedProcessingAtDeadline(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "deadline-owner@example.com", "deadline-owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "deadline-recipient@example.com", "deadline-recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	workspace := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: NewID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "deadline", Kind: "codex", WorkingCopyID: "wc-deadline", LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: NewID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID, RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: catalog.Name, Permission: "rw", CreatedAt: NowTS(), UpdatedAt: NowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	claimAt := time.Now().UTC().Truncate(time.Second)
	proposal := SessionShareProposal{ID: NewID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque", Status: "pending", CreatedAt: claimAt.Format(time.RFC3339), ExpiresAt: claimAt.Add(time.Second).Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	_, _, state, err := st.ClaimSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID,
		claimAt.Format(time.RFC3339), claimAt.Add(shareOwnerLease).Format(time.RFC3339))
	if err != nil || state != "claimed" {
		t.Fatalf("claim state=%q err=%v", state, err)
	}
	pollAt := claimAt.Add(2 * time.Second).Format(time.RFC3339)
	if err := st.ExpireSessionShareProposals(ctx, owner.ID, pollAt); err != nil {
		t.Fatal(err)
	}
	processing, ok, err := st.GetSessionShareProposal(ctx, proposal.ID)
	if err != nil || !ok || processing.Status != "processing" || processing.Ciphertext == "" {
		t.Fatalf("processing proposal expired under active lease: %+v ok=%v err=%v", processing, ok, err)
	}
	changed, err := st.FinalizeSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, pollAt)
	if err != nil || !changed {
		t.Fatalf("finalize changed=%v err=%v", changed, err)
	}
}

func TestSessionShareProposalLimitIsAtomic(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "limit-owner@example.com", "limit-owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "limit-recipient@example.com", "limit-recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	workspace := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: NewID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s1", Kind: "codex", LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	const attempts, limit = 60, 20
	var created atomic.Int32
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := SessionShareProposal{ID: NewID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: owner.ID,
				ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque", Status: "pending",
				CreatedAt: NowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
			ok, err := st.CreateSessionShareProposalLimited(ctx, p, limit)
			if err != nil {
				errs <- err
			} else if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	n, err := st.CountPendingSessionShareProposals(ctx, catalog.ID)
	if err != nil || n != limit || created.Load() != limit {
		t.Fatalf("pending=%d created=%d err=%v", n, created.Load(), err)
	}
}

func TestSessionShareProposalListIsNoStoreThroughETagMiddleware(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	identity, _ := st.UpsertIdentity(ctx, "nostore@example.com", "nostore-owner", "")
	membership, _ := st.EnsureMembership(ctx, identity.ID, tenant.ID, "member")
	views, _ := st.ListMemberships(ctx, identity.ID)
	workspace := Workspace{ID: NewID(), TenantID: tenant.ID, MembershipID: membership.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: NewID(), WorkspaceID: workspace.ID, OwnerMembershipID: membership.ID, Name: "s1", Kind: "codex", LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, membership.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	secretPayload := `{"op":"start","prompt":"decrypted proposal text"}`
	proposal := SessionShareProposal{ID: NewID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: membership.ID,
		ProposerMembershipID: membership.ID, Action: "turn", Ciphertext: base64.StdEncoding.EncodeToString([]byte(secretPayload)),
		Status: "pending", CreatedAt: NowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
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
