package main

import "regexp"

// terminalReplyRe matches ONE complete terminal-generated reply: the bytes xterm.js
// writes to onData by itself when the PTY (or a replayed history frame) asks it a
// question. No human produced them, so they are not presence.
//
// Every alternative is anchored on a shape no key press produces. When a shape could
// also be a key, the pattern is narrowed so the key still counts: F3 with a modifier
// is `ESC [ 1 ; <2..16> R`, byte-identical to a cursor-position report for row 1, so
// that row-1 case is left out (a CPR at row 1 col 2..16 then counts, which is the safe
// direction).
//
//	focus in/out        ESC [ I   ESC [ O
//	DA1 / DA2           ESC [ ? … c   ESC [ > … c
//	CPR / DECXCPR       ESC [ row ; col R   ESC [ ? row ; col [; page] R
//	DSR status          ESC [ Ps n   ESC [ ? Ps [; …] n
//	DECRPM              ESC [ [?] Ps ; Pm $ y
//	window-size report  ESC [ Ps ; … t
//	kitty kbd query     ESC [ ? flags u
//	OSC colour reply    ESC ] Ps [; idx] ; rgb:… (BEL | ESC \)
//	XTVERSION / DECRQSS ESC P > | text ST   ESC P [01] $ r text ST
var terminalReplyRe = regexp.MustCompile(
	`\A(?:` +
		`\x1b\[[IO]` +
		`|\x1b\[[?>][0-9;]*c` +
		`|\x1b\[(?:[2-9][0-9]*|1[0-9]+);[0-9]+R` + // row >= 2 (see above)
		`|\x1b\[1;(?:1|1[7-9]|[2-9][0-9]|[0-9]{3,})R` + // row 1, but not col 2..16 (F3 + modifiers)
		`|\x1b\[\?[0-9]+;[0-9]+(?:;[0-9]+)?R` +
		`|\x1b\[\??[0-9]+(?:;[0-9]+)*n` +
		`|\x1b\[\??[0-9]+;[0-9]+\$y` +
		`|\x1b\[[0-9]+(?:;[0-9]+)*t` +
		`|\x1b\[\?[0-9]+u` +
		`|\x1b\][0-9]+(?:;[0-9]+)?;rgb:[0-9a-fA-F/]+(?:\x07|\x1b\\)` +
		`|\x1bP(?:>\|[\x20-\x7e]*|[01]\$r[\x20-\x7e]*)\x1b\\` +
		`)`)

// isAutomaticTerminalReply reports whether an input payload consists ENTIRELY of
// recognised terminal reply sequences (one or more, back to back). Anything else — a
// printable byte, Enter, Ctrl-C, an arrow key, a lone ESC, a paste, a reply followed
// by a real key — is not, and counts as presence. When in doubt answer false: a
// misclassified keystroke would let the reaper halt a session someone is typing in.
// An empty payload is false for the same reason.
func isAutomaticTerminalReply(data string) bool {
	if data == "" {
		return false
	}
	for data != "" {
		loc := terminalReplyRe.FindStringIndex(data)
		if loc == nil {
			return false
		}
		data = data[loc[1]:]
	}
	return true
}
