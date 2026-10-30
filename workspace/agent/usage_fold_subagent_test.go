package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The ledger folds a claude session's subagent and Workflow-agent transcripts (#3054), each as
// its own row sequence: a session whose watermark predates the change gets each agent row once,
// its own rows are not renumbered, and repeated or out-of-order flushes add nothing twice.
func TestFoldSubagentTranscriptsOnceEach(t *testing.T) {
	useTempUsageDir(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	born := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return born.Add(d).Format(time.RFC3339Nano) }
	m := session.Meta{Name: "sub3054", Dir: t.TempDir(), Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: born.Format(time.RFC3339)}
	sid := session.UUID(m.Dir, m.Name)
	proj := filepath.Join(cfg, "projects", "p1")
	write := func(p, body string, appendTo bool) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendTo {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(p, flag, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(body); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(proj, sid+".jsonl")
	agent := filepath.Join(proj, sid, "subagents", "agent-a1.jsonl")
	wf := filepath.Join(proj, sid, "subagents", "workflows", "wf_x", "agent-b1.jsonl")
	write(main, claudeUserLine(at(0))+claudeUsageLine(at(time.Minute), "r1", 1000, 10), false)
	write(agent, claudeUserLine(at(2*time.Minute))+claudeUsageLine(at(3*time.Minute), "a1", 2000, 20)+
		claudeUserLine(at(4*time.Minute))+claudeUsageLine(at(5*time.Minute), "a2", 2000, 30), false)
	write(wf, claudeUserLine(at(2*time.Minute))+claudeUsageLine(at(150*time.Second), "b1", 0, 40), false)

	fold := func() (int, bool) {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n, dirty, err := foldSessionUsageLocked(m, &st, true)
		if err != nil {
			t.Fatal(err)
		}
		if dirty {
			if err := writeUsageFoldState(st); err != nil {
				t.Fatal(err)
			}
		}
		return n, dirty
	}
	sidechain := func() (rows int, spend int) {
		for _, r := range usagex.ReadRows() {
			if r.Ref == m.Name && r.Sidechain {
				rows++
				spend += r.Spend
			}
		}
		return
	}

	// Upgrade: the session's own transcript was folded before subagents were, with a mark from
	// that build (no subagent marks, none of the new flags).
	usageFoldMu.Lock()
	st := readUsageFoldState()
	if _, err := foldSessionUsageWithTurns(m, &st, sessionx.UsageTurns(m), true); err != nil {
		t.Fatal(err)
	}
	mk := st.Sessions[m.Name]
	mk.Uncapped = false
	st.Sessions[m.Name] = mk
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	usageFoldMu.Unlock()
	if n := len(usagex.ReadRows()); n != 1 {
		t.Fatalf("legacy ledger = %d rows, want 1", n)
	}

	if n, _ := fold(); n != 3 {
		t.Fatalf("first fold after the upgrade = %d rows, want 3 (two agent turns + one Workflow turn)", n)
	}
	if rows, spend := sidechain(); rows != 3 || spend != (2000+20)+(2000+30)+40 {
		t.Fatalf("sidechain rows = %d spend = %d", rows, spend)
	}
	if n, dirty := fold(); n != 0 || dirty {
		t.Fatalf("re-fold = %d rows, dirty=%v; want nothing", n, dirty)
	}

	// Both agents grow in the same pass, the Workflow agent's file written first: each new turn
	// is folded once, and nothing already folded comes back.
	write(wf, claudeUserLine(at(6*time.Minute))+claudeUsageLine(at(7*time.Minute), "b2", 0, 50), true)
	write(agent, claudeUserLine(at(8*time.Minute))+claudeUsageLine(at(9*time.Minute), "a3", 2000, 60), true)
	if n, _ := fold(); n != 2 {
		t.Fatalf("fold after growth = %d rows, want 2", n)
	}
	if n, _ := fold(); n != 0 {
		t.Fatalf("re-fold after growth = %d rows", n)
	}
	if rows, spend := sidechain(); rows != 5 || spend != (2020+2030+40)+50+(2000+60) {
		t.Fatalf("sidechain rows = %d spend = %d after growth", rows, spend)
	}

	// The reader's dedup counts each row once and keeps one entry per agent, not per row; the
	// agents' Idx sequences overlap (both start at 1) and must not drop one another.
	idx := usageDedupIndex{}
	kept := 0
	for _, r := range usagex.ReadRows() {
		if idx.accept(r, rowEnd(r.TS)) {
			kept++
		}
	}
	if kept != 6 {
		t.Fatalf("dedup kept %d of 6 rows", kept)
	}
	if len(idx) != 3 { // the session's own sequence + two agents
		t.Fatalf("dedup entries = %d, want 3", len(idx))
	}
	for _, r := range usagex.ReadRows() {
		if idx.accept(r, rowEnd(r.TS)) {
			t.Fatalf("a replayed row %q idx %d was counted again", r.Agent, r.Idx)
		}
	}
}
