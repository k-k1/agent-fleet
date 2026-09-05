package chatx

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readJSONFile reads a generated opencode config into a map.
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// mcpCommand returns the config's mcp.af.command as a []string, or nil when absent.
func mcpCommand(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		return nil
	}
	af, ok := mcp["af"].(map[string]any)
	if !ok {
		t.Fatalf("mcp has no af entry: %+v", mcp)
	}
	raw, _ := af["command"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// The core of docs/log/30: a session started by an af_write conversation reports completion
// back to that conversation. The report link can only be established through mcp-stdio's
// --conv, so if the opencode chat config drops --conv, reports never arrive (which is what
// happened).
func TestOpencodeChatConfigCarriesConv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "ce4f94b9-2854-44ee-8425-61859128d669"
	c := &ChatConversation{ID: id, Tools: assistants.ToolsAFWrite}

	path := opencodeChatConfig(c)
	if path == "" {
		t.Fatal("no config generated for an af_write conversation")
	}
	if got := filepath.Base(path); got != id+".json" {
		t.Fatalf("config file name = %q, want %q", got, id+".json")
	}
	cfg := readJSONFile(t, path)
	cmd := mcpCommand(t, cfg)
	if len(cmd) == 0 {
		t.Fatalf("the af MCP server is not configured: %+v", cfg)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--write") || !strings.Contains(joined, "--conv "+id) {
		t.Fatalf("mcp command = %q, want --write and --conv %s", joined, id)
	}
	// The chat contract (edit and shell denied) is carried by the per-conversation config
	// too. Whether opencode merges OPENCODE_CONFIG with the project config or replaces it is
	// undocumented, so the stance holds either way.
	perm, _ := cfg["permission"].(map[string]any)
	if perm["edit"] != "deny" || perm["bash"] != "deny" {
		t.Fatalf("permission = %+v, want edit/bash deny", perm)
	}
}

// A read grant is not given --conv (report_to is wiring on the write side).
func TestOpencodeChatConfigReadGrantHasNoConv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &ChatConversation{ID: "ce4f94b9-2854-44ee-8425-61859128d669", Tools: assistants.ToolsAFRead}
	path := opencodeChatConfig(c)
	if path == "" {
		t.Fatal("no config generated for an af_read conversation")
	}
	joined := strings.Join(mcpCommand(t, readJSONFile(t, path)), " ")
	if strings.Contains(joined, "--conv") || strings.Contains(joined, "--write") {
		t.Fatalf("mcp command = %q, want read-only", joined)
	}
}

// A conversation without tools gets no config written (no stray files).
func TestOpencodeChatConfigSkippedWithoutGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &ChatConversation{ID: "ce4f94b9-2854-44ee-8425-61859128d669", Tools: assistants.ToolsNone}
	if path := opencodeChatConfig(c); path != "" {
		t.Fatalf("a config was generated without tools: %q", path)
	}
}

// The project-side (--dir) config must not carry the af MCP server. opencode MERGES configs
// and the PROJECT config wins on a conflict (measured on 1.18.7, pinned by
// TestContractOpencodeConfigPrecedence), so writing af here would override the
// per-conversation definition that carries --conv and session reports (docs/log/30) would
// never arrive again. The registry servers are built from the same conversation in both
// configs, so they cannot disagree and may stay here.
func TestOpencodeChatDirHasNoMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &ChatConversation{ID: "ce4f94b9-2854-44ee-8425-61859128d669", Tools: assistants.ToolsAFWrite}
	dir := opencodeChatDir(c)
	if filepath.Base(dir) != "opencode-write" {
		t.Fatalf("dir = %q, want …/opencode-write (one dir per grant, unchanged)", dir)
	}
	cfg := readJSONFile(t, filepath.Join(dir, "opencode.json"))
	if _, ok := cfg["mcp"]; ok {
		t.Fatalf("mcp is still present in the project config: %+v", cfg)
	}
	perm, _ := cfg["permission"].(map[string]any)
	if perm["edit"] != "deny" || perm["bash"] != "deny" {
		t.Fatalf("permission = %+v, want edit/bash deny", perm)
	}
}

// Deleting a conversation deletes its per-conversation config too (handleChatDelete).
func TestChatDeleteRemovesOpencodeConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "ce4f94b9-2854-44ee-8425-61859128d669"
	path := opencodeChatConfig(&ChatConversation{ID: id, Tools: assistants.ToolsAFWrite})
	if path == "" {
		t.Fatal("no config generated")
	}
	if err := os.Remove(filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd",
		"opencode-conv", id+".json")); err != nil {
		t.Fatalf("no config at the path the delete path points at: %v", err)
	}
}

// Pick up the reason a turn failed. opencode reports a failure as an error event on stdout
// and exits non-zero with an empty stderr (measured on 1.18.5), so without reading this the
// user sees nothing but "exit status 1".
func TestParseOpencodeRunEventsError(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"step_start","sessionID":"ses_1","part":{"id":"p1","type":"step-start"}}`,
		`{"type":"error","timestamp":1785119549237,"sessionID":"ses_1","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_26a07104"}}}`,
	}, "\n")
	reply, sesID, _, turnErr, _ := parseOpencodeRunEvents([]byte(out))
	if reply != "" {
		t.Fatalf("reply = %q, want empty", reply)
	}
	if sesID != "ses_1" {
		t.Fatalf("sesID = %q", sesID)
	}
	if !strings.Contains(turnErr, "Unexpected server error") || !strings.Contains(turnErr, "err_26a07104") {
		t.Fatalf("turnErr = %q, want the message and the ref", turnErr)
	}
}

// An error without a message falls back to name (never an empty string, even if the shape
// changes).
func TestParseOpencodeRunEventsErrorNameOnly(t *testing.T) {
	out := `{"type":"error","sessionID":"ses_1","error":{"name":"ProviderAuthError"}}`
	_, _, _, turnErr, _ := parseOpencodeRunEvents([]byte(out))
	if turnErr != "ProviderAuthError" {
		t.Fatalf("turnErr = %q, want ProviderAuthError", turnErr)
	}
}

// A successful turn sets no turnErr (regression guard).
func TestParseOpencodeRunEventsOKHasNoError(t *testing.T) {
	out := `{"type":"text","sessionID":"ses_1","part":{"id":"p1","type":"text","text":"OK"}}`
	reply, _, _, turnErr, _ := parseOpencodeRunEvents([]byte(out))
	if reply != "OK" || turnErr != "" {
		t.Fatalf("reply=%q turnErr=%q", reply, turnErr)
	}
}
