package agents

import "strings"

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
