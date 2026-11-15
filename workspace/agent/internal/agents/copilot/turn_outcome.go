package copilot

// A failed turn as the managed route sees it. copilot answers session/prompt with
// stopReason "end_turn" even when the turn failed (measured on v1.0.112, bad --model: the error
// arrives as an agent message chunk and as a session.error line in events.jsonl, and the prompt
// still succeeds), so the stopReason cannot tell a failed turn from a finished one. events.jsonl
// is the record the transcript already trusts, so the driver reads the failure from it.

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
)

// turnWatch implements acp.Hooks.TurnWatch: it marks events.jsonl when the turn is sent, and the
// returned function reports the turn's failure from what came after the mark.
func turnWatch(sid string) func() string {
	path := EventsPath(sid)
	mark := takeEventsMark(path)
	return func() string { return failureSince(path, mark) }
}

// markSpan is how many bytes at each end of the marked region are fingerprinted.
const markSpan = 256

// eventsMark is where events.jsonl stood when a turn was sent. A bare byte size is not enough: a
// file truncated or replaced during the turn is then read from an offset past its new end (or
// from the middle of unrelated lines) and a new failure reads as a clean idle. So the mark also
// carries the file's identity (os.SameFile) and a fingerprint of its first and last bytes up to
// the mark, which catches a replacement that kept the same header and regrew past the old size.
// The zero value (no file yet, or unreadable) matches no file, so everything is read.
type eventsMark struct {
	fi         os.FileInfo
	size       int64
	head, tail [sha256.Size]byte
}

func takeEventsMark(path string) eventsMark {
	f, err := os.Open(path)
	if err != nil {
		return eventsMark{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return eventsMark{}
	}
	m := eventsMark{fi: fi, size: fi.Size()}
	var ok bool
	if m.head, m.tail, ok = fingerprints(f, m.size); !ok {
		return eventsMark{}
	}
	return m
}

// fingerprints hashes the first and last markSpan bytes of the first size bytes of f.
func fingerprints(f *os.File, size int64) (head, tail [sha256.Size]byte, ok bool) {
	n := size
	if n > markSpan {
		n = markSpan
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil && !(err == io.EOF && n == 0) {
		return head, tail, false
	}
	head = sha256.Sum256(buf)
	if _, err := f.ReadAt(buf, size-n); err != nil && !(err == io.EOF && n == 0) {
		return head, tail, false
	}
	return head, sha256.Sum256(buf), true
}

// offset is where this turn's lines start in f, the handle being read: the mark's size while f
// is still the file the mark was taken from, else 0 (every line in a replaced file is new to
// the turn). Everything is checked on the handle, not on the path, so a swap between the check
// and the read cannot slip through.
func (m eventsMark) offset(f *os.File) int64 {
	if m.fi == nil {
		return 0
	}
	cur, err := f.Stat()
	if err != nil || !os.SameFile(m.fi, cur) || cur.Size() < m.size {
		return 0
	}
	if head, tail, ok := fingerprints(f, m.size); !ok || head != m.head || tail != m.tail {
		return 0
	}
	return m.size
}

// failureSince returns the message of the last session.error appended after mark that no
// assistant output followed ("" = none). The "--model … Using auto instead" notice is not a
// failure, since the turn still runs, and an error followed by assistant.message or
// assistant.turn_end belongs to a turn that went on to answer. The text is the provider's, scrubbed
// of secret-shaped substrings and clipped, because it reaches the operator report. A file that is
// no longer the one the mark was taken from is read from its start: a turn that answered clears
// an older error anyway.
func failureSince(path string, mark eventsMark) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(mark.offset(f), io.SeekStart); err != nil {
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
