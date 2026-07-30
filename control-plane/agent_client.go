// agent_client.go — CP→Agent HTTP クライアントヘルパ（セッション取得）。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// agentHTTPClient is the CP→Agent client for control-loop calls (scheduler,
// reaper, session mirror / quota checks). The per-request timeout keeps a
// stalled Agent from blocking the single scheduler/reaper goroutine forever
// (those loops run every fire/sweep for ALL users). It is generous enough for
// the slowest bounded Agent call (an /input confirm wait is ~30s). Streaming
// endpoints (proxy/preview/browser) must NOT use this client.
var agentHTTPClient = &http.Client{Timeout: 2 * time.Minute}

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
	var body struct {
		Sessions []sessionWire `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Sessions, nil
}
