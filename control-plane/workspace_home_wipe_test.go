package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The operations that remove part of a home go through the adapter that knows where the
// home is (internal/runtime/home_wipe.go). These tests pin the CP's half: a runtime that
// cannot reach the home is refused before anything is stopped, and one that can is asked
// in the right place of the stop → wipe → start sequence.

// wipeRecorder is the call log shared by the runtimes below.
type wipeRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (w *wipeRecorder) add(c string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, c)
}

func (w *wipeRecorder) log() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.calls, ",")
}

// unreachableHomeRuntime stands in for Fargate: it runs, stops and starts, and claims
// none of the home ports.
type unreachableHomeRuntime struct {
	rec     *wipeRecorder
	state   string
	stopErr error
}

func (r *unreachableHomeRuntime) Start(context.Context) error {
	r.rec.add("start")
	r.state = "running"
	return nil
}

func (r *unreachableHomeRuntime) Stop(context.Context) error {
	r.rec.add("stop")
	if r.stopErr != nil {
		return r.stopErr
	}
	r.state = "stopped"
	return nil
}

func (r *unreachableHomeRuntime) State(context.Context) string { return r.state }
func (r *unreachableHomeRuntime) Endpoint() string             { return "" }
func (r *unreachableHomeRuntime) Token() string                { return "" }
func (r *unreachableHomeRuntime) Name() string                 { return "af-ws-home-wipe" }

// reachableHomeRuntime claims every home port and records what it was asked to remove.
type reachableHomeRuntime struct {
	unreachableHomeRuntime
	backups int
}

func (r *reachableHomeRuntime) WipeHome(_ context.Context, what runtime.HomeWipe) error {
	r.rec.add("wipe:" + string(what))
	return nil
}

func (r *reachableHomeRuntime) EraseHome(context.Context) error {
	r.rec.add("erase")
	return nil
}

func (r *reachableHomeRuntime) HomeBackups(context.Context) (runtime.HomeBackups, error) {
	return runtime.HomeBackups{Count: r.backups, Newest: time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC), HomeExists: true}, nil
}

func (r *reachableHomeRuntime) DeleteHomeBackups(context.Context) (int, error) {
	r.rec.add("delete-backups")
	n := r.backups
	r.backups = 0
	return n, nil
}

// fixedRuntimeFactory hands out the same runtime for every workspace.
type fixedRuntimeFactory struct{ rt runtime.Runtime }

func (f fixedRuntimeFactory) New(runtime.Workspace, string, []string) runtime.Runtime { return f.rt }

func wipeResolved(t *testing.T, rt runtime.Runtime) (*manager, *resolved) {
	t.Helper()
	_, ws, mgr := reaperLifecycleFixture(t)
	mv := store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}
	return mgr, &resolved{rt: rt, ws: ws, mv: mv}
}

func callMemberWipe(api workspaceAPI, op string, res *resolved) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/workspace/"+op, nil)
	if op == "recreate" {
		api.recreate(w, r, res)
	} else {
		api.cleanHome(w, r, res)
	}
	return w
}

// A member's Recreate and Clean home on a runtime that cannot reach the home: refused with
// its own code, and the workspace is not even stopped. The alternative — stop, "wipe" a
// path that is not the home, start — told the member their working copies were gone
// while every one of them survived.
func TestMemberHomeWipeRefusedWhereTheRuntimeCannotReachTheHome(t *testing.T) {
	for _, op := range []string{"recreate", "clean-home"} {
		rec := &wipeRecorder{}
		mgr, res := wipeResolved(t, &unreachableHomeRuntime{rec: rec, state: "running"})
		w := callMemberWipe(newWorkspaceAPI(mgr, false), op, res)
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), errCodeHomeWipeUnsupported) {
			t.Errorf("%s on an unreachable home = %d %s, want 501 %s", op, w.Code, w.Body.String(), errCodeHomeWipeUnsupported)
		}
		if got := rec.log(); got != "" {
			t.Errorf("%s was refused but the runtime saw %q; nothing may happen before the refusal", op, got)
		}
	}
}

// Where the runtime can reach the home, the wipe sits between the stop and the start, and
// each operation asks for its own set.
func TestMemberHomeWipeRunsBetweenStopAndStart(t *testing.T) {
	for op, want := range map[string]string{
		"recreate":   "stop,wipe:repos,start",
		"clean-home": "stop,wipe:clean,start",
	} {
		rec := &wipeRecorder{}
		mgr, res := wipeResolved(t, &reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "running"}})
		w := callMemberWipe(newWorkspaceAPI(mgr, false), op, res)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", op, w.Code, w.Body.String())
		}
		if got := rec.log(); got != want {
			t.Errorf("%s drove %q, want %q", op, got, want)
		}
	}
}

// startingHomeRuntime reaches the home but is still converging a Start in the background
// (ecs-ec2 with a live claim), so it refuses a member's wipe for now.
type startingHomeRuntime struct{ reachableHomeRuntime }

