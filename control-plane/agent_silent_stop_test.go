package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// namedFactory hands each workspace its own runtime, keyed by container name.
type namedFactory struct{ rts map[string]runtime.Runtime }

func (f namedFactory) New(w runtime.Workspace, _ runtime.SecretKeys, _ []string) runtime.Runtime {
	return f.rts[w.ContainerName]
}

type silentFx struct {
	st   *store.SQL
	mgr  *manager
	ws   []store.Workspace
	rts  []*deadlineStub
	keys []string
}

// deadEndpoint is a URL nothing listens on; liveEndpoint answers every request.
func deadEndpoint() string {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.Close()
	return s.URL
}

func liveEndpoint(t *testing.T) string {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) }))
	t.Cleanup(s.Close)
	return s.URL
}

// newSilentFx builds one workspace per endpoint in one tenant; the first is the one the tests
// make silent.
func newSilentFx(t *testing.T, endpoints ...string) *silentFx {
	t.Helper()
	ctx := context.Background()
	st, ws0, mgr := reaperLifecycleFixture(t)
	f := &silentFx{st: st, mgr: mgr}
	rts := map[string]runtime.Runtime{}
	for i, ep := range endpoints {
		ws := ws0
		if i > 0 {
			id, err := st.UpsertIdentity(ctx, "peer"+string(rune('a'+i))+"@example.com", "peer"+string(rune('a'+i)), "")
			if err != nil {
				t.Fatal(err)
			}
			m, err := st.EnsureMembership(ctx, id.ID, ws0.TenantID, "member")
			if err != nil {
				t.Fatal(err)
			}
			ws = store.Workspace{ID: store.NewID(), TenantID: ws0.TenantID, MembershipID: m.ID,
				ContainerName: "peer" + string(rune('a'+i)), Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t",
				State: "running", CreatedAt: store.NowTS()}
			if err := st.CreateWorkspace(ctx, ws); err != nil {
				t.Fatal(err)
			}
		}
		rt := &deadlineStub{state: "running", endpoint: ep}
		rts[ws.ContainerName] = rt
		f.ws, f.rts = append(f.ws, ws), append(f.rts, rt)
		f.keys = append(f.keys, endpointKey(ep))
		key := endpointKey(ep)
		agentLive.forget(key)
		t.Cleanup(func() { agentLive.forget(key) })
	}
	mgr.rtFactory = namedFactory{rts}
	return f
}

// flag seeds workspace i as flagged silent for d, the state a probe that failed d ago leaves.
func (f *silentFx) flag(i int, d time.Duration) {
	now := time.Now()
	agentLive.mu.Lock()
	agentLive.m[f.keys[i]] = &liveEntry{since: now.Add(-d - time.Hour), answered: now.Add(-d - time.Hour),
		probedAt: now, silent: true, silentAt: now.Add(-d), gen: 1}
	agentLive.mu.Unlock()
}

func (f *silentFx) sampler(after time.Duration) *usageSampler {
	u := newUsageSampler(f.mgr, 5*time.Minute)
	u.silentStop = newSilentStop(f.mgr, after)
	return u
}

func (f *silentFx) notifications(t *testing.T, i int, kind string) []store.Notification {
	t.Helper()
	rows, err := f.st.ListNotifications(context.Background(), f.ws[i].MembershipID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Notification
	for _, n := range rows {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

func TestSilentStopStopsAWorkspaceSilentPastTheCeiling(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint(), liveEndpoint(t))
	f.flag(0, 30*time.Minute)
	u := f.sampler(20 * time.Minute)

	u.sample(ctx)
	u.silentStop.wg.Wait()
	if n := f.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls on the silent workspace = %d, want 1", n)
	}
	if n := f.rts[1].stops.Load(); n != 0 {
		t.Fatalf("the answering peer was stopped (%d)", n)
	}
	got, _, err := f.st.GetWorkspaceByMembership(ctx, f.ws[0].MembershipID)
	if err != nil || got.State != "stopped" {
		t.Fatalf("recorded state = %q (err %v), want stopped", got.State, err)
	}
	if n := countAuditAction(t, f.st, f.ws[0].TenantID, "workspace.agent_silent_stop"); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	notes := f.notifications(t, 0, "agent-silent-stop")
	if len(notes) != 1 || notes[0].TargetType != "workspace" || notes[0].Payload != `{"silentMinutes":30}` {
		t.Fatalf("notifications = %+v, want one workspace notification carrying the silent minutes", notes)
	}
	if n := len(f.notifications(t, 1, "agent-silent-stop")); n != 0 {
		t.Fatalf("the peer was notified (%d)", n)
	}
	if a, ok, _ := f.st.GetWorkspaceAutoStopByMembership(ctx, f.ws[0].MembershipID); !ok || a.Kind != "agent-silent" {
		t.Fatalf("auto-stop record = %+v ok=%v, want kind agent-silent", a, ok)
	}

	// The next sweep finds it stopped and does nothing more.
	u.sample(ctx)
	u.silentStop.wg.Wait()
	if n := f.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls after a second sweep = %d, want still 1", n)
	}
	if n := countAuditAction(t, f.st, f.ws[0].TenantID, "workspace.agent_silent_stop"); n != 1 {
		t.Fatalf("audit rows after a second sweep = %d, want 1", n)
	}
}

