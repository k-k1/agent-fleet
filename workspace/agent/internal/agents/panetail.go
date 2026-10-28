package agents

import "strings"

// PaneTail returns the last n non-empty lines of s (the TUI's status/composer footer
// region), so mode detection matches the STATUS LINE — not conversation text that merely
// mentions "plan mode" (which false-positived claude's indicator).
func PaneTail(s string, n int) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	return strings.Join(out, "\n")
}
