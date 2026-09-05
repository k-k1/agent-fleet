package cursor

// Classifying live state (working / idle) from the tail of the JSONL transcript. cursor's
// TUI/-p route writes a row for the user prompt, streams the response and tool rows, and
// finally records turn_ended (measured), so "is a turn still open" decides it (the same shape
// as the copilot events.jsonl classification, and independent of TUI strings, which matches
// the false-idle lesson). The managed (ACP) route writes no transcript, so its state comes
// from the driver's runTurn boundaries (Track A2).
//
// Waiting for permission (the TUI's confirmation for a command outside the allowlist) leaves
// no trace in the JSONL, so v1 reports no "question" — the turn is still open and is treated
// as "working" (the mirror shows in-progress plus a stop button). Permission cards are
// Track D (docs/log/40).

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tailWindow bounds how much of the JSONL the poll reads. 128KB covers several turns, which
// is enough: any older open marker is necessarily closed by a turn_ended within the window.
const tailWindow = 128 * 1024

// LiveState classifies the session's live state ("" when unknowable — no chat id allocated
// yet, or no transcript file yet, i.e. right after launch).
func LiveState(m session.Meta) string {
	// managed (ACP) writes no transcript, so the JSONL classification below always comes back
	// empty. Without feeding it from the turn state machine, neither the list chip nor the
	// reaper's classification is set (driver.go managedLiveState).
	if m.DriverKind() == session.DriverManaged {
		return managedLiveState(m)
	}
	chatID := ChatID(m)
	if chatID == "" {
		return ""
	}
	path := transcriptPath(m.Dir, chatID)
	return liveStateFromFile(path)
}

func liveStateFromFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "" // not created yet — unknown (the caller treats it as no state)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailWindow {
		if _, err := f.Seek(st.Size()-tailWindow, io.SeekStart); err == nil {
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n') // discard the partial line the mid-file start produced
			return classify(br)
		}
	}
	return classify(bufio.NewReader(f))
}

func classify(r io.Reader) string {
	open := false // after a role row, before turn_ended
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ev struct {
			Role string `json:"role"`
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "turn_ended":
			open = false
		case ev.Role == "user" || ev.Role == "assistant":
			open = true
		}
	}
	if open {
		return "working"
	}
	return "idle"
}
