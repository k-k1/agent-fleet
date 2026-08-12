package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type reaperFenceRuntime struct {
	stops        atomic.Int32
	fenceEntered chan struct{}
	fenceRelease chan struct{}
	endpoint     string
	busy         atomic.Bool
}

func (r *reaperFenceRuntime) Start(context.Context) error  { return nil }
func (r *reaperFenceRuntime) Stop(context.Context) error   { r.stops.Add(1); return nil }
func (r *reaperFenceRuntime) State(context.Context) string { return "running" }
func (r *reaperFenceRuntime) Endpoint() string             { return r.endpoint }
func (r *reaperFenceRuntime) Token() string                { return "" }
func (r *reaperFenceRuntime) Name() string                 { return "reaper-fence" }
func (r *reaperFenceRuntime) AcquireOperationFence(ctx context.Context) (func(), error) {
	close(r.fenceEntered)
	select {
	case <-r.fenceRelease:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newReaperFenceRuntime(t *testing.T) *reaperFenceRuntime {
	t.Helper()
	r := &reaperFenceRuntime{fenceEntered: make(chan struct{}), fenceRelease: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		state := "idle"
		if r.busy.Load() {
			state = "working"
		}
		_, _ = w.Write([]byte(`{"sessions":[{"name":"s","alive":true,"state":"` + state + `"}]}`))
	}))
	r.endpoint = srv.URL
	t.Cleanup(srv.Close)
	return r
}

func reaperLifecycleFixture(t *testing.T) (*sqlStore, Workspace, *manager) {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	identity, _ := st.UpsertIdentity(ctx, "reaper-fence@example.com", "reaper-fence", "")
	membership, _ := st.EnsureMembership(ctx, identity.ID, tenant.ID, "member")
	ws := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: membership.ID,
		ContainerName: "reaper-fence", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
		State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	return st, ws, &manager{store: st, conns: newConnRegistry()}
}

func TestReaperStopUsesLifecycleLeaseAndRuntimeFence(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	rp := &reaper{mgr: mgr}

	// An approved share operation owns the same distributed lease. The reaper
	// must skip this sweep without reaching Runtime.Stop.
	op := newID()
	now := time.Now().UTC()
	acquired, err := st.AcquireSessionShareOwnerLease(ctx, ws.MembershipID, op, leaseTS(now), leaseTS(now.Add(time.Minute)))
	if err != nil || !acquired {
		t.Fatalf("acquire share lease: acquired=%v err=%v", acquired, err)
	}
	blockedRT := newReaperFenceRuntime(t)
	rp.stopWorkspace(ctx, blockedRT, ws, time.Second)
	if blockedRT.stops.Load() != 0 {
		t.Fatal("reaper stopped workspace while share owner lease was active")
	}
	select {
	case <-blockedRT.fenceEntered:
		t.Fatal("reaper reached runtime fence before acquiring the DB lease")
	default:
	}
	if err := st.ReleaseSessionShareOwnerLease(ctx, ws.MembershipID, op); err != nil {
		t.Fatal(err)
	}

	// Once the DB lease is available, Stop must still wait behind the host fence
	// held by an old native lifecycle operation.
	rt := newReaperFenceRuntime(t)
	done := make(chan struct{})
	go func() {
		rp.stopWorkspace(ctx, rt, ws, time.Second)
		close(done)
	}()
	select {
	case <-rt.fenceEntered:
	case <-time.After(time.Second):
		t.Fatal("reaper did not reach runtime fence")
	}
	if rt.stops.Load() != 0 {
		t.Fatal("reaper crossed the native runtime fence")
	}
	close(rt.fenceRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reaper did not finish after runtime fence release")
	}
	if rt.stops.Load() != 1 {
		t.Fatalf("Stop calls = %d, want 1", rt.stops.Load())
	}
}

func TestReaperStopWaitsForLocalWorkspaceOperation(t *testing.T) {
	_, ws, mgr := reaperLifecycleFixture(t)
	rp := &reaper{mgr: mgr}
	rt := newReaperFenceRuntime(t)
	lock := mgr.startLockFor(ws.ID)
	lock.Lock() // same-CP shared approval/recreate owns this before its side effect
	done := make(chan struct{})
	go func() {
		rp.stopWorkspace(context.Background(), rt, ws, time.Second)
		close(done)
	}()
	select {
	case <-rt.fenceEntered:
		t.Fatal("reaper crossed the local workspace mutex")
	case <-time.After(50 * time.Millisecond):
	}
	lock.Unlock()
	select {
	case <-rt.fenceEntered:
	case <-time.After(time.Second):
		t.Fatal("reaper did not proceed after local operation completed")
	}
	close(rt.fenceRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reaper did not finish after all fences released")
	}
}

