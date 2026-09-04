package session

import "strings"

// ShellQuote safely wraps s in single quotes, for assembling the launch command handed to
// tmux. Both the per-CLI packages (internal/agents/opencode and friends) and main use it.
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
