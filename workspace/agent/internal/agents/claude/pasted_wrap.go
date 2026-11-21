package claude

import (
	"encoding/json"
	"strings"
)

const (
	pastedOpenHead  = `<pasted_content id="`
	pastedCloseHead = `</pasted_content id="`
)

// unwrapPasted returns the text claude was pasted, without the wrapper claude (2.1.328 on) puts
// around a bracketed paste before it logs the prompt:
//
//	\n\n<pasted_content id="c959">\n<the paste>\n</pasted_content id="c959">\n
//
// Every multi-line prompt the Agent types reaches claude as a paste (PasteNeeded), member prompts
// and peer, notice and spawn envelopes alike, so without this every reader of the logged text sees
// the wrapper: the Console renders the closing tag as text (an end tag with attributes is not
// HTML), the pending echo never matches, and envelope and origin matching anchored at the start
// miss.
//
// Only a text that is exactly one pair with the same id on both tags is unwrapped; anything else is
// returned unchanged, so a prompt that merely mentions the tag keeps it. Measured on 236 wrapped
// lines across 2.1.328-2.1.330: every one was a single pair with nothing outside it and the paste
// between one newline on each side.
func unwrapPasted(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, pastedOpenHead) {
		return s
	}
	rest := t[len(pastedOpenHead):]
	end := strings.Index(rest, `">`)
	if end <= 0 || !pastedID(rest[:end]) {
		return s
	}
	closeTag := pastedCloseHead + rest[:end] + `">`
	inner, ok := strings.CutSuffix(rest[end+2:], closeTag)
	if !ok || strings.Contains(inner, closeTag) {
		return s
	}
	inner = strings.TrimPrefix(inner, "\n")
	return strings.TrimSuffix(inner, "\n")
}

// promptText is contentText for a logged user prompt, without claude's paste wrapper. Only prompt
// readers use it: an assistant answer or a tool result that happens to be one wrapped pair is the
// agent's own text and is shown as it is, and claudeOwnLine keeps reading the raw text so a pasted
// prompt that starts with <task-notification> is still the member's.
func promptText(raw json.RawMessage) string {
	return strings.TrimSpace(unwrapPasted(contentText(raw)))
}

// pastedID reports whether id is the kind of id claude puts on the wrapper: letters and digits
// only, so a quote or a tag inside the attribute never reads as one.
func pastedID(id string) bool {
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return id != ""
}
