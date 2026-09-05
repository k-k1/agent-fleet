// runtime_health.go — shared pieces for waiting on the Agent (/healthz).
//
// One contract governs this surface: "the Agent is not answering yet" is not a Start
// failure. Start owns bringing the container/process up; the window until the Agent
// answers is surfaced through State() as "starting". The ECS adapter was always built
// that way (watchReady, docs/log/62 §62.5 "A readiness failure must still NEVER fail
// Start"); only the local docker and native adapters treated "/healthz did not answer 200
// within the budget" as a failed start. That mismatch cost three things:
//
//   - Scheduled-run wakes died with `agent did not become healthy within 15s` while the
//     container was fine — the real cause is the entrypoint's CLI self-update (~60s,
//     measured).
//   - The same 15s applied to a human Start, and the budget only widens to 300s when the
//     self-update opt-in is ON, so users with it OFF were the ones hitting a red toast on
//     a lean/cold start, a slow link or a network home — and the workspace worked fine
//     seconds later, because the start had never stopped.
//   - Treating it as a failure made the recorded state a lie:
//     ensureWorkspaceStartedRTLocked returns on a Start error without marking the DB
//     running or touching the idle clock, leaving a running container recorded as stopped
//     with a stale lastSeen in the reaper.
//
// So the budget's meaning changed rather than its number: it is how long a caller waits
// synchronously, not a deadline. Past it we claim "starting" and let the poller converge.
package runtime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AgentBootBudget is the ceiling on claiming "still booting". Once the synchronous grace
// (startHealthWait) lapses, State() keeps returning "starting" until this deadline.
//
// 300s for the same reason as the native rootfs path and the ECS startTimeout: it has to
// cover a cold pull, the entrypoint's boot-install and the ~60s (measured) CLI
// self-update. It must stay time-limited — a "starting" that never converges is a box the
// Console can neither stop nor recreate (docs/log/70 §70.14.6). Once it expires we fall
// back to running: the container does exist.
const AgentBootBudget = 300 * time.Second

// AgentReadyWait is how long an API that needs the Agent may wait in place
// (AF_AGENT_READY_WAIT_SEC). The 55s default keeps the wait inside the ALB's 60s idle
// timeout (deploy/aws/ecs/cfn/30-ingress.yaml) — we are waiting inside an HTTP handler, so
// past that the response itself disappears into a 504 (measured, docs/log/62 §62.5). When
// the wait runs out the answer is 409 workspace_starting; the start continues in the
// background, so the caller's or the Console's next retry gets through.
func AgentReadyWait() time.Duration {
	if n := EnvInt("AF_AGENT_READY_WAIT_SEC", 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return 55 * time.Second
}

// agentHealthWait returns the Start health-wait budget: the adapter default,
// overridable via AF_AGENT_HEALTH_WAIT_SEC. Lean (boot-install) deployments
// need more than the classic 15s on FIRST start — the entrypoint downloads the
// pinned CLIs before the agent listens (docs/log/35 §35.4.1); the native rootfs
// adapter defaults higher for the same reason.
//
// Past this budget the start is not failed — it returns claiming "starting".
func agentHealthWait(def time.Duration) time.Duration {
	if n := EnvInt("AF_AGENT_HEALTH_WAIT_SEC", 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

// agentNotReadyError means only that /healthz did not answer 200 in time. The message text
// is kept byte-for-byte — scheduled-run history and operational greps match on it — but the
// distinct type is what keeps callers from mistaking this for a failure.
type agentNotReadyError struct{ timeout time.Duration }

func (e agentNotReadyError) Error() string {
	return fmt.Sprintf("agent did not become healthy within %s", e.timeout)
}

// agentHealthy makes a single /healthz call. healthzClient caps it at 5s (agent_client.go).
func agentHealthy(ctx context.Context, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := healthzClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// WaitAgentHealthy polls the Agent's /healthz until it answers 200 or the
// timeout lapses. Shared by the docker and native local adapters (ECS has its
// own converge loop) and by ensureWorkspaceReady.
//
// Two errors come back and they mean very different things:
//   - agentNotReadyError … it simply has not arrived yet; the start is still going.
//   - a ctx cancellation … the caller left (request aborted, lease lost).
func WaitAgentHealthy(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Don't keep polling to the full timeout on an already-canceled ctx.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("agent health wait canceled: %w", err)
		}
		if agentHealthy(ctx, endpoint) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return agentNotReadyError{timeout: timeout}
}

// agentStartingMarker records "this start is still waiting for the Agent" in the
// workspace's dataDir. A file rather than a process-local variable, for two reasons:
//
//   - Whoever calls State() need not be the goroutine that ran Start. The 4s
//     /api/workspace poll, SSE, the proxy and the reaper each build their own Runtime, so
//     an in-memory mark is invisible to them — and invisible means opening a terminal
//     against a workspace that reads "running" while the Agent is absent, which is the
//     symptom being fixed.
//   - If the CP dies mid-start, at most one file is left behind. Its content is a
//     deadline, so it clears itself on expiry or on a /healthz 200.
type agentStartingMarker struct{ path string }

// agentStartingMarkerIn is the mark directly under dataDir. With no dataDir (a minimal
// struct in a test, say) it returns an inert mark, i.e. never starting.
func agentStartingMarkerIn(dataDir string) agentStartingMarker {
	if dataDir == "" {
		return agentStartingMarker{}
	}
	return agentStartingMarker{path: filepath.Join(dataDir, ".agent-starting")}
}

// arm records "starting until <deadline>". Best-effort: failing to write only makes the
// workspace look running as it did before, which is the safe direction to fall.
func (m agentStartingMarker) arm(until time.Time) {
	if m.path == "" {
		return
	}
	_ = os.WriteFile(m.path, []byte(strconv.FormatInt(until.Unix(), 10)+"\n"), 0o644)
}

func (m agentStartingMarker) clear() {
	if m.path == "" {
		return
	}
	_ = os.Remove(m.path)
}

// active reports whether this workspace is still inside its boot window. While the mark
// exists, every call makes exactly one /healthz probe and drops the mark once the Agent is
// up, so this converges even when the CP that ran Start is gone.
func (m agentStartingMarker) active(ctx context.Context, endpoint string) bool {
	if m.path == "" {
		return false
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || !time.Now().Before(time.Unix(sec, 0)) {
		m.clear() // expired or corrupt mark: stop claiming starting
		return false
	}
	if agentHealthy(ctx, endpoint) {
		m.clear()
		return false
	}
	return true
}
