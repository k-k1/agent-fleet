package agy

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// staleFixture builds a conversation whose DB says "working" and whose files are age old.
func staleFixture(t *testing.T, name string, rows [][3]any, turns [][]byte, age time.Duration) (session.Meta, time.Time) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := "/home/dev/repos/proj"
	m := session.Meta{Dir: dir, Name: name, Kind: session.KindAgy}
	conv := "conv-" + name
	sids.Write(session.UUID(dir, name), conv)
	mkConvDBWithTurns(t, conv, rows, turns)
	at := time.Now().Add(-age)
	if err := os.Chtimes(conversationDBPath(conv), at, at); err != nil {
		t.Fatal(err)
	}
	return m, time.Now()
}

func probesAt(now time.Time) liveProbes {
	return liveProbes{
		now:             func() time.Time { return now },
		dbModTime:       dbModTime,
		paneIdleSettled: func(string) bool { return false },
		toolAlive:       func(string) bool { return false },
	}
}

func TestLiveStateBoundsWorking(t *testing.T) {
	const user, model, tool = 14, 15, 132
	running := [][3]any{{user, stepStatusDone, []byte("x")}, {tool, stepStatusRunning, []byte("x")}}
	// Last step done but the newest turn end is behind it: the other "working" route.
	noTurnEnd := [][3]any{{user, stepStatusDone, []byte("x")}, {model, stepStatusDone, []byte("x")}, {tool, stepStatusDone, []byte("x")}}
	ended := [][]byte{executorRow(4, 0)}

	for _, tc := range []struct {
		name   string
		rows   [][3]any
		turns  [][]byte
		age    time.Duration
		tool   bool
		footer bool
		want   string
	}{
		{"last step running, stale DB", running, ended, 61 * time.Minute, false, false, ""},
		{"turn end missing, stale DB", noTurnEnd, ended, 61 * time.Minute, false, false, ""},
		{"stale DB but a tool process lives", running, ended, 61 * time.Minute, true, false, "working"},
		{"fresh DB", running, ended, 5 * time.Minute, false, false, "working"},
		{"fresh DB, turn end missing", noTurnEnd, ended, time.Minute, false, false, "working"},
		{"idle footer settled while DB says working", running, ended, time.Minute, false, true, "idle"},
		{"idle footer settled beats a stale DB", running, ended, 5 * time.Hour, false, true, "idle"},
		{"permission is never withdrawn", [][3]any{{tool, stepStatusAwaitingUser, []byte("run_command")}}, nil, 5 * time.Hour, false, false, "permission"},
		{"idle stays idle", noTurnEnd, [][]byte{executorRow(4, 2)}, 5 * time.Hour, false, false, "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, now := staleFixture(t, "slot-bound", tc.rows, tc.turns, tc.age)
			p := probesAt(now)
			p.toolAlive = func(string) bool { return tc.tool }
			p.paneIdleSettled = func(string) bool { return tc.footer }
			if got := liveState(m, p); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The WAL keeps changing while the main file is old: a conversation being written is not stale.
func TestDBModTimeTakesTheNewerOfDBAndWAL(t *testing.T) {
	m, _ := staleFixture(t, "slot-wal", [][3]any{{14, stepStatusRunning, []byte("x")}}, nil, 3*time.Hour)
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if err := os.WriteFile(conversationDBPath(conv)+"-wal", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt, ok := dbModTime(conv)
	if !ok || time.Since(mt) > time.Minute {
		t.Fatalf("mtime %v ok=%v, want the fresh -wal to win", mt, ok)
	}
}

func TestHasIdleFooter(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		want        bool
	}{
		{"idle", "> \n─────\n? for shortcuts      Gemini 3.8 Flash · high\n", true},
		{"idle with a background task", "> \n? for shortcuts   Gemini · high · 1 task(s) · /tasks\n", true},
		{"generating", "> \n─────\n  esc to cancel      Gemini 3.8 Flash · high\n", false},
		{"no footer", "Signing in...\n", false},
		{"quoted in the transcript, far above the footer", "? for shortcuts\na\nb\nc\nd\n", false},
	} {
		if got := hasIdleFooter(tc.frame); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// Shapes measured on the real agy (see stale.go): pane root agy (sid 100), its MCP server in
// the same session, a run_command as its own session leader.
func TestToolProcessIn(t *testing.T) {
	const root = 100
	idle := func() map[int]procx.Info {
		return map[int]procx.Info{
			root: {PPID: 1, State: 'S', Comm: "agy", Pgrp: root, Sid: root},
			101:  {PPID: root, State: 'S', Comm: "workspace-agent", Pgrp: root, Sid: root},
		}
	}
	if toolProcessIn(root, idle()) {
		t.Fatal("agy's MCP server alone read as a tool process")
	}
	tab := idle()
	tab[200] = procx.Info{PPID: root, State: 'S', Comm: "bash", Pgrp: 200, Sid: 200}
	tab[201] = procx.Info{PPID: 200, State: 'S', Comm: "sleep", Pgrp: 200, Sid: 200}
	if !toolProcessIn(root, tab) {
		t.Fatal("a run_command (own-session bash > sleep) was not seen")
	}
	tab[200] = procx.Info{PPID: root, State: 'Z', Comm: "bash", Pgrp: 200, Sid: 200}
	delete(tab, 201)
	if toolProcessIn(root, tab) {
		t.Fatal("a zombie tool counted as running")
	}
	// Wrapper shell as the pane root with agy beneath it, same session.
	wrapped := map[int]procx.Info{
		50:  {PPID: 1, State: 'S', Comm: "sh", Pgrp: 50, Sid: 50},
		100: {PPID: 50, State: 'S', Comm: "agy", Pgrp: 100, Sid: 50},
		101: {PPID: 100, State: 'S', Comm: "workspace-agent", Pgrp: 50, Sid: 50},
	}
	if toolProcessIn(50, wrapped) {
		t.Fatal("agy under a wrapper shell read as a tool process")
	}
	if toolProcessIn(999, wrapped) {
		t.Fatal("unknown root read as a tool process")
	}
}