func TestSilentStopIsOffByDefaultAndBelowTheCeiling(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint(), liveEndpoint(t))
	f.flag(0, 30*time.Minute)

	// No ceiling configured: the sampler has no silentStop and the workspace is only flagged.
	if newSilentStop(f.mgr, 0) != nil {
		t.Fatal("a zero ceiling must leave the feature off")
	}
	u := f.sampler(0)
	u.sample(ctx)
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("Stop calls with the feature off = %d", n)
	}

	// Configured, but the outage is younger than the ceiling.
	f.flag(0, 12*time.Minute)
	u = f.sampler(20 * time.Minute)
	u.sample(ctx)
	u.silentStop.wg.Wait()
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("Stop calls before the ceiling = %d", n)
	}

	// A ceiling under the floor is raised to it, not honoured.
	if d := newSilentStop(f.mgr, time.Minute); d == nil || d.after != silentStopMin {
		t.Fatalf("ceiling below the floor = %+v", d)
	}
}

// A CP that cannot reach anything sees every Agent as silent. Nothing is stopped unless some
// other running workspace answered the same sweep.
func TestSilentStopRefusesWhenNothingElseAnswers(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint(), deadEndpoint())
	f.flag(0, time.Hour)
	f.flag(1, time.Hour)
	u := f.sampler(20 * time.Minute)
	u.sample(ctx)
	u.silentStop.wg.Wait()
	for i, rt := range f.rts {
		if n := rt.stops.Load(); n != 0 {
			t.Fatalf("workspace %d stopped (%d) although no Agent answered", i, n)
		}
	}
	// Same silence with one answering peer: now it is the workspace, not the CP.
	f2 := newSilentFx(t, deadEndpoint(), liveEndpoint(t))
	f2.flag(0, time.Hour)
	u2 := f2.sampler(20 * time.Minute)
	u2.sample(ctx)
	u2.silentStop.wg.Wait()
	if n := f2.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls with an answering peer = %d, want 1", n)
	}
}

func TestSilentStopCapsStopsPerSweepLongestSilentFirst(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint(), deadEndpoint(), deadEndpoint(), liveEndpoint(t))
	f.flag(0, 30*time.Minute)
	f.flag(1, 50*time.Minute)
	f.flag(2, 40*time.Minute)
	u := f.sampler(20 * time.Minute)
	u.sample(ctx)
	u.silentStop.wg.Wait()
	got := []int32{f.rts[0].stops.Load(), f.rts[1].stops.Load(), f.rts[2].stops.Load()}
	if got[0] != 0 || got[1] != 1 || got[2] != 1 {
		t.Fatalf("stops = %v, want the two longest-silent only ([0 1 1])", got)
	}
}

// The decision is taken again under the lease: a workspace that is no longer running, or whose
// Agent answers the last probe, is left alone.
func TestSilentStopRechecksUnderTheLease(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint())
	d := newSilentStop(f.mgr, 20*time.Minute)
	cand := func() silentCandidate { return silentCandidate{rt: f.rts[0], ws: f.ws[0], key: f.keys[0]} }

	f.flag(0, time.Hour)
	f.rts[0].state = "starting" // restarted by the member meanwhile; the Agent is booting
	d.stop(ctx, cand())
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("a starting workspace was stopped (%d)", n)
	}
	f.rts[0].state = "running"

	f.rts[0].endpoint = liveEndpoint(t) // the Agent answers the final probe
	d.stop(ctx, cand())
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("an answering Agent was stopped (%d)", n)
	}
	f.rts[0].endpoint = deadEndpoint()

	f.flag(0, time.Minute) // flagged, but only just: the ceiling is not reached
	d.stop(ctx, cand())
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("a young outage was stopped (%d)", n)
	}

	// Without those conditions the same call does stop, so the test cannot pass by never stopping.
	f.flag(0, time.Hour)
	d.stop(ctx, cand())
	if n := f.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls = %d, want 1", n)
	}
}

