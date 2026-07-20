package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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
	got := agyChatArgs(c, "PROMPT")
	want := []string{"--conversation", "u-1", "-p", "PROMPT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resume args = %q, want %q", got, want)
	}
	first := &chatConversation{}
	got = agyChatArgs(first, "PROMPT")
	if len(got) < 2 || got[len(got)-2] != "-p" || got[len(got)-1] != "PROMPT" {
		t.Fatalf("first-turn args = %q, want trailing -p PROMPT", got)
	}
	if containsString(got, "--conversation") {
		t.Fatalf("first-turn args = %q, must not resume", got)
	}
}

// Live end-to-end of the provider against the real signed-in agy: first turn
// captures the conversation UUID from the cwd map, second turn resumes it.
// Consumes 2 small prompts of real quota — opt-in:
// AF_AGY_LIVE=1 go test . -run TestAgyChatSendLive -v
func TestAgyChatSendLive(t *testing.T) {
	if os.Getenv("AF_AGY_LIVE") == "" {
		t.Skip("set AF_AGY_LIVE=1 to run the live agy chat turn")
	}
	c := &chatConversation{ID: randUUID(), Agent: "agy", Model: defaultAgyChatModel}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(homeDir(), ".config", "agent-fleet", "chat-wd", "agy-"+c.ID))
	})
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()
	reply, err := agyChat{}.send(ctx, c, "「稼働中」とだけ返答してください。")
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if reply == "" || c.AgyConversationID == "" {
		t.Fatalf("first turn reply=%q conversation=%q — want both non-empty", reply, c.AgyConversationID)
	}
	uuid := c.AgyConversationID
	reply2, err := agyChat{}.send(ctx, c, "直前のあなたの返答をそのまま繰り返してください。")
	if err != nil {
		t.Fatalf("resume turn: %v", err)
	}
	if !strings.Contains(reply2, "稼働中") {
		t.Fatalf("resume did not carry context: %q", reply2)
	}
	if c.AgyConversationID != uuid {
		t.Fatalf("conversation id changed on resume: %q → %q", uuid, c.AgyConversationID)
	}
}
