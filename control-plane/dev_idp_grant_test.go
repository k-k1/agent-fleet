package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// An unlisted port the owner holds a live grant for is a host the owner can open, so the
// redirect ACL admits it for the owner and for nobody else (#2500).
func TestDevIdPRedirectHonoursTheOwnersLiveGrant(t *testing.T) {
	d := newDevIdPTestEnv(t)
	ctx := context.Background()
	colleague := d.addViewer(t, "colleague")
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	cur := func() (store.Workspace, wsSettings) {
		ws, ok, err := d.mgr.store.GetWorkspaceByMembership(ctx, d.ws.MembershipID)
		if err != nil || !ok {
			t.Fatalf("workspace: %v", err)
		}
		return ws, parseWSSettings(mustSettings(ctx, d.mgr, ws.ID))
	}
	allowed := func(membershipID, uri string) bool {
		ws, st := cur()
		return d.mgr.devIdPRedirectAllowed(ctx, ws, st, membershipID, uri)
	}
	grant := func(port int, slug, inodes string) {
		t.Helper()
		if err := d.mgr.store.UpsertPreviewTempPort(ctx, store.PreviewTempPort{
			WorkspaceID: d.ws.ID, Port: port, Slug: slug, Inodes: inodes, CreatedAt: store.NowTS()}); err != nil {
			t.Fatal(err)
		}
	}
	uri := d.ownHost(5173) + "/cb"
	owner := d.ws.MembershipID

	if allowed(owner, uri) {
		t.Fatal("unlisted port without a grant was admitted")
	}
	grant(5173, d.slug, "111")
	if !allowed(owner, uri) {
		t.Fatal("owner with a live grant was refused")
	}
	// The same decision end to end through /authorize.
	if rec := d.authorizeAs(t, "preview-user", d.authQuery(uri, "")); rec.Code != http.StatusFound {
		t.Errorf("authorize on a granted port: %d %s", rec.Code, rec.Body)
	}

	// A tenant-share colleague may open listed ports, never the owner's granted one.
	if allowed(colleague.membershipID, uri) {
		t.Error("tenant-share colleague admitted on an owner-only granted port")
	}
	if !allowed(colleague.membershipID, d.ownHost(3000)+"/cb") {
		t.Error("control: colleague refused on a listed port")
	}
	// Another port with no grant stays refused, and so does a malformed membership.
	if allowed(owner, d.ownHost(5174)+"/cb") {
		t.Error("a port with no grant was admitted")
	}
	if allowed("", uri) || allowed("no-such-membership", uri) {
		t.Error("unknown membership admitted")
	}
	// Another workspace's grant: current slug and valid inodes, only the workspace id differs,
	// so a missing workspace scope in the lookup is the one thing that could admit it.
	if err := d.mgr.store.UpsertPreviewTempPort(ctx, store.PreviewTempPort{
		WorkspaceID: "ws-colleague", Port: 6000, Slug: d.slug, Inodes: "9", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	if allowed(owner, d.ownHost(6000)+"/cb") {
		t.Error("another workspace's grant admitted")
	}
	grant(6000, d.slug, "9") // positive control: the same row under the owner's workspace is read
	if !allowed(owner, d.ownHost(6000)+"/cb") {
		t.Error("control: own grant on 6000 refused")
	}
	// A grant with no recorded listener is not live.
	grant(5175, d.slug, "")
	if allowed(owner, d.ownHost(5175)+"/cb") {
		t.Error("grant without inodes admitted")
	}

	// Revoked: the row is gone.
	if err := d.mgr.store.ClearPreviewTempPorts(ctx, d.ws.ID); err != nil {
		t.Fatal(err)
	}
	if allowed(owner, uri) {
		t.Error("revoked grant admitted")
	}

	// Expired: the grant belongs to the previous slug after a restart.
	grant(5173, d.slug, "111")
	if !allowed(owner, uri) {
		t.Fatal("control: fresh grant refused")
	}
	oldSlug := d.slug
	fresh := d.mintSlug(t)
	d.slug = fresh
	// Re-insert a grant row under the OLD slug: only the slug comparison can refuse it now.
	grant(5173, oldSlug, "111")
	if allowed(owner, d.ownHost(5173)+"/cb") {
		t.Error("grant from the previous slug admitted under the new slug")
	}
	grant(5173, fresh, "111") // positive control: the lookup reads this row
	if !allowed(owner, d.ownHost(5173)+"/cb") {
		t.Error("control: grant under the new slug refused")
	}
	// A stopped workspace has no slug at all.
	ws, st := cur()
	ws.PreviewSlug = ""
	uri = d.ownHost(5173) + "/cb"
	if d.mgr.devIdPRedirectAllowed(ctx, ws, st, owner, uri) {
		t.Error("stopped workspace admitted")
	}
}

// A store failure while looking the grant up must refuse, not admit.
func TestDevIdPRedirectFailsClosedWhenTheGrantLookupFails(t *testing.T) {
	d := newDevIdPTestEnv(t)
	ctx := context.Background()
	if err := d.mgr.store.UpsertPreviewTempPort(ctx, store.PreviewTempPort{
		WorkspaceID: d.ws.ID, Port: 5173, Slug: d.slug, Inodes: "111", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	ws, ok, _ := d.mgr.store.GetWorkspaceByMembership(ctx, d.ws.MembershipID)
	if !ok {
		t.Fatal("no workspace")
	}
	if !d.mgr.previewPortOpenableBy(ctx, ws, wsSettings{}, ws.MembershipID, 5173) {
		t.Fatal("control: granted port refused")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel() // a cancelled context makes the store call fail
	if d.mgr.previewPortOpenableBy(cctx, ws, wsSettings{}, ws.MembershipID, 5173) {
		t.Error("lookup error admitted the port")
	}
}
