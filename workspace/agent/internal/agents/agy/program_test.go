package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildProgramDefaults(t *testing.T) {
	t.Setenv("AGENT_AGY_CMD", "")
	t.Setenv("AGENT_AGY_FLAGS", "")
	got := buildProgram("", "", "", true)
	want := "agy --dangerously-skip-permissions"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildProgramPlanModeReplacesBypass(t *testing.T) {
	t.Setenv("AGENT_AGY_CMD", "")
	t.Setenv("AGENT_AGY_FLAGS", "")
	got := buildProgram("", "plan", "", false)
	want := "agy --mode plan"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildProgramModelAndResume(t *testing.T) {
	t.Setenv("AGENT_AGY_CMD", "")
	t.Setenv("AGENT_AGY_FLAGS", "")
	got := buildProgram("gemini-3.1-pro", "", "55248f57-852f-44af-9f83-4a99941f0a2c", true)
	want := "agy --dangerously-skip-permissions --model 'gemini-3.1-pro' --conversation '55248f57-852f-44af-9f83-4a99941f0a2c'"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBuildProgramOverride(t *testing.T) {
	t.Setenv("AGENT_AGY_CMD", "bash")
	if got := buildProgram("m", "plan", "id", false); got != "bash" {
		t.Fatalf("got %q want bash", got)
	}
}

func writeLastConversations(t *testing.T, m map[string]string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".gemini", "antigravity-cli", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "last_conversations.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLastConversationFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := LastConversationFor("/w"); got != "" {
		t.Fatalf("missing file: got %q want empty", got)
	}
	writeLastConversations(t, map[string]string{"/w": "uuid-1"})
	if got := LastConversationFor("/w"); got != "uuid-1" {
		t.Fatalf("got %q want uuid-1", got)
	}
	if got := LastConversationFor("/other"); got != "" {
		t.Fatalf("unknown dir: got %q want empty", got)
	}
}

// 権限確認あり（docs/76: 利用者がスキップをオフにした通常起動）。plan ではないので
// --mode plan は付かず、bypass だけが消える。
func TestBuildProgramPermissionsOn(t *testing.T) {
	t.Setenv("AGENT_AGY_CMD", "")
	t.Setenv("AGENT_AGY_FLAGS", "")
	got := buildProgram("", "", "", false)
	if want := "agy"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
