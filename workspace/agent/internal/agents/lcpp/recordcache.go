package lcpp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Incremental reading of a session's record log.
//
// Every Console poll of an lcpp session reads the store twice (agentImpl.Transcript and
// WireLive's LastUsage), each through a fresh Open. Re-decoding the whole log each time cost
// ~290 ms and ~90 MB of allocation per poll on a 7 MB log (BenchmarkMirrorPoll), for a file
// whose already-read bytes can never change: append only ever adds whole lines at the end.
// So the decoded records, and the transcript built from them, are kept per path between reads,
// and a read decodes and converts only the lines written since the last one. codex's
// rolloutcache.go is the same shape for the same reason. The price is that a session read in
// the last recordCacheIdle keeps its records and turns in the Agent's heap.
//
// The rules that keep it equivalent to decoding the whole file every time:
//   - Only complete lines are cached. A trailing line without its newline is decoded afresh on
//     each read and never cached, so a tear that later becomes a whole line (or never does) is
//     judged exactly as a full re-read would judge it.
//   - A complete line that fails to decode is not consumed. While it is the last line it
//     reads as truncated; once anything follows it, it is mid-stream corruption and errors.
//   - A file that shrank, is another inode, or whose first bytes changed is not the file that
//     was cached (the session was deleted and recreated on the same path): the cache starts
//     over. The inode check is what lets an unchanged size skip the read entirely.
//
// A rewrite of the same inode that keeps the size and the head goes unseen.
// Nothing in this package writes that way; append-only is the store's own contract.

// recordCacheIdle is how long an unread session's records stay in memory: long enough that
// switching away from a session and back is free, short enough that yesterday's sessions do
// not sit in the Agent's heap.
const recordCacheIdle = 30 * time.Minute

// recordHeadLen is how much of the file's start identifies it. The first line opens with its
// record id, a nanosecond timestamp, so a recreated log differs well inside this.
const recordHeadLen = 256

type recordEntry struct {
	mu   sync.Mutex
	recs []Record    // every complete line before off, decoded
	off  int64       // bytes consumed; always ends on a line boundary
	head []byte      // the file's first bytes when recs[0] was read — its identity
	file os.FileInfo // the stat the cached records were read under
	used time.Time   // last read, for the idle sweep
	// tb holds the turns of recs[:built]; readTranscript feeds it the rest.
	tb    transcriptBuilder
	built int
	// decoded counts lines ever decoded into recs, so a test can tell an incremental read
	// from a full one.
	decoded int
	// converted counts records ever fed to tb, for the same reason.
	converted int
}

var recordCache sync.Map // log path -> *recordEntry

// readRecords brings path's cached records up to date and returns them with Records'
// contract (a missing file is nil, false, nil; a decode error returns the records before the
// bad line with it). The slice is a capped view of the cache, safe to read after the lock is
// released because the cache only ever appends past it or swaps in a new slice — but it must
// not be written to. Records hands out a copy for callers outside this package's readers.
func readRecords(path string) (view []Record, truncated bool, err error) {
	e, size, err := lockEntry(path)
	if e == nil {
		return nil, false, err
	}
	defer e.mu.Unlock()
	tail, truncated, err := e.refresh(path, size)
	if err != nil {
		return e.view(), false, err
	}
	if tail != nil {
		return append(e.view(), *tail), false, nil
	}
	return e.view(), truncated, nil
}

// readTranscript is TranscriptFor over the cache: the entry's builder is fed only the records
// it has not seen yet.
func readTranscript(path, sessionModel string) ([]transcript.Turn, error) {
	e, size, err := lockEntry(path)
	if e == nil {
		return nil, err
	}
	defer e.mu.Unlock()
	tail, _, err := e.refresh(path, size)
	if err != nil {
		return nil, err
	}
	if tail != nil {
		// Rare (a read landed mid-write) and never cached, so a one-off full build rather
		// than a builder that would have to take the record back out.
		return transcriptFromRecords(append(e.view(), *tail), sessionModel), nil
	}
	for ; e.built < len(e.recs); e.built++ {
		e.tb.add(e.recs[e.built])
		e.converted++
	}
	return e.tb.result(sessionModel), nil
}

