package main

import (
	"context"
	"path/filepath"
	"testing"
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
}
