// agent_client.go — CP→Agent HTTP クライアントヘルパ（セッション取得）。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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

// countSessions asks the Agent how many sessions are currently running. The quota
// caps concurrency, so only live (alive) sessions count — stopped/resumable ones,
// which the Agent keeps listed for the stopped-TTL window, do not occupy a slot.
// shell/ssm sessions are excluded: they are lightweight terminals (a local shell or
// a remote SSM tunnel), not agent runs, so they are unmetered by the session quota
// (see isUnmeteredKind).
func (m *manager) countSessions(ctx context.Context, rt Runtime) (int, error) {
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
func (m *manager) agentSessions(ctx context.Context, rt Runtime) ([]sessionWire, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/sessions", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// An error body would decode as an empty list and read as "no sessions" —
		// the reaper would mark a busy workspace cold and ReplaceSessions would
		// wipe the DB mirror.
		return nil, fmt.Errorf("agent /sessions: %s", resp.Status)
	}
	var body struct {
		Sessions []sessionWire `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Sessions, nil
}
