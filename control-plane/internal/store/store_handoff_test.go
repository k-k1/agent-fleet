package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type handoffFixture struct {
	st              *SQL
	tenant          Tenant
	owner           Membership
	recipient       Membership
	other           Membership
	ws              Workspace
	catalog         SharedSessionCatalog
	sessionShareRow SessionShare
}

func newHandoffFixture(t *testing.T) handoffFixture {
	t.Helper()
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	oid, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	rid, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	xid, _ := st.UpsertIdentity(ctx, "other@example.com", "other", "")
	owner, _ := st.EnsureMembership(ctx, oid.ID, tn.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, rid.ID, tn.ID, "member")
	other, _ := st.EnsureMembership(ctx, xid.ID, tn.ID, "member")
	ws := Workspace{ID: NewID(), TenantID: tn.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n",
		DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: NewID(), TenantID: tn.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "s1",
		Permission: "ro", CreatedAt: NowTS(), UpdatedAt: NowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	cat := SharedSessionCatalog{ID: NewID(), WorkspaceID: ws.ID, OwnerMembershipID: owner.ID,
		Name: "s1", Kind: "claude", Dir: "/repos/app", Repo: "app", WorkingCopyID: "wc_1",
		CreatedAt: NowTS(), State: "running", LastSeen: NowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, ws.ID, owner.ID, []SharedSessionCatalog{cat}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListSharedSessionCatalogByOwner(ctx, owner.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("catalog rows=%v err=%v", rows, err)
	}
	return handoffFixture{st: st, tenant: tn, owner: owner, recipient: recipient, other: other,
		ws: ws, catalog: rows[0], sessionShareRow: share}
}

func (f handoffFixture) offer(t *testing.T, expiresAt string) SessionHandoffOffer {
	t.Helper()
	if expiresAt == "" {
		expiresAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	}
	return SessionHandoffOffer{ID: NewID(), TenantID: f.tenant.ID, CatalogID: f.catalog.ID,
		OwnerMembershipID: f.owner.ID, RecipientMembershipID: f.recipient.ID,
		Title: "続き", Ciphertext: "body", RepoRemote: "https://example.com/x.git", Branch: "temp/x",
		SourceSessionName: "s1", SourceSessionKind: "claude", Status: "pending",
		CreatedAt: NowTS(), ExpiresAt: expiresAt}
}

// TestHandoffOfferOnePendingPerSession: at most one pending offer per session (ADR 0057
// decision 10). Counting and rejecting lets two concurrent requests both through, so a
// partial unique index makes it impossible instead.
func TestHandoffOfferOnePendingPerSession(t *testing.T) {
	ctx := context.Background()
	f := newHandoffFixture(t)
	if created, err := f.st.CreateSessionHandoffOffer(ctx, f.offer(t, "")); err != nil || !created {
		t.Fatalf("first create=%v err=%v", created, err)
	}
	second := f.offer(t, "")
	second.RecipientMembershipID = f.other.ID // a different recipient is still a second offer
	created, err := f.st.CreateSessionHandoffOffer(ctx, second)
	if err != nil {
		t.Fatalf("second create err=%v", err)
	}
	if created {
		t.Fatal("a second pending offer was created for the same session")
	}
	// Once settled another can be offered — the withdraw-and-re-offer path.
	first, _ := f.st.ListSessionHandoffOffersByOwner(ctx, f.owner.ID)
	if _, err := f.st.TransitionSessionHandoffOffer(ctx, first[0].ID, "pending", "withdrawn", NowTS(), ""); err != nil {
		t.Fatal(err)
	}
	if created, err := f.st.CreateSessionHandoffOffer(ctx, second); err != nil || !created {
		t.Fatalf("re-offer after withdraw=%v err=%v", created, err)
	}
}

// TestHandoffOfferExpiresWhenShareRevoked is the ACL coupling that matters: an offer left
// in someone else's inbox after the share was revoked is the worst failure this feature
// has, because the body stays in their hands.
func TestHandoffOfferExpiresWhenShareRevoked(t *testing.T) {
	ctx := context.Background()
	f := newHandoffFixture(t)
	o := f.offer(t, "")
	if created, err := f.st.CreateSessionHandoffOffer(ctx, o); err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	if err := f.st.DeleteSessionShare(ctx, f.sessionShareRow.ID, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := f.st.GetSessionHandoffOffer(ctx, o.ID)
	if err != nil || !ok {
		t.Fatalf("get ok=%v err=%v", ok, err)
	}
	if got.Status != "expired" {
		t.Fatalf("status = %q, want expired after the share was revoked", got.Status)
	}
	if got.Ciphertext != "" {
		t.Fatal("the body survived the revocation")
	}
	inbox, err := f.st.ListSessionHandoffOffersByRecipient(ctx, f.recipient.ID)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("inbox=%v err=%v, want empty", inbox, err)
	}
}

// TestHandoffOfferSurvivesOnReadOnlyShare: a read-only share is enough to hand off. What
// a handoff needs is that the conversation can be read, not the right to propose actions;
// reusing the RW proposal's expiry condition (permission='rw') would drop a handoff made
// on an RO share the instant it was created.
func TestHandoffOfferSurvivesOnReadOnlyShare(t *testing.T) {
	ctx := context.Background()
	f := newHandoffFixture(t)
	o := f.offer(t, "")
	if created, err := f.st.CreateSessionHandoffOffer(ctx, o); err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	// Re-putting the share (still RO) touches permission and must not expire the offer.
	row := f.sessionShareRow
	row.UpdatedAt = NowTS()
	if err := f.st.PutSessionShare(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, _, err := f.st.GetSessionHandoffOffer(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" {
		t.Fatalf("status = %q, want pending (an RO share is enough to hand off)", got.Status)
	}
}

func TestHandoffOfferAcceptKeepsBodyDeclineClearsIt(t *testing.T) {
	ctx := context.Background()
	f := newHandoffFixture(t)
	accepted := f.offer(t, "")
	if _, err := f.st.CreateSessionHandoffOffer(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	if changed, err := f.st.TransitionSessionHandoffOffer(ctx, accepted.ID, "pending", "accepted", NowTS(), "new-session"); err != nil || !changed {
		t.Fatalf("accept changed=%v err=%v", changed, err)
	}
	got, _, _ := f.st.GetSessionHandoffOffer(ctx, accepted.ID)
	if got.Ciphertext == "" || got.AcceptedSessionName != "new-session" {
		t.Fatalf("accepted offer lost its body or session name: %+v", got)
	}
	// A second decision must not land: this conditional update is what closes the race
	// between A launching it themselves and B accepting.
	if changed, err := f.st.TransitionSessionHandoffOffer(ctx, accepted.ID, "pending", "withdrawn", NowTS(), ""); err != nil || changed {
		t.Fatalf("second decision changed=%v err=%v, want false", changed, err)
	}

	declined := f.offer(t, "")
	declined.CatalogID = f.catalog.ID
	if _, err := f.st.TransitionSessionHandoffOffer(ctx, accepted.ID, "accepted", "accepted", NowTS(), "new-session"); err != nil {
		t.Fatal(err)
	}
	if created, err := f.st.CreateSessionHandoffOffer(ctx, declined); err != nil || !created {
		t.Fatalf("create second=%v err=%v", created, err)
	}
	if _, err := f.st.TransitionSessionHandoffOffer(ctx, declined.ID, "pending", "declined", NowTS(), ""); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.st.GetSessionHandoffOffer(ctx, declined.ID); got.Ciphertext != "" {
		t.Fatal("a declined offer kept its body")
	}
}

func TestHandoffOfferExpiryReturnsRows(t *testing.T) {
	ctx := context.Background()
	f := newHandoffFixture(t)
	o := f.offer(t, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))
	if _, err := f.st.CreateSessionHandoffOffer(ctx, o); err != nil {
		t.Fatal(err)
	}
	expired, err := f.st.ExpireSessionHandoffOffers(ctx, NowTS())
	if err != nil || len(expired) != 1 || expired[0].ID != o.ID {
		t.Fatalf("expired=%v err=%v", expired, err)
	}
	// The second sweep returns nothing; that idempotence is what stops the expiry
	// notification from firing on every poll.
	if again, err := f.st.ExpireSessionHandoffOffers(ctx, NowTS()); err != nil || len(again) != 0 {
		t.Fatalf("second sweep=%v err=%v", again, err)
	}
}

// TestHandoffInboxHidesArchivedSession: an archived session's handoff stays out of the
// inbox, the same rule as docs/log/59 §1.
func TestHandoffInboxHidesArchivedSession(t *testing.T) {
	ctx := context.Background()
	f := newHandoffFixture(t)
	if _, err := f.st.CreateSessionHandoffOffer(ctx, f.offer(t, "")); err != nil {
		t.Fatal(err)
	}
	arch := f.catalog
	arch.Archived = true
	if err := f.st.ReplaceSharedSessionCatalog(ctx, f.ws.ID, f.owner.ID, []SharedSessionCatalog{arch}); err != nil {
		t.Fatal(err)
	}
	inbox, err := f.st.ListSessionHandoffOffersByRecipient(ctx, f.recipient.ID)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("inbox=%v err=%v, want empty while the owner has it archived", inbox, err)
	}
}
