package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func subagentUsageLine(i, in int, text string) string {
	n := strconv.Itoa(i)
	return `{"type":"user","timestamp":"2026-10-30T09:00:0` + n + `Z","isSidechain":true,"uuid":"q` + n + `","message":{"role":"user","content":"go"}}` + "\n" +
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
	turns, err := SubagentTurns([]string{p})
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
	if _, err := SubagentTurns([]string{t.TempDir()}); err == nil {
		t.Fatal("reading a directory: want an error, got none")
	}
	if _, err := SubagentTurns([]string{filepath.Join(t.TempDir(), "missing.jsonl")}); err == nil {
		t.Fatal("reading a missing file: want an error, got none")
	}
}

// The subagents directory can exist under two project directories; one agent found in both is
// one agent, grouped so its copies are merged rather than priced twice.
func TestSubagentUsageLogsGroupsEachAgent(t *testing.T) {
	cfg := isolateSlot(t)
	const sid = "aaaaaaaa-bbbb-cccc-dddd-3051305130ff"
	one := filepath.Join(cfg, "projects", "-p1", sid, "subagents", "agent-a.jsonl")
	two := filepath.Join(cfg, "projects", "-p2", sid, "subagents", "agent-a.jsonl")
	other := filepath.Join(cfg, "projects", "-p2", sid, "subagents", "workflows", "wf_x", "agent-a.jsonl")
	for _, p := range []string{one, two, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := SubagentUsageLogs(sid)
	if len(got) != 2 {
		t.Fatalf("SubagentUsageLogs = %v, want agent-a's two copies as one group plus the Workflow agent", got)
	}
	for _, g := range got {
		switch len(g) {
		case 2:
			if !(g[0] == one && g[1] == two || g[0] == two && g[1] == one) {
				t.Fatalf("agent-a group = %v, want %s and %s", g, one, two)
			}
		case 1:
			if g[0] != other {
				t.Fatalf("Workflow agent group = %v, want %s", g, other)
			}
		default:
			t.Fatalf("group %v", g)
		}
	}
}

// Two copies of one agent, each holding a call the other lacks (r1 only in the first, r3 only
// in the second) and one call both hold (r2): every call counts once, whichever copy is newer.
func TestSubagentTurnsMergesCopies(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(a, []byte(subagentUsageLine(1, 1, "one")+subagentUsageLine(2, 10, "two")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(subagentUsageLine(2, 10, "two")+subagentUsageLine(3, 100, "three")), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := SubagentTurns([]string{b, a})
	if err != nil {
		t.Fatal(err)
	}
	in, last := 0, ""
	for _, tr := range turns {
		in += tr.InTok
		if tr.TS < last {
			t.Fatalf("turns out of time order: %s after %s", tr.TS, last)
		}
		last = tr.TS
	}
	if in != 111 {
		t.Fatalf("input tokens = %d, want 111 (r1 + r2 once + r3)", in)
	}
	// A line both copies hold is one line: kept twice it would split a logical turn in two.
	if len(turns) != 6 {
		t.Fatalf("turns = %d, want 6 (three calls, each with its prompt, once)", len(turns))
	}
}

// A line with no timestamp stays where its copy put it. Sorted as the zero time it would jump
// to the front and merge the two calls it separates into one turn, whose input snapshot is
// then only the second call's.
func TestSubagentTurnsMergeKeepsUntimedLinesInPlace(t *testing.T) {
	dir := t.TempDir()
	body := `{"type":"user","timestamp":"2026-10-30T09:00:01Z","uuid":"q1","message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-10-30T09:00:02Z","uuid":"a1","requestId":"r1","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"one"}],"usage":{"input_tokens":1000,"output_tokens":1}}}` + "\n" +
		`{"type":"user","uuid":"q2","message":{"role":"user","content":"again"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-10-30T09:00:04Z","uuid":"a2","requestId":"r2","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"two"}],"usage":{"input_tokens":2000,"output_tokens":1}}}` + "\n"
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	turns, err := SubagentTurns([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, tr := range turns {
		roles = append(roles, tr.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,user,assistant" {
		t.Fatalf("roles = %s, want the untimed prompt still between the two calls", got)
	}
}

// One unreadable copy must not hide the readable ones: their turns come back with the error.
func TestSubagentTurnsReadsTheCopiesItCan(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(good, []byte(subagentUsageLine(1, 7, "x")), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := SubagentTurns([]string{t.TempDir(), good})
	if err == nil {
		t.Fatal("want the unreadable copy's error")
	}
	in := 0
	for _, tr := range turns {
		in += tr.InTok
	}
	if in != 7 {
		t.Fatalf("input tokens = %d, want 7 from the readable copy", in)
	}
}
