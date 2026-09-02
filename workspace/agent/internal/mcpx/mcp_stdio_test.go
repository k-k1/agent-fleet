package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	withWriteEnabled(t, true)
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

	withWriteEnabled(t, true)
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

func TestMCPGetSessionOutputRequestsTailClip(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.String()
		_, _ = w.Write([]byte(`{"output":"ok","cursor":3,"clipped":true}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	// tail は常時付く（オペレーターのコンテキスト防衛 — 上書き口なし）。since は併存。
	args, _ := json.Marshal(map[string]any{"name": "slot01", "since": 7})
	params, _ := json.Marshal(map[string]any{"name": "get_session_output", "arguments": json.RawMessage(args)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	if want := "/sessions/slot01/output?tail=32768&since=7"; got != want {
		t.Fatalf("agent path = %q, want %q", got, want)
	}
	if !strings.Contains(string(resp), "clipped") {
		t.Fatalf("MCP response should pass the body through: %s", resp)
	}

	// since 無しでも tail は付く（会話ID無し = カーソル記憶は効かない）。
	args, _ = json.Marshal(map[string]any{"name": "slot01"})
	params, _ = json.Marshal(map[string]any{"name": "get_session_output", "arguments": json.RawMessage(args)})
	_ = mcpStdioCall(mcpReq{ID: json.RawMessage(`2`), Params: params})
	if want := "/sessions/slot01/output?tail=32768"; got != want {
		t.Fatalf("agent path = %q, want %q", got, want)
	}
}

// TestMCPGetSessionOutputCursorMemory pins the since 既定化: 会話別に前回 cursor を
// 記憶し、省略時はその続きから読む。明示 since（0 含む）が最優先。続き読みで新規
// 出力ゼロのときは、その意味を本文で伝える。
func TestMCPGetSessionOutputCursorMemory(t *testing.T) {
	withTempHome(t)
	var got string
	next := `{"output":"最初の出力","cursor":5,"clipped":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.String()
		_, _ = w.Write([]byte(next))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	oldConv := convID()
	setConvID("conv-cursor-test")
	t.Cleanup(func() { setConvID(oldConv) })

	call := func(id string, args map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": "get_session_output", "arguments": json.RawMessage(raw)})
		return string(mcpStdioCall(mcpReq{ID: json.RawMessage(id), Params: params}))
	}

	// 1回目: 記憶なし → since は付かない。返却 cursor=5 が記憶される。
	call(`1`, map[string]any{"name": "slot01"})
	if want := "/sessions/slot01/output?tail=32768"; got != want {
		t.Fatalf("first call path = %q, want %q", got, want)
	}

	// 2回目: since 省略 → 記憶した 5 の続きから。新規出力ゼロは説明文に差し替わる。
	next = `{"output":"","cursor":5,"clipped":false}`
	resp := call(`2`, map[string]any{"name": "slot01"})
	if want := "/sessions/slot01/output?tail=32768&since=5"; got != want {
		t.Fatalf("second call path = %q, want %q", got, want)
	}
	if !strings.Contains(resp, "新しい出力はありません") || !strings.Contains(resp, "since=0") {
		t.Fatalf("empty-diff response should explain itself: %s", resp)
	}

	// 3回目: since=0 の明示は記憶より優先（先頭から読み直し）。
	next = `{"output":"全文","cursor":6,"clipped":false}`
	resp = call(`3`, map[string]any{"name": "slot01", "since": 0})
	if want := "/sessions/slot01/output?tail=32768&since=0"; got != want {
		t.Fatalf("explicit since path = %q, want %q", got, want)
	}
	if strings.Contains(resp, "新しい出力はありません") {
		t.Fatalf("explicit since must not be rewritten: %s", resp)
	}

	// 4回目: 3回目の cursor=6 が新しい既定になっている。別セッション名は独立。
	next = `{"output":"x","cursor":9,"clipped":false}`
	call(`4`, map[string]any{"name": "slot01"})
	if want := "/sessions/slot01/output?tail=32768&since=6"; got != want {
		t.Fatalf("fourth call path = %q, want %q", got, want)
	}
	call(`5`, map[string]any{"name": "slot02"})
	if want := "/sessions/slot02/output?tail=32768"; got != want {
		t.Fatalf("other session path = %q, want %q", got, want)
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

	withWriteEnabled(t, true)
	args, _ := json.Marshal(map[string]any{
		"dir": "/repos/app", "kind": "codex", "initial_prompt": "start",
		"worktree": true, "branch": "main", "new_branch": "feat/mcp-wt",
	})
	params, _ := json.Marshal(map[string]any{"name": "create_session", "arguments": json.RawMessage(args)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	if string(resp) == "" {
		t.Fatal("empty MCP response")
	}

	body := awaitHit(t, got, "create_session")
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

	withWriteEnabled(t, false) // read-only server must serve it

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
	withWriteEnabled(t, false)
	call("list_cleanup_candidates", "")
	if h := awaitHit(t, got, "list_cleanup_candidates"); h.method != "GET" || h.path != "/sessions/cleanup" {
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

	withWriteEnabled(t, true)

	call("archive_session", "slot01")
	if h := awaitHit(t, got, "archive_session"); h.method != "POST" || h.path != "/sessions/slot01/archive" {
		t.Fatalf("archive_session hit %s %s", h.method, h.path)
	}
	call("delete_worktree", "app@wip-x")
	h := awaitHit(t, got, "delete_worktree")
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
	if h = awaitHit(t, got, "delete_session"); h.method != "DELETE" || h.path != "/sessions/slot9" || h.query != "reclaim=1" {
		t.Fatalf("delete_session hit %s %s?%s", h.method, h.path, h.query)
	}
	// delete_branch → DELETE /repos/{repo}/branch?branch=<name> (slash-safe query).
	dbArgs, _ := json.Marshal(map[string]any{"repo": "app", "branch": "temp/wip-x"})
	dbParams, _ := json.Marshal(map[string]any{"name": "delete_branch", "arguments": json.RawMessage(dbArgs)})
	mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: dbParams})
	if h = awaitHit(t, got, "delete_branch"); h.method != "DELETE" || h.path != "/repos/app/branch" || h.query != "branch=temp%2Fwip-x" {
		t.Fatalf("delete_branch hit %s %s?%s", h.method, h.path, h.query)
	}
	// restore / purge cleanup archives.
	idArgs, _ := json.Marshal(map[string]any{"id": "20260720-090000-slot9"})
	restoreParams, _ := json.Marshal(map[string]any{"name": "restore_cleanup_archive", "arguments": json.RawMessage(idArgs)})
	mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: restoreParams})
	if h = awaitHit(t, got, "restore_cleanup_archive"); h.method != "POST" || h.path != "/cleanup/archives/20260720-090000-slot9/restore" {
		t.Fatalf("restore hit %s %s", h.method, h.path)
	}
	purgeParams, _ := json.Marshal(map[string]any{"name": "purge_cleanup_archive", "arguments": json.RawMessage(idArgs)})
	mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: purgeParams})
	if h = awaitHit(t, got, "purge_cleanup_archive"); h.method != "DELETE" || h.path != "/cleanup/archives/20260720-090000-slot9" {
		t.Fatalf("purge hit %s %s", h.method, h.path)
	}
}

// TestMCPStopResumeSessionRelay covers the operator's session-lifecycle tools:
// stop_session must relay to /halt (resumable — NOT the destructive /stop) carrying
// disarm_report (the stop cancels the outstanding instruction, docs/log/30), and
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

	withWriteEnabled(t, true)

	call := func(tool, name string) []byte {
		args, _ := json.Marshal(map[string]any{"name": name})
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": json.RawMessage(args)})
		return mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	}

	if resp := call("stop_session", "slot01"); string(resp) == "" {
		t.Fatal("empty stop_session response")
	}
	h := awaitHit(t, got, "stop_session")
	if h.path != "POST /sessions/slot01/halt" {
		t.Fatalf("stop_session hit %q, want POST /sessions/slot01/halt", h.path)
	}
	if h.body["disarm_report"] != true {
		t.Fatalf("stop_session body = %#v, want disarm_report=true", h.body)
	}

	if resp := call("resume_session", "slot01"); string(resp) == "" {
		t.Fatal("empty resume_session response")
	}
	if h = awaitHit(t, got, "resume_session"); h.path != "POST /sessions/slot01/start" {
		t.Fatalf("resume_session hit %q, want POST /sessions/slot01/start", h.path)
	}

	// Without --write the tools are refused in-band and the Agent is never called.
	withWriteEnabled(t, false)
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

// mcpMemoryStub は Agent REST を差し替え、叩かれたパスを記録する。
func mcpMemoryStub(t *testing.T, body func(path string) string) *[]string {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(body(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
	return &paths
}

func mcpCall(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	ab, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(ab)})
	return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
}

func mcpIsError(t *testing.T, resp string) bool {
	t.Helper()
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		t.Fatalf("decode %s: %v", resp, err)
	}
	return parsed.Result.IsError
}

