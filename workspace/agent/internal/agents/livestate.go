package agents

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
	// InputSeen is the delivery half of the same identity: whether the new input's own user
	// line / step has reached the source at or after mark, known=false when the source cannot
	// say (same cases as EndedAfter). A turn whose input never shows up is the one a stored
	// "working" would otherwise hold until the next input (sessionx.reconcileInputDelivery).
	// Set together with TurnMark and EndedAfter, and with InputWindow.
	InputSeen func(m session.Meta, mark string) (seen, known bool)
	// InputWindow is how long after the working write the user line may still be missing
	// before the input counts as not delivered. It only bounds a transient: an input that lands
	// later is picked up again by InputSeen, so too short costs a brief idle, never a lost turn.
	InputWindow time.Duration
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

// markPrefixLen bounds the prefix fingerprinted into a file mark.
const markPrefixLen = 256

// FileSizeMark is the turn mark of an append-only file: "size:n:fp", its size now plus a
// fingerprint of its first n (at most markPrefixLen) bytes, "0" while it does not exist yet
// (every line it will ever hold lies after that). The fingerprint is what tells the same file
// grown from a replacement (a resumed CLI, a rewritten file) that has regrown past the size: an
// offset compare alone would read the new file's lines before the mark as "not there" and call a
// running turn's input undelivered. ok=false for any other failure.
func FileSizeMark(path string) (string, bool) {
	fi, err := os.Stat(path)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		return "0", true
	default:
		return "", false
	}
	size := fi.Size()
	n := size
	if n > markPrefixLen {
		n = markPrefixLen
	}
	fp, ok := prefixFingerprint(path, n)
	if !ok {
		return "", false
	}
	return strconv.FormatInt(size, 10) + ":" + strconv.FormatInt(n, 10) + ":" + fp, true
}

func prefixFingerprint(path string, n int64) (string, bool) {
	if n == 0 {
		return "", true
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return "", false
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:8]), true
}

// parseFileMark returns the offset of a FileSizeMark and whether path still holds the file the
// mark was taken from (its prefix is unchanged). A plain integer is a mark without a fingerprint
// and is trusted as before. ok=false for an unparseable mark or a different file.
func parseFileMark(path, mark string) (at int64, ok bool) {
	parts := strings.Split(mark, ":")
	at, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || (len(parts) != 1 && len(parts) != 3) {
		return 0, false
	}
	if len(parts) == 3 {
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n < 0 || n > at {
			return 0, false
		}
		if fp, ok := prefixFingerprint(path, n); !ok || fp != parts[2] {
			return 0, false
		}
	}
	return at, true
}

// firstLineFrom streams the file from byte offset at and returns the offset of the first line
// accepted by match. A mark that falls inside a line (the CLI was mid-write) skips that line's
// remainder: it began before the mark.
func firstLineFrom(path string, at int64, match func(line []byte) bool) (off int64, found bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	off = at
	if at > 0 {
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], at-1); err != nil {
			return 0, false, err
		}
		if prev[0] != '\n' {
			// Mid-line: skip to the next line start.
			br := bufio.NewReader(io.NewSectionReader(f, at, 1<<62))
			rest, err := br.ReadBytes('\n')
			if err != nil {
				return 0, false, nil
			}
			off += int64(len(rest))
		}
	}
	br := bufio.NewReader(io.NewSectionReader(f, off, 1<<62))
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && match(bytes.TrimRight(line, "\n")) {
			return off, true, nil
		}
		off += int64(len(line))
		if err != nil {
			return 0, false, nil
		}
	}
}

// InputSeenOffset is InputSeen for an append-only JSONL file: a line accepted by isStart at or
// after mark. known=false when the file is unreadable or smaller than mark (rotated, truncated).
// A file that does not exist yet is a known "not seen" only for mark 0.
func InputSeenOffset(path, mark string, isStart func(line []byte) bool) (seen, known bool) {
	at, ok := parseFileMark(path, mark)
	if !ok {
		return false, false
	}
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		if fi.Size() < at {
			return false, false
		}
	case os.IsNotExist(err) && at == 0:
		return false, true
	default:
		return false, false
	}
	_, found, err := firstLineFrom(path, at, isStart)
	if err != nil {
		return false, false
	}
	return found, true
}

// EndedAfterOffset is EndedAfter for an append-only JSONL file. It needs the PAIR: a line
// accepted by isStart at or after mark (the new input reaching the source) and, after that
// start, a line accepted by isEnd. An end alone proves nothing: the previous turn's late end (a
// shutdown flush, or a previous run's huge tail followed by its end) can land after the
// baseline but before the new input shows up, and growth of the file is no evidence of input.
// The start is therefore searched from the mark forward, not in the tail window, so a turn
// that wrote more than a window still has its real start found, and a source without one
// denies the end. known=false when the file is unreadable or smaller than mark (rotated or
// truncated: offsets from before are meaningless).
func EndedAfterOffset(path, mark string, window int64, isStart, isEnd func(line []byte) bool) (after, known bool) {
	at, ok := parseFileMark(path, mark)
	if !ok {
		return false, false
	}
	lastEnd := int64(-1)
	size, _, err := scanLines(path, window, func(off int64, line []byte) {
		if isEnd(line) {
			lastEnd = off
		}
	})
	if err != nil || size < at {
		return false, false
	}
	if lastEnd < at {
		return false, true
	}
	first, found, err := firstLineFrom(path, at, isStart)
	if err != nil {
		return false, false
	}
	return found && lastEnd > first, true
}
