package claude

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

// BGShell is one background shell claude left running under a session's pane: a
// run_in_background Bash or a Monitor loop. The mirror lists them so the member can see what
// keeps the session "background busy" and stop one that will never finish (#3279).
type BGShell struct {
	PID int `json:"pid"`
	// Start is the process start time in clock ticks since boot. The stop request sends it
	// back, and a mismatch means the pid now belongs to another process.
	Start   uint64 `json:"start"`
	Command string `json:"command"`
	// StartedAt is Start as wall time (RFC 3339); empty when the boot time is unreadable.
	StartedAt string `json:"startedAt,omitempty"`
	// Running: something in the subtree is on a CPU or in disk wait right now.
	Running bool `json:"running"`
	// CPUSec is the subtree's total user+system CPU time. A shell waiting for 30 minutes with
	// almost none of it is what the Console flags as possibly stuck.
	CPUSec float64 `json:"cpuSec"`
}

// clockTicks is USER_HZ. It is 100 on every Linux ABI this runs on, and reading it properly
// needs sysconf (cgo).
const clockTicks = 100

// BackgroundShells lists the background shells under the session's pane, oldest first.
func BackgroundShells(name string) []BGShell {
	root := paneRoot(name)
	if root == 0 {
		return nil
	}
	return backgroundShellsIn(root, procx.Snapshot(), bootTime())
}

// backgroundShellsIn is the pure core of BackgroundShells. It applies the same signature as
// backgroundShellBusyIn (a shell whose parent is claude, not wrapping another claude), so the
// list never names a shell the busy badge does not count, and vice versa.
func backgroundShellsIn(root int, tab map[int]procInfo, boot time.Time) []BGShell {
	kids := procx.Children(tab)
	var out []BGShell
	for _, pid := range descendants(root, kids) {
		pi, ok := tab[pid]
		if !ok || !isBackgroundShell(pid, pi, kids, tab) {
			continue
		}
		sh := BGShell{PID: pid, Start: pi.StartTicks, Command: shellCommand(procArgv(pid))}
		if !boot.IsZero() && pi.StartTicks > 0 {
			sh.StartedAt = boot.Add(time.Duration(pi.StartTicks) * time.Second / clockTicks).Format(time.RFC3339)
		}
		var ticks uint64
		for _, p := range append([]int{pid}, descendants(pid, kids)...) {
			c := tab[p]
			ticks += c.CPUTicks
			if p != pid && (c.State == 'R' || c.State == 'D') {
				sh.Running = true
			}
		}
		sh.CPUSec = float64(ticks) / clockTicks
		out = append(out, sh)
	}
	// Oldest first: the one most likely to be stuck heads the list.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Start < out[j-1].Start; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// isBackgroundShell is the signature shared by the busy badge and the list.
func isBackgroundShell(pid int, pi procInfo, kids map[int][]int, tab map[int]procInfo) bool {
	return shellComm[pi.Comm] && procIsClaude(pi.PPID, tab[pi.PPID]) && !subtreeHasClaude(pid, kids, tab)
}

// descendants lists every process below root (root excluded), breadth first.
func descendants(root int, kids map[int][]int) []int {
	seen := map[int]bool{root: true}
	var out []int
	queue := append([]int(nil), kids[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		queue = append(queue, kids[pid]...)
	}
	return out
}

// shellCommand recovers the command the agent asked for from claude's Bash wrapper,
// `bash -c "source <snapshot> … && eval '<command>' && pwd -P >| <cwd file>"`. Inside the
// single quotes a quote is spelled '"'"'. Anything else (a different wrapper, a newer CLI)
// falls back to the whole script, which is still the truth, just noisier.
func shellCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	script := argv[len(argv)-1]
	if len(argv) >= 3 && argv[len(argv)-2] == "-c" {
		const open = "eval '"
		if i := strings.Index(script, open); i >= 0 {
			body := script[i+len(open):]
			if j := strings.LastIndex(body, "' && pwd -P"); j >= 0 {
				body = body[:j]
			} else if j := strings.LastIndex(body, "'"); j >= 0 {
				body = body[:j]
			}
			return strings.ReplaceAll(body, `'"'"'`, `'`)
		}
	}
	return strings.Join(argv, " ")
}

// bootTime reads btime from /proc/stat; zero when unreadable.
func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(s, 0)
			}
		}
	}
	return time.Time{}
}

// ErrBGShellGone is returned when the pid is no longer one of the session's background
// shells, or now belongs to a process with a different start time.
var ErrBGShellGone = errors.New("background shell not found")

// bgStopGrace is how long SIGTERM gets before SIGKILL. Variables for tests.
var (
	bgStopGrace = 3 * time.Second
	killProc    = syscall.Kill
	freshProcs  = procx.Fresh
	paneRoot    = procx.PaneRoot
)

// StopBackgroundShell ends one background shell of the session and everything under it.
// The pid must still be one of this session's background shells in a FRESH snapshot and carry
// the start time the caller saw, so a stale Console row can never kill an unrelated process
// that reused the pid. It returns once the targets are gone or SIGKILL has been sent.
func StopBackgroundShell(name string, pid int, start uint64) error {
	root := paneRoot(name)
	if root == 0 {
		return ErrBGShellGone
	}
	targets, group, err := bgStopTargets(root, freshProcs(), pid, start)
	if err != nil {
		return err
	}
	if group {
		_ = killProc(-pid, syscall.SIGTERM)
	}
	signalLive(targets, syscall.SIGTERM)
	for deadline := time.Now().Add(bgStopGrace); time.Now().Before(deadline); {
		if len(liveTargets(targets)) == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Not the process group here: by now the leader may be gone and its pgid reused.
	signalLive(targets, syscall.SIGKILL)
	return nil
}

// bgTarget is a process to signal, pinned to its start time: every signal re-checks it,
// because during the grace a target can exit and its pid be handed to someone else.
type bgTarget struct {
	pid   int
	start uint64
}

// bgStopTargets validates the request against tab and returns what to signal: the shell and
// its subtree, deepest last, plus whether the shell leads its own process group (claude starts
// each background shell as a session leader; signalling the group also reaches a member that
// re-parented away from the tree).
func bgStopTargets(root int, tab map[int]procInfo, pid int, start uint64) ([]bgTarget, bool, error) {
	pi, ok := tab[pid]
	if !ok || pi.StartTicks != start {
		return nil, false, ErrBGShellGone
	}
	kids := procx.Children(tab)
	inPane := false
	for _, p := range descendants(root, kids) {
		if p == pid {
			inPane = true
			break
		}
	}
	if !inPane || !isBackgroundShell(pid, pi, kids, tab) {
		return nil, false, ErrBGShellGone
	}
	out := []bgTarget{{pid, pi.StartTicks}}
	for _, p := range descendants(pid, kids) {
		out = append(out, bgTarget{p, tab[p].StartTicks})
	}
	return out, pi.Pgrp == pid, nil
}

// liveTargets returns the targets still running as the same process (a zombie has already
// exited and only waits for its parent).
func liveTargets(ts []bgTarget) []bgTarget {
	tab := freshProcs()
	var out []bgTarget
	for _, t := range ts {
		if pi, ok := tab[t.pid]; ok && pi.StartTicks == t.start && pi.State != 'Z' {
			out = append(out, t)
		}
	}
	return out
}

// signalLive signals the targets that are still the same process, deepest first, so the
// shell does not outlive its children and report each of them as killed.
func signalLive(ts []bgTarget, sig syscall.Signal) {
	live := liveTargets(ts)
	for i := len(live) - 1; i >= 0; i-- {
		_ = killProc(live[i].pid, sig)
	}
}
