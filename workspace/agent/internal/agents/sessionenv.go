package agents

import (
	"log"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// SessionNameEnvVar is the variable a session-side af MCP server reads to learn which
// session it serves (mcpx.mcpOwningSession). A TERMINAL session gets it from its tmux launch
// env; a MANAGED kind has to hand it down through whatever per-session channel its host has.
const SessionNameEnvVar = "AF_SESSION_NAME"

// WithSessionName returns env with SessionNameEnvVar set to name, for a vendor process that
// serves exactly one session (the ACP kinds and muse spawn one child per session). The
// Agent's own environment normally has no such variable, but any value it does carry belongs
// to some other session, so it is replaced rather than appended after: a child that sees the
// variable twice takes whichever its runtime happens to read.
func WithSessionName(env []string, name string) []string {
	out := make([]string, 0, len(env)+1)
	prefix := SessionNameEnvVar + "="
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	if name == "" {
		return out
	}
	return append(out, prefix+name)
}

// WorkDirEnvVar is the variable that names a session's throwaway directory. A Terminal
// session gets it from its tmux launch env; a MANAGED kind gets it through the same
// per-session channel as the name (WithSessionEnv, or the thread config for codex).
const WorkDirEnvVar = "AF_WORK_DIR"

// SessionWorkDir creates the session's throwaway directory (~/.af-work/<name>) and returns
// it, "" when it cannot be had. The name is the session's immutable slug (a rename changes
// only the title), and paths.SessionWorkDir refuses anything that could leave ~/.af-work. A
// failure never blocks a launch: the variable is simply not set.
func SessionWorkDir(name string) string {
	wd, err := paths.MakeSessionWorkDir(name)
	if err != nil {
		log.Printf("session work dir for %q: %v", name, err)
		return ""
	}
	return wd
}

// WithSessionEnv is WithSessionName plus AF_WORK_DIR, for a vendor process that serves
// exactly one session. An inherited AF_WORK_DIR belongs to some other session (or to a
// Terminal pane the Agent was started from), so it is dropped even when this session's
// directory cannot be created.
func WithSessionEnv(env []string, name string) []string {
	env = WithSessionName(env, name)
	prefix := WorkDirEnvVar + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	if wd := SessionWorkDir(name); wd != "" {
		out = append(out, prefix+wd)
	}
	return out
}
