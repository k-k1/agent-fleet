package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// restLoginFlow gates the agent-CLI login endpoints (Claude/agy/cursor/kiro/
// opencode/codex/github start-poll-complete) on the workspace being fully rolled
// out: their state (OAuth flow_id, device code, PTY login session) lives only in
// the Workspace Agent process's memory, so a wake-triggered task swap mid-flow
// silently drops it (docs investigation 2026-08-19, af.lazmix.jp).
func doCPRestLoginFlow(proxy agentProxyAPI, res *resolved, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	proxy.restLoginFlow(rec, req, res)
	return rec
}

func TestRestLoginFlowBlocksUntilWorkspaceIsFullyRolledOut(t *testing.T) {
	var called atomic.Int32
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called.Add(1)
	}))
	defer cleanup()
	baseRuntime := res.rt.(stubRuntime)
	for _, state := range []string{"starting", "stopped", "none"} {
		res.rt = fsStateRuntime{stubRuntime: baseRuntime, state: state}
		rec := doCPRestLoginFlow(proxy, res, http.MethodPost, "/api/connections/claude/start", "{}")
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status=%d body=%s", state, rec.Code, rec.Body.String())
			continue
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &envelope) != nil || envelope.Error.Code != "workspace_starting" {
			t.Errorf("%s: body=%s", state, rec.Body.String())
		}
	}
	if called.Load() != 0 {
		t.Fatalf("non-running requests reached the Agent %d times", called.Load())
	}
}

func TestRestLoginFlowProxiesOnceRunning(t *testing.T) {
	var called atomic.Int32
	proxy, res, _, cleanup := newFSProxyTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		if r.URL.Path != "/connections/claude/complete" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"connected":true}`))
	}))
	defer cleanup()
	// newFSProxyTest already resolves res.rt to a stubRuntime; the fixture's
	// implicit default State() is "running" (§ stubRuntime), matching what
	// registerConnectionRoutes wires restLogin to in production.
	rec := doCPRestLoginFlow(proxy, res, http.MethodPost, "/api/connections/claude/complete", `{"flowId":"f1","code":"abc"}`)
	if rec.Code != http.StatusOK || called.Load() != 1 {
		t.Fatalf("status=%d called=%d body=%s", rec.Code, called.Load(), rec.Body.String())
	}
}
