package claude

import (
	"bytes"
	"encoding/json"
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
// task ends, so the recorded state stays idle and the Console shows "waiting for
// input" even though work is ongoing. We surface the truth from the process tree: while idle, if
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

// Background-work reasons, the vocabulary the Console maps to a badge label. Kept as
// constants because the string travels the whole wire (agents.LiveInfo → session.Session
// → CP → Console) and a typo anywhere reads as "no reason" and silently falls back to the
// generic text.
const (
	BGReasonProcess  = "process"  // a run_in_background worker process under the pane
	BGReasonSubagent = "subagent" // an in-process background subagent / Workflow agent
	BGReasonShell    = "shell"    // a Monitor / waiting background shell
)

// BackgroundWork names what is still running while the session's own turn reads idle.
// The three detectors see structurally different things and none subsumes another:
// BackgroundBusy sees run_in_background worker processes under the pane; SubagentBusy
// sees in-process background subagents / Workflow agents, which spawn no such process at
// all; BackgroundShellBusy sees a Monitor / sleep- or I/O-bound background shell that
// sits in S state and so slips past both.
//
// First hit wins, in the original evaluation order — when several are true at once any of
// them is a true statement about the session, and the order keeps the cheap cached /proc
// snapshot ahead of the transcript reads.
func BackgroundWork(name, sid string) (bool, string) {
	switch {
	case BackgroundBusy(name):
		return true, BGReasonProcess
	case SubagentBusy(sid):
		return true, BGReasonSubagent
	case BackgroundShellBusy(name):
		return true, BGReasonShell
	}
	return false, ""
}

// BackgroundShellBusy reports whether a long-lived background shell runs under the
// session's pane while claude is idle — a Monitor's poll loop, or any
// run_in_background shell that spends its life sleeping or waiting on I/O. It closes
// the gap the other two detectors structurally can't see:
//   - BackgroundBusy only flags R/D workers, so a Monitor's `while …; sleep 30; done`
//     slips past — the loop's own bash and its gh/curl children sit in S almost the
//     whole time (sleeping, or blocked on the network), never R/D.
//   - SubagentBusy only sees subagent/Workflow transcripts, which a Monitor never
//     writes (it is neither).
//
// The signature they share: a shell spawned directly by the claude(node) process.
// The pane's own login shell is the BFS root (excluded), and a transient foreground
// Bash tool only exists mid-turn — we run only when idle — so during idle a shell
// hanging off node is a backgrounded one. State is ignored on purpose: the whole
// point is to catch the S-state loop BackgroundBusy misses.
func BackgroundShellBusy(name string) bool {
	root := paneRootPID(session.TmuxName(name))
	if root == 0 {
		return false
	}
	return backgroundShellBusyIn(root, procSnapshot())
}

// backgroundShellBusyIn is the pure core of BackgroundShellBusy, split out so the
// process-tree signature can be tested against a fixtured proc table.
func backgroundShellBusyIn(root int, tab map[int]procInfo) bool {
	kids := childrenOf(tab)
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
		if !shellComm[pi.comm] {
			continue // only a shell carries the background-loop signature
		}
		if !procIsClaude(pi.ppid, tab[pi.ppid]) {
			continue // not spawned by claude → not a claude-backgrounded shell
		}
		if subtreeHasClaude(pid, kids, tab) {
			continue // a wrapper launching another claude (bash → claude), not real work
		}
		return true
	}
	return false
}

// childrenOf inverts a proc table into a ppid → children index.
func childrenOf(tab map[int]procInfo) map[int][]int {
	kids := map[int][]int{}
	for pid, pi := range tab {
		kids[pi.ppid] = append(kids[pi.ppid], pid)
	}
	return kids
}

// procIsClaude reports whether pid is a claude process. claude's node process sets
// its comm to "claude" (or "claude.exe"), so that alone decides it without a /proc
// read; isClaudeProc is the fallback for a node whose comm stayed "node".
func procIsClaude(pid int, pi procInfo) bool {
	return pi.comm == "claude" || pi.comm == "claude.exe" || isClaudeProc(pid)
}

