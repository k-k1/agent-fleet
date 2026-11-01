package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ownHomeStub is a runtime whose home is the member's own volume (a per-user claim).
type ownHomeStub struct{ stubRuntime }

func (ownHomeStub) OwnHomeVolume() bool { return true }

// noticeFixture is a tenant with the subject member, one tenant admin, one plain member, plus a
// second tenant with its own admin. The agent's stats answer is switchable between samples.
type noticeFixture struct {
	st                        *store.SQL
	mgr                       *manager
	ws                        store.Workspace
	admin, member, otherAdmin store.Membership
	mu                        sync.Mutex
	stats                     string
}

func newNoticeFixture(t *testing.T, own bool) *noticeFixture {
	t.Helper()
	ctx := context.Background()
	st, mgr, tn, mem, ws := usageHourFixture(t)
	f := &noticeFixture{st: st, mgr: mgr, ws: ws, member: mem}
	add := func(tenantID, email, key, role string) store.Membership {
		id, err := st.UpsertIdentity(ctx, email, key, "")
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tenantID, role)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	f.admin = add(tn.ID, "boss@acme.co.jp", "boss-acme-co-jp", "tenant_admin")
	add(tn.ID, "peer@acme.co.jp", "peer-acme-co-jp", "member")
	other, _ := st.CreateTenant(ctx, "other", "他部")
	f.otherAdmin = add(other.ID, "oboss@other.co.jp", "oboss-other-co-jp", "tenant_admin")

	srv := diskAgentFunc(t, func() string { f.mu.Lock(); defer f.mu.Unlock(); return f.stats })
	var rt runtime.Runtime = stubRuntime{endpoint: srv.URL}
	if own {
		rt = ownHomeStub{stubRuntime{endpoint: srv.URL}}
	}
	mgr.rtFactory = sizedFactory{stubFactory{rt: rt}, runtime.DiskMeaningHome}
	return f
}

func (f *noticeFixture) setStats(s string) { f.mu.Lock(); f.stats = s; f.mu.Unlock() }

// sample runs one sweep on a fresh sampler: a new one per call is also what a CP restart is.
func (f *noticeFixture) sample() { newUsageSampler(f.mgr, 5*time.Minute).sample(context.Background()) }

func (f *noticeFixture) kinds(t *testing.T, m store.Membership, kind string) int {
	t.Helper()
	ns, err := f.st.ListNotifications(context.Background(), m.ID, "0000", 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range ns {
		if x.Kind == kind {
			n++
		}
	}
	return n
}

func TestHomeFullNotifiesTenantAdminsOncePerCrossing(t *testing.T) {
	f := newNoticeFixture(t, true)
	const full = `{"disk_used":102005473280,"disk_total":107374182400}` // 95 of 100 GiB
	f.setStats(full)

	f.sample()
	f.sample() // a second sweep of the same crossing
	f.sample() // and a restarted sampler
	if n := f.kinds(t, f.admin, "member-home-full"); n != 1 {
		t.Fatalf("tenant admin got %d notifications, want exactly 1 per crossing", n)
	}
	if n := f.kinds(t, f.otherAdmin, "member-home-full"); n != 0 {
		t.Errorf("an admin of another tenant got %d notifications, want 0 (cross-tenant leak)", n)
	}
	if n := f.kinds(t, f.member, "member-home-full"); n != 0 {
		t.Errorf("the plain member got %d admin notifications, want 0", n)
	}
	ns, _ := f.st.ListNotifications(context.Background(), f.admin.ID, "0000", 10)
	if ns[0].TargetType != "member" || ns[0].TargetID != "w-acme-co-jp" || ns[0].DisplayName != "w@acme.co.jp" {
		t.Errorf("notification does not point at the member: %+v", ns[0])
	}

	// Inside the hysteresis band (88 GiB used: plenty free, but not below 85 %): still raised.
	f.setStats(`{"disk_used":94489280512,"disk_total":107374182400}`)
	f.sample()
	f.setStats(full)
	f.sample()
	if n := f.kinds(t, f.admin, "member-home-full"); n != 1 {
		t.Fatalf("a dip inside the hysteresis band re-notified: %d", n)
	}

	// Real recovery clears the mark; the next crossing notifies again.
	f.setStats(`{"disk_used":53687091200,"disk_total":107374182400}`) // 50 GiB
	f.sample()
	f.setStats(full)
	f.sample()
	if n := f.kinds(t, f.admin, "member-home-full"); n != 2 {
		t.Fatalf("a crossing after recovery gave %d notifications in total, want 2", n)
	}
}

func TestHomeFullStaysQuietWithoutAKnownReadingOrOwnHome(t *testing.T) {
	shared := newNoticeFixture(t, false)
	shared.setStats(`{"disk_used":9900,"disk_total":10000}`)
	shared.sample()
	if n := shared.kinds(t, shared.admin, "member-home-full"); n != 0 {
		t.Errorf("a shared home notified: %d", n)
	}

	own := newNoticeFixture(t, true)
	own.setStats(`{"mem_used":1}`) // answered, but no disk figures
	own.sample()
	if n := own.kinds(t, own.admin, "member-home-full"); n != 0 {
		t.Errorf("an unknown reading notified: %d", n)
	}
	// A stopped workspace is not read at all.
	own.setStats(`{"disk_used":9900,"disk_total":10000}`)
	if err := own.st.SetWorkspaceState(context.Background(), own.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	own.mgr.rtFactory = sizedFactory{stubFactory{rt: ownHomeStub{stubRuntime{endpoint: "http://127.0.0.1:1", state: "stopped"}}}, runtime.DiskMeaningHome}
	own.sample()
	if n := own.kinds(t, own.admin, "member-home-full"); n != 0 {
		t.Errorf("a stopped workspace notified: %d", n)
	}
}

func TestBootInstallFailureNotifiesAdminsOncePerFailure(t *testing.T) {
	// Boot outcomes are recorded on every runtime, own home or not.
	f := newNoticeFixture(t, false)
	f.setStats(`{"boot_install_failed":{"claude":"network"}}`)
	f.sample()
	f.sample()
	if n := f.kinds(t, f.admin, "member-boot-install"); n != 1 {
		t.Fatalf("admin got %d boot notifications, want 1", n)
	}
	if n := f.kinds(t, f.otherAdmin, "member-boot-install"); n != 0 {
		t.Errorf("other tenant's admin got %d, want 0", n)
	}
	f.setStats(`{}`) // a clean start withdraws it
	f.sample()
	f.setStats(`{"boot_install_failed":{"codex":"network"}}`)
	f.sample()
	if n := f.kinds(t, f.admin, "member-boot-install"); n != 2 {
		t.Fatalf("a later failure gave %d in total, want 2", n)
	}
}

func TestBootInstallSkippedUpgradeIsNotAFailure(t *testing.T) {
	f := newNoticeFixture(t, false)
	f.setStats(`{"boot_install_failed":{"claude":"skipped_no_space"}}`)
	f.sample()
	if n := f.kinds(t, f.admin, "member-boot-install"); n != 0 {
		t.Errorf("a skipped upgrade notified: %d", n)
	}
}