// lockEntry returns path's entry locked, already reset if the file is not the one it
// cached, with the file's size; or nil with Records' error for a missing or unreadable file.
func lockEntry(path string) (*recordEntry, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			recordCache.Delete(path)
			return nil, 0, nil
		}
		return nil, 0, err
	}
	// used is stamped at creation: the sweep runs before this entry is locked, and a zero
	// timestamp reads as idle since the epoch.
	v, loaded := recordCache.LoadOrStore(path, &recordEntry{used: time.Now()})
	if !loaded {
		sweepRecordCache(time.Now()) // on a miss only, off the hot path of a polled session
	}
	e := v.(*recordEntry)
	e.mu.Lock()
	e.used = time.Now()
	if fi.Size() < e.off || (e.file != nil && !os.SameFile(e.file, fi)) {
		e.reset(nil)
	}
	e.file = fi
	return e, fi.Size(), nil
}

// refresh folds whatever was appended since the last read; with nothing new it is free.
func (e *recordEntry) refresh(path string, size int64) (tail *Record, truncated bool, err error) {
	if size == e.off && e.off > 0 {
		return nil, false, nil
	}
	return e.fold(path)
}

// view is recs capped at its length, so a caller's append can never write into the cache.
func (e *recordEntry) view() []Record { return e.recs[:len(e.recs):len(e.recs)] }

func (e *recordEntry) reset(head []byte) {
	e.recs, e.off, e.head = nil, 0, head
	e.tb, e.built = transcriptBuilder{}, 0
}

// fold decodes the complete lines after e.off into e.recs. tail is a final line with no
// newline that decoded anyway; truncated reports a last line (complete or not) that did not.
func (e *recordEntry) fold(path string) (tail *Record, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	head := make([]byte, recordHeadLen)
	n, err := f.ReadAt(head, 0)
	if n == 0 && err != nil && err != io.EOF {
		return nil, false, err
	}
	head = head[:n]
	if !sameHead(e.head, head) {
		e.reset(head)
	} else if len(head) > len(e.head) {
		e.head = head // the file was shorter than the window and has grown into it
	}
	if _, err := f.Seek(e.off, io.SeekStart); err != nil {
		return nil, false, err
	}
	// Line by line rather than slurping: one record can be megabytes (a large tool result),
	// and the Agent shares a memory-capped container with every session.
	r := bufio.NewReaderSize(f, 64<<10)
	var bad error // a complete line that failed to decode, pending what follows it
	for {
		ln, rerr := readLine(r)
		if rerr == bufio.ErrTooLong {
			return nil, false, fmt.Errorf("lcpp: %s: line %d: %w", path, len(e.recs), rerr)
		}
		complete := rerr == nil
		body := bytes.TrimSpace(ln)
		if len(body) > 0 {
			if bad != nil {
				return nil, false, bad // something follows the undecodable line
			}
			var rec Record
			if derr := json.Unmarshal(body, &rec); derr != nil {
				bad = fmt.Errorf("lcpp: %s: line %d: %w", path, len(e.recs), derr)
			} else if !complete {
				return &rec, false, nil
			} else {
				e.recs = append(e.recs, rec)
				e.decoded++
			}
		}
		if complete && bad == nil {
			e.off += int64(len(ln))
		}
		if rerr != nil {
			if rerr != io.EOF {
				return nil, false, rerr
			}
			return nil, bad != nil, nil
		}
	}
}

// readLine is bufio.Reader.ReadBytes('\n') that gives up past maxRecordLine instead of
// buffering a runaway line whole.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxRecordLine {
			return nil, bufio.ErrTooLong
		}
		line = append(line, chunk...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

// sameHead reports whether two reads of a file's first bytes can be the same file. One side
// may be shorter simply because the file was, and a log grows into the window as it is
// written; that must not read as a replacement.
func sameHead(a, b []byte) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	return bytes.HasPrefix(b, a)
}

// sweepRecordCache drops sessions nothing has read for recordCacheIdle. An entry a reader
// currently holds is skipped rather than waited for: it is by definition in use.
func sweepRecordCache(now time.Time) {
	recordCache.Range(func(k, v any) bool {
		e := v.(*recordEntry)
		if !e.mu.TryLock() {
			return true
		}
		idle := now.Sub(e.used) > recordCacheIdle
		e.mu.Unlock()
		if idle {
			recordCache.Delete(k)
		}
		return true
	})
}
