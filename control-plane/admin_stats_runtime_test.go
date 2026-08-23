package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

func (f stateOnlyFactory) New(Workspace, string, []string) Runtime {
	return stateOnlyRuntime{state: f.state}
}

// memberStats reads the container's cgroup through `docker inspect`, which does not
// exist in a CP running as an ECS task. It therefore answered running:false for a
// workspace that was plainly up — and the Console disables "force stop" on exactly
// that field, so a tenant_admin could never stop anyone's workspace on ANY ECS
// deployment (docs/64 §64.27, 齟齬 5). Fall back to the runtime's own State.
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