func (r *startingHomeRuntime) HomeWipeBlocked(context.Context) error {
	return runtime.ErrHomeWipeWhileStarting
}

// A Start converging in the background holds no lease, so stopping under it would not
// stop it. The refusal comes before the Stop, and says so with a code the Console reads
// as "nothing happened".
func TestMemberHomeWipeRefusedWhileAStartConverges(t *testing.T) {
	for _, op := range []string{"recreate", "clean-home"} {
		rec := &wipeRecorder{}
		mgr, res := wipeResolved(t, &startingHomeRuntime{reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "starting"}}})
		w := callMemberWipe(newWorkspaceAPI(mgr, false), op, res)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), errCodeHomeWipeWhileStarting) {
			t.Errorf("%s while starting = %d %s, want 409 %s", op, w.Code, w.Body.String(), errCodeHomeWipeWhileStarting)
		}
		if got := rec.log(); got != "" {
			t.Errorf("%s was refused but the runtime saw %q", op, got)
		}
	}
}

// The administrator's Clean home: the offboarding step, which leaves the workspace stopped.
func TestAdminCleanHomeErasesThroughTheRuntime(t *testing.T) {
	ctx := context.Background()

	t.Run("refused before anything stops where the home is unreachable", func(t *testing.T) {
		rec := &wipeRecorder{}
		_, ws, mgr := reaperLifecycleFixture(t)
		mgr.rtFactory = fixedRuntimeFactory{&unreachableHomeRuntime{rec: rec, state: "running"}}
		if err := mgr.cleanHomeByMembership(ctx, ws.MembershipID); !errors.Is(err, runtime.ErrHomeWipeUnsupported) {
			t.Fatalf("cleanHomeByMembership = %v, want ErrHomeWipeUnsupported", err)
		}
		if got := rec.log(); got != "" {
			t.Errorf("the runtime saw %q before the refusal", got)
		}
	})

	t.Run("stops, erases and records the workspace as stopped", func(t *testing.T) {
		rec := &wipeRecorder{}
		st, ws, mgr := reaperLifecycleFixture(t)
		mgr.rtFactory = fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "running"}}}
		if err := mgr.cleanHomeByMembership(ctx, ws.MembershipID); err != nil {
			t.Fatalf("cleanHomeByMembership: %v", err)
		}
		if got := rec.log(); got != "stop,erase" {
			t.Errorf("drove %q, want stop,erase", got)
		}
		if got, _, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID); got.State != "stopped" {
			t.Errorf("workspace state = %q, want stopped", got.State)
		}
	})

	// Erasing under a live workspace leaves its home inconsistent, so a Stop that failed
	// while the workspace still runs ends the operation (runtime.WorkspaceAlive).
	t.Run("aborted when the workspace will not stop", func(t *testing.T) {
		rec := &wipeRecorder{}
		_, ws, mgr := reaperLifecycleFixture(t)
		mgr.rtFactory = fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{
			rec: rec, state: "running", stopErr: errors.New("docker stop: timeout")}}}
		if err := mgr.cleanHomeByMembership(ctx, ws.MembershipID); err == nil {
			t.Fatal("clean home went ahead although the workspace is still running")
		}
		if got := rec.log(); got != "stop" {
			t.Errorf("drove %q, want only the failed stop", got)
		}
	})

	// The administrator's request going away (an ingress idle timeout on a long ecs-ec2
	// erase) must not stop the erase halfway.
	t.Run("finishes when the request has already gone", func(t *testing.T) {
		rec := &wipeRecorder{}
		_, ws, mgr := reaperLifecycleFixture(t)
		mgr.rtFactory = fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "running"}}}
		gone, cancel := context.WithCancel(ctx)
		cancel()
		if err := mgr.cleanHomeByMembership(gone, ws.MembershipID); err != nil {
			t.Fatalf("cleanHomeByMembership with a cancelled request: %v", err)
		}
		if got := rec.log(); got != "stop,erase" {
			t.Errorf("drove %q, want stop,erase", got)
		}
	})
}

func adminRequest(method, target, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	return r
}

