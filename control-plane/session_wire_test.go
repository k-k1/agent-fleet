// session_wire_test.go — drop regression for the sessionWire relay.
//
// sessionWire decodes the Agent's /sessions response and re-emits it, so a field that the
// Agent wire (workspace/agent/internal/session.Session) has and this struct does not is
// dropped silently — an accident that has already cost title, driver and the
// color/context/exit fields. Round-trips a whole Agent-shaped JSON body and pins that the
// fields the Console consumes survive it.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubRuntime is a minimal Runtime that answers for real only on Endpoint/Token.
type stubRuntime struct {
	endpoint string
	token    string
	state    string // "" = running; existing callers rely on that default
}

func (s stubRuntime) Start(context.Context) error { return nil }
func (s stubRuntime) Stop(context.Context) error  { return nil }
func (s stubRuntime) State(context.Context) string {
	if s.state != "" {
		return s.state
	}
	return "running"
}
func (s stubRuntime) Endpoint() string { return s.endpoint }
func (s stubRuntime) Token() string    { return s.token }
func (s stubRuntime) Name() string     { return "stub" }

// One /sessions row in the shape the Agent really emits (the same keys as workspace/agent's
// wireSession). The exit fields are real values from docs/log/26: a stopped session the OOM
// killer took.
const agentSessionsPayload = `{"sessions":[{
	"name":"s1","tmux":"claude_s1","dir":"/home/dev/repos/x","workingCopyId":"wc_123","kind":"claude",
	"driver":"managed","repo":"x","title":"t","display":"[AF] t","color":"#332211",
	"label":"[AF] t","started":"07/15 12:00","createdAt":"2026-07-15T12:00:00+09:00",
	"remoteUrl":"","state":"","alive":false,"resumable":true,"locked":true,
	"backgroundBusy":true,"backgroundBusyReason":"subagent",
	"context":{"read":1000,"create":200,"fresh":30,"model":"claude-fable-5"},
	"branch":"main","currentBranch":"dev","branchDrift":true,"worktree":true,
	"exitReason":"oom","exitCode":137,"exitSignal":9
}]}`

// TestAgentSessionsRelayKeepsFields pins that the CP's decode→re-emit round trip drops none
// of the fields the Console consumes — above all exitReason/exitCode/exitSignal behind the
// exit chip (docs/log/26), color for the SSM background, and context for the ContextBar.
func TestAgentSessionsRelayKeepsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(agentSessionsPayload))
	}))
	defer srv.Close()

	list, err := (&manager{}).agentSessions(context.Background(), stubRuntime{endpoint: srv.URL, token: "tok"})
	if err != nil {
		t.Fatalf("agentSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}

	// Turn the re-emitted form (what sessionsList writes as JSON) back into JSON and check
	// it field by field.
	out, err := json.Marshal(list[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]any{
		// docs/log/26 exit chip (OOM / crash display) — the ones the relay was dropping.
		"exitReason": "oom",
		"exitCode":   float64(137),
		"exitSignal": float64(9),
		// The display field dropped along with them.
		"color": "#332211",
		// driver, pinned against the same regression.
		"driver":        "managed",
		"title":         "t",
		"workingCopyId": "wc_123",
		// branch/worktree fields, pinned against the same kind of accident.
		"branch":        "main",
		"currentBranch": "dev",
		"branchDrift":   true,
		"worktree":      true,
		// Deletion lock (docs/log/45): dropped here, the lock badge and its unlock menu go.
		"locked": true,
		// Background busy and its reason. Drop the flag and the badge falls back to
		// "waiting for input"; drop only the reason and it is stuck with generic wording
		// that cannot say what is running.
		"backgroundBusy":       true,
		"backgroundBusyReason": "subagent",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("relayed %s = %v, want %v", k, got[k], v)
		}
	}
	// context passes straight through without the CP interpreting its shape (RawMessage);
	// the contents have to survive as well.
	ctxObj, ok := got["context"].(map[string]any)
	if !ok {
		t.Fatalf("relayed context = %v, want object", got["context"])
	}
	if ctxObj["read"] != float64(1000) || ctxObj["model"] != "claude-fable-5" {
		t.Errorf("relayed context = %v, want read=1000 model=claude-fable-5", ctxObj)
	}
	// tmux is deliberately not relayed: the Console does not use it and it is derivable as
	// "claude_"+name. If it ever shows up here, review it along with the struct's comments.
	if _, exists := got["tmux"]; exists {
		t.Errorf("tmux should not be relayed, got %v", got["tmux"])
	}
}
