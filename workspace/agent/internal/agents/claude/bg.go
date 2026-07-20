package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Background-activity detection. A claude session can launch a run_in_background
// task (a long build/test, a server, …), finish its turn, and return to "idle" at
// the prompt while that task keeps running. claude fires no hook when a background
// task ends, so the recorded state stays idle and the Console shows 入力待ち even
// though work is ongoing. We surface the truth from the process tree: while idle, if
// a live worker process (state R = running, or D = uninterruptible I/O) runs under
// the session's tmux pane — and it isn't claude itself or a shell wrapper — the
// session is still busy in the background. This needs no completion hook and
// self-clears the instant the process exits.
//
// Caveat: a task fully daemonized away from claude's process tree (double-fork +
// setsid, ppid→1) wouldn't be seen; run_in_background tasks stay tracked children,
// which is the case this covers.

type procInfo struct {
	ppid  int
	state byte // R running, S sleeping, D disk-wait, Z zombie, T stopped, …
	comm  string
}

// procSnapshot caches a full /proc scan briefly so one session-list poll (which
// wires every session) triggers at most one scan, not one per session.
var (
	procMu  sync.Mutex
	procAt  time.Time
	procTab map[int]procInfo
)

const procTTL = 750 * time.Millisecond

func procSnapshot() map[int]procInfo {
	procMu.Lock()
	defer procMu.Unlock()
	if procTab != nil && time.Since(procAt) < procTTL {
		return procTab
	}
	procTab = scanProc()
	procAt = time.Now()
	return procTab
}

func scanProc() map[int]procInfo {
	tab := map[int]procInfo{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return tab
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid dir
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // exited between readdir and read
		}
		if pi, ok := parseStat(pid, string(b)); ok {
			tab[pid] = pi
		}
	}
	return tab
}

// parseStat pulls state + ppid from /proc/<pid>/stat. comm sits inside the FIRST
// '(' … LAST ')' (it may itself contain spaces and parens), so the fixed fields we
// want are read from after the closing paren.
func parseStat(pid int, s string) (procInfo, bool) {
	l := strings.IndexByte(s, '(')
	r := strings.LastIndexByte(s, ')')
	if l < 1 || r < l {
		return procInfo{}, false
	}
	rest := strings.Fields(s[r+1:])
	if len(rest) < 2 || rest[0] == "" {
		return procInfo{}, false
	}
	ppid, _ := strconv.Atoi(rest[1])
	return procInfo{ppid: ppid, state: rest[0][0], comm: s[l+1 : r]}, true
}

// shellComm are the interactive/wrapper shells that run a background command but
// aren't the work themselves — the real worker is their (non-shell) child, which we
// catch instead. Skipping them avoids flagging a `bash -c` wrapper as busy.
var shellComm = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ash": true,
}

// paneRootPID returns the pane's root process (the shell/program tmux launched for
// the session), the root of the tree we search. 0 if it can't be resolved.
func paneRootPID(tn string) int {
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return 0
	}
	out, err := tmuxx.Cmd("display-message", "-p", "-t", pane, "#{pane_pid}").Output()
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return pid
}

// BackgroundBusy reports whether a live worker process runs under the
// session's pane — a run_in_background task still going while claude is idle.
func BackgroundBusy(name string) bool {
	root := paneRootPID(session.TmuxName(name))
	if root == 0 {
		return false
	}
	tab := procSnapshot()
	kids := map[int][]int{}
	for pid, pi := range tab {
		kids[pi.ppid] = append(kids[pi.ppid], pid)
	}
	// BFS the descendants of the pane root (root itself excluded).
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
		if pi.state != 'R' && pi.state != 'D' {
			continue // not actively running / in I/O → not "in progress"
		}
		if shellComm[pi.comm] {
			continue // a shell wrapper; its real child is judged on its own
		}
		if pi.comm == "claude" || isClaudeProc(pid) {
			continue // claude itself (native comm, or node whose cmdline names claude),
			// momentarily R while redrawing — or our own agent (MCP helper).
		}
		return true
	}
	return false
}

// isClaudeProc skips the claude CLI itself (node running claude) and our own agent
// binary (the MCP helper), so neither is mistaken for a background task.
func isClaudeProc(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	cl := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.Contains(cl, "claude") || strings.Contains(cl, "workspace-agent")
}

// In-process background agents — a run_in_background subagent (Task/Explore/…) or a
// Workflow agent — are invisible to BackgroundBusy: they run INSIDE the main
// claude(node) process and spawn NO worker under the tmux pane, so the /proc tree
// shows nothing to flag. claude also fires no hook when one finishes, so the recorded
// state stays idle and the Console shows a bare 入力待ち while work is ongoing. Their
// only live signal is transcript freshness: while such an agent runs, claude appends
// to its per-agent jsonl every few seconds. We flag the session busy when any such
// log was touched recently, and — like BackgroundBusy — self-clear once they go stale
// (there is no completion marker to key off).

// subagentFreshTTL bounds how recently a subagent/Workflow transcript must have been
// appended to count as "still running". A window shorter than the gap between an
// agent's writes (a long tool call or think) would flap the badge off mid-run; 90s
// bridges those gaps while still clearing soon after the agent stops writing.
const subagentFreshTTL = 90 * time.Second

// SubagentBusy reports whether the session (keyed by its deterministic sid) has an
// in-process background subagent or Workflow agent still working. claude writes each
// agent's turns to ConfigDir()/projects/*/<sid>/subagents/agent-*.jsonl (regular
// subagents) or subagents/workflows/wf_*/agent-*.jsonl (Workflow agents); a
// recently-appended log means one is live. Complements BackgroundBusy, which covers
// the process-tree case it structurally cannot see.
func SubagentBusy(sid string) bool {
	if sid == "" {
		return false
	}
	base := filepath.Join(ConfigDir(), "projects", "*", sid, "subagents")
	cutoff := time.Now().Add(-subagentFreshTTL)
	// Regular subagents sit directly under subagents/; Workflow agents nest under
	// subagents/workflows/wf_*/. Check both, returning on the first fresh log.
	for _, pat := range []string{
		filepath.Join(base, "agent-*.jsonl"),
		filepath.Join(base, "workflows", "wf_*", "agent-*.jsonl"),
	} {
		logs, _ := filepath.Glob(pat)
		for _, p := range logs {
			if fi, err := os.Stat(p); err == nil && fi.ModTime().After(cutoff) {
				return true
			}
		}
	}
	return false
}
