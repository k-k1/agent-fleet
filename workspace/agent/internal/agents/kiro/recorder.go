package kiro

// Approval state from the TUI's ACP recorder (KIRO_ACP_RECORD_PATH), measured in
// docs/log/132 §1. The recorder appends one JSONL line per ACP message the TUI exchanges
// with its agent; `session/request_permission` (dir "in") opens an approval and the TUI's
// JSON-RPC response with the same string id (dir "out") closes it. It is an undocumented
// flag and was measured on 2.31.0 and the 2.32.0 pin (docs/log/132 §4), so it can only ADD a pending
// approval to the pane scrape in state.go; it never hides one the scrape sees. Anything the
// parser does not recognise is ignored and left to the scrape.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

const (
	// The file holds every ACP message (~120 KB before the first prompt). Past this size the
	// recorder is no longer trusted for the rest of that launch and the scrape decides alone:
	// the TUI keeps the file open, so truncating it under the writer is not safe.
	maxRecorderBytes = 64 << 20
	// One read step; lines are assembled across steps.
	recorderReadChunk = 1 << 20
	maxRecorderLine   = 8 << 20
	maxPendingIDs     = 64
	maxDetailRunes    = 120
)

// recorderPath is the per-session recorder file, private to the agent's state dir.
func recorderPath(name string) string {
	return filepath.Join(paths.AgentStateDir(), "kiro-acp", name+".jsonl")
}

// prepareRecorder gives a launch a fresh, empty recorder file's directory. A relaunch
// (resume) must not replay the previous run's lines.
func prepareRecorder(name string) string {
	p := recorderPath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return ""
	}
	_ = os.Remove(p)
	return p
}

type recorderLine struct {
	Dir string `json:"dir"`
	Msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ToolCall struct {
				Title    string `json:"title"`
				RawInput struct {
					Command string `json:"command"`
				} `json:"rawInput"`
			} `json:"toolCall"`
			Options []json.RawMessage `json:"options"`
		} `json:"params"`
		Result map[string]json.RawMessage `json:"result"`
	} `json:"msg"`
}

// recorderState replays the file incrementally by byte offset.
type recorderState struct {
	mu      sync.Mutex
	file    os.FileInfo
	offset  int64             // bytes consumed from the file
	partial []byte            // unterminated tail of the last read, at most maxRecorderLine
	skip    bool              // inside a line longer than maxRecorderLine: drop bytes up to its newline
	pending map[string]string // request id -> one-line detail
	over    bool              // file exceeded maxRecorderBytes
}

var (
	recordersMu sync.Mutex
	recorders   = map[string]*recorderState{}
)

// forgetRecorder removes a deleted session's recorder file and its cache entry.
func forgetRecorder(name string) {
	p := recorderPath(name)
	_ = os.Remove(p)
	recordersMu.Lock()
	delete(recorders, p)
	recordersMu.Unlock()
}

// ForgetRecorder is forgetRecorder for callers outside the package (session deletion).
func ForgetRecorder(name string) { forgetRecorder(name) }

// recorderPending returns the details of approvals the recorder shows as open. Nothing is
// returned unless the poll caught up with the file's size at the time of the poll: a
// half-read history would publish requests that a later line already answered. Empty also
// when the file is missing, unreadable, or too large.
func recorderPending(path string) []string {
	recordersMu.Lock()
	st := recorders[path]
	if st == nil {
		st = &recorderState{pending: map[string]string{}}
		recorders[path] = st
	}
	recordersMu.Unlock()
	return st.poll(path)
}

func (st *recorderState) poll(path string) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	fi, err := os.Stat(path)
	if err != nil {
		st.reset(nil)
		return nil
	}
	if st.file == nil || !os.SameFile(st.file, fi) || fi.Size() < st.offset {
		st.reset(fi)
	}
	if fi.Size() > maxRecorderBytes {
		st.over = true
	}
	if st.over {
		st.pending = map[string]string{}
		return nil
	}
	if fi.Size() > st.offset && !st.read(path, fi.Size()) {
		// Unreadable: restart from the top on recovery rather than keep a stale view.
		st.reset(nil)
		return nil
	}
	out := make([]string, 0, len(st.pending))
	for _, d := range st.pending {
		out = append(out, d)
	}
	return out
}

func (st *recorderState) reset(fi os.FileInfo) {
	st.file, st.offset, st.over = fi, 0, false
	st.partial, st.skip = nil, false
	st.pending = map[string]string{}
}

// read consumes the file up to size in bounded chunks and reports whether it got there.
func (st *recorderState) read(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, recorderReadChunk)
	for st.offset < size {
		n := size - st.offset
		if n > recorderReadChunk {
			n = recorderReadChunk
		}
		got, err := f.ReadAt(buf[:n], st.offset)
		if int64(got) < n && err != nil {
			return false
		}
		st.offset += int64(got)
		st.feed(buf[:got])
	}
	return true
}

// feed splits a chunk into lines. A line is assembled across chunks up to maxRecorderLine;
// a longer one is skipped to its newline (a huge tool output is never a request).
func (st *recorderState) feed(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !st.skip {
				if len(st.partial)+len(b) > maxRecorderLine {
					st.skip, st.partial = true, nil
				} else {
					st.partial = append(st.partial, b...)
				}
			}
			return
		}
		if st.skip {
			st.skip = false
		} else if len(st.partial)+i > maxRecorderLine {
			// too long: dropped
		} else if len(st.partial) > 0 {
			st.apply(append(st.partial, b[:i]...))
		} else {
			st.apply(b[:i])
		}
		st.partial = nil
		b = b[i+1:]
	}
}

func (st *recorderState) apply(raw []byte) {
	var l recorderLine
	if json.Unmarshal(raw, &l) != nil {
		return
	}
	id := jsonString(l.Msg.ID)
	switch {
	case l.Dir == "in" && l.Msg.Method == "session/request_permission":
		// Only the measured shape counts: a string id and at least one offered option.
		// Otherwise the request is unrecognised and the pane scrape decides.
		if id == "" || len(l.Msg.Params.Options) == 0 || len(st.pending) >= maxPendingIDs {
			return
		}
		st.pending[id] = requestDetail(l)
	case l.Dir == "out" && l.Msg.Method == "session/cancel":
		st.pending = map[string]string{}
	case l.Dir == "out" && l.Msg.Method == "" && id != "" && l.Msg.Result != nil:
		delete(st.pending, id)
	case l.Dir == "in" && l.Msg.Method == "" && l.Msg.Result != nil:
		// A prompt result ends the turn; nothing can still be waiting inside it.
		if _, ok := l.Msg.Result["stopReason"]; ok {
			st.pending = map[string]string{}
		}
	}
}

func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func requestDetail(l recorderLine) string {
	d := strings.TrimSpace(l.Msg.Params.ToolCall.Title)
	if d == "" {
		d = strings.TrimSpace(l.Msg.Params.ToolCall.RawInput.Command)
	}
	d = strings.Join(strings.Fields(d), " ")
	if utf8.RuneCountInString(d) > maxDetailRunes {
		d = string([]rune(d)[:maxDetailRunes]) + "…"
	}
	if d == "" {
		return "requires approval"
	}
	return d
}
