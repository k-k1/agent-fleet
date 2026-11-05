package tmuxx

import (
	"os"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// LaunchPanePID returns the pid of the process in the pane session name was launched in.
//
// Not the active pane: a user who splits the session's window and selects the new pane would
// otherwise move the answer to a shell the launch never started. The launch pane is the one
// whose start command carries session.ExitRecorder, searched across every window (-s).
//
// TMUX is dropped from the command's environment: inside a nested tmux it names that other
// server, and plain tmux follows it (measured: a bogus TMUX makes `tmux list-sessions` fail),
// so the lookup would miss the session.
func LaunchPanePID(name string) (int, bool) {
	cmd := Cmd("list-panes", "-s", "-t", session.ExactTarget(session.TmuxName(name)), "-F", "#{pane_pid} #{pane_start_command}")
	cmd.Env = withoutEnv(os.Environ(), "TMUX", "TMUX_PANE")
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	return LaunchPaneIn(string(out), name)
}

// LaunchPaneIn picks the launch pane's pid out of list-panes output. Exactly one match or no
// answer: a second pane started with the same command is not something to guess between.
func LaunchPaneIn(out, name string) (int, bool) {
	marker := session.ExitRecorder(name)
	pid := 0
	for _, ln := range strings.Split(out, "\n") {
		f, cmdline, ok := strings.Cut(ln, " ")
		if !ok || !strings.Contains(cmdline, marker) {
			continue
		}
		p, err := strconv.Atoi(f)
		if err != nil || p <= 1 {
			continue
		}
		if pid != 0 {
			return 0, false
		}
		pid = p
	}
	return pid, pid > 1
}

// withoutEnv returns environ without the named variables.
func withoutEnv(environ []string, names ...string) []string {
	out := make([]string, 0, len(environ))
next:
	for _, kv := range environ {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}
