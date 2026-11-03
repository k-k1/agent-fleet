package claude

import (
	"encoding/json"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// promptRecord is the subset of a transcript line LastUserPrompt needs.
type promptRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// LastUserPrompt is the text of the newest prompt the session's conversation received, exactly
// as claude logged it, or "" when there is none. A tool result, a subagent's line and claude's
// own meta lines are not prompts.
//
// It exists for the chat's stop (issue #3290): a prompt interrupted before its first streamed
// byte is put back into claude's input box, and the copy the pane shows is wrapped at the pane
// width and may be scrolled, so the transcript is the only exact copy to hand back.
func LastUserPrompt(m session.Meta) string {
	for _, p := range jsonlByMtime(session.UUID(m.Dir, m.Name)) {
		if text, found := lastUserPromptIn(p); found {
			return text
		}
	}
	return ""
}

// lastUserPromptIn is LastUserPrompt for one transcript file.
func lastUserPromptIn(p string) (string, bool) {
	var text string
	_, found := lastLineWhere(p, func(l []byte) bool {
		t, ok := userPromptText(l)
		text = t
		return ok
	})
	return text, found
}

// userPromptText returns the prompt a transcript line carries; ok is false for every other line.
func userPromptText(line []byte) (string, bool) {
	// A compaction summary and a background-task notification are user lines claude writes
	// itself; one logged after the member's prompt must not stand in for it.
	if claudeOwnLine(line) {
		return "", false
	}
	var r promptRecord
	if json.Unmarshal(line, &r) != nil || r.Type != "user" || r.IsSidechain || r.IsMeta {
		return "", false
	}
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		return s, strings.TrimSpace(s) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			return "", false
		case "text":
			parts = append(parts, b.Text)
		}
	}
	s = strings.Join(parts, "\n")
	return s, strings.TrimSpace(s) != ""
}