func TestReaperRevalidatesActivityAfterFenceWait(t *testing.T) {
	tests := []struct {
		name     string
		activate func(*manager, *reaperFenceRuntime, Workspace) error
	}{
		{name: "connection resumed", activate: func(m *manager, _ *reaperFenceRuntime, ws Workspace) error {
			m.conns.addConn(ws.ID, "")
			return nil
		}},
		{name: "request touched workspace", activate: func(m *manager, _ *reaperFenceRuntime, ws Workspace) error {
			m.conns.touch(ws.ID)
			return nil
		}},
		{name: "database activity refreshed", activate: func(m *manager, _ *reaperFenceRuntime, ws Workspace) error {
			return m.store.SetWorkspaceState(context.Background(), ws.ID, "running")
		}},
		{name: "agent became busy", activate: func(_ *manager, rt *reaperFenceRuntime, _ Workspace) error {
			rt.busy.Store(true)
			return nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ws, mgr := reaperLifecycleFixture(t)
			rp := &reaper{mgr: mgr}
			rt := newReaperFenceRuntime(t)
			done := make(chan struct{})
			go func() {
				rp.stopWorkspace(context.Background(), rt, ws, time.Second)
				close(done)
			}()
			select {
			case <-rt.fenceEntered:
			case <-time.After(time.Second):
				t.Fatal("reaper did not wait at runtime fence")
			}
			if err := tc.activate(mgr, rt, ws); err != nil {
				t.Fatal(err)
			}
			close(rt.fenceRelease)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("reaper did not finish activity revalidation")
			}
			if rt.stops.Load() != 0 {
				t.Fatal("reaper stopped workspace that became active during fence wait")
			}
		})
	}
}

func TestReaperObservesActivityFromAnotherControlPlane(t *testing.T) {
	tests := []struct {
		name     string
		activate func(context.Context, *manager, Workspace) func()
	}{
		{name: "remote request", activate: func(ctx context.Context, m *manager, ws Workspace) func() {
			if err := m.touchWorkspace(ctx, ws.ID); err != nil {
				panic(err)
			}
			return func() {}
		}},
		{name: "remote live connection", activate: func(ctx context.Context, m *manager, ws Workspace) func() {
			release, err := m.trackWorkspaceConnection(ctx, ws.ID, "session-a")
			if err != nil {
				panic(err)
			}
			return release
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, ws, managerB := reaperLifecycleFixture(t)
			managerA := &manager{store: st, conns: newConnRegistry()}
			releaseActivity := tc.activate(context.Background(), managerA, ws)
			defer releaseActivity()

			rt := newReaperFenceRuntime(t)
			close(rt.fenceRelease) // no lifecycle conflict; test shared activity only
			(&reaper{mgr: managerB}).stopWorkspace(context.Background(), rt, ws, time.Minute)
			if rt.stops.Load() != 0 {
				t.Fatal("reaper stopped activity served by another CP replica")
			}
		})
	}
}