// Another replica (or an approved share operation) holding the lifecycle lease keeps the stop out;
// once the first stop is done a second replica finds the workspace stopped and adds nothing.
func TestSilentStopRespectsTheLeaseAndDoesNotDoubleStop(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint())
	d := newSilentStop(f.mgr, 20*time.Minute)
	f.flag(0, time.Hour)
	op := store.NewID()
	now := time.Now().UTC()
	if ok, err := f.st.AcquireSessionShareOwnerLease(ctx, f.ws[0].MembershipID, op, leaseTS(now), leaseTS(now.Add(time.Minute))); err != nil || !ok {
		t.Fatalf("acquire share lease: %v %v", ok, err)
	}
	c := silentCandidate{rt: f.rts[0], ws: f.ws[0], key: f.keys[0]}
	d.stop(ctx, c)
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("stopped while another operation held the lease (%d)", n)
	}
	if err := f.st.ReleaseSessionShareOwnerLease(ctx, f.ws[0].MembershipID, op); err != nil {
		t.Fatal(err)
	}

	// Two replicas racing for the same outage: one Stop, one audit row, one notification.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.stop(ctx, c) }()
	}
	wg.Wait()
	if n := f.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls from two racing replicas = %d, want 1", n)
	}
	if n := countAuditAction(t, f.st, f.ws[0].TenantID, "workspace.agent_silent_stop"); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	if n := len(f.notifications(t, 0, "agent-silent-stop")); n != 1 {
		t.Fatalf("notifications = %d, want 1", n)
	}
}

// A Stop/Start completed through another replica between this replica's flag and its stop: the
// row's last_active_at is newer than the flag, so the verdict is about the previous Agent.
func TestSilentStopIgnoresAVerdictOlderThanTheCurrentLaunch(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint())
	d := newSilentStop(f.mgr, 20*time.Minute)
	f.flag(0, time.Hour)
	setLastActive(t, f.st, f.ws[0].ID, time.Now()) // the member restarted it; this replica never saw it leave running
	d.stop(ctx, silentCandidate{rt: f.rts[0], ws: f.ws[0], key: f.keys[0]})
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("a launch newer than the silent verdict was stopped (%d)", n)
	}
	if agentLive.silentFor(f.keys[0], time.Now()) != 0 {
		t.Fatal("the stale verdict was kept")
	}
}

// An answer to any other call while the final probe waits makes the probe's failure stale.
func TestSilentStopDropsAProbeFailureOutdatedByAnAnswer(t *testing.T) {
	ctx := context.Background()
	var f *silentFx
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentLive.note(f.keys[0], time.Now()) // /sessions answered meanwhile
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	f = newSilentFx(t, srv.URL)
	d := newSilentStop(f.mgr, 20*time.Minute)
	f.flag(0, time.Hour)
	d.stop(ctx, silentCandidate{rt: f.rts[0], ws: f.ws[0], key: f.keys[0]})
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("stopped on a probe failure outdated by an answer (%d)", n)
	}
}

// An incomplete walk proves nothing, even with other Agents answering.
func TestSilentStopNeedsACompleteWalk(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint())
	d := newSilentStop(f.mgr, 20*time.Minute)
	f.flag(0, time.Hour)
	c := []silentCandidate{{rt: f.rts[0], ws: f.ws[0], key: f.keys[0]}}
	d.dispatch(ctx, c, 3, false)
	d.wg.Wait()
	if n := f.rts[0].stops.Load(); n != 0 {
		t.Fatalf("stopped after an incomplete walk (%d)", n)
	}
	d.dispatch(ctx, c, 3, true)
	d.wg.Wait()
	if n := f.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls after a complete walk = %d, want 1", n)
	}
}

// Replicas do not share a manager (nor its start locks): the lifecycle lease and the runtime
// state under it are what keep a second one out.
func TestSilentStopTwoIndependentManagersStopOnce(t *testing.T) {
	ctx := context.Background()
	f := newSilentFx(t, deadEndpoint())
	f.flag(0, time.Hour)
	other := &manager{store: f.st, conns: newConnRegistry(), rtFactory: f.mgr.rtFactory}
	da, db := newSilentStop(f.mgr, 20*time.Minute), newSilentStop(other, 20*time.Minute)
	c := silentCandidate{rt: f.rts[0], ws: f.ws[0], key: f.keys[0]}
	var wg sync.WaitGroup
	for _, d := range []*silentStop{da, db} {
		wg.Add(1)
		go func() { defer wg.Done(); d.stop(ctx, c) }()
	}
	wg.Wait()
	if n := f.rts[0].stops.Load(); n != 1 {
		t.Fatalf("Stop calls from two independent managers = %d, want 1", n)
	}
	if n := countAuditAction(t, f.st, f.ws[0].TenantID, "workspace.agent_silent_stop"); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}
