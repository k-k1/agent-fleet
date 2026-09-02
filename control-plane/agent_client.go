// agent_client.go — CP→Agent HTTP クライアントヘルパ（セッション取得）。
// manager.go からの機械的分割（docs/log/23 P2-W2）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// agentHTTPClient is the CP→Agent client for control-loop calls (scheduler,
// reaper, session mirror / quota checks). The per-request timeout keeps a
// stalled Agent from blocking the single scheduler/reaper goroutine forever
// (those loops run every fire/sweep for ALL users). It is generous enough for
// the slowest bounded Agent call (an /input confirm wait is ~30s). Streaming
// endpoints (proxy/preview/browser) must NOT use this client.
var agentHTTPClient = &http.Client{
	Timeout: 2 * time.Minute,
	// Service Connect の別名が引けないときに Cloud Map で引き直す共有 Transport
	// （agent_dial.go）。CP→Agent の経路は全部これを通す。
	Transport: newAgentTransport(),
	// Never re-follow an Agent redirect with the bearer attached — control-loop
	// paths are CP-built, so a 3xx is unexpected and surfaces as its status code.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// agentLongCallClient is agentHTTPClient WITHOUT the client timeout, for the few
// deliberately long synchronous Agent calls whose deadline the CALLER's context
// carries (POST /assistant-turns: 8 min so the Agent-side operatorTurnTimeout —
// which may pause 4 min on a bridge approval — always gives up first). The
// 2-minute shared timeout would fake-fail those turns.
var agentLongCallClient = &http.Client{
	Transport:     newAgentTransport(),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// healthzClient bounds the /healthz probes (waitAgentHealthy): the poll loops
// re-issue every couple of seconds, so one hung request must not stall its
// caller — a wedged probe inside a scheduler fire would otherwise hold the
// tick's wg.Wait forever (the same failure mode agentHTTPClient closed).
var healthzClient = &http.Client{Timeout: 5 * time.Second, Transport: newAgentTransport()}

// agentStatsClient bounds the resource-gauge poll. It must be its own client: the
// stats read sits inside the 4-second /api/events tick, so a wedged Agent would
// otherwise hold a subscriber's tick for the shared client's 2 MINUTES — the SSE
// stream would go silent and the Console would show stale everything. A gauge that
// cannot be read in a few seconds is simply "not measurable this tick".
var agentStatsClient = &http.Client{Timeout: 5 * time.Second, Transport: newAgentTransport()}

// agentStats asks the Agent for the workspace's own resource gauges — the only
// source that works on a runtime the CP cannot see into (every ECS profile: no
// docker binary, no cgroup for that workspace on the CP's host). The Agent reads
// its OWN cgroup namespace, so the numbers mean the same thing as the host-side
// read (docs/log/63 §63.9 / workspace/agent/internal/resources).
//
// The returned map carries only the axes the Agent could actually read — a missing
// key means "not measurable", never zero. Callers merge it into the stats map, so
// keys are deliberately identical to the docker path's.
func (m *manager) agentStats(ctx context.Context, rt runtime.Runtime) (map[string]any, error) {
	if rt.Endpoint() == "" {
		return nil, fmt.Errorf("agent /workspace/stats: no endpoint")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/workspace/stats", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentStatsClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An older Agent has no such route (404). Report it as an error so the caller
		// leaves the axes unmeasured rather than merging an error body's fields — a
		// CP newer than the image it launched must degrade, not lie.
		return nil, fmt.Errorf("agent /workspace/stats: %s", resp.Status)
	}
	var body struct {
		MemUsed      *uint64  `json:"mem_used"`
		MemMax       *uint64  `json:"mem_max"`
		CPUPct       *float64 `json:"cpu_pct"`
		OOMKillTotal *uint64  `json:"oom_kill_total"`
		DiskUsed     *uint64  `json:"disk_used"`
		DiskTotal    *uint64  `json:"disk_total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]any{}
	// Decoded into pointers on purpose: cpu_pct 0 and oom_kill_total 0 are real
	// readings, so "present but zero" must not be dropped the way a bare zero value
	// would be.
	if body.MemUsed != nil {
		out["mem_used"] = *body.MemUsed
	}
	if body.MemMax != nil {
		out["mem_max"] = *body.MemMax
	}
	if body.CPUPct != nil {
		out["cpu_pct"] = *body.CPUPct
	}
	if body.OOMKillTotal != nil {
		out["oom_kill_total"] = *body.OOMKillTotal
	}
	if body.DiskUsed != nil {
		out["disk_used"] = *body.DiskUsed
	}
	if body.DiskTotal != nil {
		out["disk_total"] = *body.DiskTotal
	}
	return out, nil
}

// countSessions asks the Agent how many sessions are currently running. The quota
// caps concurrency, so only live (alive) sessions count — stopped/resumable ones,
// which the Agent keeps listed for the stopped-TTL window, do not occupy a slot.
// shell/ssm sessions are excluded: they are lightweight terminals (a local shell or
// a remote SSM tunnel), not agent runs, so they are unmetered by the session quota
// (see isUnmeteredKind).
func (m *manager) countSessions(ctx context.Context, rt runtime.Runtime) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/sessions", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An error body (401/5xx JSON) would otherwise decode into zero sessions
		// and read as "0 running" — which waves requests past the session quota.
		return 0, fmt.Errorf("agent /sessions: %s", resp.Status)
	}
	var body struct {
		Sessions []struct {
			Alive bool   `json:"alive"`
			Kind  string `json:"kind"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range body.Sessions {
		if s.Alive && !isUnmeteredKind(s.Kind) {
			n++
		}
	}
	return n, nil
}

// isUnmeteredKind reports whether a session kind is excluded from the concurrent-
// session quota. shell and ssm are plain terminals rather than agent runs, so they
// neither consume a slot nor are blocked when the cap is reached.
func isUnmeteredKind(kind string) bool {
	return kind == "shell" || kind == "ssm"
}

// agentSessions fetches the Agent's full session list (for the DB mirror).
func (m *manager) agentSessions(ctx context.Context, rt runtime.Runtime) ([]sessionWire, error) {
	env, err := m.agentSessionsEnv(ctx, rt)
	return env.Sessions, err
}

// agentSessionsEnvelope is the Agent's GET /sessions body. RepoJobs is the count of
// running repository imports (docs/log/78) — a workspace with none of its own sessions can
// still be busy for an hour cloning, and the reaper must not stop it (the import dies
// with the container and leaves a half-written working copy).
type agentSessionsEnvelope struct {
	Sessions []sessionWire `json:"sessions"`
	RepoJobs int           `json:"repoJobs"`
}

// agentSessionsEnv is agentSessions plus the workspace-level busy signals that ride
// the same response, so the reaper needs no extra request per sweep.
func (m *manager) agentSessionsEnv(ctx context.Context, rt runtime.Runtime) (agentSessionsEnvelope, error) {
	var body agentSessionsEnvelope
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/sessions", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return body, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An error body would decode as an empty list and read as "no sessions" —
		// the reaper would mark a busy workspace cold and ReplaceSessions would
		// wipe the DB mirror.
		return body, fmt.Errorf("agent /sessions: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return agentSessionsEnvelope{}, err
	}
	return body, nil
}