func TestWorkspaceActivityMergeNeverShortensRemotePresence(t *testing.T) {
	st, ws, _ := reaperLifecycleFixture(t)
	now := time.Now().UTC()
	if ok, err := st.RecordWorkspaceActivity(context.Background(), ws.ID, leaseTS(now), leaseTS(now.Add(time.Minute)), leaseTS(now)); err != nil || !ok {
		t.Fatal(err)
	}
	// An inactive replica reports connectedUntil=now. The existing future lease
	// from another replica must remain authoritative.
	if ok, err := st.RecordWorkspaceActivity(context.Background(), ws.ID, leaseTS(now.Add(time.Second)), leaseTS(now), leaseTS(now)); err != nil || !ok {
		t.Fatal(err)
	}
	active, err := st.WorkspaceHasRecentActivity(context.Background(), ws.ID, leaseTS(now.Add(time.Hour)), leaseTS(now.Add(30*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("later inactive report shortened another replica's connection lease")
	}
}

func TestWorkspaceIdleStopClaimRejectsLaterActivity(t *testing.T) {
	st, ws, _ := reaperLifecycleFixture(t)
	ctx := context.Background()
	lease, err := acquireWorkspaceLifecycleLease(ctx, st, ws.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	now := time.Now().UTC()
	claimed, err := st.ClaimWorkspaceIdleStop(ctx, ws.ID, ws.MembershipID, lease.token,
		leaseTS(now.Add(time.Hour)), leaseTS(now))
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	managerA := &manager{store: st, conns: newConnRegistry()}
	if err := managerA.touchWorkspace(ctx, ws.ID); !errors.Is(err, errWorkspaceStopping) {
		t.Fatalf("activity after stop claim error=%v, want workspace stopping", err)
	}
	if err := st.ReleaseWorkspaceIdleStop(ctx, ws.ID, lease.token); err != nil {
		t.Fatal(err)
	}
	if err := managerA.touchWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("activity after intent release: %v", err)
	}
}

func TestWorkspaceActivityCoalescesWithinProtectedLease(t *testing.T) {
	st, ws, _ := reaperLifecycleFixture(t)
	m := &manager{store: st, conns: newConnRegistry()}
	ctx := context.Background()
	if err := m.touchWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM workspace_activity WHERE workspace_id=?`, ws.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := m.touchWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	var second string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM workspace_activity WHERE workspace_id=?`, ws.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("coalesced request performed another DB write: first=%s second=%s", first, second)
	}
}

// TestIdleBase pins the tier-2 idle clock's start to the LATEST of the three
// activity signals (boot time / in-memory lastSeen / DB last_active_at). The
// headline case is the regression that made a just-started workspace stop right
// after coming up: a stale in-memory lastSeen must NOT mask a fresh DB
// last_active_at written by the Start.
func TestIdleBase(t *testing.T) {
	boot := time.Date(2026, 7, 12, 4, 10, 0, 0, time.UTC)
	rp := &reaper{bootTime: boot}
	rfc := func(h, m int) string {
		return time.Date(2026, 7, 12, h, m, 0, 0, time.UTC).Format(time.RFC3339)
	}
	ts := func(h, m int) time.Time {
		return time.Date(2026, 7, 12, h, m, 0, 0, time.UTC)
	}

	cases := []struct {
		name         string
		seen         bool
		lastSeen     time.Time
		dbLastActive string
		want         time.Time
	}{
		{
			// The bug: an old terminal left an in-memory lastSeen (05:00), the user
			// then Start-ed hours later (DB last_active_at bumped to 07:41). base must
			// follow the fresh DB stamp, not the stale in-memory one.
			name:         "fresh DB start wins over stale in-memory lastSeen",
			seen:         true,
			lastSeen:     ts(5, 0),
			dbLastActive: rfc(7, 41),
			want:         ts(7, 41),
		},
		{
			// No in-memory record and a stale DB stamp: fall back to boot time so a CP
			// restart grants a fresh grace window.
			name:         "boot floor when nothing newer",
			seen:         false,
			lastSeen:     time.Time{},
			dbLastActive: rfc(3, 0),
			want:         boot,
		},
		{
			// Live proxy/preview traffic (recent lastSeen) beats an older DB stamp.
			name:         "recent in-memory lastSeen wins",
			seen:         true,
			lastSeen:     ts(6, 30),
			dbLastActive: rfc(5, 0),
			want:         ts(6, 30),
		},
		{
			// Unparseable/empty DB value is ignored, not treated as zero-time.
			name:         "empty DB last_active ignored",
			seen:         true,
			lastSeen:     ts(6, 0),
			dbLastActive: "",
			want:         ts(6, 0),
		},
		{
			// seen==false must still consult the DB (this is the branch the old
			// `if !seen` guard covered — kept working).
			name:         "DB consulted when unseen",
			seen:         false,
			lastSeen:     time.Time{},
			dbLastActive: rfc(7, 41),
			want:         ts(7, 41),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rp.idleBase(tc.seen, tc.lastSeen, tc.dbLastActive)
			if !got.Equal(tc.want) {
				t.Fatalf("idleBase = %s, want %s", got, tc.want)
			}
		})
	}
}
