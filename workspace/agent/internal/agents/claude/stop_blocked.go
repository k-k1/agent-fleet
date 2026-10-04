package claude

import (
	"encoding/json"
	"strings"
	"time"
)

// StopContinued reports whether the Stop that wrote the session's end-of-turn marker (at
// marker) was blocked by another Stop hook, so the turn is in fact still running (#1600).
//
// claude runs every Stop hook of an event in parallel, so ours writes the idle marker before
// it can know that a sibling answered `decision:"block"`; claude then feeds the reason back
// and the turn goes on until a later Stop (measured 2.1.288: our hook started 2 ms before the
// blocking one and got stop_hook_active=false, exactly like an unblocked stop). The marker
// alone cannot tell the two apart, but the transcript can: claude closes every Stop with a
// system/stop_hook_summary record, and a blocked one is preceded by an attachment
// hook_blocking_error (hookEvent Stop) plus an isMeta "Stop hook feedback:" user line.
//
// It answers true only when all of these hold, and false whenever it cannot tell — a false
// true holds a finished turn's report until the next prompt, so every doubt falls to the
// pre-#1600 behaviour:
//   - the newest stop_hook_summary in the tail window belongs to the marker's Stop (the
//     marker is not later than it; a later idle came from somewhere else, such as a heal)
//   - that Stop was blocked
//   - no prompt-like user record came after it: an Esc writes "[Request interrupted by
//     user…]" and fires no Stop, so it is the only end the continued turn can have that
//     leaves no newer summary behind (an API error is the caller's TailAborted)
//
// Only the tail window is read (tailLines), never the whole file: this runs on the report
// reconciler's tick for every settle candidate.
func StopContinued(sid string, marker time.Time) bool {
	if sid == "" || marker.IsZero() {
		return false
	}
	for _, p := range jsonlByMtime(sid) {
		lines, _ := tailLines(p)
		if continued, found := stopContinuedFrom(lines, marker); found {
			return continued
		}
	}
	return false
}

// stopRecord is the part of a transcript line the Stop verdict reads.
type stopRecord struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Timestamp   string          `json:"timestamp"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
	Attachment  struct {
		Type      string `json:"type"`
		HookEvent string `json:"hookEvent"`
	} `json:"attachment"`
}

func (r stopRecord) summary() bool { return r.Type == "system" && r.Subtype == "stop_hook_summary" }

// blocking reports whether r is the record a blocked Stop leaves before its summary. The
// isMeta feedback line is accepted as well as the attachment so the verdict survives one of
// the two being renamed between releases.
func (r stopRecord) blocking() bool {
	if r.Type == "attachment" {
		return r.Attachment.Type == "hook_blocking_error" && r.Attachment.HookEvent == "Stop"
	}
	return r.Type == "user" && r.IsMeta && strings.HasPrefix(userText(r.Message), "Stop hook feedback:")
}

// prompt reports whether r is a user record the user (not a tool, not claude itself) put
// there: a new prompt or an interruption.
func (r stopRecord) prompt() bool {
	return r.Type == "user" && !r.IsMeta && userText(r.Message) != ""
}

// userText returns the text of a user message: the string content, or its text blocks.
// tool_result blocks are not text, so a tool's output reads as "".
func userText(raw json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// stopMarkerGrace absorbs the marker's RFC3339 second truncation: the marker is written
// while the Stop's hooks run, the summary once they have all finished.
const stopMarkerGrace = 2 * time.Second

// stopContinuedFrom is StopContinued over one transcript's lines. found says whether the
// lines decided it (a stop_hook_summary, or a prompt after the last one); when not, the
// caller tries a sibling log.
func stopContinuedFrom(lines [][]byte, marker time.Time) (continued, found bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		var r stopRecord
		if json.Unmarshal(lines[i], &r) != nil || r.IsSidechain {
			continue
		}
		if r.prompt() {
			return false, true
		}
		if !r.summary() {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil || marker.After(at.Add(stopMarkerGrace)) {
			return false, true
		}
		for j := i - 1; j >= 0; j-- {
			var b stopRecord
			if json.Unmarshal(lines[j], &b) != nil || b.IsSidechain {
				continue
			}
			if b.blocking() {
				return true, true
			}
			if b.Type == "assistant" || b.prompt() || b.summary() {
				return false, true
			}
		}
		return false, true
	}
	return false, false
}