// docs/log/39 P4: メモリの持ち出し／取り込みは MCP に出さない。P3 で export に
// secret スキャン＋本人の明示 ack を課したのに、モデルが ack できる経路を作ると
// 防御を迂回する二つ目の出口になる。広告ツール集合がゲートなので、ここで固定する。
func TestMCPMemoryToolsExposeNoExportOrImport(t *testing.T) {
	withWriteEnabled(t, true)
	names := map[string]bool{}
	for _, tool := range mcpStdioToolList() {
		names[tool["name"].(string)] = true
	}
	for _, want := range []string{"list_memory_snapshots", "get_memory_snapshot", "restore_memory_snapshot"} {
		if !names[want] {
			t.Errorf("tool %q is not advertised", want)
		}
	}
	for name := range names {
		if strings.Contains(name, "memory") && (strings.Contains(name, "export") || strings.Contains(name, "import")) {
			t.Errorf("tool %q exposes the memory transfer path to the model", name)
		}
	}
	// 読み取り専用の会話には restore を一切広告しない。
	withWriteEnabled(t, false)
	for _, tool := range mcpStdioToolList() {
		if tool["name"] == "restore_memory_snapshot" {
			t.Fatal("restore_memory_snapshot is advertised to an af_read conversation")
		}
	}
}

