// Package procx reads the process table (/proc) for the agents that judge a session by what
// runs under its tmux pane: claude's background-work detectors and agy's stale-turn guard.
package procx

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

// Info is the slice of /proc/<pid>/stat the detectors need.
type Info struct {
	PPID  int
	State byte // R running, S sleeping, D disk-wait, Z zombie, T stopped, …
	Comm  string
	Pgrp  int
	Sid   int // session id; equals the pid of the session leader
}

// snapshot caches a full /proc scan briefly so one session-list poll (which wires every
// session) triggers at most one scan, not one per session.
var (
	mu  sync.Mutex
	at  time.Time
	tab map[int]Info
)

const ttl = 750 * time.Millisecond

// Snapshot returns the process table, at most ttl old.
func Snapshot() map[int]Info {
	mu.Lock()
	defer mu.Unlock()
	if tab != nil && time.Since(at) < ttl {
		return tab
	}
	tab = scan()
	at = time.Now()
	return tab
}

func scan() map[int]Info {
	t := map[int]Info{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return t
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
		if pi, ok := Parse(pid, string(b)); ok {
			t[pid] = pi
		}
	}
	return t
}

// Parse pulls the fields out of /proc/<pid>/stat. comm sits inside the FIRST '(' … LAST ')'
// (it may itself contain spaces and parens), so the fixed fields we want are read from after
// the closing paren: state ppid pgrp session.
func Parse(pid int, s string) (Info, bool) {
	l := strings.IndexByte(s, '(')
	r := strings.LastIndexByte(s, ')')
	if l < 1 || r < l {
		return Info{}, false
	}
	rest := strings.Fields(s[r+1:])
	if len(rest) < 2 || rest[0] == "" {
		return Info{}, false
	}
	ppid, _ := strconv.Atoi(rest[1])
	pi := Info{PPID: ppid, State: rest[0][0], Comm: s[l+1 : r]}
	if len(rest) >= 4 {
		pi.Pgrp, _ = strconv.Atoi(rest[2])
		pi.Sid, _ = strconv.Atoi(rest[3])
	}
	return pi, true
}

// PaneRootPID returns the pane's root process (the shell/program tmux launched for the
// session), the root of the tree to search. 0 if it can't be resolved.
func PaneRootPID(tn string) int {
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

// PaneRoot is PaneRootPID for a session name.
func PaneRoot(name string) int { return PaneRootPID(session.TmuxName(name)) }

// Children inverts a process table into a ppid → children index.
func Children(t map[int]Info) map[int][]int {
	kids := map[int][]int{}
	for pid, pi := range t {
		kids[pi.PPID] = append(kids[pi.PPID], pid)
	}
	return kids
}

// ToolProcessIn reports whether a tool process lives under root. A CLI such as agy runs each run_command
// as a child that is its own session leader, while its long-lived helpers (MCP servers) share
// the CLI's session, so "session leader other than the pane's" separates the two without naming
// any helper. A helper that setsid()s itself would read as a tool and only delay the
// lapse (status quo), never end a live tool early.
func ToolProcessIn(root int, tab map[int]Info) bool {
	rootInfo, ok := tab[root]
	if !ok {
		return false
	}
	kids := Children(tab)
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