// subtreeHasClaude reports whether any descendant of pid is a claude process, used to
// skip a shell that merely wraps a nested claude launch rather than doing work.
func subtreeHasClaude(pid int, kids map[int][]int, tab map[int]procInfo) bool {
	seen := map[int]bool{}
	queue := append([]int(nil), kids[pid]...)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		if pi, ok := tab[p]; ok && procIsClaude(p, pi) {
			return true
		}
		queue = append(queue, kids[p]...)
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
// state stays idle and the Console shows a bare "waiting for input" while work is
// ongoing. Their only live signal is transcript freshness: while such an agent runs,
// claude appends to its per-agent jsonl every few seconds. We flag the session busy when
// any such log was touched recently, and — like BackgroundBusy — self-clear once they go
// stale (there is no completion marker to key off).

// subagentFreshTTL bounds how recently a subagent/Workflow transcript must have been
// appended to count as "still running". A window shorter than the gap between an
// agent's writes (a long tool call or think) would flap the badge off mid-run; 90s
// bridges those gaps while still clearing soon after the agent stops writing.
//
// 90s is nowhere near enough for the silence during generation (measured: 215s / 342s / 396s,
// bg_agents.go). That gap is closed by the open/close pairing in the main transcript
// (BackgroundAgentsRunning); this arm stays as the catch-all for whatever leaves no trace of
// that shape: Workflow's wf_* agents, an agent resumed through SendMessage (a resume does not
// rewrite the launch record), and versions where claude changed the shape of the record. The
// window is not widened because whenever this arm is positive the agent really is writing now.
const subagentFreshTTL = 90 * time.Second

// SubagentBusy reports whether the session (keyed by its deterministic sid) has an
// in-process background subagent or Workflow agent still working. Two arms, in order of
// strength:
//
//   - the main transcript's launch/notification pairing (bg_agents.go) — a positive
//     open/close signal with no time window, so it holds through a long generation;
//   - transcript freshness: claude writes each agent's turns to
//     ConfigDir()/projects/*/<sid>/subagents/agent-*.jsonl (regular subagents) or
//     subagents/workflows/wf_*/agent-*.jsonl (Workflow agents), and a just-appended log
//     means one is live even when no pairing record exists for it.
//
// Complements BackgroundBusy, which covers the process-tree case both structurally
// cannot see.
func SubagentBusy(sid string) bool {
	if BackgroundAgentsRunning(sid) {
		return true
	}
	cutoff := time.Now().Add(-subagentFreshTTL)
	for _, p := range SubagentLogs(sid) {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// SubagentLogs lists sid's background-agent transcripts. Regular subagents sit directly
// under subagents/; Workflow agents nest under subagents/workflows/wf_*/.
func SubagentLogs(sid string) []string {
	if sid == "" {
		return nil
	}
	base := filepath.Join(ConfigDir(), "projects", "*", sid, "subagents")
	var out []string
	for _, pat := range []string{
		filepath.Join(base, "agent-*.jsonl"),
		filepath.Join(base, "workflows", "wf_*", "agent-*.jsonl"),
	} {
		logs, _ := filepath.Glob(pat)
		out = append(out, logs...)
	}
	return out
}

// SubagentSnapshot is TranscriptSnapshot for the background agents' logs — the baseline
// SubagentReceivedSince compares against.
func SubagentSnapshot(sid string) map[string]int64 {
	snap := map[string]int64{}
	for _, p := range SubagentLogs(sid) {
		if fi, err := os.Stat(p); err == nil {
			snap[p] = fi.Size()
		}
	}
	return snap
}

// SubagentReceivedSince reports whether the prompt landed in a BACKGROUND AGENT's
// transcript instead of the session's own — the signature of typing into the pane while
// its input box is bound to an agent (claude records it there as "The user sent a new
// message while you were working:"). Matched on the prompt's own text, so an agent that
// merely kept working since the baseline does not read as a misdelivery.
//
// Going on to retype (self-repair) while this holds would fire the same interruption into the
// subagent a second time; telling them apart before firing is the point (2026-07-30 sannme2).
func SubagentReceivedSince(sid string, snap map[string]int64, prompt string) bool {
	needle := jsonNeedle(prompt)
	if needle == nil {
		return false
	}
	for _, p := range SubagentLogs(sid) {
		if bytes.Contains(appendedSince(p, snap[p]), needle) {
			return true
		}
	}
	return false
}

// TranscriptBusy reports whether the session's MAIN transcript
// (ConfigDir()/projects/*/<sid>.jsonl) grew a real turn record recently — the turn
// itself is still running. Same freshness signal and TTL as SubagentBusy, aimed at the
// main turn instead of its background agents: a mid-turn think gap fires no hooks, so
// the status marker alone cannot distinguish "quiet because thinking" from "done".
func TranscriptBusy(sid string) bool {
	at, ok := TranscriptTouched(sid)
	return ok && TranscriptFresh(at)
}

// TranscriptFresh applies the freshness window to an already-observed transcript time,
// so a caller that needs the time anyway (reportTranscriptBusy compares it against the
// turn-end marker) does not have to read the file twice.
func TranscriptFresh(at time.Time) bool { return at.After(time.Now().Add(-subagentFreshTTL)) }

// TranscriptTouched returns WHEN the session's main transcript last grew a REAL turn record.
// Callers that hold an independent turn-end event (the marker the Stop hook wrote, say) compare
// against it instead of using the bare freshness window: if the transcript grew after that
// marker, the marker was not the end of the turn.
//
// A real record = a line whose type is user or assistant. Reading the line's timestamp rather
// than the file's mtime is the crux, and is the permanent fix for a defect that hit us in
// practice (2026-07-30 s2bl5pv/sannme2/sp2qemx): claude appends bookkeeping lines unrelated to
// the turn (system/away_summary, last-prompt, custom-title, agent-name, mode, permission-mode,
// file-history-*) after the fact. While only mtime was read, such an append passed for "still
// working", and the compensating reconciler misread an already-reported instruction as "work
// resumed", sending a correction plus a duplicate report (sp2qemx had zero user/assistant lines
// for the 40 minutes after completing at 09:56:56, yet was reopened at 10:06).
//
// Accepting by allowlist rather than by exclusion is for the same reason as AbortedTurn: the
// kinds of bookkeeping record grow and shrink from version to version, so an unknown type must
// fall on the ignored side by default. isSidechain is included here, whereas AbortedTurn
// excludes it because it decides termination: older claude wrote subagent turns inline into the
// main transcript, and that is unmistakable evidence of a run in progress.
func TranscriptTouched(sid string) (time.Time, bool) {
	if sid == "" {
		return time.Time{}, false
	}
	var newest time.Time
	for _, p := range jsonlPaths(sid) {
		if at, ok := lastRealRecordAt(p); ok && at.After(newest) {
			newest = at
		}
	}
	return newest, !newest.IsZero()
}

// transcriptTailWindow bounds the tail reads the polled predicates do (lastLineWhere).
// Transcripts reach several MB and this runs on the reconciler's tick, so we read the end
// of the file and only widen to the whole thing when the window holds nothing usable (a
// tail made entirely of bookkeeping, or one enormous record).
const transcriptTailWindow = 512 << 10

// lastRealRecordAt returns the timestamp of the last user/assistant record in p.
func lastRealRecordAt(p string) (time.Time, bool) {
	line, ok := lastLineWhere(p, func(l []byte) bool { _, ok := realRecordAt(l); return ok })
	if !ok {
		return time.Time{}, false
	}
	return realRecordAt(line)
}

// realRecordAt parses one transcript line and returns its timestamp when the line is a
// real turn record (not bookkeeping, not an untimed line).
func realRecordAt(line []byte) (time.Time, bool) {
	var r struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &r) != nil {
		return time.Time{}, false
	}
	if r.Type != "user" && r.Type != "assistant" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, r.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
