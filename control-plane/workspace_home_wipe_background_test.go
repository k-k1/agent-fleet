package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The home operations on ecs run a Fargate task and take minutes (internal/runtime/
// runtime_ecs_home_task.go). These tests pin the CP's background half: the refusals and
// the stop answer the request, the rest follows under the same lease, and the outcome
// reaches the member (the workspace payload) or the administrator (the audit log).

// backgroundHomeRuntime claims every home port and says its wipes take minutes. Each wipe,
// erase and destroy waits for gate, so a test can look at the world while one is running.
type backgroundHomeRuntime struct {
	reachableHomeRuntime
	gate     chan struct{}
	mu       sync.Mutex
	clearing bool
	wipeErr  error
	blocked  error
}

func newBackgroundHomeRuntime(state string) *backgroundHomeRuntime {
	return &backgroundHomeRuntime{
		reachableHomeRuntime: reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: state}},
		gate:                 make(chan struct{}),
	}
}

func (r *backgroundHomeRuntime) MarkHomeClearing() func() {
	r.mu.Lock()
	r.clearing = true
	r.mu.Unlock()
	r.rec.add("mark")
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.clearing = false
			r.mu.Unlock()
			r.rec.add("unmark")
		})
	}
}

func (r *backgroundHomeRuntime) State(context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clearing {
		return "starting"
	}
	return r.state
}

func (r *backgroundHomeRuntime) Stop(context.Context) error {
	r.rec.add("stop")
	r.mu.Lock()
	r.state = "stopped"
	r.mu.Unlock()
	return nil
}

func (r *backgroundHomeRuntime) Start(context.Context) error {
	r.rec.add("start")
	r.mu.Lock()
	r.state = "running"
	r.mu.Unlock()
	return nil
}

func (r *backgroundHomeRuntime) HomeWipeBlocked(context.Context) error { return r.blocked }

func (r *backgroundHomeRuntime) WipeHome(ctx context.Context, what runtime.HomeWipe) error {
	<-r.gate
	r.rec.add("wipe:" + string(what))
	return r.wipeErr
}

func (r *backgroundHomeRuntime) EraseHome(context.Context) error {
	<-r.gate
	r.rec.add("erase")
	return r.wipeErr
}

func (r *backgroundHomeRuntime) Destroy(context.Context) ([]string, error) {
	<-r.gate
	r.rec.add("destroy")
	return nil, r.wipeErr
}

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// leaseFree reports whether the member's lifecycle lease can be taken now.
func leaseFree(t *testing.T, mgr *manager, membershipID string) bool {
	t.Helper()
	l, err := acquireWorkspaceLifecycleLease(context.Background(), mgr.store, membershipID)
	if errors.Is(err, store.ErrSessionShareOwnerBusy) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	return true
}

