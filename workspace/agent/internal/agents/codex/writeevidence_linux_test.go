package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex/writeevidence"
)

func TestScopedShellEvidenceReconcilesCodexRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	scope := writeevidence.Scope{SessionID: "slot", ThreadID: "thread", TurnID: "turn-a"}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := writeevidence.Save(writeevidence.Evidence{
		Scope: scope, Root: root, Paths: []string{"both", "relative-only", "shell only"},
	}); err != nil {
		t.Fatal(err)
	}
	line := func(kind string, payload map[string]string) []byte {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"type": kind, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	patch := func(id, target string) []byte {
		return wrapItem(t, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": id,
			"input": "*** Begin Patch\n*** Update File: " + target + "\n@@\n-old\n+new\n*** End Patch"})
	}
	rows, _ := parseRollout([][]byte{
		line("session_meta", map[string]string{"cwd": alias}),
		line("event_msg", map[string]string{"type": "task_started", "turn_id": scope.TurnID}),
		wrapItem(t, map[string]any{
			"type": "function_call", "name": "exec_command", "call_id": "shell",
			"arguments": `{"cmd":"printf changed > both; printf changed > 'shell only'"}`,
		}),
		patch("relative", "both"),
		patch("absolute", filepath.Join(root, "both")),
		patch("aliased-cwd", "relative-only"),
		line("event_msg", map[string]string{"type": "task_started", "turn_id": "turn-b"}),
		patch("other-turn", "shell only"),
	})
	edits, err := writeevidence.FileEdits(scope, rows)
	if err != nil || len(edits) != 4 {
		t.Fatalf("FileEdits = %+v, %v; want three explicit calls and one shell-only edit", edits, err)
	}
	for _, edit := range edits[:3] {
		if edit.Added != 1 || edit.Removed != 1 {
			t.Errorf("patch stats missing: %+v", edit)
		}
	}
	if edit := edits[3]; edit.Path != filepath.Join(root, "shell only") || edit.Added != 0 || edit.Removed != 0 {
		t.Errorf("shell edit = %+v", edit)
	}
}
