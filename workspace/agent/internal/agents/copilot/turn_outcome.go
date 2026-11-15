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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
)

// turnWatch implements acp.Hooks.TurnWatch: it notes where events.jsonl ends when the turn is
// sent, and the returned function reports the turn's failure from what was appended after.
func turnWatch(sid string) func() string {
	path := EventsPath(sid)
	var off int64 // the file may not exist yet: a new session's first turn creates it
	if fi, err := os.Stat(path); err == nil {
		off = fi.Size()
	}
	return func() string { return failureSince(path, off) }
}

// failureSince returns the message of the last session.error appended after off that no
// assistant output followed ("" = none). The "--model … Using auto instead" notice is not a
// failure, since the turn still runs, and an error followed by assistant.message or
// assistant.turn_end belongs to a turn that went on to answer. The text is the provider's, scrubbed
// of secret-shaped substrings and clipped, because it reaches the operator report.
func failureSince(path string, off int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
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
