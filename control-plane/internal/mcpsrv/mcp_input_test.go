package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// send_to_session goes to the Agent's /mcp-input — never /input, which the Console shares — with
// the origin taken from the authenticated principal and the client's own clientInfo, and the
// #2533 record of the call names the tool without any of the prompt.
func TestSendToSessionCarriesTheAuthenticatedOrigin(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	a := New(cp)
	const secret = "the-prompt-text-xyz"
	params, _ := json.Marshal(map[string]any{
		"name":      "send_to_session",
		"arguments": map[string]any{"name": "s1", "prompt": secret},
		"_meta":     map[string]any{metaClientInfo: map[string]string{"name": "claude.ai", "version": "1"}},
	})
	prin := cp.prin
	prin.scope = scopeWrite
	prin.oauth, prin.name = true, "Claude"
	prin.patID = "grant_abc"
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	resp, _, item := a.dispatchTraced(r, &prin, rpcRequest{ID: json.RawMessage(`1`), Method: "tools/call", Params: params})
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if cp.gotMethod != "POST" || cp.gotPath != "/sessions/s1/mcp-input" {
		t.Fatalf("Agent call = %s %s, want POST /sessions/s1/mcp-input", cp.gotMethod, cp.gotPath)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(cp.gotBody), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"prompt": secret, "connection": "grant_abc", "via": "Claude", "impl": "claude.ai"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%s] = %q, want %q", k, got[k], v)
		}
	}
	rec, _ := json.Marshal(item)
	if strings.Contains(string(rec), secret) || item.Name != "send_to_session" || item.ToolError {
		t.Errorf("call record = %s, want the tool name and no prompt text", rec)
	}
}

// A caller-supplied origin in the arguments is refused as an unknown argument, and a call that
// reaches the tool with no principal on its context cannot send.
func TestSendToSessionOriginCannotBeSuppliedOrOmitted(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	text, isErr := memCall(t, cp, scopeWrite, "send_to_session",
		map[string]any{"name": "s1", "prompt": "hi", "connection": "someone-else"})
	if !isErr || !strings.Contains(text, "connection") || cp.calls != 0 {
		t.Errorf("extra argument: %q isErr=%v calls=%d, want refused before the Agent", text, isErr, cp.calls)
	}
	tool := memberTool(t, "send_to_session")
	if _, err := tool.run(context.Background(), API{cp: cp}, &Resolved{}, map[string]any{"name": "s1", "prompt": "hi"}); err == nil || cp.calls != 0 {
		t.Errorf("no principal on ctx: err=%v calls=%d, want an error and no Agent call", err, cp.calls)
	}
}

// create_session goes to the Agent's /sessions-mcp — never /sessions, which the Console shares —
// with the origin taken from the authenticated principal, as send_to_session's is (#3027).
func TestCreateSessionCarriesTheAuthenticatedOrigin(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	a := New(cp)
	params, _ := json.Marshal(map[string]any{
		"name":      "create_session",
		"arguments": map[string]any{"initial_prompt": "hand-off note"},
		"_meta":     map[string]any{metaClientInfo: map[string]string{"name": "claude.ai", "version": "1"}},
	})
	prin := cp.prin
	prin.scope = scopeWrite
	prin.oauth, prin.name = true, "Claude"
	prin.patID = "grant_abc"
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	resp, _, _ := a.dispatchTraced(r, &prin, rpcRequest{ID: json.RawMessage(`1`), Method: "tools/call", Params: params})
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if cp.gotMethod != "POST" || cp.gotPath != "/sessions-mcp" {
		t.Fatalf("Agent call = %s %s, want POST /sessions-mcp", cp.gotMethod, cp.gotPath)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(cp.gotBody), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"initial_prompt": "hand-off note", "connection": "grant_abc", "via": "Claude", "impl": "claude.ai"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%s] = %v, want %q", k, got[k], v)
		}
	}
}

// create_session cannot be given an origin through its arguments, and a call that reaches the
// tool with no principal on its context cannot create.
func TestCreateSessionOriginCannotBeSuppliedOrOmitted(t *testing.T) {
	cp := newMemCP(t, memStore(t), "running")
	text, isErr := memCall(t, cp, scopeWrite, "create_session",
		map[string]any{"initial_prompt": "hi", "connection": "someone-else"})
	if !isErr || !strings.Contains(text, "connection") || cp.calls != 0 {
		t.Errorf("extra argument: %q isErr=%v calls=%d, want refused before the Agent", text, isErr, cp.calls)
	}
	tool := memberTool(t, "create_session")
	if _, err := tool.run(context.Background(), API{cp: cp}, &Resolved{}, map[string]any{"initial_prompt": "hi"}); err == nil || cp.calls != 0 {
		t.Errorf("no principal on ctx: err=%v calls=%d, want an error and no Agent call", err, cp.calls)
	}
}
