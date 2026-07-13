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
}
