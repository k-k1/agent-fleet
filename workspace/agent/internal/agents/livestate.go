package agents

import (
	"bytes"
	"io"
	"os"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// LiveStater is an optional Agent capability: the live-state verdicts the kind's own store or
// runtime gives beyond the status store its hooks write. sessionx's DriveState (the mirror and
// chat chip) and the sessions list's turn-end record read it. A nil func or a false field means
// the kind has no such verdict, which is what every kind without the capability gets. It is
// resolved by exact registry key: no kind with it is reached through the claude fallback.
type LiveStater interface {
	LiveStateTraits() LiveStateTraits
}

// LiveStateTraits is one kind's live-state verdicts. Each field names what breaks without it.
type LiveStateTraits struct {
	// State is the kind's own verdict for a live session, "" for no opinion; a non-empty one is
	// the state, ahead of the status store. Without it a kind whose hooks are missing or
	// unreliable keeps the optimistic "working" /input stored, or never shows its question.
	State func(m session.Meta) string
	// PolledTurnEnd means the kind has no Stop hook, so reading State is the only place a turn's
	// end can be observed: a working→idle transition read there fires the turn-end notification
	// and is recorded by the sessions list. Without it the completion report never arrives.
	PolledTurnEnd bool
	// StaleWorkingIdle means that while State has no opinion, a stored "working" past the
	// warm-up reads idle, since nothing of the kind's ever clears it. Nothing is persisted, so a
	// turn that really ends later still reports.
	StaleWorkingIdle bool
	// RateLimited reports that an idle session's last turn hit a usage limit, which waiting
	// lifts: the session reads StateLimited instead of idle, because re-sending hits the same
	// limit. RateLimitedManagedOnly restricts it to Managed sessions.
	RateLimited            func(name string) bool
	RateLimitedManagedOnly bool
	// TurnMark and EndedAfter are the kind's turn identity: where its state source stands when
	// a turn starts, and whether the newest turn end in it lies after that. Both are nil for a
	// kind whose source exposes no position (kiro's is a pane scrape), which keeps sessionx's
	// settle window as the only guard. Without them a poll right after a new input can read the
	// previous turn's idle as this turn's end.
	//
	// TurnMark returns an opaque token, ok=false when the source cannot be read (not created
	// yet, managed driver, unreadable): the turn then has no identity and the window applies.
	// EndedAfter says whether the newest turn end lies after mark; known=false when the source
	// cannot say (unreadable, rotated or truncated below the mark, a build without the
	// needed table), which sends the caller back to the window, never to "always idle".
	TurnMark   func(m session.Meta) (mark string, ok bool)
	EndedAfter func(m session.Meta, mark string) (after, known bool)
}

// scanLines calls fn with the byte offset and text of each line in at most the last window
// bytes of the file at path, returning the file size it saw and the offset the first reported
// line may follow (the window's base). A read that starts mid-file drops its first, probably
// cut, line. Offsets are absolute and only grow, which is what makes "after the mark taken at
// turn start" a plain compare.
func scanLines(path string, window int64, fn func(off int64, line []byte)) (size, base int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size = fi.Size()
	from := int64(0)
	if size > window {
		from = size - window
	}
	buf := make([]byte, size-from)
	if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
		return size, from, err
	}
	base = from
	if from > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return size, from, nil
		}
		buf, base = buf[i+1:], from+int64(i)+1
	}
	for off := 0; off < len(buf); {
		end := bytes.IndexByte(buf[off:], '\n')
		line := buf[off:]
		next := len(buf)
		if end >= 0 {
			line, next = buf[off:off+end], off+end+1
		}
		fn(base+int64(off), line)
		off = next
	}
	return size, from, nil
}

// FileSizeMark is the turn mark of an append-only file: its size now, "0" while it does not
// exist yet (every line it will ever hold lies after that). ok=false for any other failure.
func FileSizeMark(path string) (string, bool) {
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		return strconv.FormatInt(fi.Size(), 10), true
	case os.IsNotExist(err):
		return "0", true
	}
	return "", false
}

// EndedAfterOffset is EndedAfter for an append-only JSONL file. It needs the PAIR: a line
// accepted by isStart at or after mark (the new input reaching the source) and, after that
// start, a line accepted by isEnd. An end alone proves nothing: the previous turn's late end (a
// shutdown flush) can land after the baseline but before the new input shows up. When the
// window begins past the mark, more than a window of lines were written since the baseline, so
// the window's start stands in for the start line. known=false when the file is unreadable or
// smaller than mark (rotated or truncated: offsets from before are meaningless).
func EndedAfterOffset(path, mark string, window int64, isStart, isEnd func(line []byte) bool) (after, known bool) {
	at, err := strconv.ParseInt(mark, 10, 64)
	if err != nil {
		return false, false
	}
	first, lastEnd := int64(-1), int64(-1)
	size, from, err := scanLines(path, window, func(off int64, line []byte) {
		if first < 0 && off >= at && isStart(line) {
			first = off
		}
		if isEnd(line) {
			lastEnd = off
		}
	})
	if err != nil || size < at {
		return false, false
	}
	if first < 0 && from > at {
		first = from
	}
	return first >= 0 && lastEnd > first, true
}