// 範囲の省略は「全体」ではなく拒否。モデルがフィールドを落としただけでメモリ全体が
// 巻き戻ると、利用者が承認した範囲を超えた操作になる。
func TestMCPRestoreMemoryRequiresExplicitScope(t *testing.T) {
	paths := mcpMemoryStub(t, func(string) string { return `{"committed":true}` })
	withWriteEnabled(t, true)

	if resp := mcpCall(t, "restore_memory_snapshot", map[string]any{"rev": "abc"}); !mcpIsError(t, resp) {
		t.Fatalf("a scope-less restore was accepted: %s", resp)
	}
	if resp := mcpCall(t, "restore_memory_snapshot", map[string]any{"all": true}); !mcpIsError(t, resp) {
		t.Fatalf("a restore without rev/at was accepted: %s", resp)
	}
	if len(*paths) != 0 {
		t.Fatalf("a rejected restore still called the Agent: %v", *paths)
	}

	resp := mcpCall(t, "restore_memory_snapshot", map[string]any{"rev": "abc", "projects": []string{"-home-dev-repos-demo"}})
	if mcpIsError(t, resp) {
		t.Fatalf("a scoped restore was rejected: %s", resp)
	}
	if got := strings.Join(*paths, ","); got != "POST /agents/memory/restore" {
		t.Fatalf("Agent calls = %q", got)
	}
}

func TestMCPRestoreMemoryDeniedWithoutWrite(t *testing.T) {
	paths := mcpMemoryStub(t, func(string) string { return `{}` })
	withWriteEnabled(t, false)
	if resp := mcpCall(t, "restore_memory_snapshot", map[string]any{"rev": "abc", "all": true}); !mcpIsError(t, resp) {
		t.Fatalf("an af_read conversation restored memory: %s", resp)
	}
	if len(*paths) != 0 {
		t.Fatalf("a denied restore still called the Agent: %v", *paths)
	}
}

// get_memory_snapshot は tree（その時点の中身）と diff（その snapshot の変更）を
// 1 回で返す。restore の範囲は tree からしか作れない（docs/log/39 ③）。
func TestMCPGetMemorySnapshotJoinsTreeAndDiff(t *testing.T) {
	paths := mcpMemoryStub(t, func(p string) string {
		if strings.HasSuffix(p, "/tree") {
			return `{"rev":"abc","projects":[{"slug":"s"}]}`
		}
		return `{"diff":"--- a\n+++ b\n"}`
	})
	withWriteEnabled(t, false) // 読み取りツールなので af_read でも使える

	if resp := mcpCall(t, "get_memory_snapshot", map[string]any{}); !mcpIsError(t, resp) {
		t.Fatalf("a snapshot lookup without rev/at was accepted: %s", resp)
	}
	resp := mcpCall(t, "get_memory_snapshot", map[string]any{"rev": "abc"})
	if mcpIsError(t, resp) {
		t.Fatalf("get_memory_snapshot failed: %s", resp)
	}
	if got := strings.Join(*paths, ","); got != "GET /agents/memory/tree,GET /agents/memory/diff" {
		t.Fatalf("Agent calls = %q", got)
	}
	for _, want := range []string{`tree`, `diff`, `abc`} {
		if !strings.Contains(resp, want) {
			t.Fatalf("response is missing %q: %s", want, resp)
		}
	}
}