func auditActions(t *testing.T, st *store.SQL, tn store.Tenant) []string {
	t.Helper()
	logs, err := st.ListAuditByTenant(context.Background(), tn.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range logs {
		out = append(out, l.Action+":"+l.Detail)
	}
	return out
}

// Over HTTP: a refusal is a 501 with the Console's code, and its audit outcome says it was
// refused; a clean home that ran is audited as done.
func TestAdminCleanHomeEndpoint(t *testing.T) {
	body := `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`

	rec := &wipeRecorder{}
	st, mgr, _, tn := destroyFixture(t, fixedRuntimeFactory{&unreachableHomeRuntime{rec: rec, state: "stopped"}})
	w := httptest.NewRecorder()
	newAdminAPI(mgr).cleanHome(w, adminRequest(http.MethodPost, "/api/admin/clean-home", body))
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "home_wipe_unsupported") {
		t.Errorf("clean home on an unreachable home = %d %s, want 501 home_wipe_unsupported", w.Code, w.Body.String())
	}
	for _, a := range auditActions(t, st, tn) {
		if strings.HasPrefix(a, "workspace.clean_home:") && !strings.HasPrefix(a, "workspace.clean_home:error home_wipe_unsupported") {
			t.Errorf("a refused clean home was audited as done: %q", a)
		}
	}

	rec = &wipeRecorder{}
	st, mgr, _, tn = destroyFixture(t, fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "stopped"}}})
	w = httptest.NewRecorder()
	newAdminAPI(mgr).cleanHome(w, adminRequest(http.MethodPost, "/api/admin/clean-home", body))
	if w.Code != http.StatusOK || rec.log() != "stop,erase" {
		t.Fatalf("clean home = %d %s, runtime saw %q", w.Code, w.Body.String(), rec.log())
	}
	found := false
	for _, a := range auditActions(t, st, tn) {
		found = found || a == "workspace.clean_home:home erased"
	}
	if !found {
		t.Error("a clean home that ran left no audit entry")
	}
}

func callHomeBackups(adm adminAPI, method string) *httptest.ResponseRecorder {
	r := adminRequest(method, "/api/admin/tenants/sales/members/leaver-acme-co-jp/home-backups", "")
	r.SetPathValue("slug", "sales")
	r.SetPathValue("key", "leaver-acme-co-jp")
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		adm.homeBackups(w, r)
	} else {
		adm.deleteHomeBackups(w, r)
	}
	return w
}

// Clean home leaves the backup copies, so the administrator needs to see them and delete
// them as a separate, audited step.
func TestAdminHomeBackupsEndpoints(t *testing.T) {
	rec := &wipeRecorder{}
	rt := &reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "stopped"}, backups: 3}
	st, mgr, _, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	adm := newAdminAPI(mgr)

	w := callHomeBackups(adm, http.MethodGet)
	var got struct {
		Count      int    `json:"count"`
		Newest     string `json:"newest"`
		HomeExists bool   `json:"home_exists"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Count != 3 || got.Newest == "" || !got.HomeExists {
		t.Fatalf("GET home-backups = %d %s, want count 3 with a newest time and the home present", w.Code, w.Body.String())
	}

	w = callHomeBackups(adm, http.MethodDelete)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":3`) {
		t.Fatalf("DELETE home-backups = %d %s, want deleted 3", w.Code, w.Body.String())
	}
	found := false
	for _, a := range auditActions(t, st, tn) {
		found = found || a == "workspace.delete_backups:backup copies of the home deleted: 3"
	}
	if !found {
		t.Errorf("deleting the backups was not audited with the count: %v", auditActions(t, st, tn))
	}

	// Nothing left to delete: the answer says 0, and so does the outcome entry.
	w = callHomeBackups(adm, http.MethodDelete)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":0`) {
		t.Fatalf("second DELETE home-backups = %d %s, want deleted 0", w.Code, w.Body.String())
	}
	zero := false
	for _, a := range auditActions(t, st, tn) {
		zero = zero || a == "workspace.delete_backups:backup copies of the home deleted: 0"
	}
	if !zero {
		t.Errorf("audit entries = %v, want the second deletion recorded as 0", auditActions(t, st, tn))
	}

	// A runtime that keeps no copies says so.
	_, mgr, _, tn2 := destroyFixture(t, fixedRuntimeFactory{&unreachableHomeRuntime{rec: &wipeRecorder{}, state: "stopped"}})
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w := callHomeBackups(newAdminAPI(mgr), method)
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "home_backups_unsupported") {
			t.Errorf("%s home-backups without backups = %d %s, want 501 home_backups_unsupported", method, w.Code, w.Body.String())
		}
	}
	_ = tn2
}

// The Console offers the buttons from these flags, so they have to say what the CP will do.
func TestWhoamiReportsTheHomeOperations(t *testing.T) {
	for name, c := range map[string]struct {
		rt   runtime.Runtime
		want map[string]bool
	}{
		"unreachable": {&unreachableHomeRuntime{rec: &wipeRecorder{}}, map[string]bool{"home_wipe": false, "home_erase": false, "home_backups": false}},
		"reachable":   {&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}}}, map[string]bool{"home_wipe": true, "home_erase": true, "home_backups": true}},
	} {
		mgr := &manager{rtFactory: fixedRuntimeFactory{c.rt}, emailHeader: "X-Forwarded-Email"}
		w := httptest.NewRecorder()
		newWorkspaceAPI(mgr, false).whoami(w, httptest.NewRequest(http.MethodGet, "/api/whoami", nil))
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: whoami %s = %v, want %v", name, k, got[k], v)
			}
		}
	}
}
