package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// refreshRuntime is a running workspace that reports stale and names the image a Start
// would take. It reuses the fence fixture's fake Agent for the activity read.
type refreshRuntime struct {
	*reaperFenceRuntime
	stale  atomic.Bool
	target atomic.Value // string
}

func (r *refreshRuntime) AcquireOperationFence(context.Context) (func(), error) {
	return func() {}, nil
}
func (r *refreshRuntime) Stale(context.Context) bool { return r.stale.Load() }
func (r *refreshRuntime) StaleTarget(context.Context) string {
	s, _ := r.target.Load().(string)
	return s
}

type refreshFixture struct {
	st     *store.SQL
	ws     store.Workspace
	mgr    *manager
	rp     *reaper
	rt     *refreshRuntime
	starts atomic.Int32
	failed atomic.Bool
	quiet  time.Duration
}

func newRefreshFixture(t *testing.T, optIn bool) *refreshFixture {
	t.Helper()
	st, ws, mgr := reaperLifecycleFixture(t)
	f := &refreshFixture{st: st, ws: ws, mgr: mgr, quiet: time.Minute}
	f.rt = &refreshRuntime{reaperFenceRuntime: newReaperFenceRuntime(t)}
	f.rt.stale.Store(true)
	f.rt.target.Store("sha256:aaa")
	f.rp = &reaper{mgr: mgr}
	f.rp.refreshStart = func(context.Context, store.Workspace) *apiError {
		f.starts.Add(1)
		if f.failed.Load() {
			return &apiError{message: "boom"}
		}
		return nil
	}
	if optIn {
		if err := st.SetWorkspaceSettings(context.Background(), ws.ID, `{"autoImageRefresh":true}`); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *refreshFixture) run() {
	f.rp.refreshImage(context.Background(), f.rt, f.ws, tierClocks{refresh: f.quiet, refreshOn: true})
}

func TestImageRefreshRestartsStaleQuietOptedIn(t *testing.T) {
	f := newRefreshFixture(t, true)
	f.run()
	if f.rt.stops.Load() != 1 || f.starts.Load() != 1 {
		t.Fatalf("stops=%d starts=%d, want 1/1", f.rt.stops.Load(), f.starts.Load())
	}
	audit, _ := f.st.ListAuditByTenant(context.Background(), f.ws.TenantID, 10)
	if len(audit) != 1 || audit[0].Action != "workspace.image_refresh" {
		t.Fatalf("audit = %+v", audit)
	}
	notes, _ := f.st.ListNotifications(context.Background(), f.ws.MembershipID, "", 10)
	if len(notes) != 1 || notes[0].Kind != "image-refresh" {
		t.Fatalf("notifications = %+v", notes)
	}
}

func TestImageRefreshLeavesAloneWhenNotEligible(t *testing.T) {
	cases := map[string]func(f *refreshFixture){
		"not opted in":    func(f *refreshFixture) { _ = f.st.SetWorkspaceSettings(context.Background(), f.ws.ID, `{}`) },
		"not stale":       func(f *refreshFixture) { f.rt.stale.Store(false) },
		"target unknown":  func(f *refreshFixture) { f.rt.target.Store("") },
		"session working": func(f *refreshFixture) { f.rt.busy.Store(true) },
		"image job":       func(f *refreshFixture) { f.rt.imageJobs.Store(1) },
		"terminal open": func(f *refreshFixture) {
			f.mgr.conns.addConn(f.ws.ID, "", true)
		},
		"recent request": func(f *refreshFixture) { f.mgr.conns.touch(f.ws.ID) },
		"activity on another replica": func(f *refreshFixture) {
			now := time.Now().UTC()
			if _, err := f.st.RecordWorkspaceActivity(context.Background(), f.ws.ID, leaseTS(now), leaseTS(now.Add(time.Minute)), leaseTS(now)); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRefreshFixture(t, true)
			arrange(f)
			f.run()
			if f.rt.stops.Load() != 0 || f.starts.Load() != 0 {
				t.Fatalf("stops=%d starts=%d, want none", f.rt.stops.Load(), f.starts.Load())
			}
		})
	}
}

func TestImageRefreshOneAttemptPerImageAndFailureDoesNotLoop(t *testing.T) {
	f := newRefreshFixture(t, true)
	// A stop stamps last_active_at, so a new image can only be tried again once the
	// workspace has been quiet again; a nanosecond stands in for that wait.
	f.quiet = time.Nanosecond
	f.failed.Store(true)
	f.run()
	f.run()
	f.run()
	if f.rt.stops.Load() != 1 || f.starts.Load() != 1 {
		t.Fatalf("same image: stops=%d starts=%d, want 1/1", f.rt.stops.Load(), f.starts.Load())
	}
	// A different image is a new attempt.
	f.rt.target.Store("sha256:bbb")
	f.run()
	if f.rt.stops.Load() != 2 || f.starts.Load() != 2 {
		t.Fatalf("new image: stops=%d starts=%d, want 2/2", f.rt.stops.Load(), f.starts.Load())
	}
}

func TestImageRefreshSwitchTurnedOffUnderTheFencesStopsNothing(t *testing.T) {
	f := newRefreshFixture(t, true)
	f.rp.refreshStart = nil
	// Turn the switch off while the sweep waits for the fences: the runtime fence is the
	// last thing taken before the re-check.
	f.rt.reaperFenceRuntime = newReaperFenceRuntime(t)
	rt := &switchOffRuntime{refreshRuntime: f.rt, off: func() {
		_ = f.st.SetWorkspaceSettings(context.Background(), f.ws.ID, `{}`)
	}}
	f.rp.refreshImage(context.Background(), rt, f.ws, tierClocks{refresh: time.Minute, refreshOn: true})
	if f.rt.stops.Load() != 0 {
		t.Fatal("stopped after the member switched the refresh off")
	}
}

type switchOffRuntime struct {
	*refreshRuntime
	off func()
}

func (r *switchOffRuntime) AcquireOperationFence(context.Context) (func(), error) {
	r.off()
	return func() {}, nil
}
