package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// stateOnlyFactory answers a fixed State and nothing else — the shape every cloud
// adapter has from the CP's point of view: reachable over an API, invisible to
// `docker inspect`.
type stateOnlyFactory struct{ state string }

type stateOnlyRuntime struct {
	stubRuntime
	state string
}

func (r stateOnlyRuntime) State(context.Context) string { return r.state }

func (f stateOnlyFactory) New(runtime.Workspace, runtime.SecretKeys, []string) runtime.Runtime {
	return stateOnlyRuntime{state: f.state}
}

// memberStats reads the container's cgroup through `docker inspect`, which does not
// exist in a CP running as an ECS task. It therefore answered running:false for a
// workspace that was plainly up — and the Console disables "force stop" on exactly
// that field, so a tenant_admin could never stop anyone's workspace on ANY ECS
// deployment (docs/log/64 §64.27, discrepancy 5). Fall back to the runtime's own State.
func TestMemberStatsReportsRunningFromTheRuntimeWhenDockerCannotSee(t *testing.T) {
	for _, c := range []struct {
		state        string
		wantRunning  bool
		wantStarting bool
	}{
		{"running", true, false},
		{"starting", false, true},
		{"stopped", false, false},
		{"none", false, false},
	} {
		_, mgr, _, _ := destroyFixture(t, stateOnlyFactory{state: c.state})
		r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members/leaver-acme-co-jp/stats", nil)
		r.SetPathValue("slug", "sales")
		r.SetPathValue("key", "leaver-acme-co-jp")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		newAdminAPI(mgr).memberStats(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("state=%s: %d %s", c.state, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("state=%s: %v", c.state, err)
		}
		if out["running"] != c.wantRunning {
			t.Errorf("state=%s: running=%v, want %v", c.state, out["running"], c.wantRunning)
		}
		if starting, _ := out["starting"].(bool); starting != c.wantStarting {
			t.Errorf("state=%s: starting=%v, want %v", c.state, starting, c.wantStarting)
		}
	}
}

// A running workspace whose Agent is silent is reported as such next to running:true, so the
// administrator's detail can say why nothing works while Force-stop stays offered (#2518).
func TestMemberStatsFlagsASilentAgentAndNothingElse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // nothing listens: the endpoint is only a key here
	key := endpointKey(srv.URL)
	for _, c := range []struct {
		state string
		want  bool
	}{{"running", true}, {"starting", false}} {
		_, mgr, _, _ := destroyFixture(t, endpointFactory{state: c.state, endpoint: srv.URL})
		now := time.Now()
		agentLive.mu.Lock()
		agentLive.m[key] = &liveEntry{since: now.Add(-time.Hour), answered: now.Add(-time.Hour), probedAt: now, silent: true}
		agentLive.mu.Unlock()
		r := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/sales/members/leaver-acme-co-jp/stats", nil)
		r.SetPathValue("slug", "sales")
		r.SetPathValue("key", "leaver-acme-co-jp")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		newAdminAPI(mgr).memberStats(w, r)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("state=%s: %v (%d %s)", c.state, err, w.Code, w.Body.String())
		}
		if got, _ := out["agent_silent"].(bool); got != c.want {
			t.Errorf("state=%s: agent_silent=%v, want %v", c.state, got, c.want)
		}
		agentLive.forget(key)
	}
}

type endpointFactory struct{ state, endpoint string }

func (f endpointFactory) New(runtime.Workspace, runtime.SecretKeys, []string) runtime.Runtime {
	return stateOnlyRuntime{stubRuntime: stubRuntime{endpoint: f.endpoint}, state: f.state}
}
