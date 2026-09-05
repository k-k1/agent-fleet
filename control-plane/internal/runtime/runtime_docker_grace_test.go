// runtime_docker_grace_test.go — the courtesy health grace the docker adapter waits on
// after a start.
//
// It reads dockerRuntime's unexported extraEnv, so it belongs beside the adapter.
package runtime

import (
	"testing"
	"time"
)

// TestStartHealthWaitSelfUpdate: the docker start budget.
//
// It used to stretch to 300s exactly for the boot that can run the long pre-agent
// update — because overrunning it was a FAILURE. It is not a failure any more
// (runtime_health.go): the budget only decides whether Start answers "running" or
// "starting", so a self-updating boot no longer needs a longer one, and a 300s block
// inside an HTTP request is what the Runtime port forbids (docs/log/62 §62.5 = a 504).
// What must stay pinned is the unattended carve-out: the scheduler's tick goroutine
// polls the Agent itself afterwards, so making it sit here buys nothing.
func TestStartHealthWaitSelfUpdate(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want time.Duration
	}{
		{"no self-update", []string{"FOO=1"}, dockerStartGrace},
		{"opt-in allowed but off", []string{"AF_AGENT_SELF_UPDATE_ALLOWED=1"}, dockerStartGrace},
		{"self-update on", []string{"AF_AGENT_SELF_UPDATE_ALLOWED=1", "AF_AGENT_SELF_UPDATE=1"}, dockerStartGrace},
		// The unattended marker wins even alongside the opt-in: nobody is waiting on
		// this answer, and no update runs either.
		{"unattended overrides", []string{"AF_AGENT_SELF_UPDATE=1", UnattendedStartEnv}, 15 * time.Second},
		{"unattended alone", []string{UnattendedStartEnv}, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &dockerRuntime{extraEnv: tc.env}
			if got := d.startHealthWait(); got != tc.want {
				t.Fatalf("startHealthWait(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
	// The synchronous grace must stay inside the total an Agent-dependent API is willing
	// to wait. Invert the two and Start alone consumes the deadline ensureWorkspaceReady
	// set at the entrance, leaving no time at all to wait for reachability.
	if dockerStartGrace >= AgentReadyWait() {
		t.Fatalf("start grace %v must stay under the ready budget %v", dockerStartGrace, AgentReadyWait())
	}
}