// --- 作業計画（docs/log/33 第5段 案D）---------------------------------------------

// set_chat_plan は会話 id を引数に取らず、常に自分の会話（mcpConvID）へ書く。
// notice:true を必ず立てる — 利用者が見ていない間に計画が動く唯一の経路なので、
// 会話にカードが残らないと誤上書きに誰も気づけない。
func TestMCPSetChatPlanWritesOwnConversationWithNotice(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.Method+" "+r.URL.Path, string(b)
		_, _ = w.Write([]byte(`{"id":"conv-1","messages":[{"role":"user","content":"…"}]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	withMCPWriteConv(t, "conv-1")

	resp := mcpCall(t, "set_chat_plan", map[string]any{"plan": "## これからやること\n- Lane A"})
	if want := "PUT /chat/conversations/conv-1/plan"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if !strings.Contains(gotBody, `"notice":true`) || !strings.Contains(gotBody, "Lane A") {
		t.Fatalf("body = %s", gotBody)
	}
	// 会話まるごとを返さない（返すと計画を書くたびに会話全文がモデルへ戻る）。
	if strings.Contains(resp, "role") {
		t.Fatalf("conversation leaked into the tool result: %s", resp)
	}
}

// 空の全文置換で計画を消させない（破棄は利用者の判断）。Agent も叩かない。
func TestMCPSetChatPlanRefusesEmpty(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	withMCPWriteConv(t, "conv-1")

	for _, plan := range []string{"", "   \n "} {
		resp := mcpCall(t, "set_chat_plan", map[string]any{"plan": plan})
		if !mcpIsError(t, resp) {
			t.Fatalf("plan=%q: want isError, got %s", plan, resp)
		}
	}
	if called {
		t.Fatal("empty plan must not reach the Agent")
	}
}

// 広告集合がスコープの境界（docs/log/19 Q2 と同じ作法）: read/write とも --write 下でのみ。
func TestMCPChatPlanToolsDeniedWithoutWrite(t *testing.T) {
	withMCPWriteConv(t, "conv-1")
	withWriteEnabled(t, false)
	for _, name := range []string{"get_chat_plan", "set_chat_plan"} {
		if !mcpIsError(t, mcpCall(t, name, map[string]any{"plan": "x"})) {
			t.Fatalf("%s: want denial without --write", name)
		}
	}
}

// 会話に結び付いていない経路（--conv なし）では扱えない。
func TestMCPChatPlanToolsRequireConv(t *testing.T) {
	withMCPWriteConv(t, "")
	if !mcpIsError(t, mcpCall(t, "get_chat_plan", map[string]any{})) {
		t.Fatal("want error without a conversation")
	}
}

func TestMCPGetChatPlanReturnsPlanOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/chat/conversations/conv-1/plan" {
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"plan":"## これからやること\n- Lane A","plan_updated_at":1}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	withMCPWriteConv(t, "conv-1")
	if !strings.Contains(mcpCall(t, "get_chat_plan", map[string]any{}), "Lane A") {
		t.Fatal("plan not returned")
	}
}

// 計画ツールは write 集合にだけ広告する（read 専用アシスタントのツール表を太らせない）。
func TestMCPChatPlanToolsAdvertisedOnlyUnderWrite(t *testing.T) {
	has := func(tools []map[string]any, name string) bool {
		for _, tl := range tools {
			if tl["name"] == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"get_chat_plan", "set_chat_plan"} {
		if !has(mcpStdioWriteTools, name) {
			t.Fatalf("%s missing from the write tool set", name)
		}
		if has(mcpStdioTools, name) {
			t.Fatalf("%s must not be advertised to read-only assistants", name)
		}
	}
}

func withMCPWriteConv(t *testing.T, conv string) {
	t.Helper()
	oldWrite, oldConv := writeEnabled(), convID()
	withWriteEnabled(t, true)
	setConvID(conv)
	t.Cleanup(func() { setWriteEnabled(oldWrite); setConvID(oldConv) })
}
