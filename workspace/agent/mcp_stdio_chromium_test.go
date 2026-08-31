package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var chromiumReadToolNames = []string{
	"get_chromium_attachment",
	"list_chromium_targets",
}

var chromiumWriteToolNames = []string{
	"attach_chromium",
	"detach_chromium",
	"get_browser_action_result",
	"request_browser_action",
	"set_chromium_control_mode",
}

func TestMCPChromiumToolsReadWriteGateAndSchemas(t *testing.T) {
	oldWrite, oldSelfReport, oldSessionChromium := mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled
	mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = false, false, false
	t.Cleanup(func() {
		mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = oldWrite, oldSelfReport, oldSessionChromium
	})

	assertToolSet := func(wantWrite bool) {
		t.Helper()
		found := map[string]map[string]any{}
		for _, tool := range mcpStdioToolList() {
			name, _ := tool["name"].(string)
			found[name] = tool
		}
		for _, name := range chromiumReadToolNames {
			if found[name] == nil {
				t.Errorf("read tool %s is not advertised", name)
			} else if found[name]["outputSchema"] == nil {
				t.Errorf("read tool %s has no outputSchema", name)
			}
		}
		for _, name := range chromiumWriteToolNames {
			if got := found[name] != nil; got != wantWrite {
				t.Errorf("write tool %s advertised=%t, want %t", name, got, wantWrite)
			} else if got && found[name]["outputSchema"] == nil {
				t.Errorf("write tool %s has no outputSchema", name)
			}
		}
	}

	assertToolSet(false)
	for _, name := range chromiumWriteToolNames {
		resp := callChromiumMCP(t, name, map[string]any{})
		if !mcpCallIsError(t, resp) || !strings.Contains(string(resp), "変更を許可されていません") {
			t.Errorf("guessed read-only call %s was not gated: %s", name, resp)
		}
	}

	mcpWriteEnabled = true
	assertToolSet(true)
}

func TestMCPChromiumSessionScopeIsExact(t *testing.T) {
	oldWrite, oldSelfReport, oldSessionChromium := mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled
	t.Cleanup(func() {
		mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = oldWrite, oldSelfReport, oldSessionChromium
	})

	toolNames := func() map[string]map[string]any {
		t.Helper()
		found := map[string]map[string]any{}
		for _, tool := range mcpStdioToolList() {
			found[tool["name"].(string)] = tool
		}
		return found
	}

	// Backward compatibility: --self-report without the additive capability remains
	// the historical one-tool server.
	mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = false, true, false
	legacy := toolNames()
	if len(legacy) != 2 || legacy["af_report"] == nil || legacy["propose_session_handoff"] == nil {
		t.Fatalf("legacy self-report tools = %v, want [af_report propose_session_handoff]", sortedChromiumToolMapKeys(legacy))
	}
	if resp := callChromiumMCP(t, "list_chromium_targets", map[string]any{"port": 9222}); !mcpCallIsError(t, resp) || !strings.Contains(string(resp), "許可されていない") {
		t.Fatalf("legacy self-report guessed Chromium call was not gated: %s", resp)
	}

	// The current session builtin adds exactly Chromium Attach View. It does not
	// inherit the assistant's fleet read/write grants.
	mcpSessionChromiumEnabled = true
	found := toolNames()
	wantNames := append([]string{"af_report", "propose_session_handoff"}, chromiumReadToolNames...)
	wantNames = append(wantNames, chromiumWriteToolNames...)
	if got, want := sortedChromiumToolMapKeys(found), append([]string(nil), wantNames...); !sameSortedStrings(got, want) {
		t.Fatalf("session tools = %v, want exactly %v", got, sortedStringsCopy(want))
	}
	for _, name := range append(append([]string{}, chromiumReadToolNames...), chromiumWriteToolNames...) {
		if found[name]["outputSchema"] == nil {
			t.Errorf("session Chromium tool %s has no outputSchema", name)
		}
	}
	for _, name := range []string{"list_my_sessions", "send_to_session", "restore_memory_snapshot"} {
		resp := callChromiumMCP(t, name, map[string]any{})
		if !mcpCallIsError(t, resp) || !strings.Contains(string(resp), "許可されていない") {
			t.Errorf("session guessed fleet call %s was not gated: %s", name, resp)
		}
	}
}