// A member's Recreate / Clean home: answered `starting` once the workspace is stopped, with
// the clearing mark set before the stop; the lease stays held while the wipe runs, so a
// start or a second operation is refused; then the mark goes and the workspace starts.
func TestMemberHomeWipeInTheBackground(t *testing.T) {
	for op, what := range map[string]string{"recreate": "repos", "clean-home": "clean"} {
		rt := newBackgroundHomeRuntime("running")
		mgr, res := wipeResolved(t, rt)
		api := newWorkspaceAPI(mgr, false)
		w := callMemberWipe(api, op, res)
		if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"state":"starting"`) {
			t.Fatalf("%s = %d %s, want 202 starting", op, w.Code, w.Body.String())
		}
		if got := rt.rec.log(); got != "mark,stop" {
			t.Errorf("%s answered after %q, want mark,stop (the mark first, so nobody reads `stopped`)", op, got)
		}
		if leaseFree(t, mgr, res.mv.MembershipID) {
			t.Errorf("%s: the lease is free while the wipe runs; a start could boot onto a half-removed home", op)
		}
		if aerr := api.ensureWorkspaceStarted(context.Background(), res); aerr == nil || aerr.code != "workspace_operation_in_progress" {
			t.Errorf("%s: a start during the wipe = %+v, want workspace_operation_in_progress", op, aerr)
		}
		close(rt.gate)
		want := "mark,stop,wipe:" + what + ",unmark,start"
		waitFor(t, op+" to start the workspace", func() bool { return rt.rec.log() == want })
		waitFor(t, op+" to release the lease", func() bool { return leaseFree(t, mgr, res.mv.MembershipID) })
	}
}

// A wipe that fails in the background leaves the workspace stopped, unmarked, and says why
// in the workspace payload: the member's request was answered long ago.
func TestMemberHomeWipeFailureInTheBackgroundReachesThePayload(t *testing.T) {
	rt := newBackgroundHomeRuntime("running")
	rt.wipeErr = errors.New("the home task (repos) failed with exit 1")
	close(rt.gate)
	mgr, res := wipeResolved(t, rt)
	api := newWorkspaceAPI(mgr, false)
	if w := callMemberWipe(api, "recreate", res); w.Code != http.StatusAccepted {
		t.Fatalf("recreate = %d %s", w.Code, w.Body.String())
	}
	waitFor(t, "the lease to be released", func() bool {
		return strings.Contains(rt.rec.log(), "unmark") && leaseFree(t, mgr, res.mv.MembershipID)
	})
	if got := rt.rec.log(); strings.Contains(got, "start") {
		t.Errorf("a failed wipe went on to start the workspace: %q", got)
	}
	payload := api.workspacePayload(context.Background(), res, rt.State(context.Background()))
	if payload["state"] != "stopped" || !strings.Contains(fmt.Sprint(payload["homeWipeFailed"]), "exit 1") {
		t.Errorf("payload = %v, want stopped with the failure", payload)
	}
	// A CP restarted in between (a deploy) still says why: a new manager over the same
	// store, nothing carried in memory (#1537).
	restarted := newWorkspaceAPI(&manager{store: mgr.store, conns: newConnRegistry()}, false)
	if got := restarted.workspacePayload(context.Background(), res, "stopped")["homeWipeFailed"]; !strings.Contains(fmt.Sprint(got), "exit 1") {
		t.Errorf("after a restart homeWipeFailed = %v, want the failure", got)
	}
	// The tenant admins see the same record.
	if as := store.CurrentAutoStop(context.Background(), mgr.store, res.ws.MembershipID, "stopped"); as == nil || as.Kind != autoStopHomeWipe {
		t.Errorf("auto-stop record = %+v, want kind %s", as, autoStopHomeWipe)
	}
	// The next start clears it.
	if aerr := api.ensureWorkspaceStarted(context.Background(), res); aerr != nil {
		t.Fatal(aerr.message)
	}
	if _, ok := api.workspacePayload(context.Background(), res, "stopped")["homeWipeFailed"]; ok {
		t.Error("the failure outlived the next start")
	}
}

// A home task still running from earlier (a restart lost track of it) refuses before
// anything is marked or stopped, with its own code.
func TestMemberHomeWipeRefusedWhileAHomeTaskRuns(t *testing.T) {
	rt := newBackgroundHomeRuntime("running")
	rt.blocked = runtime.ErrHomeTaskInFlight
	mgr, res := wipeResolved(t, rt)
	w := callMemberWipe(newWorkspaceAPI(mgr, false), "clean-home", res)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), errCodeHomeOperationInProgress) {
		t.Errorf("clean-home = %d %s, want 409 %s", w.Code, w.Body.String(), errCodeHomeOperationInProgress)
	}
	if got := rt.rec.log(); got != "" {
		t.Errorf("refused, but the runtime saw %q", got)
	}
	if !leaseFree(t, mgr, res.mv.MembershipID) {
		t.Error("the refusal kept the lease")
	}
}

func auditHas(t *testing.T, st *store.SQL, tn store.Tenant, want string) bool {
	t.Helper()
	for _, a := range auditActions(t, st, tn) {
		if a == want {
			return true
		}
	}
	return false
}

// The administrator's Clean home: 202 pending once stopped, the outcome audited when the
// erase has finished, and the workspace recorded as stopped.
func TestAdminCleanHomeInTheBackground(t *testing.T) {
	rt := newBackgroundHomeRuntime("running")
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	w := httpCleanHome(mgr)
	if w.Code != http.StatusAccepted {
		t.Fatalf("clean home = %d %s, want 202", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["pending"] != true {
		t.Errorf("body = %v, want pending", body)
	}
	if got := rt.rec.log(); got != "stop" {
		t.Errorf("answered after %q, want stop", got)
	}
	if auditHas(t, st, tn, "workspace.clean_home:home erased") {
		t.Error("audited as erased before the erase ran")
	}
	close(rt.gate)
	waitFor(t, "the outcome entry", func() bool { return auditHas(t, st, tn, "workspace.clean_home:home erased") })
	if rt.rec.log() != "stop,erase" {
		t.Errorf("drove %q, want stop,erase", rt.rec.log())
	}
	mid := membershipIDOf(t, st, victim, tn)
	waitFor(t, "the lease to be released", func() bool { return leaseFree(t, mgr, mid) })
}

// A failed background erase is audited as an error, not as done.
func TestAdminCleanHomeFailureInTheBackgroundIsAudited(t *testing.T) {
	rt := newBackgroundHomeRuntime("stopped")
	rt.wipeErr = errors.New("the home task (clean) refused and removed nothing (exit 2)")
	close(rt.gate)
	st, mgr, _, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	if w := httpCleanHome(mgr); w.Code != http.StatusAccepted {
		t.Fatalf("clean home = %d %s", w.Code, w.Body.String())
	}
	waitFor(t, "the outcome entry", func() bool {
		for _, a := range auditActions(t, st, tn) {
			if strings.HasPrefix(a, "workspace.clean_home:error: ") && strings.Contains(a, "exit 2") {
				return true
			}
		}
		return false
	})
	if auditHas(t, st, tn, "workspace.clean_home:home erased") {
		t.Error("a failed erase was audited as done")
	}
}

// A refusal on a background deployment is still the request's answer.
func TestAdminCleanHomeRefusedWhileAHomeTaskRuns(t *testing.T) {
	rt := newBackgroundHomeRuntime("stopped")
	rt.blocked = runtime.ErrHomeTaskInFlight
	_, mgr, _, _ := destroyFixture(t, fixedRuntimeFactory{rt})
	w := httpCleanHome(mgr)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "home_operation_in_progress") {
		t.Errorf("clean home = %d %s, want 409 home_operation_in_progress", w.Code, w.Body.String())
	}
	if got := rt.rec.log(); got != "" {
		t.Errorf("refused, but the runtime saw %q", got)
	}
}

func httpCleanHome(mgr *manager) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newAdminAPI(mgr).cleanHome(w, adminRequest(http.MethodPost, "/api/admin/clean-home",
		`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`))
	return w
}

// Destroy: 202 pending, the row removed and the outcome audited once the runtime's
// Destroy (here the EFS task) has finished.
func TestDestroyWorkspaceInTheBackground(t *testing.T) {
	ctx := context.Background()
	rt := newBackgroundHomeRuntime("stopped")
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	mid := membershipIDOf(t, st, victim, tn)
	if err := st.SetMembershipStatus(ctx, mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"pending":true`) {
		t.Fatalf("destroy = %d %s, want 202 pending", w.Code, w.Body.String())
	}
	if _, ok, _ := st.GetWorkspaceByMembership(ctx, mid); !ok {
		t.Error("the row went before the home did")
	}
	close(rt.gate)
	waitFor(t, "the row to go", func() bool {
		_, ok, _ := st.GetWorkspaceByMembership(ctx, mid)
		return !ok
	})
	waitFor(t, "the outcome entry", func() bool {
		return auditHas(t, st, tn, "workspace.destroy:workspace destroyed (home and runtime resources deleted)")
	})
}

