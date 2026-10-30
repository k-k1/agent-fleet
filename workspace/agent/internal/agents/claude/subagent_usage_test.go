package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func subagentUsageLine(i, in int, text string) string {
	n := strconv.Itoa(i)
	return `{"type":"user","timestamp":"2026-10-30T09:00:0` + n + `Z","isSidechain":true,"message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-10-30T09:00:0` + n + `Z","isSidechain":true,"uuid":"u` + n + `","requestId":"r` + n +
		`","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"` + text + `"}],` +
		`"usage":{"input_tokens":` + strconv.Itoa(in) + `,"output_tokens":1}}}` + "\n"
}

// The chat's 1 MiB cap keeps the newest turns. Applied to an agent's usage it would drop the
// oldest calls, and the agent's spend would fall as it grew (#3051 review).
func TestSubagentTurnsKeepsEveryCallPastOneMiB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent-a.jsonl")
	big := strings.Repeat("x", 600<<10)
	body := subagentUsageLine(1, 111, "first") + subagentUsageLine(2, 2, big) + subagentUsageLine(3, 3, big)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := SubagentTurns(p)
	if err != nil {
		t.Fatal(err)
	}
	in := 0
	for _, tr := range turns {
		if !tr.Sidechain {
			t.Fatalf("turn %d not marked Sidechain", tr.Idx)
		}
		in += tr.InTok
	}
	if in != 116 {
		t.Fatalf("input tokens = %d, want 116 (the oldest call included)", in)
	}
}

// A file that cannot be read is an error, never an agent that spent nothing: the budget caches
// what this returns.
func TestSubagentTurnsReportsAReadError(t *testing.T) {
	if _, err := SubagentTurns(t.TempDir()); err == nil {
		t.Fatal("reading a directory: want an error, got none")
	}
	if _, err := SubagentTurns(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("reading a missing file: want an error, got none")
	}
}

// The subagents directory can exist under two project directories; one agent found in both is
// one agent, and its newest copy is the one priced.
func TestSubagentUsageLogsListsEachAgentOnce(t *testing.T) {
	cfg := isolateSlot(t)
	const sid = "aaaaaaaa-bbbb-cccc-dddd-3051305130ff"
	old := filepath.Join(cfg, "projects", "-p1", sid, "subagents", "agent-a.jsonl")
	cur := filepath.Join(cfg, "projects", "-p2", sid, "subagents", "agent-a.jsonl")
	other := filepath.Join(cfg, "projects", "-p2", sid, "subagents", "workflows", "wf_x", "agent-a.jsonl")
	for _, p := range []string{old, cur, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got := SubagentUsageLogs(sid)
	if len(got) != 2 {
		t.Fatalf("SubagentUsageLogs = %v, want agent-a once plus the Workflow agent", got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen[cur] || !seen[other] {
		t.Fatalf("SubagentUsageLogs = %v, want %s and %s", got, cur, other)
	}
}
