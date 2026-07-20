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

// TestMCPGetAgentUsageMergesEndpoints: get_agent_usage is a READ tool (no --write)
// that merges the two WsBar usage endpoints into {claude:{...}, codex:{...}} so an
// assistant answers quota/reset questions in one call. opencode is absent by design
// (no usage source).
func TestMCPGetAgentUsageMergesEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/claude/usage":
			_, _ = w.Write([]byte(`{"ok":true,"authed":true,"fiveHour":{"pct":42,"resetsAt":"2026-07-20T12:00:00Z"}}`))
		case "/codex/usage":
			_, _ = w.Write([]byte(`{"ok":false,"authed":false}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	oldWrite := mcpWriteEnabled
	mcpWriteEnabled = false // read-only server must serve it
	t.Cleanup(func() { mcpWriteEnabled = oldWrite })

	params, _ := json.Marshal(map[string]any{"name": "get_agent_usage", "arguments": map[string]any{}})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || parsed.Result.IsError || len(parsed.Result.Content) == 0 {
		t.Fatalf("resp = %s err = %v", resp, err)
	}
	var merged struct {
		Claude struct {
			FiveHour *struct {
				Pct      float64 `json:"pct"`
				ResetsAt string  `json:"resetsAt"`
			} `json:"fiveHour"`
		} `json:"claude"`
		Codex struct {
			Authed bool `json:"authed"`
			OK     bool `json:"ok"`
		} `json:"codex"`
	}
	if err := json.Unmarshal([]byte(parsed.Result.Content[0].Text), &merged); err != nil {
		t.Fatalf("merged payload not JSON: %v: %s", err, parsed.Result.Content[0].Text)
	}
	if merged.Claude.FiveHour == nil || merged.Claude.FiveHour.Pct != 42 || merged.Claude.FiveHour.ResetsAt != "2026-07-20T12:00:00Z" {
		t.Fatalf("claude window = %+v", merged.Claude.FiveHour)
	}
	if merged.Codex.Authed || merged.Codex.OK {
		t.Fatalf("codex = %+v, want authed=false ok=false", merged.Codex)
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
