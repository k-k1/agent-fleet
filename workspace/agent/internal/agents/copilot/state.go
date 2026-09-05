package copilot

// Live state classification (working / question / idle) from the tail of events.jsonl. copilot
// has no status hook, so this is the only state source on the TUI route - the counterpart of
// agy's conversation-DB probe, and like it independent of TUI strings, which is what the
// false-idle lesson asks for. It stays consistent on the managed route too, because the child
// process writes the same file.
//
// The classification (based on the event order measured on v1.0.73):
//   - a permission.requested with no matching permission.completed -> "question" (waiting on
//     the permission menu or a plan-mode approval)
//   - a user.message / assistant.turn_start with no assistant.turn_end after it -> "working"
//     (a turn in progress; the gap before turn_start while routing is caught from user.message)
//   - anything else -> "idle"
//   - no session id yet, or no file -> "" (starting up / unknown - the caller treats it as no
//     state)

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tailWindow bounds how much of events.jsonl the poll reads. 128KB is a few turns' worth
// (measured: 3-60KB per turn); an open marker older than that has either been closed by a
// later turn or was displayed long ago.
const tailWindow = 128 * 1024

// LiveState classifies the session's live state ("" when unknowable).
func LiveState(m session.Meta) string {
	sid := SessionID(m)
	if sid == "" {
		return ""
	}
	return liveStateFromFile(EventsPath(sid))
}

func liveStateFromFile(path string) string {
	st, _ := liveStateDetailFromFile(path)
	return st
}

// PendingPermission returns the subject of an outstanding permission request ("" = not waiting
// on one, or it could not be read). Read by the docs/log/75 P5 carry-over so it can say what
// was being asked.
func PendingPermission(m session.Meta) (string, bool) {
	sid := SessionID(m)
	if sid == "" {
		return "", false
	}
	st, detail := liveStateDetailFromFile(EventsPath(sid))
	return detail, st == "question"
}

func liveStateDetailFromFile(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailWindow {
		if _, err := f.Seek(st.Size()-tailWindow, io.SeekStart); err == nil {
			// Reading from the middle, so the first line is probably truncated - drop it.
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n')
			return classifyDetail(br)
		}
	}
	return classifyDetail(bufio.NewReader(f))
}

func classify(r io.Reader) string {
	st, _ := classifyDetail(r)
	return st
}

// classifyDetail is classify plus what an outstanding permission was asking for (for the
// docs/log/75 P5 carry-over). detail is filled only while waiting on a permission, and stays
// empty when it cannot be read.
func classifyDetail(r io.Reader) (string, string) {
	open := false                 // after user.message / turn_start, before turn_end
	perms := map[string]bool{}    // requestIds requested and not yet completed
	detail := map[string]string{} // requestId -> what it asked for (only where readable)
	last := ""                    // the last requested id (this is the one displayed)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				RequestID string `json:"requestId"`
				// The subject of the permission. The events.jsonl schema moves between
				// versions, so this is used only when it happens to be there: deciding
				// that a permission is pending needs nothing but the requestId. When it
				// is empty the carry-over card states the fact alone.
				ToolName string `json:"toolName"`
				Tool     string `json:"tool"`
				Command  string `json:"command"`
				Title    string `json:"title"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "user.message", "assistant.turn_start":
			open = true
		case "assistant.turn_end":
			open = false
		case "permission.requested":
			if ev.Data.RequestID != "" {
				perms[ev.Data.RequestID] = true
				last = ev.Data.RequestID
				if d := firstNonEmpty(ev.Data.Title, ev.Data.Command, ev.Data.ToolName, ev.Data.Tool); d != "" {
					detail[ev.Data.RequestID] = d
				}
			}
		case "permission.completed":
			delete(perms, ev.Data.RequestID)
		case "session.shutdown":
			// A graceful shutdown was recorded, so nothing runs after this: any turn or
			// permission still open died with the process.
			open = false
			perms = map[string]bool{}
		}
	}
	if len(perms) > 0 {
		if perms[last] {
			return "question", detail[last]
		}
		for id := range perms {
			return "question", detail[id]
		}
		return "question", ""
	}
	if open {
		return "working", ""
	}
	return "idle", ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
