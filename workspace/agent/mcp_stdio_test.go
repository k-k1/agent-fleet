package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAgentDoReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":"unauthorized","message":"bad token"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	_, err = agentGET("/sessions")
	var httpErr *agentHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("agentGET error = %#v, want agentHTTPError(401)", err)
	}
}

func TestMCPSendToSessionResumesStoppedSessionAndConfirmsSend(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/status") {
			alive := len(paths) > 1
			_, _ = fmt.Fprintf(w, `{"alive":%t,"ready":%t}`, alive, alive)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/input") {
			_, _ = w.Write([]byte(`{"sent":"slot01"}`))
			return
		}
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
	args, _ := json.Marshal(map[string]any{"name": "slot01", "prompt": "続けて"})
	params, _ := json.Marshal(map[string]any{"name": "send_to_session", "arguments": json.RawMessage(args)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	if got, want := strings.Join(paths, ","), "GET /sessions/slot01/status,POST /sessions/slot01/start,GET /sessions/slot01/status,POST /sessions/slot01/input"; got != want {
		t.Fatalf("Agent calls = %q, want %q", got, want)
	}
	if !strings.Contains(string(resp), `\"sent\":true`) || !strings.Contains(string(resp), `\"resumed\":true`) {
		t.Fatalf("MCP response = %s, want sent/resumed confirmation", resp)
	}
}

func TestMCPSendToSessionDoesNotMaskConflictAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"question_pending"}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	oldWrite := mcpWriteEnabled
	mcpWriteEnabled = true
	t.Cleanup(func() { mcpWriteEnabled = oldWrite })
	args, _ := json.Marshal(map[string]any{"name": "slot01", "prompt": "続けて"})
	params, _ := json.Marshal(map[string]any{"name": "send_to_session", "arguments": json.RawMessage(args)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || !parsed.Result.IsError {
		t.Fatalf("MCP response = %s, err=%v, want isError", resp, err)
	}
}

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
		case "/connections/agy/usage":
			_, _ = w.Write([]byte(`{"ok":true,"authed":true,"account":"a@b.com","groups":[{"label":"GEMINI MODELS","remainingPct":98.8}]}`))
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
		Agy struct {
			Authed  bool   `json:"authed"`
			Account string `json:"account"`
			Groups  []struct {
				Label        string  `json:"label"`
				RemainingPct float64 `json:"remainingPct"`
			} `json:"groups"`
		} `json:"agy"`
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
	// agy rides under its own key with its distinct {account, groups} shape.
	if !merged.Agy.Authed || merged.Agy.Account != "a@b.com" || len(merged.Agy.Groups) != 1 ||
		merged.Agy.Groups[0].Label != "GEMINI MODELS" {
		t.Fatalf("agy = %+v, want authed=true account=a@b.com one GEMINI MODELS group", merged.Agy)
	}
}

// TestMCPCleanupToolsRelay covers the cleanup tools: list_cleanup_candidates is a
// READ tool → GET /sessions/cleanup; archive_session (write) → POST .../archive;
// delete_worktree (write) → DELETE /repos/{name}?prune_sessions=1 and NEVER force.
func TestMCPCleanupToolsRelay(t *testing.T) {
	type hit struct{ method, path, query string }
	got := make(chan hit, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- hit{r.Method, r.URL.Path, r.URL.RawQuery}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	call := func(tool, name string) []byte {
		args := map[string]any{}
		if name != "" {
			args["name"] = name
		}
		ab, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": json.RawMessage(ab)})
		return mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	}

	// list_cleanup_candidates works WITHOUT --write (read tool).
	oldWrite := mcpWriteEnabled
	mcpWriteEnabled = false
	call("list_cleanup_candidates", "")
	if h := <-got; h.method != "GET" || h.path != "/sessions/cleanup" {
		t.Fatalf("list_cleanup_candidates hit %s %s", h.method, h.path)
	}
	// The write tools are refused read-only and never reach the Agent.
	for _, tool := range []string{"archive_session", "delete_worktree"} {
		resp := call(tool, "x")
		var p struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if json.Unmarshal(resp, &p) != nil || !p.Result.IsError {
			t.Fatalf("%s read-only: want isError, got %s", tool, resp)
		}
	}
	select {
	case h := <-got:
		t.Fatalf("write tool reached Agent read-only: %s %s", h.method, h.path)
	default:
	}

	mcpWriteEnabled = true
	t.Cleanup(func() { mcpWriteEnabled = oldWrite })

	call("archive_session", "slot01")
	if h := <-got; h.method != "POST" || h.path != "/sessions/slot01/archive" {
		t.Fatalf("archive_session hit %s %s", h.method, h.path)
	}
	call("delete_worktree", "app@wip-x")
	h := <-got
	if h.method != "DELETE" || h.path != "/repos/app@wip-x" {
		t.Fatalf("delete_worktree hit %s %s", h.method, h.path)
	}
	if h.query != "prune_sessions=1" {
		t.Fatalf("delete_worktree query = %q, want prune_sessions=1 (and NO force)", h.query)
	}
	if strings.Contains(h.query, "force") {
		t.Fatalf("delete_worktree must never send force: %q", h.query)
	}

	// delete_session reclaims (jsonl) → DELETE /sessions/{name}?reclaim=1.
	call("delete_session", "slot9")
	if h = <-got; h.method != "DELETE" || h.path != "/sessions/slot9" || h.query != "reclaim=1" {
		t.Fatalf("delete_session hit %s %s?%s", h.method, h.path, h.query)
	}
	// delete_branch → DELETE /repos/{repo}/branch?branch=<name> (slash-safe query).
	dbArgs, _ := json.Marshal(map[string]any{"repo": "app", "branch": "temp/wip-x"})
	dbParams, _ := json.Marshal(map[string]any{"name": "delete_branch", "arguments": json.RawMessage(dbArgs)})
	mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: dbParams})
	if h = <-got; h.method != "DELETE" || h.path != "/repos/app/branch" || h.query != "branch=temp%2Fwip-x" {
		t.Fatalf("delete_branch hit %s %s?%s", h.method, h.path, h.query)
	}
	// restore / purge cleanup archives.
	idArgs, _ := json.Marshal(map[string]any{"id": "20260720-090000-slot9"})
	restoreParams, _ := json.Marshal(map[string]any{"name": "restore_cleanup_archive", "arguments": json.RawMessage(idArgs)})
	mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: restoreParams})
	if h = <-got; h.method != "POST" || h.path != "/cleanup/archives/20260720-090000-slot9/restore" {
		t.Fatalf("restore hit %s %s", h.method, h.path)
	}
	purgeParams, _ := json.Marshal(map[string]any{"name": "purge_cleanup_archive", "arguments": json.RawMessage(idArgs)})
	mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: purgeParams})
	if h = <-got; h.method != "DELETE" || h.path != "/cleanup/archives/20260720-090000-slot9" {
		t.Fatalf("purge hit %s %s", h.method, h.path)
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
