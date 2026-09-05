// Package mdblock owns the marker-delimited regions agent-fleet writes into
// markdown files that belong to an agent CLI (docs/log/60 §60.7).
//
// One implementation because the same strip/append logic had been copied into codex/rtk.go
// and agy/rtk.go, and user instructions (docs/log/60) became a third writer. When the
// spelling of the markers, or the way a block is removed, drifts between files, one version
// lingers forever or the removal takes the user's own text with it.
//
// The contract:
//   - A block is bounded by <!-- agent-fleet:<name> --> … <!-- /agent-fleet:<name> -->.
//   - Set strips the existing block and appends at the end, so when several blocks are
//     written to one file the call order becomes their order in the file.
//   - Never touch anything outside the markers. Text the user wrote into the same file
//     survives; this replaces the behaviour where AF `cp -f`'d the whole file away on every
//     start (docs/log/60 damage 1).
package mdblock

import "strings"

// Markers returns the start/end marker pair for a named agent-fleet block.
func Markers(name string) (start, end string) {
	return "<!-- agent-fleet:" + name + " -->", "<!-- /agent-fleet:" + name + " -->"
}

// Has reports whether the named block is present.
func Has(s, name string) bool {
	start, _ := Markers(name)
	return strings.Contains(s, start)
}

// Strip removes the named block and rejoins the remainder. A missing end marker
// (hand-mangled file) drops everything from the start marker onward — the block is
// unbounded, so keeping the tail would keep an arbitrary amount of AF's old text.
func Strip(s, name string) string {
	start, end := Markers(name)
	i := strings.Index(s, start)
	if i < 0 {
		return s
	}
	rest := s[i+len(start):]
	k := strings.Index(rest, end)
	if k < 0 {
		return strings.TrimRight(s[:i], "\n") + "\n"
	}
	head := strings.TrimRight(s[:i], "\n")
	tail := strings.TrimLeft(rest[k+len(end):], "\n")
	switch {
	case head == "":
		return tail
	case tail == "":
		return head + "\n"
	default:
		return head + "\n\n" + tail
	}
}

// Get returns the body of the named block (without the markers, trimmed of the
// blank lines that Set adds), and whether it was present.
func Get(s, name string) (string, bool) {
	start, end := Markers(name)
	i := strings.Index(s, start)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(start):]
	k := strings.Index(rest, end)
	if k < 0 {
		return strings.Trim(rest, "\n"), true
	}
	return strings.Trim(rest[:k], "\n"), true
}

// StripLegacyPrefix removes a leading *unmarked* copy of text agent-fleet used to
// write before it started marking its regions — the era when the entrypoint `cp -f`'d
// the whole workspace guide over $CODEX_HOME/AGENTS.md and opencode's AGENTS.md
// (docs/log/60 damage 1). Without this, the first run of the marker-based writer would treat
// 30 KB of stale guide as "the user's own text", preserve it, and append a second copy.
//
// The match is made on legacy's FIRST LINE only. A byte comparison cannot carry the
// migration, because the body differs between image versions; anything weaker than the
// first line (a substring match, say) risks deleting text the user wrote. Stop at the first
// marker found, so blocks AF manages are kept.
func StripLegacyPrefix(s, legacy string) string {
	head := firstNonEmptyLine(legacy)
	if head == "" || !strings.HasPrefix(strings.TrimLeft(s, " \t\n"), head) {
		return s
	}
	if i := strings.Index(s, "<!-- agent-fleet:"); i >= 0 {
		return strings.TrimLeft(s[i:], "\n")
	}
	return ""
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Set replaces (or removes, when body is empty) the named block, appending it at
// the end of the document. Everything outside agent-fleet's markers is preserved.
func Set(s, name, body string) string {
	out := Strip(s, name)
	if strings.TrimSpace(body) == "" {
		return out
	}
	start, end := Markers(name)
	block := start + "\n" + strings.Trim(body, "\n") + "\n" + end + "\n"
	if strings.TrimSpace(out) == "" {
		return block
	}
	return strings.TrimRight(out, "\n") + "\n\n" + block
}
