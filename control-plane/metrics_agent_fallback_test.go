// metrics_agent_fallback_test.go — the contract that on a deployment where the host
// cannot read the cgroup (every ECS profile) the measured resource figures come from the
// Agent (docs/log/63 §63.9).
//
// Break it and the symptom is a running workspace showing "–" for memory, CPU and disk
// alike, with no exception and no log line: the tiles are simply blank, so nothing tells
// anyone it broke.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// agentStatsRuntime is the shape of a cloud adapter — State is known through an API but
// nothing is visible to docker — plus the Agent's endpoint.
type agentStatsRuntime struct {
	stubRuntime
	state string
}

func (r agentStatsRuntime) State(context.Context) string { return r.state }

type agentStatsFactory struct {
	endpoint string
	state    string
}

func (f agentStatsFactory) New(runtime.Workspace, string, []string) runtime.Runtime {
	return agentStatsRuntime{stubRuntime: stubRuntime{endpoint: f.endpoint}, state: f.state}
}

// statsAgent is an Agent that answers GET /workspace/stats only, with body verbatim.
func statsAgent(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace/stats" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func memberStatsJSON(t *testing.T, mgr *manager) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members/leaver-acme-co-jp/stats", nil)
	r.SetPathValue("slug", "sales")
	r.SetPathValue("key", "leaver-acme-co-jp")
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	newAdminAPI(mgr).memberStats(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The point of the file: even for a workspace docker cannot see, the values the Agent
// read from its own cgroup reach the member detail view.
func TestMemberStatsFallsBackToTheAgentWhenTheHostCannotSeeTheCgroup(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":1073741824,"mem_max":4294967296,"cpu_pct":12.5,"oom_kill_total":0,"disk_used":21474836480,"disk_total":42949672960}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	if out["running"] != true {
		t.Fatalf("running = %v, want true", out["running"])
	}
	if out["mem_used"] != float64(1073741824) {
		t.Errorf("mem_used = %v, want 1073741824", out["mem_used"])
	}
	if out["mem_max"] != float64(4294967296) {
		t.Errorf("mem_max = %v, want 4294967296", out["mem_max"])
	}
	if out["cpu_pct"] != 12.5 {
		t.Errorf("cpu_pct = %v, want 12.5", out["cpu_pct"])
	}
	// The CP has no path to du here, so the disk figures come from the Agent's statfs.
	// Reporting the capacity too is the point on `ecs-ec2` (home is a persistent EBS
	// volume) — the UI uses it as the denominator.
	if out["disk_used"] != float64(21474836480) || out["disk_total"] != float64(42949672960) {
		t.Errorf("disk = %v/%v, want 21474836480/42949672960", out["disk_used"], out["disk_total"])
	}
}

// A cpu_pct of 0 and "CPU cannot be measured" are different answers. Omitting zero
// values collapses them, and the UI then draws the unmeasurable as 0%.
func TestZeroGaugesSurviveTheWire(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":100,"cpu_pct":0,"oom_kill_total":0}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	if v, ok := out["cpu_pct"]; !ok || v != float64(0) {
		t.Errorf("cpu_pct = %v (present=%v), want 0 present", v, ok)
	}
	if v, ok := out["oom_kill_total"]; !ok || v != float64(0) {
		t.Errorf("oom_kill_total = %v (present=%v), want 0 present", v, ok)
	}
}

// An axis that could not be measured drops its key entirely. A 0 substituted by the CP
// would erase that distinction.
func TestUnmeasuredAxesStayAbsent(t *testing.T) {
	srv := statsAgent(t, http.StatusOK, `{"mem_used":100}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	for _, key := range []string{"cpu_pct", "mem_max", "disk_total"} {
		if v, ok := out[key]; ok {
			t.Errorf("%s = %v, want the key absent", key, v)
		}
	}
}

// A CP newer than the image talks to an Agent without this route (404). Never mix fields
// out of the error body in — degrade silently to "not measurable".
func TestOlderAgentWithoutTheRouteDegrades(t *testing.T) {
	srv := statsAgent(t, http.StatusNotFound, `{"error":"not_found"}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})

	out := memberStatsJSON(t, mgr)
	if out["running"] != true {
		t.Errorf("running = %v, want true — 版ずれでも稼働の事実は変わらない", out["running"])
	}
	if _, ok := out["mem_used"]; ok {
		t.Errorf("mem_used present (%v) from a 404 body", out["mem_used"])
	}
}

// A stopped workspace is never probed. Hitting an unreachable endpoint every tick delays
// the SSE tick by the timeout — a 5s wait inside a 4s cycle.
func TestStoppedWorkspaceIsNotProbed(t *testing.T) {
	probed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probed = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"mem_used":1}`))
	}))
	defer srv.Close()
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "stopped"})

	out := memberStatsJSON(t, mgr)
	if probed {
		t.Error("stopped workspace was probed for stats")
	}
	if out["running"] != false {
		t.Errorf("running = %v, want false", out["running"])
	}
}
