package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodexChatBaseArgsNeverPromptAndStayReadOnly(t *testing.T) {
	wantPrefix := []string{"-a", "never", "-s", "read-only", "exec"}
	got := codexChatBaseArgs()
	if len(got) < len(wantPrefix) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("codex args prefix = %q, want %q", got, wantPrefix)
	}
}

func TestCodexMCPArgsPreApproveHeadlessTools(t *testing.T) {
	got, _ := codexMCPArgs(afWriteConv())
	want := "mcp_servers.af.default_tools_approval_mode=\"approve\""
	if !containsString(got, want) {
		t.Fatalf("codex MCP args = %q, missing %q", got, want)
	}
}

func TestCodexMCPArgsForwardAgentAndMemoCredentials(t *testing.T) {
	got, _ := codexMCPArgs(afWriteConv())
	want := `mcp_servers.af.env_vars=["AGENT_TOKEN","AGENT_ADDR","AF_CP_BASE_URL","AF_MEMO_TOKEN","AF_SCHEDULE_TOKEN"]`
	if !containsString(got, want) {
		t.Fatalf("codex MCP args = %q, missing %q", got, want)
	}
}

// afWriteConv is a conversation with the af_write grant and no registry servers —
// the shape these af-plumbing assertions have always described.
func afWriteConv() *chatConversation {
	return &chatConversation{ID: "00000000-0000-4000-8000-000000000000", Tools: assistants.ToolsAFWrite}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestChatMessageRecordsActualFallbackAgent(t *testing.T) {
	b, err := json.Marshal(chatMessage{Role: "assistant", Content: "ok", Agent: "codex", TS: 1})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["agent"] != "codex" {
		t.Fatalf("agent = %v, want codex", got["agent"])
	}
}

func TestNormalizeLegacyMessagesAtCodexFallback(t *testing.T) {
	c := &chatConversation{
		Agent:          "claude",
		CodexSessionID: "019f7ea6-65e0-7341-be2f-4bb8721460a8", // 1784536262112 ms
		Messages: []chatMessage{
			{Role: "assistant", Content: "before", TS: 1784536262000},
			{Role: "assistant", Content: "fallback", TS: 1784536263000},
			{Role: "assistant", Content: "explicit switch-back", Agent: "claude", TS: 1784536264000},
		},
	}
	normalizeChatAgentMetadata(c)
	if got := c.Messages[0].Agent; got != "claude" {
		t.Fatalf("before fallback agent = %q", got)
	}
	if got := c.Messages[1].Agent; got != "codex" {
		t.Fatalf("fallback agent = %q", got)
	}
	if got := c.Messages[2].Agent; got != "claude" {
		t.Fatalf("explicit agent overwritten = %q", got)
	}
	if c.ActiveAgent != "claude" {
		t.Fatalf("active agent = %q, want latest explicit claude", c.ActiveAgent)
	}
}

func TestSyncProviderPromptReplaysFallbackDeltaOnLegacyClaudeResume(t *testing.T) {
	c := &chatConversation{
		Agent: "claude", ClaudeSessionID: "old-claude", CodexSessionID: "old-codex",
		Messages: []chatMessage{
			{Role: "user", Content: "古い依頼"},
			{Role: "assistant", Content: "古い完了", Agent: "claude"},
			{Role: "user", Content: "停止中の対象へ送って"},
			{Role: "assistant", Content: "Codex側の失敗", Agent: "codex"},
			{Role: "user", Content: "今度こそ送って"}, // current turn: supplied separately
		},
	}
	got := syncProviderPrompt(c, "claude", "今度こそ送って", len(c.Messages)-1)
	if strings.Contains(got, "古い依頼") || strings.Contains(got, "古い完了") {
		t.Fatalf("already-known Claude history was replayed: %q", got)
	}
	for _, want := range []string{"停止中の対象へ送って", "Codex側の失敗", "今度こそ送って"} {
		if !strings.Contains(got, want) {
			t.Fatalf("synced prompt = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "今度こそ送って") != 1 {
		t.Fatalf("current request duplicated in synced prompt: %q", got)
	}
}

func TestSyncProviderPromptUsesCursorAfterSuccessfulTurn(t *testing.T) {
	c := &chatConversation{
		Agent: "claude", ClaudeSessionID: "claude", ClaudeMessageCursor: 4,
		Messages: []chatMessage{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1", Agent: "claude"},
			{Role: "user", Content: "u2"},
			{Role: "assistant", Content: "a2", Agent: "claude"},
			{Role: "user", Content: "u3"},
		},
	}
	got := syncProviderPrompt(c, "claude", "u3", 4)
	if got != "u3" {
		t.Fatalf("prompt with no missing turns = %q, want current prompt only", got)
	}
}

func TestSyncProviderPromptRepairsLegacyGapEvenAfterSwitchBack(t *testing.T) {
	c := &chatConversation{
		Agent: "claude", ClaudeSessionID: "old-claude", CodexSessionID: "old-codex",
		Messages: []chatMessage{
			{Role: "user", Content: "最初"},
			{Role: "assistant", Content: "Claude初回", Agent: "claude"},
			{Role: "user", Content: "フォールバック中の依頼"},
			{Role: "assistant", Content: "Codex応答", Agent: "codex"},
			{Role: "user", Content: "認証復旧後の依頼"},
			{Role: "assistant", Content: "Claude復帰後の応答", Agent: "claude"},
			{Role: "user", Content: "今回"},
		},
	}
	got := syncProviderPrompt(c, "claude", "今回", len(c.Messages)-1)
	for _, want := range []string{"フォールバック中の依頼", "Codex応答", "Claude復帰後の応答"} {
		if !strings.Contains(got, want) {
			t.Fatalf("legacy gap repair = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "Claude初回") {
		t.Fatalf("known pre-gap history was replayed: %q", got)
	}
}

func TestCopyLegacyChatClaudeProjectsCreateOnly(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "old", "projects")
	dst := filepath.Join(root, "shared", "projects")
	if err := os.MkdirAll(filepath.Join(src, "repo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "repo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "repo", "new.jsonl"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "repo", "existing.jsonl"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "repo", "existing.jsonl"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}

	copyLegacyChatClaudeProjects(src, dst)

	newBody, err := os.ReadFile(filepath.Join(dst, "repo", "new.jsonl"))
	if err != nil || string(newBody) != "new" {
		t.Fatalf("new transcript = %q, err=%v", newBody, err)
	}
	existingBody, err := os.ReadFile(filepath.Join(dst, "repo", "existing.jsonl"))
	if err != nil || string(existingBody) != "current" {
		t.Fatalf("existing transcript = %q, err=%v", existingBody, err)
	}
}
