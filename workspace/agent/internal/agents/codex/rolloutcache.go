package codex

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"sync"
	"time"
)

// Incremental reading of a codex rollout.
//
// A rollout is append-only, so the bytes a poll already parsed can never change. Reading
// and parsing the whole file on every poll was therefore pure waste — and not a small
// one: an image-generating session's rollout reached 147 MB in an hour (the inline base64
// of each tool result is megabytes), where one parse measured 3.9 s of CPU to produce a
// 94 KB answer. /messages polls that at 1.2-3 s, the sessions list adds one per stale
// working state, and the Console re-opens a session from scratch on every tab switch — so
// a long codex conversation left the Agent permanently behind, and re-showing it took
// seconds (user report, 2026-09-05).
//
// So the parse is kept between reads and only the newly appended lines are folded in. The
// cost of a poll becomes a stat plus whatever codex wrote since the last one.
//
// Two rules make that safe:
//   - Only COMPLETE lines are consumed. codex may be mid-append, and a half-written line
//     must keep its index for when the rest of it lands (the old code re-read the file, so
//     it simply dropped the unparsable line and picked it up next time).
//   - A file that shrank, or whose head changed, is not the file we were reading (a
//     replacement landed on the same path). The parse is thrown away and started over. The
//     old code could not get this wrong because it re-read everything every time; the head
//     check is what buys the same guarantee back.

// rolloutCacheIdle is how long an untouched parse is kept. Long enough that reading a
// session, going away and coming back is free; short enough that yesterday's conversations
// do not sit in the Agent's heap.
const rolloutCacheIdle = 30 * time.Minute

// rolloutHeadLen is how much of the file's start identifies it. codex opens a rollout with
// its session_meta line, which carries the creation timestamp and the session id, so a
// replacement differs well inside this.
const rolloutHeadLen = 256

type rolloutEntry struct {
	mu   sync.Mutex
	p    *rolloutParser
	off  int64     // bytes folded into p; always ends on a line boundary
	head []byte    // the file's first bytes when the parse started — its identity
	used time.Time // last read, for the idle sweep
}

var rolloutCache sync.Map // rollout path -> *rolloutEntry

// withRollout brings path's parse up to date and calls fn with it. fn runs under the
// entry's lock (so concurrent readers of one session queue instead of each parsing) and
// must not retain the parser or hand out its turns — use snapshot.
//
// slot is the session's sid, for the view_image bytes: codex records those screenshots as
// inline base64 and nowhere else, so the parse stashes them until they are written out.
// Doing that here, right after folding, is what keeps megabytes of base64 from sitting in
// a cached parse until somebody happens to open the chat.
//
// It reports false only when there is nothing to read at all: the file is gone and no
// earlier parse of it survives. A transient read error on a file we already know is served
// from what we have, which is what the Console wants — a poll that fails must not blank a
// conversation.
func withRollout(path, slot string, fn func(*rolloutParser)) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	// used is stamped at creation, not at first read: the sweep below runs before this
	// entry is locked, and a zero timestamp reads as "idle since the epoch" — it would
	// delete the entry we are about to fill, on every call, and nothing would ever cache.
	v, loaded := rolloutCache.LoadOrStore(path, &rolloutEntry{used: time.Now()})
	if !loaded {
		// Sweeping on a miss keeps it off the hot path: a session being polled hits its
		// own entry, and only a new one pays for the walk.
		sweepRolloutCache(time.Now())
	}
	e := v.(*rolloutEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if fi.Size() < e.off {
		e.p = nil // truncated: whatever this is, it is not what we were reading
	}
	if e.p == nil || fi.Size() > e.off {
		if err := e.fold(path); err != nil {
			if e.p == nil {
				rolloutCache.Delete(path) // nothing was ever folded — don't cache the failure
				return false
			}
			// A transient failure over a parse we already hold: serve what we have.
		} else if slot != "" {
			persistViewImages(e.p.turns, slot)
		}
	}
	e.used = time.Now()
	fn(e.p)
	return true
}

// fold brings the entry up to date from path, in one open: the head is read first as the
// file's identity, then the complete lines after e.off are fed in. A trailing partial line
// is left where it is — the next fold starts at the offset it did not pass and sees the
// line whole.
func (e *rolloutEntry) fold(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, rolloutHeadLen)
	n, err := f.ReadAt(head, 0)
	if n == 0 && err != nil && err != io.EOF {
		return err
	}
	head = head[:n]
	if e.p == nil || !sameHead(e.head, head) {
		e.p, e.off, e.head = newRolloutParser(), 0, head
	} else if len(head) > len(e.head) {
		e.head = head // the file was shorter than the window and has grown into it
	}
	if _, err := f.Seek(e.off, io.SeekStart); err != nil {
		return err
	}
	// Read line by line rather than slurping the file: one rollout line can be megabytes
	// (an inline image) and a whole file hundreds, and the Agent shares a memory-capped
	// container with every other session.
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		ln, err := r.ReadBytes('\n')
		if err != nil {
			return nil // io.EOF with a partial line (or none) — leave it unread
		}
		e.off += int64(len(ln))
		if ln = bytes.TrimSpace(ln); len(ln) > 0 {
			e.p.feed(ln)
		}
	}
}

// sameHead reports whether two reads of a file's first bytes can be the same file. One
// side may be shorter simply because the file was: a rollout under rolloutHeadLen bytes
// grows into the window as codex writes, and that must not read as a replacement.
func sameHead(a, b []byte) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	return bytes.HasPrefix(b, a)
}

// sweepRolloutCache drops parses nothing has read for rolloutCacheIdle. An entry a reader
// currently holds is skipped rather than waited for — it is by definition in use.
func sweepRolloutCache(now time.Time) {
	rolloutCache.Range(func(k, v any) bool {
		e := v.(*rolloutEntry)
		if !e.mu.TryLock() {
			return true
		}
		idle := now.Sub(e.used) > rolloutCacheIdle
		e.mu.Unlock()
		if idle {
			rolloutCache.Delete(k)
		}
		return true
	})
}
