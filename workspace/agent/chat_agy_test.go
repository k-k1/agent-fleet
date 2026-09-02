package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
)

func agyCatalog(names ...string) []agents.ModelChoice {
	var list []agents.ModelChoice
	for _, n := range names {
		list = append(list, agents.ModelChoice{ID: n, Label: n})
	}
	return list
}

func TestAgyChatModelDropsStalePin(t *testing.T) {
	catalog := agyCatalog("Gemini 3.5 Flash (Medium)", "Gemini 3.1 Pro (High)")
	if got := agyChatModel("Gemini 3.5 Flash (Medium)", catalog); got != "Gemini 3.5 Flash (Medium)" {
		t.Fatalf("listed pin = %q, want passthrough", got)
	}
	if got := agyChatModel("Gemini 2.0 Legacy", catalog); got != "" {
		t.Fatalf("stale pin = %q, want dropped (agy default)", got)
	}
	// An empty catalog (CLI absent / signed out) cannot validate — pass through.
	if got := agyChatModel("Gemini 2.0 Legacy", nil); got != "Gemini 2.0 Legacy" {
		t.Fatalf("unvalidatable pin = %q, want passthrough", got)
	}
	if got := agyChatModel("", catalog); got != "" {
		t.Fatalf("empty pin = %q, want empty", got)
	}
}

func TestAgyChatArgsFlagsBeforePrompt(t *testing.T) {
	c := &chatConversation{Model: "", AgyConversationID: "u-1"}
	got, model := agyChatArgs(c, "PROMPT")
	want := []string{"--conversation", "u-1", "-p", "PROMPT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resume args = %q, want %q", got, want)
	}
	// No pin = agy runs on its own default: nothing truthful to record for the turn.
	if model != "" {
		t.Fatalf("unpinned turn model = %q, want empty", model)
	}
	first := &chatConversation{}
	got, _ = agyChatArgs(first, "PROMPT")
	if len(got) < 2 || got[len(got)-2] != "-p" || got[len(got)-1] != "PROMPT" {
		t.Fatalf("first-turn args = %q, want trailing -p PROMPT", got)
	}
	if containsString(got, "--conversation") {
		t.Fatalf("first-turn args = %q, must not resume", got)
	}
}

func TestAgyChatAllowRulesFollowToolGrant(t *testing.T) {
	none := agyChatAllowRules(&chatConversation{Tools: assistants.ToolsNone})
	if containsString(none, "mcp(af/*)") {
		t.Fatalf("tools=none rules = %q, must not allow af", none)
	}
	if !containsString(none, "read_file") {
		t.Fatalf("tools=none rules = %q, read tools missing", none)
	}
	read := agyChatAllowRules(&chatConversation{Tools: assistants.ToolsAFRead})
	if !containsString(read, "mcp(af/*)") {
		t.Fatalf("tools=af_read rules = %q, mcp(af/*) missing", read)
	}
	if containsString(read, "command") {
		t.Fatalf("rules = %q must never allow command execution", read)
	}
}

func TestAgyChatServersCarryWriteConv(t *testing.T) {
	c := &chatConversation{ID: "00000000-0000-4000-8000-000000000001", Tools: assistants.ToolsAFWrite}
	servers := agyChatServers(c)
	af, ok := servers["af"].(map[string]any)
	if !ok {
		t.Fatalf("servers = %v, af missing", servers)
	}
	args, _ := af["args"].([]any)
	var flat []string
	for _, a := range args {
		flat = append(flat, a.(string))
	}
	for _, want := range []string{"mcp-stdio", "--write", "--conv", c.ID} {
		if !containsString(flat, want) {
			t.Fatalf("af args = %q, missing %q", flat, want)
		}
	}
	env, _ := af["env"].(map[string]any)
	if env["HOME"] != homeDir() {
		t.Fatalf("af env HOME = %v, want real home %q (isolated-HOME leak)", env["HOME"], homeDir())
	}
	if s := agyChatServers(&chatConversation{Tools: assistants.ToolsNone}); len(s) != 0 {
		t.Fatalf("tools=none servers = %v, want empty", s)
	}
}

func TestChatAgyHomeWritesIsolatedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &chatConversation{ID: "00000000-0000-4000-8000-000000000002", Tools: assistants.ToolsAFRead}
	home, wd, err := chatAgyHome(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(home, homeDir()) || !strings.HasPrefix(wd, homeDir()) {
		t.Fatalf("home=%q wd=%q escaped the test home", home, wd)
	}
	var settings struct {
		EnableTelemetry   bool     `json:"enableTelemetry"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
		Permissions       struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.EnableTelemetry {
		t.Fatal("telemetry must be pinned off")
	}
	if len(settings.TrustedWorkspaces) != 1 || settings.TrustedWorkspaces[0] != wd {
		t.Fatalf("trustedWorkspaces = %v, want [%s]", settings.TrustedWorkspaces, wd)
	}
	if !containsString(settings.Permissions.Allow, "mcp(af/*)") {
		t.Fatalf("allow = %v, mcp(af/*) missing", settings.Permissions.Allow)
	}
	var mcp struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	b, err = os.ReadFile(filepath.Join(home, ".gemini", "config", "mcp_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &mcp); err != nil {
		t.Fatal(err)
	}
	if _, ok := mcp.Servers["af"]; !ok {
		t.Fatalf("mcp_config servers = %v, af missing", mcp.Servers)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "config", "config.json")); err != nil {
		t.Fatalf("config.json missing: %v", err)
	}
}

// Live end-to-end of the provider against the real signed-in agy: the first
// turn must call an af MCP tool through the isolated home (permissions + MCP
// config + env.HOME pin) and the second turn must resume the captured UUID.
// Consumes 2 small prompts of real quota — opt-in:
// AF_AGY_LIVE=1 go test . -run TestAgyChatSendLive -v
func TestAgyChatSendLive(t *testing.T) {
	if os.Getenv("AF_AGY_LIVE") == "" {
		t.Skip("set AF_AGY_LIVE=1 to run the live agy chat turn")
	}
	prevExe := agyChatExe
	agyChatExe = func() string { return "/usr/local/bin/workspace-agent" }
	t.Cleanup(func() { agyChatExe = prevExe })
	c := &chatConversation{ID: randUUID(), Agent: "agy", Model: defaultAgyChatModel, Tools: assistants.ToolsAFRead}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd", "agy-"+c.ID))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*chatTimeout)
	defer cancel()
	reply, err := agyChat{}.send(ctx, c,
		"MCPツール list_my_sessions を実際に呼び出してください。呼び出しに成功したら「TOOL_OK <セッション件数>」、失敗したら「TOOL_FAIL <理由>」という形式だけで返答してください。")
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if !strings.Contains(reply, "TOOL_OK") {
		t.Fatalf("MCP tool call did not succeed: %q", reply)
	}
	if c.AgyConversationID == "" {
		t.Fatal("conversation UUID not captured from the isolated home")
	}
	uuid := c.AgyConversationID
	reply2, err := agyChat{}.send(ctx, c, "直前のあなたの返答をそのまま繰り返してください。")
	if err != nil {
		t.Fatalf("resume turn: %v", err)
	}
	if !strings.Contains(reply2, "TOOL_OK") {
		t.Fatalf("resume did not carry context: %q", reply2)
	}
	if c.AgyConversationID != uuid {
		t.Fatalf("conversation id changed on resume: %q → %q", uuid, c.AgyConversationID)
	}
}
