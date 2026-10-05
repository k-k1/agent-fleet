package muse

// commentary.go recovers the text the model writes BEFORE a tool call ("13 unlabelled issues;
// here is the proposal: …"), which the host never puts on the wire.
//
// Measured on 1.4.0 and again on 1.4.2-R4684.1: the host's runtime log records it as
// `assistant_message_committed` with `phase: "commentary"`, but no `item/*` notification and no
// `session/read` item carries it — 83 commentary messages across every local session, 0 of them
// // in AF's store, and neither version's MSP schema has a phase field on Item. Without it a
// question that says "apply the proposal above?" points at nothing.
//
// This reads muse's internal session.jsonl, which transcript.go's header rules out as the
// transcript source because the format carries no stability promise. The exception is kept
// narrow on purpose: two event kinds, read forward from an offset, only to add text beside the
// tool call it introduced. When the format moves, the commentary disappears again and nothing
// else breaks — every failure here is silent and the wire items still render.
//
// It is spliced in when the transcript is READ, never written to AF's store. Placing it by its
// call at render time is what keeps it right across everything that reorders or rebuilds the
// store (a resume backfill, the host's order, a fork), and a read is also what notices a log
// line written after the call's item arrived — the mirror polls, so the proposal appears while
// the question still waits. Nothing here follows the turn's lifecycle.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type commentaryMsg struct {
	id, text string
}

// commentaryLog is what one session.jsonl has said so far, kept between reads so each read
// only decodes what was appended. It holds the commentary of responses that issued tool
// calls (keyed by response, with every call of that response pointing at it), plus, per
// stream, the one response whose tool calls are not logged yet. A response that never logs
// tool calls — it was interrupted, or answered without tools — is dropped as soon as another
// response of its stream speaks, so its text never accumulates. Per stream because subagent
// records are interleaved into the same file under their own stream id.
type commentaryLog struct {
	mu      sync.Mutex
	path    string
	ident   os.FileInfo // the file the offset belongs to; a replaced file is read from 0
	off     int64
	partial []byte
	pending map[string]pendingResp // stream id → response waiting for its tool calls
	byResp  map[string][]commentaryMsg
	byCall  map[string]string // call id → response id, only for responses with commentary
	used    time.Time
}

// commentaryLogCap bounds how many logs stay cached. A read past it costs one re-read of that
// session's log, not a wrong answer.
const commentaryLogCap = 64

type pendingResp struct {
	resp string
	msgs []commentaryMsg
}

var (
	commentaryLogsMu sync.Mutex
	commentaryLogs   = map[string]*commentaryLog{}
)

// commentaryFor returns what path's log holds now, as a set the caller owns. Empty for an
// empty path or an unreadable file.
func commentaryFor(path string) *commentarySet {
	if path == "" {
		return nil
	}
	commentaryLogsMu.Lock()
	l := commentaryLogs[path]
	if l == nil {
		l = &commentaryLog{path: path}
		l.reset()
		commentaryLogs[path] = l
		if len(commentaryLogs) > commentaryLogCap {
			evictOldestLog()
		}
	}
	l.used = time.Now()
	commentaryLogsMu.Unlock()
	return l.snapshot()
}

// evictOldestLog drops the least recently read log. Caller holds commentaryLogsMu.
func evictOldestLog() {
	var oldest string
	var at time.Time
	for p, l := range commentaryLogs {
		if oldest == "" || l.used.Before(at) {
			oldest, at = p, l.used
		}
	}
	delete(commentaryLogs, oldest)
}

func (l *commentaryLog) reset() {
	l.off, l.partial = 0, nil
	l.pending = map[string]pendingResp{}
	l.byResp = map[string][]commentaryMsg{}
	l.byCall = map[string]string{}
}

func (l *commentaryLog) snapshot() *commentarySet {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scanLocked()
	cs := &commentarySet{
		byResp: make(map[string][]commentaryMsg, len(l.byResp)),
		byCall: make(map[string]string, len(l.byCall)),
		done:   map[string]bool{},
	}
	for k, v := range l.byResp {
		cs.byResp[k] = v
	}
	for k, v := range l.byCall {
		cs.byCall[k] = v
	}
	return cs
}

// runtimeEvent is the slice of a session.jsonl record this file reads.
type runtimeEvent struct {
	Stream struct {
		ID string `json:"id"`
	} `json:"stream"`
	Payload struct {
		Event struct {
			Kind       string `json:"kind"`
			MessageID  string `json:"message_id"`
			Phase      string `json:"phase"`
			ResponseID string `json:"response_id"`
			Text       string `json:"text"`
			ToolCalls  []struct {
				CallID string `json:"call_id"`
			} `json:"tool_calls"`
		} `json:"event"`
	} `json:"payload"`
}

var (
	kindMessage   = []byte(`"assistant_message_committed"`)
	kindToolCalls = []byte(`"assistant_tool_calls_committed"`)
)

// scanLocked reads whatever was appended since the last read. A line still being written is
// kept in partial until its newline arrives. A file that is not the one the offset was taken
// on, or that shrank, is read again from the start with nothing carried over.
func (l *commentaryLog) scanLocked() {
	f, err := os.Open(l.path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if l.ident != nil && (!os.SameFile(l.ident, fi) || fi.Size() < l.off) {
		l.reset()
	}
	l.ident = fi
	if _, err := f.Seek(l.off, io.SeekStart); err != nil {
		return
	}
	br := bufio.NewReaderSize(f, 256*1024)
	for {
		chunk, err := br.ReadBytes('\n')
		l.off += int64(len(chunk))
		if err != nil {
			l.partial = append(l.partial, chunk...)
			return
		}
		line := chunk
		if len(l.partial) > 0 {
			line = append(l.partial, chunk...)
			l.partial = nil
		}
		l.consume(line)
	}
}

func (l *commentaryLog) consume(line []byte) {
	if !bytes.Contains(line, kindMessage) && !bytes.Contains(line, kindToolCalls) {
		return // the prefilter: almost every line is neither, and is never decoded
	}
	var ev runtimeEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	e := ev.Payload.Event
	if e.ResponseID == "" {
		return
	}
	stream := ev.Stream.ID
	p := l.pending[stream]
	if p.resp != e.ResponseID {
		// Another response of this stream spoke: the held one will never log tool calls.
		p = pendingResp{resp: e.ResponseID}
	}
	switch e.Kind {
	case "assistant_message_committed":
		if e.Phase == "commentary" && e.MessageID != "" && strings.TrimSpace(e.Text) != "" {
			p.msgs = append(p.msgs, commentaryMsg{id: e.MessageID, text: e.Text})
		}
	case "assistant_tool_calls_committed":
		if len(p.msgs) > 0 {
			l.byResp[e.ResponseID] = p.msgs
			for _, c := range e.ToolCalls {
				if c.CallID != "" {
					l.byCall[c.CallID] = e.ResponseID
				}
			}
		}
		p = pendingResp{}
	}
	if len(p.msgs) > 0 {
		l.pending[stream] = p
	} else {
		delete(l.pending, stream)
	}
}

// commentarySet is one read's view of a log, consumed while one transcript is built.
type commentarySet struct {
	byResp map[string][]commentaryMsg
	byCall map[string]string
	done   map[string]bool
}

// take returns the commentary that introduced callID, once per response: the response's
// other calls, and every later revision of the same call, get none.
func (c *commentarySet) take(callID string) []commentaryMsg {
	if c == nil {
		return nil
	}
	resp, ok := c.byCall[callID]
	if !ok || c.done[resp] {
		return nil
	}
	c.done[resp] = true
	return c.byResp[resp]
}
