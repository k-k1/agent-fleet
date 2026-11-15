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
	"errors"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// turnWatch implements acp.Hooks.TurnWatch: it marks events.jsonl when the turn is sent, and the
// returned function reports the turn's failure from what came after the mark.
func turnWatch(sid string) func() string {
	path := EventsPath(sid)
	mark := takeEventsMark(path)
	return func() string { return failureSince(path, mark) }
}

// openRegular opens path for reading only if it is a regular file. The type is checked on the
// open handle, and O_NONBLOCK keeps the open itself from waiting: a FIFO planted at the path
// would otherwise block until a writer shows up, and TurnWatch runs before session/prompt with
// the lifecycle lease held, so the turn could neither start nor end.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

var errNotRegular = errors.New("events.jsonl is not a regular file")

// eventsMark is where events.jsonl stood when a turn was sent: its size and a hash of every byte
// before that size. events.jsonl is append-only, so "the lines after the mark" is only a valid
// reading while the bytes before it are untouched; a size, a head fingerprint or the file's
// identity each miss some rewrite (a truncation that regrew, a replacement that kept the header,
// an in-place insertion that kept both ends), and a new failure then reads as a clean idle. The
// hash costs one sequential read of the file at each end of a turn, which is small next to the
// turn. The zero value (no file yet, or not readable) matches no file, so everything is read.
type eventsMark struct {
	ok   bool
	size int64
	sum  [sha256.Size]byte
}

func takeEventsMark(path string) eventsMark {
	f, err := openRegular(path)
	if err != nil {
		return eventsMark{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return eventsMark{}
	}
	m := eventsMark{size: fi.Size()}
	if m.sum, m.ok = prefixSum(f, m.size); !m.ok {
		return eventsMark{}
	}
	return m
}

// prefixSum hashes the first size bytes of f; ok=false when f holds fewer.
func prefixSum(f *os.File, size int64) (sum [sha256.Size]byte, ok bool) {
	h := sha256.New()
	if n, err := io.Copy(h, io.NewSectionReader(f, 0, size)); err != nil || n != size {
		return sum, false
	}
	copy(sum[:], h.Sum(nil))
	return sum, true
}

// offset is where this turn's lines start in f, the handle being read: the mark's size while the
// bytes before it are unchanged, else 0 (a rewritten file is read whole; a turn that answered
// clears an older error anyway). It is checked on the handle, not the path, so a swap between
// the check and the read cannot slip through.
func (m eventsMark) offset(f *os.File) int64 {
	if !m.ok {
		return 0
	}
	if sum, ok := prefixSum(f, m.size); !ok || sum != m.sum {
		return 0
	}
	return m.size
}

// failureSince returns the message of the last session.error appended after mark that no
// assistant output followed ("" = none). The "--model … Using auto instead" notice is not a
// failure, since the turn still runs, and an error followed by assistant.message or
// assistant.turn_end belongs to a turn that went on to answer. The text is the provider's, scrubbed
// of secret-shaped substrings and clipped, because it reaches the operator report. A source that
// is not a regular file reads as no failure.
func failureSince(path string, mark eventsMark) string {
	f, err := openRegular(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return failureFrom(f, mark.offset(f), false)
}

// failureFrom is failureSince's read, from byte offset off of the open regular file f. Shared
// with the Terminal route's TurnFailure, which has an offset from agents.FileSizeMark instead
// of an eventsMark.
//
// userResets is the Terminal route's: an old turn's late session.error can land after the mark
// and before this turn's user.message (the same window EndedAfterOffset allows for a late end),
// so a user.message starts the turn and drops what was read before it. The Managed route keeps
// it off: its failed turn writes no user.message at all.
func failureFrom(f *os.File, off int64, userResets bool) string {
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
			if isTurnError(ev.Data.Message) {
				msg = ev.Data.Message
			}
		case "assistant.message", "assistant.turn_end":
			msg = ""
		case "user.message":
			if userResets {
				msg = ""
			}
		}
	}
	return strings.TrimSpace(clip(bridge.ScrubSecrets(msg)))
}

// isTurnError reports whether a session.error message is a turn failure: it has text and is not
// the "--model … Using auto instead" notice (isModelFallbackNotice), after which the turn runs.
// The one place that filter lives; the transcript, the live classifier and both routes' failure
// readers ask it.
func isTurnError(msg string) bool {
	return msg != "" && !isModelFallbackNotice(msg)
}

// TurnFailure is agents.LiveStateTraits.TurnFailure: the Terminal route's counterpart of
// turnWatch, reading the failure from the lines events.jsonl gained after the turn's mark.
func TurnFailure(m session.Meta, mark string) string {
	sid := SessionID(m)
	if sid == "" {
		return ""
	}
	path := EventsPath(sid)
	off, ok := agents.FileMarkOffset(path, mark)
	if !ok {
		return ""
	}
	f, err := openRegular(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return failureFrom(f, off, true)
}
