// agent_client.go — CP→Agent HTTP クライアントヘルパ（セッション取得）。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"encoding/json"
	"net/http"
)

// countSessions asks the Agent how many sessions are currently running. The quota
// caps concurrency, so only live (alive) sessions count — stopped/resumable ones,
// which the Agent keeps listed for the stopped-TTL window, do not occupy a slot.
func (m *manager) countSessions(ctx context.Context, rt Runtime) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/sessions", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var body struct {
		Sessions []struct {
			Alive bool `json:"alive"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range body.Sessions {
		if s.Alive {
			n++
		}
	}
	return n, nil
}

// agentSessions fetches the Agent's full session list (for the DB mirror).
func (m *manager) agentSessions(ctx context.Context, rt Runtime) ([]sessionWire, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", rt.Endpoint()+"/sessions", nil)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := http.DefaultClient.Do(req)
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
