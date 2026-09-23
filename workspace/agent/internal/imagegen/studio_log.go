package imagegen

// The studio's edit log, <id>.log.jsonl (ADR 0100 decision 9): append-only, one event per line —
// edits, rewinds, presses and press results. It is kept out of the studio's own file because
// the file is rewritten on every debounced keystroke, and 500 entries of whole drafts would make
// every one of those writes half a megabyte.
//
// Two rules make a torn append harmless. The writer, when an append fails, cuts the file back
// to the last newline before anything else is written, so a retry never glues itself onto a
// fragment; and every reader skips a line it cannot parse rather than failing the whole log.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
)

// studioLogState is what the log's writer needs to know without reading the whole file on every
// write: the next Seq and the next version number. Loaded from the file on first use and kept
// under the studio's lock.
type studioLogState struct {
	loaded   bool
	seq      int
	versions int
}

var studioLogStates sync.Map // id -> *studioLogState

// logStateLocked is the writer's state for id. The caller holds the studio's lock.
func logStateLocked(id string) *studioLogState {
	v, _ := studioLogStates.LoadOrStore(id, &studioLogState{})
	st := v.(*studioLogState)
	if !st.loaded {
		for _, e := range readStudioLogRaw(id) {
			st.seq = max(st.seq, e.Seq)
			if e.Kind == DraftLogPress {
				st.versions++
			}
		}
		st.loaded = true
	}
	return st
}

func forgetStudioLogState(id string) { studioLogStates.Delete(id) }

// studioLogWrite is the write itself, a var so a test can make it fail half-way.
var studioLogWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b) }

// appendStudioLog numbers entries and appends them as lines. The caller holds the studio's lock.
// On failure the file is cut back to where it was and the Seq is not consumed.
func appendStudioLog(id string, entries ...*DraftLogEntry) error {
	st := logStateLocked(id)
	var buf bytes.Buffer
	seq := st.seq
	for _, e := range entries {
		seq++
		e.Seq = seq
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := appendLines(studioLogPath(id), buf.Bytes()); err != nil {
		for _, e := range entries {
			e.Seq = 0
		}
		return err
	}
	st.seq = seq
	return nil
}

// appendLines appends whole lines to path. Before writing it cuts any unterminated tail left by
// an earlier failure, and after a failed write it cuts its own partial line — both back to the
// last newline — so the next line always starts a line.
func appendLines(path string, lines []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	end, err := lastNewlineEnd(f)
	if err != nil {
		return err
	}
	if err := f.Truncate(end); err != nil {
		return err
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return err
	}
	if _, err := studioLogWrite(f, lines); err != nil {
		_ = f.Truncate(end)
		return err
	}
	return nil
}

// lastNewlineEnd is the offset just past the file's last '\n', 0 when it has none.
func lastNewlineEnd(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	const chunk = 4096
	buf := make([]byte, chunk)
	for off := size; off > 0; {
		n := int64(chunk)
		if off < n {
			n = off
		}
		off -= n
		if _, err := f.ReadAt(buf[:n], off); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return off + int64(i) + 1, nil
		}
	}
	return 0, nil
}

// readStudioLogRaw is every parseable line, in file order.
func readStudioLogRaw(id string) []DraftLogEntry {
	f, err := os.Open(studioLogPath(id))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []DraftLogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var e DraftLogEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Seq <= 0 || e.Kind == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// readStudioLog is the log as every reader sees it: parseable lines only, and for each version
// only the FIRST press_result — a retry after an unknown failure and the start-up recovery can
// both write another, and the first is the one that happened.
func readStudioLog(id string) []DraftLogEntry {
	raw := readStudioLogRaw(id)
	out := raw[:0]
	seen := map[string]bool{}
	for _, e := range raw {
		if e.Kind == DraftLogPressResult {
			if seen[e.Version] {
				continue
			}
			seen[e.Version] = true
		}
		out = append(out, e)
	}
	return out
}

// draftLogPage is the entries older than before (all when before <= 0), the newest limit of
// them, oldest first.
func draftLogPage(entries []DraftLogEntry, before, limit int) DraftLogPage {
	end := len(entries)
	if before > 0 {
		end = 0
		for i, e := range entries {
			if e.Seq < before {
				end = i + 1
			}
		}
	}
	start := max(end-limit, 0)
	page := DraftLogPage{Entries: append([]DraftLogEntry{}, entries[start:end]...)}
	if start > 0 {
		page.Before = entries[start].Seq
	}
	return page
}
