package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	got := codexMCPArgs(true, "00000000-0000-4000-8000-000000000000")
	want := "mcp_servers.af.default_tools_approval_mode=\"approve\""
	if !containsString(got, want) {
		t.Fatalf("codex MCP args = %q, missing %q", got, want)
	}
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
