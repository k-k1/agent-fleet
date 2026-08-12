package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionShareStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
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
	ws := Workspace{ID: newID(), TenantID: tn.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	share := SessionShare{ID: newID(), TenantID: tn.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "worktree", ScopeKey: "wc_1",
		Permission: "ro", CreatedAt: nowTS(), UpdatedAt: nowTS()}
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

	cat := SharedSessionCatalog{ID: newID(), WorkspaceID: ws.ID, OwnerMembershipID: owner.ID,
		Name: "s1", Kind: "codex", Dir: "/repos/app@wt", Repo: "app@wt", WorkingCopyID: "wc_1",
		Title: "Review", CreatedAt: nowTS(), State: "stopped", Archived: true, LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, ws.ID, owner.ID, []SharedSessionCatalog{cat}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListSharedSessionCatalogByOwner(ctx, owner.ID)
	if err != nil || len(rows) != 1 || !rows[0].Archived || rows[0].WorkingCopyID != "wc_1" {
		t.Fatalf("catalog=%+v err=%v", rows, err)
	}

	p := SessionShareProposal{ID: newID(), TenantID: tn.ID, CatalogID: rows[0].ID,
		OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn",
		Ciphertext: "opaque", Status: "pending", CreatedAt: nowTS(), ExpiresAt: nowTS()}
	if err := st.CreateSessionShareProposal(ctx, p); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountPendingSessionShareProposals(ctx, rows[0].ID); err != nil || n != 1 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
	if changed, err := st.TransitionSessionShareProposal(ctx, p.ID, "pending", "approved", ownerID.ID, nowTS(), true); err != nil || !changed {
		t.Fatalf("decide changed=%v err=%v", changed, err)
	}
	decided, ok, err := st.GetSessionShareProposal(ctx, p.ID)
	if err != nil || !ok || decided.Status != "approved" || decided.Ciphertext != "" {
		t.Fatalf("proposal=%+v ok=%v err=%v", decided, ok, err)
	}
	expired := p
	expired.ID = newID()
	expired.Status = "pending"
	expired.Ciphertext = "opaque"
	expired.ExpiresAt = "2000-01-01T00:00:00Z"
	if err := st.CreateSessionShareProposal(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireSessionShareProposals(ctx, owner.ID, nowTS()); err != nil {
		t.Fatal(err)
	}
	gotExpired, ok, err := st.GetSessionShareProposal(ctx, expired.ID)
	if err != nil || !ok || gotExpired.Status != "expired" || gotExpired.Ciphertext != "" {
		t.Fatalf("expired proposal=%+v ok=%v err=%v", gotExpired, ok, err)
	}
	processing := expired
	processing.ID = newID()
	processing.Status = "processing"
	processing.Ciphertext = "possibly-applied"
	if err := st.CreateSessionShareProposal(ctx, processing); err != nil {
		t.Fatal(err)
	}
	if err := st.ExpireSessionShareProposals(ctx, owner.ID, nowTS()); err != nil {
		t.Fatal(err)
	}
	gotProcessing, ok, err := st.GetSessionShareProposal(ctx, processing.ID)
	if err != nil || !ok || gotProcessing.Status != "expired" || gotProcessing.Ciphertext != "" {
		t.Fatalf("processing expiry=%+v ok=%v err=%v", gotProcessing, ok, err)
	}
}

func TestSessionShareClaimRechecksRWAndNeverRepeatsUnknown(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
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
	workspace := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: newID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s1", Kind: "codex", WorkingCopyID: "wc-1", LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: newID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID, RecipientMembershipID: recipient.ID, ScopeType: "worktree", ScopeKey: "wc-1", Permission: "rw", CreatedAt: nowTS(), UpdatedAt: nowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	proposal := SessionShareProposal{ID: newID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque", Status: "pending", CreatedAt: nowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	state, applyErr := st.ClaimAndApplySessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, nowTS(), func(SessionShareProposal, SharedSessionCatalog) error {
		calls.Add(1)
		return context.DeadlineExceeded // models response loss after an unknown outcome
	})
	if applyErr == nil || state != "processing" || calls.Load() != 1 {
		t.Fatalf("first state=%q err=%v calls=%d", state, applyErr, calls.Load())
	}
	state, applyErr = st.ClaimAndApplySessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, nowTS(), func(SessionShareProposal, SharedSessionCatalog) error {
		calls.Add(1)
		return nil
	})
	if applyErr != nil || state != "processing" || calls.Load() != 1 {
		t.Fatalf("retry state=%q err=%v calls=%d", state, applyErr, calls.Load())
	}

	second := proposal
	second.ID = newID()
	if err := st.CreateSessionShareProposal(ctx, second); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.UpdateSessionSharePermission(ctx, share.ID, owner.ID, "ro", nowTS()); err != nil || !changed {
		t.Fatalf("downgrade changed=%v err=%v", changed, err)
	}
	state, applyErr = st.ClaimAndApplySessionShareProposal(ctx, second.ID, owner.ID, ownerIdentity.ID, nowTS(), func(SessionShareProposal, SharedSessionCatalog) error {
		calls.Add(1)
		return nil
	})
	if applyErr != nil || state != "expired" || calls.Load() != 1 {
		t.Fatalf("downgraded state=%q err=%v calls=%d", state, applyErr, calls.Load())
	}
	unknown, ok, err := st.GetSessionShareProposal(ctx, proposal.ID)
	if err != nil || !ok || unknown.Status != "expired" || unknown.Ciphertext != "" {
		t.Fatalf("unknown after downgrade=%+v ok=%v err=%v", unknown, ok, err)
	}
}

func TestSessionShareACLChangeSerializesWithClaimedApply(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
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
	workspace := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: newID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "s1", Kind: "codex", WorkingCopyID: "wc-race", LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: newID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID, RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "s1", Permission: "rw", CreatedAt: nowTS(), UpdatedAt: nowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	proposal := SessionShareProposal{ID: newID(), TenantID: tenant.ID, CatalogID: catalog.ID, OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque", Status: "pending", CreatedAt: nowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	applyDone := make(chan error, 1)
	go func() {
		state, err := st.ClaimAndApplySessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, nowTS(), func(SessionShareProposal, SharedSessionCatalog) error {
			close(entered)
			<-release
			return nil
		})
		if err == nil && state != "approved" {
			err = context.Canceled
		}
		applyDone <- err
	}()
	<-entered
	downgradeDone := make(chan error, 1)
	go func() {
		_, err := st.UpdateSessionSharePermission(ctx, share.ID, owner.ID, "ro", nowTS())
		downgradeDone <- err
	}()
	select {
	case err := <-downgradeDone:
		t.Fatalf("ACL downgrade crossed an in-flight authorized apply: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-downgradeDone; err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetSessionShareProposal(ctx, proposal.ID)
	if err != nil || !ok || got.Status != "approved" {
		t.Fatalf("proposal=%+v ok=%v err=%v", got, ok, err)
	}
}