func TestMCPChromiumToolsRelayAndStructuredFallback(t *testing.T) {
	type hit struct {
		method string
		path   string
		query  string
		body   map[string]any
		label  string
	}
	var hits []hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		label, _ := base64.RawURLEncoding.DecodeString(r.Header.Get(browserAttachmentLabelHeader))
		hits = append(hits, hit{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: body, label: string(label)})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/browser/attach-targets":
			_, _ = w.Write([]byte(`{"targets":[{"targetId":"opaque-target","type":"page","title":"編集","url":"https://example.invalid/edit"}],"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/secret"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/browser/attachments":
			_, _ = w.Write([]byte(`{"id":"ba_random_7f3","openUrl":"/open/browser-attachment/ba_random_7f3","expiresAt":"2026-08-02T00:30:00Z","port":9222,"targetId":"opaque-target","webSocketDebuggerUrl":"ws://secret"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/browser/attachments/ba_random_7f3":
			_, _ = w.Write([]byte(`{"id":"ba_random_7f3","state":"attached","viewer":true,"controlMode":"user-control","handoff":{"message":"確認","completionLabel":"操作完了","allowCancel":true,"controlMode":"user-control","result":"completed"},"expiresAt":"2026-08-02T00:30:00Z","title":"秘密の画面","url":"https://example.invalid/?token=secret","targetId":"opaque-target","port":9222}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/browser/attachments/ba_random_7f3":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/browser/attachments/ba_random_7f3/handoff":
			_, _ = w.Write([]byte(`{"id":"ba_random_7f3","state":"attached","openUrl":"/open/browser-attachment/ba_random_7f3","viewer":false,"controlMode":"user-control","handoff":{"message":"内容を確認してください","completionLabel":"操作完了","allowCancel":false,"controlMode":"user-control","result":"pending"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/browser/attachments/ba_random_7f3/control-mode":
			_, _ = w.Write([]byte(`{"id":"ba_random_7f3","state":"attached","openUrl":"/open/browser-attachment/ba_random_7f3","viewer":false,"controlMode":"locked"}`))
		default:
			http.Error(w, `{"code":"unexpected"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	oldWrite, oldSelfReport, oldSessionChromium := mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled
	mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = false, true, true
	t.Cleanup(func() {
		mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = oldWrite, oldSelfReport, oldSessionChromium
	})

	list := structuredMCPValue(t, callChromiumMCP(t, "list_chromium_targets", map[string]any{"port": 9222}))
	if len(list) != 1 || list["targets"] == nil {
		t.Fatalf("list result = %#v", list)
	}
	if hits[0].query != "port=9222" {
		t.Fatalf("target query = %q", hits[0].query)
	}

	status := structuredMCPValue(t, callChromiumMCP(t, "get_chromium_attachment", map[string]any{"attachment_id": "ba_random_7f3"}))
	if status["state"] != "attached" || status["viewer_connected"] != true || status["action_result"] != "completed" {
		t.Fatalf("attachment status = %#v", status)
	}
	for _, forbidden := range []string{"title", "url", "target_id", "port"} {
		if _, ok := status[forbidden]; ok {
			t.Errorf("attachment status leaked %s: %#v", forbidden, status)
		}
	}

	attach := structuredMCPValue(t, callChromiumMCP(t, "attach_chromium", map[string]any{
		"port": 9222, "target_id": "opaque-target", "label": "確認画面",
	}))
	wantAttach := map[string]any{
		"attachment_id": "ba_random_7f3",
		"open_url":      "/open/browser-attachment/ba_random_7f3",
		"expires_at":    "2026-08-02T00:30:00Z",
	}
	if !reflect.DeepEqual(attach, wantAttach) {
		t.Fatalf("attach result = %#v, want only %#v", attach, wantAttach)
	}
	attachHit := hits[len(hits)-1]
	if attachHit.body["targetId"] != "opaque-target" || attachHit.body["port"] != float64(9222) {
		t.Fatalf("attach REST body = %#v", attachHit.body)
	}
	if _, ok := attachHit.body["label"]; ok {
		t.Fatalf("attach REST body sent an unsupported label field: %#v", attachHit.body)
	}
	if attachHit.label != "確認画面" {
		t.Fatalf("attach internal label = %q", attachHit.label)
	}
	if _, ok := attachHit.body["target_id"]; ok {
		t.Fatalf("attach REST body retained MCP snake_case: %#v", attachHit.body)
	}

	allowCancel := false
	action := structuredMCPValue(t, callChromiumMCP(t, "request_browser_action", map[string]any{
		"attachment_id": "ba_random_7f3", "message": "内容を確認してください",
		"completion_label": "操作完了", "allow_cancel": allowCancel, "control_mode": "user-control",
	}))
	if action["result"] != "pending" || action["control_mode"] != "user-control" {
		t.Fatalf("action result = %#v", action)
	}
	handoffHit := hits[len(hits)-1]
	if handoffHit.body["completionLabel"] != "操作完了" || handoffHit.body["allowCancel"] != false || handoffHit.body["controlMode"] != "user-control" {
		t.Fatalf("handoff REST body = %#v", handoffHit.body)
	}

	_ = structuredMCPValue(t, callChromiumMCP(t, "request_browser_action", map[string]any{
		"attachment_id": "ba_random_7f3", "message": "省略値を確認してください",
	}))
	omittedHandoffHit := hits[len(hits)-1]
	if omittedHandoffHit.body["allowCancel"] != false {
		t.Fatalf("omitted allow_cancel REST body = %#v", omittedHandoffHit.body)
	}

	result := structuredMCPValue(t, callChromiumMCP(t, "get_browser_action_result", map[string]any{"attachment_id": "ba_random_7f3"}))
	if result["result"] != "completed" {
		t.Fatalf("browser action result = %#v", result)
	}

	mode := structuredMCPValue(t, callChromiumMCP(t, "set_chromium_control_mode", map[string]any{
		"attachment_id": "ba_random_7f3", "control_mode": "locked",
	}))
	if mode["control_mode"] != "locked" || hits[len(hits)-1].body["controlMode"] != "locked" {
		t.Fatalf("control mode result=%#v hit=%#v", mode, hits[len(hits)-1])
	}

	detach := structuredMCPValue(t, callChromiumMCP(t, "detach_chromium", map[string]any{"attachment_id": "ba_random_7f3"}))
	if detach["detached"] != true || hits[len(hits)-1].method != http.MethodDelete {
		t.Fatalf("detach result=%#v hit=%#v", detach, hits[len(hits)-1])
	}

	var gotRoutes []string
	for _, h := range hits {
		gotRoutes = append(gotRoutes, h.method+" "+h.path)
	}
	sort.Strings(gotRoutes)
	for _, want := range []string{
		"DELETE /browser/attachments/ba_random_7f3",
		"GET /browser/attach-targets",
		"POST /browser/attachments/ba_random_7f3/control-mode",
		"POST /browser/attachments/ba_random_7f3/handoff",
	} {
		if !containsChromiumRoute(gotRoutes, want) {
			t.Errorf("missing Agent route %q in %v", want, gotRoutes)
		}
	}
}

// TestMCPRequestBrowserActionCarriesOwningSessionForDelivery covers the wiring
// that lets a completed/cancelled handoff be delivered back into the
// requesting session's conversation (browser_handoff_ledger.go): the tool
// must attach its own AF_SESSION_NAME to the handoff REST body so the Agent
// knows who to notify, but must never fail the call over a missing one.
func TestMCPRequestBrowserActionCarriesOwningSessionForDelivery(t *testing.T) {
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody = nil // json.Decode into a live map only overwrites shared keys, never clears absent ones
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"id":"ba_1","state":"attached","openUrl":"/open/browser-attachment/ba_1","viewer":false,"controlMode":"user-control","handoff":{"message":"m","completionLabel":"c","allowCancel":false,"controlMode":"user-control","result":"pending"}}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	// Isolates mcpOwningSession's cwd-based fallback (an empty session store +
	// an unrelated cwd never resolves to a session), so clearing mcpSourceSession
	// below deterministically reaches the "no owning session" branch.
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Chdir(t.TempDir())

	oldWrite, oldSelfReport, oldSessionChromium, oldSource := mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled, mcpSourceSession
	mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = false, true, true
	t.Cleanup(func() {
		mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled, mcpSourceSession = oldWrite, oldSelfReport, oldSessionChromium, oldSource
	})

	mcpSourceSession = "the-requesting-session"
	_ = structuredMCPValue(t, callChromiumMCP(t, "request_browser_action", map[string]any{
		"attachment_id": "ba_1", "message": "m",
	}))
	if lastBody["sessionName"] != "the-requesting-session" {
		t.Fatalf("handoff REST body = %#v, want sessionName carried through", lastBody)
	}

	mcpSourceSession = "" // no owning session (e.g. AF_SESSION_NAME unset) — must not fail the call
	result := structuredMCPValue(t, callChromiumMCP(t, "request_browser_action", map[string]any{
		"attachment_id": "ba_1", "message": "m",
	}))
	if result["result"] != "pending" {
		t.Fatalf("request_browser_action without an owning session should still succeed: %#v", result)
	}
	if _, ok := lastBody["sessionName"]; ok {
		t.Fatalf("handoff REST body must omit sessionName when there is no owning session: %#v", lastBody)
	}
}

func TestMCPChromiumErrorsDoNotExposeAgentDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"cdp_unreachable","message":"dial ws://127.0.0.1:9222/devtools/browser/secret-target"}}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	resp := callChromiumMCP(t, "list_chromium_targets", map[string]any{"port": 9222})
	if !mcpCallIsError(t, resp) || !strings.Contains(string(resp), "cdp_unreachable") {
		t.Fatalf("error result = %s", resp)
	}
	for _, secret := range []string{"ws://", "secret-target", "127.0.0.1:9222"} {
		if strings.Contains(string(resp), secret) {
			t.Errorf("error leaked %q: %s", secret, resp)
		}
	}
}

// The instance check is only worth anything if the MCP boundary carries it:
// list must hand back the browser id, and attach must forward the caller's
// expectation as camelCase browserId (docs/log/53 §53.16).
func TestMCPChromiumCarriesBrowserIdentity(t *testing.T) {
	const guid = "c162d83f-b0a3-41d3-9db6-e9f6012c1491"
	var attachBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/browser/attach-targets":
			_, _ = w.Write([]byte(`{"targets":[{"targetId":"opaque-target","type":"page","title":"編集","url":"https://example.invalid/edit"}],"browserId":"` + guid + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/browser/attachments":
			_ = json.NewDecoder(r.Body).Decode(&attachBody)
			_, _ = w.Write([]byte(`{"id":"ba_random_7f3","openUrl":"/open/browser-attachment/ba_random_7f3","expiresAt":"2026-08-02T00:30:00Z"}`))
		default:
			http.Error(w, `{"code":"unexpected"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	oldWrite, oldSelfReport, oldSessionChromium := mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled
	mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = false, true, true
	t.Cleanup(func() {
		mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled = oldWrite, oldSelfReport, oldSessionChromium
	})

	list := structuredMCPValue(t, callChromiumMCP(t, "list_chromium_targets", map[string]any{"port": 9222}))
	if list["browser_id"] != guid {
		t.Fatalf("list result = %#v", list)
	}

	// DevToolsActivePort's second line is what a caller actually has on disk.
	_ = structuredMCPValue(t, callChromiumMCP(t, "attach_chromium", map[string]any{
		"port": 9222, "target_id": "opaque-target", "expected_browser_id": "/devtools/browser/" + guid,
	}))
	if attachBody["browserId"] != guid {
		t.Fatalf("attach REST body = %#v", attachBody)
	}
	if _, ok := attachBody["expected_browser_id"]; ok {
		t.Fatalf("attach REST body retained MCP snake_case: %#v", attachBody)
	}

	resp := callChromiumMCP(t, "attach_chromium", map[string]any{
		"port": 9222, "target_id": "opaque-target", "expected_browser_id": "http://evil.invalid/x",
	})
	if !mcpCallIsError(t, resp) {
		t.Fatalf("a malformed expected_browser_id must not reach the Agent: %s", resp)
	}
}

// A port collision is not a retryable failure — the agent must be told to
// relaunch its own Chromium, not to poke the same port again.
func TestMCPChromiumPortCollisionExplainsTheFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"cdp_port_ambiguous","message":"port 9222 has more than one listening process (IPv4 pid=1 user-data-dir=/home/dev/secret-profile)"}}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	resp := callChromiumMCP(t, "list_chromium_targets", map[string]any{"port": 9222})
	if !mcpCallIsError(t, resp) {
		t.Fatalf("collision must be an error: %s", resp)
	}
	for _, want := range []string{"cdp_port_ambiguous", "--remote-debugging-port=0", "DevToolsActivePort"} {
		if !strings.Contains(string(resp), want) {
			t.Errorf("collision error must mention %q: %s", want, resp)
		}
	}
	// The rival's profile path is another session's business; the hint must not
	// relay whatever the Agent said about it.
	if strings.Contains(string(resp), "secret-profile") {
		t.Errorf("collision error leaked the other session's profile: %s", resp)
	}
}

func callChromiumMCP(t *testing.T, name string, args map[string]any) []byte {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	return mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
}

func mcpCallIsError(t *testing.T, response []byte) bool {
	t.Helper()
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil {
		t.Fatalf("decode MCP response: %v: %s", err, response)
	}
	return parsed.Result.IsError
}

func structuredMCPValue(t *testing.T, response []byte) map[string]any {
	t.Helper()
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil {
		t.Fatalf("decode MCP response: %v: %s", err, response)
	}
	if parsed.Result.IsError || len(parsed.Result.Content) != 1 || parsed.Result.Content[0].Type != "text" {
		t.Fatalf("unexpected MCP result: %s", response)
	}
	var fallback map[string]any
	if err := json.Unmarshal([]byte(parsed.Result.Content[0].Text), &fallback); err != nil {
		t.Fatalf("text fallback is not a JSON object: %v: %q", err, parsed.Result.Content[0].Text)
	}
	if !reflect.DeepEqual(fallback, parsed.Result.Structured) {
		t.Fatalf("text fallback and structuredContent differ: text=%#v structured=%#v", fallback, parsed.Result.Structured)
	}
	return parsed.Result.Structured
}

func containsChromiumRoute(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedChromiumToolMapKeys(values map[string]map[string]any) []string {
	out := make([]string, 0, len(values))
	for name := range values {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedStringsCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sameSortedStrings(got, want []string) bool {
	return reflect.DeepEqual(got, sortedStringsCopy(want))
}
