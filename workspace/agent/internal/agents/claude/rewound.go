package claude

import "encoding/json"

// RewoundPrompts finds the user prompts claude took back: a prompt stopped before its first
// streamed byte is put back into the input box and the conversation is rewound, yet its user
// line stays in the transcript. The next prompt is then written as that line's sibling, with
// the same parentUuid (measured on claude 2.1.318 with a fake API server, mid-session and right
// after /clear). Shown, the dead line reads as a prompt the model ignored (issue #3372).
//
// The result maps each rewound prompt's line index to the index of the later prompt that
// replaced it. A prompt counts as rewound only when no assistant line descends from it: one
// that got any answer ran, and a /rewind to before it leaves its whole branch on screen as
// before. A prompt still waiting for its sibling (the stop just happened) is not in the map;
// the mirror can tell it apart only once the next prompt is written. A session's very first
// prompt has no parent and is never matched, so a stop of it stays on screen.
func RewoundPrompts(lines [][]byte) map[int]int {
	type node struct {
		uuid, parent string
		idx          int
	}
	// owner maps a line's uuid to the prompt it descends from (through attachments, hook
	// output and the like), so an assistant line marks its prompt answered.
	owner := map[string]string{}
	answered := map[string]bool{}
	var prompts []node
	for i, ln := range lines {
		var ev struct {
			Type             string          `json:"type"`
			UUID             string          `json:"uuid"`
			ParentUUID       string          `json:"parentUuid"`
			IsMeta           bool            `json:"isMeta"`
			IsSidechain      bool            `json:"isSidechain"`
			IsCompactSummary bool            `json:"isCompactSummary"`
			Message          json.RawMessage `json:"message"`
		}
		if json.Unmarshal(ln, &ev) != nil || ev.UUID == "" || ev.IsSidechain {
			continue
		}
		if ev.Type == "user" && !ev.IsMeta && !ev.IsCompactSummary && isPromptMessage(ev.Message) {
			owner[ev.UUID] = ev.UUID
			prompts = append(prompts, node{uuid: ev.UUID, parent: ev.ParentUUID, idx: i})
			continue
		}
		o, ok := owner[ev.ParentUUID]
		if !ok {
			continue
		}
		owner[ev.UUID] = o
		if ev.Type == "assistant" {
			answered[o] = true
		}
	}
	out := map[int]int{}
	// latest holds, per parent, the newest prompt seen so far under it.
	latest := map[string]node{}
	for _, p := range prompts {
		// A null parent is no evidence: every session's first prompt has one, and so does
		// every line of a transcript written without the links. Matching on it would hide
		// answered prompts whose answers cannot be traced back to them.
		if p.parent == "" {
			continue
		}
		if prev, ok := latest[p.parent]; ok && !answered[prev.uuid] {
			out[prev.idx] = p.idx
		}
		latest[p.parent] = p
	}
	return out
}

// isPromptMessage reports a user message that carries text someone sent, not only tool
// results (claude writes each tool result as a user line of its own).
func isPromptMessage(raw json.RawMessage) bool {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil || len(m.Content) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s != ""
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "image" {
			return true
		}
	}
	return false
}

// RewoundSince reports that a prompt the client already holds (a line before since) was found
// rewound by a prompt at or after since: the client's copy must be replaced, not appended to.
func RewoundSince(rewound map[int]int, since int) bool {
	for u, v := range rewound {
		if u < since && v >= since {
			return true
		}
	}
	return false
}
