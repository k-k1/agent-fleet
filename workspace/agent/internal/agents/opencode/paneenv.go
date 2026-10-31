package opencode

// The environment of a Terminal pane's TUI, read back from the process. A server restart loses
// every session's environment (it is held in memory), and the Agent must put it back from
// something: the pane's own process is the one place that has it exactly — the tmux -e values
// plus the toolchain exports the pane's shell added — without keeping a copy of provider keys
// on disk.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// serverOnlyEnv are the variables that belong to the TUI's connection to the server and must not
// reach a shell.
func serverOnlyEnv(name string) bool {
	return name == "OPENCODE_SERVER_PASSWORD" || name == "OPENCODE_SERVER_USERNAME"
}

// shellEnvironment turns KEY=VALUE entries into the map PUT /environment takes, minus the
// server credentials.
func shellEnvironment(environ []string) map[string]string {
	vars := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || serverOnlyEnv(k) {
			continue
		}
		vars[k] = v
	}
	return vars
}

// paneEnv is the environment slot name's TUI runs with, as the shell environment to PUT. A var
// so tests need neither tmux nor /proc.
var paneEnv = func(name string) (map[string]string, bool) {
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return nil, false
	}
	out, err := tmuxx.Cmd("display-message", "-p", "-t", pane, "#{pane_pid}").Output()
	if err != nil {
		return nil, false
	}
	root, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || root <= 0 {
		return nil, false
	}
	return envOfPaneTree("/proc", root, name)
}

// envOfPaneTree walks the processes under root (breadth first, so the pane shell comes before the
// TUI it started) and returns the environment of the deepest one that is this slot's attached
// TUI: it carries AF_SESSION_NAME=name and the server password. The shell itself qualifies too
// (tmux -e puts the same values in its environment) and is the fallback, though it lacks the
// toolchain exports.
func envOfPaneTree(procRoot string, root int, name string) (map[string]string, bool) {
	children := map[int][]int{}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, false
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if ppid, ok := parentPID(procRoot, pid); ok {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var best []string
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if env := processEnviron(procRoot, pid); isAttachedEnv(env, name) {
			best = env
		}
		queue = append(queue, children[pid]...)
	}
	if best == nil {
		return nil, false
	}
	return shellEnvironment(best), true
}

func isAttachedEnv(environ []string, name string) bool {
	var named, pw bool
	for _, kv := range environ {
		switch {
		case kv == "AF_SESSION_NAME="+name:
			named = true
		case strings.HasPrefix(kv, "OPENCODE_SERVER_PASSWORD="):
			pw = true
		}
	}
	return named && pw
}

func processEnviron(procRoot string, pid int) []string {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil
	}
	var out []string
	for _, kv := range strings.Split(string(b), "\x00") {
		if kv != "" {
			out = append(out, kv)
		}
	}
	return out
}

// parentPID reads field 4 of /proc/<pid>/stat; the command name (field 2) may contain spaces and
// parentheses, so the fields are counted from its closing parenthesis.
func parentPID(procRoot string, pid int) (int, bool) {
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
