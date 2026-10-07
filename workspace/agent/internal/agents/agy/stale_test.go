package agy

import (
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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

func TestStateSourceModTimeIsTheConversationDB(t *testing.T) {
	m, _ := staleFixture(t, "src", [][3]any{{1, 2, "x"}}, nil, 3*time.Hour)
	got, ok := agentImpl{}.StateSourceModTime(m)
	if !ok || time.Since(got) < 2*time.Hour {
		t.Fatalf("StateSourceModTime = %v, %v; want the DB's 3h-old mtime", got, ok)
	}
	if _, ok := (agentImpl{}).StateSourceModTime(session.Meta{Dir: "/other", Name: "none"}); ok {
		t.Fatal("a session with no conversation answered")
	}
}

func TestBackgroundWork(t *testing.T) {
	m := session.Meta{Name: "bg"}
	alive := func(v bool) liveProbes { return liveProbes{toolAlive: func(string) bool { return v }} }
	for _, tc := range []struct {
		name, state string
		tool        bool
		busy        bool
		reason      string
	}{
		{"idle with a tool process", "idle", true, true, "process"},
		{"idle, no tool process", "idle", false, false, ""},
		{"working is not asked", "working", true, false, ""},
		{"no opinion", "", true, false, ""},
		{"question", "question", true, false, ""},
	} {
		busy, reason := backgroundWork(m, tc.state, alive(tc.tool))
		if busy != tc.busy || reason != tc.reason {
			t.Errorf("%s: got %v %q, want %v %q", tc.name, busy, reason, tc.busy, tc.reason)
		}
	}
}

// WireLive end to end: the measured shape of a backgrounded run_command — the DB step stays
// running, the pane is back at the idle footer, a tool process lives.
func TestWireLiveBackgroundBusy(t *testing.T) {
	const user, tool = 14, 132
	running := [][3]any{{user, stepStatusDone, []byte("x")}, {tool, stepStatusRunning, []byte("x")}}
	for _, tc := range []struct {
		name  string
		tool  bool
		alive bool
		busy  bool
	}{
		{"tool process lives", true, true, true},
		{"no tool process (negative control)", false, true, false},
		{"pane dead", true, false, false},
	} {
		m, now := staleFixture(t, "wl", running, [][]byte{executorRow(4, 0)}, time.Minute)
		saved := realProbes
		realProbes = probesAt(now)
		realProbes.paneIdleSettled = func(string) bool { return true }
		realProbes.toolAlive = func(string) bool { return tc.tool }
		li := agentImpl{}.WireLive(m, tc.alive)
		realProbes = saved
		if li.BackgroundBusy != tc.busy {
			t.Errorf("%s: BackgroundBusy=%v (state %q), want %v", tc.name, li.BackgroundBusy, li.State, tc.busy)
		}
		if tc.busy && li.BackgroundBusyReason != "process" {
			t.Errorf("%s: reason %q", tc.name, li.BackgroundBusyReason)
		}
	}
}

func TestAgentIsBackgroundReporter(t *testing.T) {
	var a agents.Agent = agentImpl{}
	br, ok := a.(agents.BackgroundReporter)
	if !ok {
		t.Fatal("agy must implement agents.BackgroundReporter")
	}
	saved := realProbes
	defer func() { realProbes = saved }()
	for _, tool := range []bool{true, false} {
		realProbes.toolAlive = func(string) bool { return tool }
		busy, reason := br.BackgroundWork(session.Meta{Name: "br"})
		wb, wr := backgroundWork(session.Meta{Name: "br"}, "idle", realProbes)
		if busy != tool || busy != wb || reason != wr {
			t.Errorf("tool=%v: got %v %q, WireLive-side %v %q", tool, busy, reason, wb, wr)
		}
	}
}
