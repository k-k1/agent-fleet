package agy

// Bounding LiveState's "working" verdict (#1811). The conversation DB says "working" in two
// ways that nothing ever closes if agy dies or hangs mid-turn: the last step's status stays
// running(2)/streaming(8), or the newest turn end is behind the last step. A session stuck
// there read "working" for ~21 h in production and kept its Workspace awake.
//
// Measured on the real agy in this container (isolated tmux probe, 2026-10-07):
//   - The footer read "esc to cancel" for the whole of a generation, a thinking phase and a
//     foreground run_command, and flipped to "? for shortcuts" in the same 2 s sample in which
//     the turn-end executor_metadata row landed: no sample showed the idle footer while the DB
//     said working.
//   - agy moves a run_command to a background task ~2 s after it starts and ends the turn: a
//     `sleep 25` kept its step at status 2 and its process alive for all 25 s while the DB said
//     idle and the footer "? for shortcuts · 1 task(s)".
//   - While that task runs the DB is untouched: a `sleep 420` left the db/-wal mtime ageing
//     35 s → 177 s without a write, so a long build can look stale from the DB alone.
//   - Under the pane (agy is the pane root) the permanent child is one MCP server
//     (`workspace-agent mcp-stdio`, agy's session). A run_command is a direct child `bash` that
//     is its own session leader (sid == pid): `sleep 25` ran as bash > sleep, a pipeline as
//     bash > {bash > sleep, cat}.

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
	if !hasIdleFooter(frame) {
		return false
	}
	return tmuxx.FrameSettled(name, frame)
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
	return toolProcessIn(root, procx.Snapshot())
}

// toolProcessIn reports whether a tool process lives under root. agy runs each run_command
// as a child that is its own session leader, while its long-lived helpers (MCP servers) share
// agy's session, so "session leader other than the pane's" separates the two without naming
// any helper. A helper that setsid()s itself would read as a tool and only delay the
// withdrawal (status quo), never end a live tool early.
func toolProcessIn(root int, tab map[int]procx.Info) bool {
	rootInfo, ok := tab[root]
	if !ok {
		return false
	}
	kids := procx.Children(tab)
	seen := map[int]bool{root: true}
	queue := append([]int(nil), kids[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		pi, ok := tab[pid]
		if !ok {
			continue
		}
		queue = append(queue, kids[pid]...)
		if pi.State != 'Z' && pi.Sid == pid && pi.Sid != rootInfo.Sid {
			return true
		}
	}
	return false
}
