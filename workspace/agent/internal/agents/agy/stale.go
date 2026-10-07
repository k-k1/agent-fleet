package agy

// Bounding LiveState's "working" verdict (#1811). The conversation DB says "working" in two
// ways that nothing ever closes if agy dies or hangs mid-turn: the last step's status stays
// running(2)/streaming(8), or the newest turn end is behind the last step. A session stuck
// there read "working" for ~21 h in production and kept its Workspace awake.
//
// Invariants (measurements: docs/log/32, "LiveState の "working" に上限を付ける実測"):
//   - The footer reads "esc to cancel" for the whole of a turn, so an idle footer that stays
//     unpainted is the TUI waiting for input whatever the DB says.
//   - A long run_command is a background task that ends the turn and writes nothing to the DB,
//     so DB staleness alone must never withdraw "working" while a tool process lives.
//   - A tool is a child that is its own session leader; agy's MCP helpers share agy's session.

import (
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// staleAfter is how long the conversation DB (and its -wal) may stay untouched while the DB
// says "working" before the verdict is withdrawn — provided no tool process is alive.
const staleAfter = time.Hour

// liveProbes are the world inputs of the working-verdict checks, injectable for tests.
type liveProbes struct {
	now func() time.Time
	// dbModTime is the newest mtime of <conv>.db and <conv>.db-wal.
	dbModTime func(conv string) (time.Time, bool)
	// paneIdleSettled: the pane shows the idle composer footer and has not been repainted for
	// the tmuxx settle window.
	paneIdleSettled func(name string) bool
	// toolAlive: a tool process (a run_command and its subtree) runs under the agy pane.
	toolAlive func(name string) bool
}

var realProbes = liveProbes{
	now:             time.Now,
	dbModTime:       dbModTime,
	paneIdleSettled: paneIdleSettled,
	toolAlive:       toolProcessAlive,
}

// settleWorking applies the bounds to a DB verdict of "working": "idle" when the pane
// contradicts it, "" (no opinion — not idle, so no completion fires) when it is stale and
// nothing runs, otherwise "working".
func settleWorking(m session.Meta, conv string, p liveProbes) string {
	// An idle footer that stays unpainted means the TUI is waiting for input whatever the DB
	// says; a real turn keeps "esc to cancel" on screen.
	if p.paneIdleSettled(m.Name) {
		return "idle"
	}
	mt, ok := p.dbModTime(conv)
	if !ok || p.now().Sub(mt) < staleAfter {
		return "working"
	}
	// A long build leaves the DB untouched for as long as it runs; only a hung or dead agy has
	// neither writes nor a tool process.
	if p.toolAlive(m.Name) {
		return "working"
	}
	return ""
}

func dbModTime(conv string) (time.Time, bool) {
	var newest time.Time
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(conversationDBPath(conv) + suffix); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest, !newest.IsZero()
}

func paneIdleSettled(name string) bool {
	frame := tmuxx.CapturePane(session.TmuxName(name))
	return tmuxx.FooterSettled(name, frame, hasIdleFooter)
}

// hasIdleFooter reports whether the composer footer in the pane's last lines is the idle one.
// "esc to cancel" wins: both strings never share a footer, but a transcript line quoting
// either must not decide, so only the bottom of the pane is read.
func hasIdleFooter(frame string) bool {
	lines := strings.Split(strings.TrimRight(frame, "\n "), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	idle := false
	for _, l := range lines {
		if strings.Contains(l, "esc to cancel") {
			return false
		}
		if strings.Contains(l, "? for shortcuts") {
			idle = true
		}
	}
	return idle
}

func toolProcessAlive(name string) bool {
	root := procx.PaneRoot(name)
	if root == 0 {
		return false
	}
	return procx.ToolProcessIn(root, procx.Snapshot())
}

// bgReasonProcess is claude.BGReasonProcess's wire value, which the Console maps to its badge
// wording.
const bgReasonProcess = "process"

// backgroundWork reports a tool process still running behind an idle turn. Only "idle" is
// asked: a working row is already held awake, and probing then would cost a /proc scan for
// nothing. The scan is the shared snapshot (procx.ttl), so a list poll over every session
// triggers one scan.
func backgroundWork(m session.Meta, state string, p liveProbes) (bool, string) {
	if state == "idle" && p.toolAlive(m.Name) {
		return true, bgReasonProcess
	}
	return false, ""
}

// BackgroundWork is the agents.BackgroundReporter read, so the /messages mirror header answers
// as WireLive does. The caller has already gated on idle; the shared /proc snapshot keeps it
// cheap and non-blocking.
func (agentImpl) BackgroundWork(m session.Meta) (bool, string) {
	return backgroundWork(m, "idle", realProbes)
}
