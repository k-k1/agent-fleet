package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Which claude process ran this hook: the one in the session's own pane, or another claude
// started from inside it.
//
// AF_SESSION_NAME is in the pane's environment, so every descendant inherits it — a probe in
// a private `tmux -L …` server, or `claude -p` from the Bash tool. Those nested processes run
// the same status hooks (they live in the shared CLAUDE_CONFIG_DIR), and without this check
// NormalizeHookSID took their session_id for the self-relaunch drift: the mirror showed the
// nested conversation, the badge followed its permission prompt, and a restart would have
// resumed it (#3355).
//
// The self-relaunch is an exec in the same PID (measured on 2.1.239), so the pane's claude is
// always the direct child of the pane shell, and a hook it runs reaches that child walking up
// without passing through another process of the same command.

type hookOrigin int

const (
	// hookOriginUnknown: the pane or the process chain could not be read. Callers keep the
	// old behaviour, so a tmux or /proc hiccup never costs a real relaunch its recovery.
	hookOriginUnknown hookOrigin = iota
	// hookOriginPane: the hook belongs to the claude running in the session's pane.
	hookOriginPane
	// hookOriginNested: the hook belongs to some other claude that only inherited the name.
	hookOriginNested
)

// hookOriginOf classifies the current hook process for session name. A var so tests need
// neither tmux nor a real process tree.
var hookOriginOf = func(name string) hookOrigin {
	pane, ok := tmuxx.LaunchPanePID(name)
	if !ok {
		return hookOriginUnknown
	}
	return classifyHookChain("/proc", os.Getpid(), pane)
}

// classifyHookChain walks from pid up to pane. The process directly under pane must be the
// claude CLI; another claude between the hook and it means the hook belongs to a nested
// claude, and so does a chain that never reaches pane.
func classifyHookChain(procRoot string, pid, pane int) hookOrigin {
	var chain []int
	for steps := 0; pid != pane; steps++ {
		if pid <= 1 {
			return hookOriginNested
		}
		if steps > 256 {
			return hookOriginUnknown
		}
		chain = append(chain, pid)
		ppid, ok := procParent(procRoot, pid)
		if !ok {
			return hookOriginUnknown
		}
		pid = ppid
	}
	if len(chain) < 2 || !chainIsClaude(procRoot, chain[len(chain)-1]) {
		// Not a hook the pane's claude ran, or the pane runs the CLI through a wrapper:
		// nothing here to compare against.
		return hookOriginUnknown
	}
	for _, p := range chain[:len(chain)-1] {
		if chainIsClaude(procRoot, p) {
			return hookOriginNested
		}
	}
	return hookOriginPane
}

// chainIsClaude recognises the claude CLI the way background-work detection does
// (procIsClaude): comm claude or claude.exe, or a JS runtime running the claude script, so a
// native claude starting an npm one (or the reverse) still counts as nested.
func chainIsClaude(procRoot string, pid int) bool {
	return procIsClaude(pid, procInfo{Comm: procComm(procRoot, pid)})
}

// procParent reads field 4 of /proc/<pid>/stat, counting from the closing parenthesis of the
// command name, which may itself hold spaces and parentheses.
func procParent(procRoot string, pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, err == nil
}

func procComm(procRoot string, pid int) string {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
