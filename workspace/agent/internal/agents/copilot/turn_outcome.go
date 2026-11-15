package copilot

// A failed turn as the managed route sees it. copilot answers session/prompt with
// stopReason "end_turn" even when the turn failed (measured on v1.0.112, bad --model: the error
// arrives as an agent message chunk and as a session.error line in events.jsonl, and the prompt
// still succeeds), so the stopReason cannot tell a failed turn from a finished one. events.jsonl
// is the record the transcript already trusts, so the driver reads the failure from it.

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
)

// turnWatch implements acp.Hooks.TurnWatch: it marks events.jsonl when the turn is sent, and the
// returned function reports the turn's failure from what came after the mark. The mark carries a
// fingerprint of the file's head and not only its size (agents.FileSizeMark): a bare size would
// seek past the end of a file that was truncated or replaced during the turn and read nothing,
// turning a new failure into a clean idle.
func turnWatch(sid string) func() string {
	path := EventsPath(sid)
	mark, _ := agents.FileSizeMark(path) // "" (unreadable) is treated like a replaced file below
	return func() string { return failureSince(path, mark) }
}

// failureSince returns the message of the last session.error appended after mark that no
// assistant output followed ("" = none). The "--model … Using auto instead" notice is not a
// failure, since the turn still runs, and an error followed by assistant.message or
// assistant.turn_end belongs to a turn that went on to answer. The text is the provider's, scrubbed
// of secret-shaped substrings and clipped, because it reaches the operator report. A file that is
// no longer the one the mark was taken from (replaced, or truncated below the mark) is read from
// its start: every line in it is new to this turn, and a turn that answered clears an older error
// anyway.
func failureSince(path, mark string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	off, _ := agents.FileMarkOffset(path, mark) // 0 when the mark no longer fits the file
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	msg := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "session.error":
			if ev.Data.Message != "" && !isModelFallbackNotice(ev.Data.Message) {
				msg = ev.Data.Message
			}
		case "assistant.message", "assistant.turn_end":
			msg = ""
		}
	}
	return strings.TrimSpace(clip(bridge.ScrubSecrets(msg)))
}
