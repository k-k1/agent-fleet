package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMCPCreateSessionForwardsWorktreeOptions(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions" {
			t.Errorf("request = %s %s, want POST /sessions", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		got <- body
		_, _ = w.Write([]byte(`{"name":"created"}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	oldWrite := mcpWriteEnabled
	mcpWriteEnabled = true
	t.Cleanup(func() { mcpWriteEnabled = oldWrite })
	args, _ := json.Marshal(map[string]any{
		"dir": "/repos/app", "kind": "codex", "initial_prompt": "start",
		"worktree": true, "branch": "main", "new_branch": "feat/mcp-wt",
	})
	params, _ := json.Marshal(map[string]any{"name": "create_session", "arguments": json.RawMessage(args)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	if string(resp) == "" {
		t.Fatal("empty MCP response")
	}

	body := <-got
	if body["worktree"] != true || body["branch"] != "main" || body["new_branch"] != "feat/mcp-wt" {
		t.Fatalf("forwarded body = %#v", body)
	}
	if body["driver"] != "managed" {
		t.Fatalf("driver = %#v, want managed", body["driver"])
	}
}

// TestMCPStopResumeSessionRelay covers the operator's session-lifecycle tools:
// stop_session must relay to /halt (resumable — NOT the destructive /stop) carrying
// disarm_report (the stop cancels the outstanding instruction, docs/30), and
// resume_session must relay to /start.
func TestMCPStopResumeSessionRelay(t *testing.T) {
	type hit struct {
		path string
		body map[string]any
	}
	got := make(chan hit, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- hit{path: r.Method + " " + r.URL.Path, body: body}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	oldWrite := mcpWriteEnabled
	mcpWriteEnabled = true
	t.Cleanup(func() { mcpWriteEnabled = oldWrite })

	call := func(tool, name string) []byte {
		args, _ := json.Marshal(map[string]any{"name": name})
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": json.RawMessage(args)})
		return mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	}

	if resp := call("stop_session", "slot01"); string(resp) == "" {
		t.Fatal("empty stop_session response")
	}
	h := <-got
	if h.path != "POST /sessions/slot01/halt" {
		t.Fatalf("stop_session hit %q, want POST /sessions/slot01/halt", h.path)
	}
	if h.body["disarm_report"] != true {
		t.Fatalf("stop_session body = %#v, want disarm_report=true", h.body)
	}

	if resp := call("resume_session", "slot01"); string(resp) == "" {
		t.Fatal("empty resume_session response")
	}
	if h = <-got; h.path != "POST /sessions/slot01/start" {
		t.Fatalf("resume_session hit %q, want POST /sessions/slot01/start", h.path)
	}

	// Without --write the tools are refused in-band and the Agent is never called.
	mcpWriteEnabled = false
	for _, tool := range []string{"stop_session", "resume_session"} {
		resp := call(tool, "slot01")
		var parsed struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(resp, &parsed); err != nil || !parsed.Result.IsError {
			t.Fatalf("%s without --write: resp=%s err=%v, want isError", tool, resp, err)
		}
	}
	select {
	case h = <-got:
		t.Fatalf("read-only call reached the Agent: %q", h.path)
	default:
	}
}