// A failed background destroy keeps the row, so the retry runs the task again.
func TestDestroyWorkspaceFailureInTheBackgroundKeepsTheRow(t *testing.T) {
	ctx := context.Background()
	rt := newBackgroundHomeRuntime("stopped")
	rt.wipeErr = errors.New("remove the EFS home: the home task (destroy) failed with exit 1")
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	mid := membershipIDOf(t, st, victim, tn)
	_ = st.SetMembershipStatus(ctx, mid, "inactive")
	if w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusAccepted {
		t.Fatalf("destroy = %d %s", w.Code, w.Body.String())
	}
	waitFor(t, "the outcome entry", func() bool {
		for _, a := range auditActions(t, st, tn) {
			if strings.HasPrefix(a, "workspace.destroy:error: ") {
				return true
			}
		}
		return false
	})
	if _, ok, _ := st.GetWorkspaceByMembership(ctx, mid); !ok {
		t.Error("the row went although the home was not removed; nothing would point at it any more")
	}
}

// poolDestroyRuntime is ecs-ec2 with the stack's home task: its own wipes fit in the
// request, but its Destroy runs the task on the member's EFS directories (#1536).
type poolDestroyRuntime struct {
	*reachableHomeRuntime
	gate chan struct{}
}

func (poolDestroyRuntime) DestroyRunsHomeTask() bool { return true }

// Destroy waits for the test to open gate, which it does only after the request has been
// answered; a Destroy run inside the request gives up after a few seconds instead of hanging.
func (r poolDestroyRuntime) Destroy(context.Context) ([]string, error) {
	select {
	case <-r.gate:
	case <-time.After(3 * time.Second):
		return nil, errors.New("Destroy ran inside the request")
	}
	r.rec.add("destroy")
	return nil, nil
}

// The pool's Destroy runs the same task, so it answers 202 and finishes after the request
// like ecs's; an administrator's Clean home on it still runs in the request.
func TestDestroyWorkspaceInTheBackgroundOnThePool(t *testing.T) {
	ctx := context.Background()
	rt := poolDestroyRuntime{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "stopped"}},
		make(chan struct{})}
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	if ops := mgr.homeOperations(); ops.Background || !ops.DestroyBackground {
		t.Fatalf("homeOperations = %+v, want only Destroy in the background", ops)
	}
	mid := membershipIDOf(t, st, victim, tn)
	if err := st.SetMembershipStatus(ctx, mid, "inactive"); err != nil {
		t.Fatal(err)
	}
	w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"pending":true`) {
		t.Fatalf("destroy = %d %s, want 202 pending", w.Code, w.Body.String())
	}
	close(rt.gate)
	waitFor(t, "the row to go", func() bool {
		_, ok, _ := st.GetWorkspaceByMembership(ctx, mid)
		return !ok
	})
}
