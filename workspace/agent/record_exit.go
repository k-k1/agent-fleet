package main

// Records WHY an agent session's process terminated (normal quit / crash / OOM kill).
//
// Sessions run as `tmux new-session -d <program>`, so the Agent never gets a
// cmd.Wait() on them — the pane's shell is the only place the exit status is visible.
// startSessionTmux therefore appends `; __af_ec=$?; workspace-agent record-exit <name>
// "$__af_ec"` after the agent CLI, and this subcommand runs in that shell once the CLI
// exits. $? is the CLI's wait status: 128+N on a signal kill, so an OOM SIGKILL(9)
// arrives here as 137. A deliberate `tmux kill-session` tears the wrapping shell down
// too, so record-exit never runs for an intentional stop — we therefore can't mislabel
// a stop as a crash (verified on tmux 3.3a: kill-session leaves no record; an inner
// SIGKILL records 137).

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// cgroupDir is the container's own cgroup v2 root. Overridable for tests.
func cgroupDir() string {
	if v := os.Getenv("AF_CGROUP_DIR"); v != "" {
		return v
	}
	return "/sys/fs/cgroup"
}

// containerOOMKill reads the cumulative oom_kill counter from the container's own
// cgroup v2 memory.events. From inside the container /sys/fs/cgroup is cgroup-namespaced
// to this container, so this is our own count. Reports !ok when unreadable (a non
// cgroup-v2 host, a different layout, etc.) so callers degrade instead of guessing OOM.
func containerOOMKill() (uint64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/memory.events")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "oom_kill" {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// runRecordExit is `workspace-agent record-exit <name> <code>`. It reads the launch
// baseline (for OOM attribution) and writes the interpreted ExitInfo the sessions list
// surfaces. Best-effort and silent: it runs in a dying shell with nowhere to report to.
func runRecordExit(args []string) {
	if len(args) < 2 {
		return
	}
	name := args[0]
	if !session.ValidName(name) {
		return
	}
	code, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil {
		return
	}
	// Baseline oom_kill captured at launch, so an OOM is attributed only when the
	// counter advanced DURING this session — not a stale count from an earlier death.
	base, _ := status.ReadExit(name)
	oomNow, okOOM := containerOOMKill()
	oomDuringSession := okOOM && oomNow > base.OOMBase

	sig := 0
	if code >= 128 {
		sig = code - 128
	}
	status.PersistExit(name, status.ExitInfo{
		Reason: exitReason(code, sig, oomDuringSession),
		Code:   code, Signal: sig,
		At:      time.Now().Format(time.RFC3339),
		OOMBase: base.OOMBase,
	})
}

// exitReason interprets a pane wait status into a cause the Console can show:
//   - 0                       → exited  (normal quit)
//   - SIGKILL(9) + OOM        → oom     (memory cgroup / host OOM killer)
//   - SIGKILL(9) no OOM       → killed  (a SIGKILL from something other than an OOM)
//   - SIGHUP/INT/TERM(1/2/15) → stopped (graceful signals: quit, shutdown, a kill leak)
//   - other signal            → crashed (SIGSEGV/ABRT/… = an application fault)
//   - other non-zero (<128)   → crashed (the CLI itself exited non-zero)
func exitReason(code, sig int, oom bool) string {
	if code == 0 {
		return "exited"
	}
	if sig == 9 {
		if oom {
			return "oom"
		}
		return "killed"
	}
	switch sig {
	case 1, 2, 15: // SIGHUP, SIGINT, SIGTERM
		return "stopped"
	}
	return "crashed"
}
