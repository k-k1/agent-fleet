package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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

type operationFenceGateStore struct {
	Store
	mu sync.Mutex
}

func (s *operationFenceGateStore) AcquireWorkspaceOperationFence(context.Context, string) (func(), error) {
	s.mu.Lock()
	var once sync.Once
	return func() { once.Do(s.mu.Unlock) }, nil
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

func TestExplicitStartClearsOrphanedIdleStopIntentWhileRunning(t *testing.T) {
	st, ws, mgr := reaperLifecycleFixture(t)
	ctx := context.Background()
	old, err := acquireWorkspaceLifecycleLease(ctx, st, ws.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := st.ClaimWorkspaceIdleStop(ctx, ws.ID, ws.MembershipID, old.token,
		leaseTS(now.Add(time.Hour)), leaseTS(now))
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	old.Close() // simulate the crashed holder's owner lease eventually disappearing

	rt := newReaperFenceRuntime(t)
	close(rt.fenceRelease)
	res := &resolved{rt: rt, ws: ws, mv: MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStartedRT(ctx, res, rt); aerr != nil {
		t.Fatalf("explicit Start reconciliation: %+v", aerr)
	}
	var intents int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_stop_intent WHERE workspace_id=?`, ws.ID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("orphaned stop intents after running Start = %d", intents)
	}
	if err := mgr.touchWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("activity remained blocked after explicit Start: %v", err)
	}
}

func TestWorkspaceOperationFenceBlocksSecondControlPlaneWithoutAdapterFence(t *testing.T) {
	st, ws, _ := reaperLifecycleFixture(t)
	gate := &operationFenceGateStore{Store: st}
	mgrA := &manager{store: gate}
	mgrB := &manager{store: gate}
	rt := &shareLifecycleRuntime{} // Docker/ECS-like: no runtimeOperationFencer
	releaseA, err := mgrA.acquireWorkspaceOperationFence(context.Background(), ws.ID, rt)
	if err != nil {
		t.Fatal(err)
	}
	acquiredB := make(chan func(), 1)
	go func() {
		release, err := mgrB.acquireWorkspaceOperationFence(context.Background(), ws.ID, rt)
		if err == nil {
			acquiredB <- release
		}
	}()
	select {
	case release := <-acquiredB:
		release()
		t.Fatal("second CP crossed the distributed operation fence")
	case <-time.After(50 * time.Millisecond):
	}
	releaseA()
	select {
	case release := <-acquiredB:
		release()
	case <-time.After(time.Second):
		t.Fatal("second CP did not proceed after old holder quiesced")
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

// --- Tier 3: home hibernation (ADR 0045 決定 13-2) ---

// hibernateStub is a runtime that CAN put its home away. Everything tier 3 decides is
// decided against this: it never actually goes to AWS.
type hibernateStub struct {
	state  string
	begins atomic.Int32
	err    error
}

func (r *hibernateStub) Start(context.Context) error  { return nil }
func (r *hibernateStub) Stop(context.Context) error   { return nil }
func (r *hibernateStub) State(context.Context) string { return r.state }
func (r *hibernateStub) Endpoint() string             { return "" }
func (r *hibernateStub) Token() string                { return "" }
func (r *hibernateStub) Name() string                 { return "hib-stub" }
func (r *hibernateStub) BeginHibernate(context.Context) error {
	r.begins.Add(1)
	return r.err
}

// plainStub is every other runtime: no cheaper place to put a home, so tier 3 must be
// absent rather than half-working.
type plainStub struct{ state string }

func (r *plainStub) Start(context.Context) error  { return nil }
func (r *plainStub) Stop(context.Context) error   { return nil }
func (r *plainStub) State(context.Context) string { return r.state }
func (r *plainStub) Endpoint() string             { return "" }
func (r *plainStub) Token() string                { return "" }
func (r *plainStub) Name() string                 { return "plain-stub" }

func setLastActive(t *testing.T, st *sqlStore, wsID string, at time.Time) string {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	if _, err := st.db.Exec(`UPDATE workspace SET last_active_at=? WHERE id=?`, ts, wsID); err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestReaperHibernatesOnlyAColdHome(t *testing.T) {
	ctx := context.Background()
	month := 30 * 24 * time.Hour

	t.Run("a home nobody has opened for two months", func(t *testing.T) {
		st, ws, mgr := reaperLifecycleFixture(t)
		ws.LastActiveAt = setLastActive(t, st, ws.ID, time.Now().Add(-60*24*time.Hour))
		rt := &hibernateStub{state: "stopped"}
		(&reaper{mgr: mgr, bootTime: time.Now()}).hibernateHome(ctx, rt, ws, month)
		if rt.begins.Load() != 1 {
			t.Fatalf("BeginHibernate calls = %d, want 1", rt.begins.Load())
		}
	})

	t.Run("a CP restart does not push the deadline out", func(t *testing.T) {
		// The regression this guards: tier 2's idleBase starts its clock at the reaper's
		// boot time, which is right for minutes and fatal for weeks — a CP that restarts
		// more often than the window would leave the setting enabled and never firing.
		st, ws, mgr := reaperLifecycleFixture(t)
		ws.LastActiveAt = setLastActive(t, st, ws.ID, time.Now().Add(-60*24*time.Hour))
		rt := &hibernateStub{state: "stopped"}
		rp := &reaper{mgr: mgr, bootTime: time.Now()} // just booted
		rp.hibernateHome(ctx, rt, ws, month)
		if rt.begins.Load() != 1 {
			t.Fatalf("a fresh CP boot suppressed hibernation entirely (calls = %d)", rt.begins.Load())
		}
	})

	t.Run("not before the window", func(t *testing.T) {
		st, ws, mgr := reaperLifecycleFixture(t)
		ws.LastActiveAt = setLastActive(t, st, ws.ID, time.Now().Add(-2*time.Hour))
		rt := &hibernateStub{state: "stopped"}
		(&reaper{mgr: mgr, bootTime: time.Now()}).hibernateHome(ctx, rt, ws, month)
		if rt.begins.Load() != 0 {
			t.Fatal("put away a home that was used two hours ago")
		}
	})

	t.Run("an unreadable last_active_at means leave it alone", func(t *testing.T) {
		_, ws, mgr := reaperLifecycleFixture(t) // last_active_at never written
		rt := &hibernateStub{state: "stopped"}
		(&reaper{mgr: mgr, bootTime: time.Now()}).hibernateHome(ctx, rt, ws, month)
		if rt.begins.Load() != 0 {
			t.Fatal("an empty timestamp was read as 'idle forever' — that is a home, deleted")
		}
	})

	t.Run("the owner came back while we waited for the fences", func(t *testing.T) {
		st, ws, mgr := reaperLifecycleFixture(t)
		ws.LastActiveAt = setLastActive(t, st, ws.ID, time.Now().Add(-60*24*time.Hour))
		rt := &hibernateStub{state: "running"} // re-read under the lease
		(&reaper{mgr: mgr, bootTime: time.Now()}).hibernateHome(ctx, rt, ws, month)
		if rt.begins.Load() != 0 {
			t.Fatal("released the slot of a workspace that had come back up")
		}
	})

	t.Run("a runtime with nowhere to put a home", func(t *testing.T) {
		st, ws, mgr := reaperLifecycleFixture(t)
		ws.LastActiveAt = setLastActive(t, st, ws.ID, time.Now().Add(-60*24*time.Hour))
		// Nothing to assert but "does not panic and does not stop the sweep": on docker,
		// native and Fargate the home stays exactly where it is.
		(&reaper{mgr: mgr, bootTime: time.Now()}).hibernateHome(ctx, &plainStub{state: "stopped"}, ws, month)
	})
}

// The tenant's setting is what decides, and "0" has to mean never — this is the only
// automatic path in the product that takes a user's home off the disk it was on.
func TestHomeHibernateAfterResolution(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		def     time.Duration
		want    time.Duration
		enabled bool
	}{
		{"deployment default when unset", "", 30 * 24 * time.Hour, 30 * 24 * time.Hour, true},
		{"off by default", "", 0, 0, false},
		{"tenant opts in on a deployment that did not", "720h", 0, 720 * time.Hour, true},
		{"tenant opts out of a deployment that did", "0", 30 * 24 * time.Hour, 0, false},
		{"garbage falls back rather than silently disabling", "soon", 24 * time.Hour, 24 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim := parseLimits(`{"home_hibernate_after":` + jsonQuote(tc.tenant) + `}`)
			got, on := idleTimeout(lim.HomeHibernateAfter, tc.def)
			if got != tc.want || on != tc.enabled {
				t.Fatalf("= (%s, %v), want (%s, %v)", got, on, tc.want, tc.enabled)
			}
		})
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type stubFactory struct{ rt Runtime }

func (f stubFactory) New(Workspace, string, []string) Runtime { return f.rt }

// The wiring, not the decision: a STOPPED workspace never reaches tiers 1–2 (they return
// on anything that is not running), so tier 3 has to be reached from the other side of
// that return. This is the branch that would silently do nothing if it were misplaced.
func TestReaperSweepReachesTier3OnAStoppedWorkspace(t *testing.T) {
	ctx := context.Background()
	st, ws, mgr := reaperLifecycleFixture(t)
	ws.LastActiveAt = setLastActive(t, st, ws.ID, time.Now().Add(-60*24*time.Hour))
	rt := &hibernateStub{state: "stopped"}
	mgr.rtFactory = stubFactory{rt: rt}
	rp := &reaper{mgr: mgr, bootTime: time.Now()}

	// Tiers 1 and 2 on, tier 3 off: nothing may happen to a stopped workspace's home.
	rp.sweepWorkspace(ctx, ws, time.Minute, true, time.Minute, true, 0, false, map[string]bool{})
	if rt.begins.Load() != 0 {
		t.Fatal("hibernated a home although the tenant never asked for it")
	}
	rp.sweepWorkspace(ctx, ws, time.Minute, true, time.Minute, true, 30*24*time.Hour, true, map[string]bool{})
	if rt.begins.Load() != 1 {
		t.Fatalf("tier 3 was never reached from sweepWorkspace (calls = %d)", rt.begins.Load())
	}

	// "starting" is a launch converging, and "none" has no home left to put away.
	for _, state := range []string{"starting", "none", "running"} {
		rt.state = state
		before := rt.begins.Load()
		rp.sweepWorkspace(ctx, ws, time.Minute, true, time.Minute, true, 30*24*time.Hour, true, map[string]bool{})
		if rt.begins.Load() != before {
			t.Errorf("state %q was hibernated", state)
		}
	}
}
