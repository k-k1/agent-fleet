// workspace_machine_test.go — the contract of GET /api/workspace/machine: two answers that
// must stay two answers.
//
// Break the separation and the failure is silent in both directions — a member is told they
// are on the box their settings describe while running on the previous one, or a shared
// host's RAM is presented as their own machine. Neither raises anything.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// slotRuntime is an adapter that declares a box, the way the EC2 slot pool does.
type slotRuntime struct {
	stubRuntime
	profile runtime.WorkspaceMachine
}

func (r slotRuntime) MachineProfile() runtime.WorkspaceMachine { return r.profile }

// machineAgent answers GET /workspace/machine only, with the body verbatim.
func machineAgent(t *testing.T, status int, body string) (*httptest.Server, *bool) {
	t.Helper()
	probed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspace/machine" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		probed = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &probed
}

func machineJSON(t *testing.T, mgr *manager, rt runtime.Runtime) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace/machine", nil)
	newWorkspaceAPI(mgr, false).machine(w, r, &resolved{rt: rt, mv: store.MembershipView{}})
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const ec2Measured = `{"arch":"arm64","vcpu":2,"mem_max":7516192768,"mem_total":8174716928,` +
	`"disk_total":64424509440,"instance_type":"m8g.large"}`

func ec2Declared() runtime.WorkspaceMachine {
	return runtime.WorkspaceMachine{
		InstanceType: "m8g.large", Arch: "arm64", VCPU: 2,
		SlotMemMiB: 8192, MemCapMiB: 6656, HomeGiB: 60,
		ClassID: "arm", ClassLabel: "低コスト (Arm)", Dedicated: true,
	}
}

func TestMachineReportsMeasuredAndDeclaredSeparately(t *testing.T) {
	srv, _ := machineAgent(t, http.StatusOK, ec2Measured)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	rt := slotRuntime{stubRuntime{endpoint: srv.URL, state: "running"}, ec2Declared()}

	out := machineJSON(t, mgr, rt)
	if out["running"] != true {
		t.Fatalf("running = %v, want true", out["running"])
	}
	m, _ := out["measured"].(map[string]any)
	d, _ := out["declared"].(map[string]any)
	if m == nil || d == nil {
		t.Fatalf("want both halves, got %v", out)
	}
	if m["instance_type"] != "m8g.large" || m["arch"] != "arm64" || m["vcpu"] != float64(2) {
		t.Errorf("measured = %v", m)
	}
	// The box's RAM survives only because the declared half says the box is this member's.
	if m["mem_total"] != float64(8174716928) {
		t.Errorf("mem_total = %v, want the box's RAM on a dedicated box", m["mem_total"])
	}
	if d["class_label"] != "低コスト (Arm)" || d["mem_cap_mib"] != float64(6656) {
		t.Errorf("declared = %v", d)
	}
}

// The case the whole design exists for: a size change applies at the next start, so the box
// in the configuration is not the box under the container. Neither value may overwrite the
// other — the screen shows both and says which is which.
func TestMachineKeepsADisagreementVisible(t *testing.T) {
	srv, _ := machineAgent(t, http.StatusOK, ec2Measured)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	declared := ec2Declared()
	declared.InstanceType, declared.SlotMemMiB, declared.MemCapMiB = "m8g.xlarge", 16384, 14336
	rt := slotRuntime{stubRuntime{endpoint: srv.URL, state: "running"}, declared}

	out := machineJSON(t, mgr, rt)
	m, _ := out["measured"].(map[string]any)
	d, _ := out["declared"].(map[string]any)
	if m["instance_type"] != "m8g.large" {
		t.Errorf("measured instance_type = %v, want the box it is actually on", m["instance_type"])
	}
	if d["instance_type"] != "m8g.xlarge" {
		t.Errorf("declared instance_type = %v, want the box the next start uses", d["instance_type"])
	}
}

// A shared host's /proc/meminfo and SMBIOS describe a machine the member does not have to
// themselves. The cgroup-derived figures still describe this container and stay.
func TestMachineHidesTheBoxWhenItIsNotTheMembersOwn(t *testing.T) {
	srv, _ := machineAgent(t, http.StatusOK, ec2Measured)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	// No MachineProfile at all (docker / Fargate): nothing declares a dedicated box.
	rt := agentStatsRuntime{stubRuntime{endpoint: srv.URL}, "running"}

	out := machineJSON(t, mgr, rt)
	if _, ok := out["declared"]; ok {
		t.Errorf("declared present (%v) for a runtime with no box to name", out["declared"])
	}
	m, _ := out["measured"].(map[string]any)
	if v, ok := m["mem_total"]; ok {
		t.Errorf("mem_total = %v, want it dropped on a shared host", v)
	}
	if v, ok := m["instance_type"]; ok {
		t.Errorf("instance_type = %v, want it dropped on a shared host", v)
	}
	if m["mem_max"] != float64(7516192768) || m["arch"] != "arm64" {
		t.Errorf("measured = %v, want this container's own cgroup figures kept", m)
	}
}

// Stopped: the Agent is not reachable and must not be dialled, but the declared half still
// answers "what will I start on" — which is the useful thing to know while it is down.
func TestStoppedWorkspaceIsNotProbedForItsMachine(t *testing.T) {
	srv, probed := machineAgent(t, http.StatusOK, ec2Measured)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "stopped"})
	rt := slotRuntime{stubRuntime{endpoint: srv.URL, state: "stopped"}, ec2Declared()}

	out := machineJSON(t, mgr, rt)
	if *probed {
		t.Error("stopped workspace was probed for its machine")
	}
	if out["running"] != false {
		t.Errorf("running = %v, want false", out["running"])
	}
	if _, ok := out["measured"]; ok {
		t.Errorf("measured present (%v) for a stopped workspace", out["measured"])
	}
	if d, _ := out["declared"].(map[string]any); d == nil || d["instance_type"] != "m8g.large" {
		t.Errorf("declared = %v, want the box the next start uses", out["declared"])
	}
}

// A CP newer than the image it launched talks to an Agent without the route. Degrade to the
// declared half instead of mixing fields out of the error body.
func TestOlderAgentWithoutTheMachineRouteDegrades(t *testing.T) {
	srv, _ := machineAgent(t, http.StatusNotFound, `{"error":"not_found","arch":"x86_64"}`)
	_, mgr, _, _ := destroyFixture(t, agentStatsFactory{endpoint: srv.URL, state: "running"})
	rt := slotRuntime{stubRuntime{endpoint: srv.URL, state: "running"}, ec2Declared()}

	out := machineJSON(t, mgr, rt)
	if _, ok := out["measured"]; ok {
		t.Errorf("measured = %v, built out of a 404 body", out["measured"])
	}
	if out["running"] != true {
		t.Errorf("running = %v, want true — a version skew does not stop it running", out["running"])
	}
	if d, _ := out["declared"].(map[string]any); d == nil {
		t.Error("declared missing; the screen has nothing to show")
	}
}
