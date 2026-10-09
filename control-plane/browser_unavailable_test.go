package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// browserlessTestRuntime is a running workspace whose runtime withholds browser features,
// the way the kubernetes adapter does (runtime/browser_support.go).
type browserlessTestRuntime struct{ browserTestRuntime }

func (browserlessTestRuntime) BrowserUnavailable() string { return "kubernetes" }

// fargateTestRuntime answers the way the ecs (Fargate) adapter does.
type fargateTestRuntime struct{ browserTestRuntime }

func (fargateTestRuntime) BrowserUnavailable() string { return "ecs" }

// The CP refuses every browser route itself, in any state and without calling the Agent,
// so the answer does not depend on the image and a stopped workspace is not told to start.
func TestBrowserRoutesRefuseOnBrowserlessRuntime(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		t.Run(state, func(t *testing.T) {
			var agentCalls atomic.Int32
			agent := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { agentCalls.Add(1) }))
			defer agent.Close()
			env := newBrowserTestEnv(t, browserlessTestRuntime{browserTestRuntime{endpoint: agent.URL, state: state}})
			for _, request := range []*http.Request{
				httptest.NewRequest(http.MethodPost, "/api/browser/pages", bytes.NewBufferString(`{}`)),
				httptest.NewRequest(http.MethodGet, "/api/browser/attachments", nil),
				httptest.NewRequest(http.MethodGet, "/ws/browser?id=page-1", nil),
				httptest.NewRequest(http.MethodGet, "/ws/browser-attachments?id=a1", nil),
			} {
				w := httptest.NewRecorder()
				env.mux.ServeHTTP(w, request)
				var body struct {
					Error struct{ Code, Message string } `json:"error"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				if w.Code != http.StatusConflict || body.Error.Code != "browser_unavailable" {
					t.Fatalf("%s %s = %d %s, want 409 browser_unavailable", request.Method, request.URL.Path, w.Code, w.Body.String())
				}
				if !strings.Contains(body.Error.Message, "(kubernetes)") {
					t.Fatalf("message %q does not name the runtime", body.Error.Message)
				}
			}
			if n := agentCalls.Load(); n != 0 {
				t.Fatalf("the Agent was called %d times", n)
			}
		})
	}
}

// The workspace payload carries the decision in every state, so the Console can hide its
// browser entry points before a start; every other runtime keeps the payload's shape.
func TestWorkspacePayloadBrowserUnavailable(t *testing.T) {
	a := workspaceAPI{}
	ctx := context.Background()
	for _, state := range []string{"running", "none"} {
		rt := browserlessTestRuntime{browserTestRuntime{state: state}}
		if m := a.workspacePayload(ctx, &resolved{rt: rt}, state); m["browserUnavailable"] != "kubernetes" {
			t.Fatalf("%s: browserUnavailable = %v, want kubernetes", state, m["browserUnavailable"])
		}
	}
	if m := a.workspacePayload(ctx, &resolved{rt: fargateTestRuntime{browserTestRuntime{state: "running"}}}, "running"); m["browserUnavailable"] != "ecs" {
		t.Fatalf("fargate: browserUnavailable = %v, want ecs", m["browserUnavailable"])
	}
	m := a.workspacePayload(ctx, &resolved{rt: browserTestRuntime{state: "running"}}, "running")
	if _, ok := m["browserUnavailable"]; ok {
		t.Fatalf("a runtime with browser features carries browserUnavailable = %v", m["browserUnavailable"])
	}
}
