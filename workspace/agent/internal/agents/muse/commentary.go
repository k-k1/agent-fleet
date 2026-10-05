package muse

// commentary.go recovers the text the model writes BEFORE a tool call ("13 unlabelled issues;
// here is the proposal: …"), which the host never puts on the wire.
//
// Measured on 1.4.0 and again on 1.4.2-R4684.1: the host's runtime log records it as
// `assistant_message_committed` with `phase: "commentary"`, but no `item/*` notification and no
// `session/read` item carries it — 83 commentary messages across every local session, 0 of them
// in AF's store, and neither version's MSP schema has a phase field on Item. Without it a question that says "apply the proposal
// above?" points at nothing.
//
// This reads muse's internal session.jsonl, which transcript.go's header rules out as the
// transcript source because the format carries no stability promise. The exception is kept
// narrow on purpose: two event kinds, read forward from an offset, only to add text beside the
// tool call it introduced. When the format moves, the commentary disappears again and nothing
// else breaks — every failure here is silent and the wire items still render.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

type commentaryMsg struct {
	id, text, at string
}

// commentaryReader tails one session.jsonl. Only commentary text is held, keyed by the
// response that carries the tool calls, and it is dropped once taken. A resumed handle reads
// from the start, so commentary of calls that went by before the resume stays held — the
// session's commentary text at most, which is what it costs to catch a turn in flight.
type commentaryReader struct {
	mu      sync.Mutex
	path    string
	off     int64
	partial []byte
	byResp  map[string][]commentaryMsg // response id → commentary not yet attached
	byCall  map[string]string          // call id → response id, only for responses in byResp
}

func newCommentaryReader(path string) *commentaryReader {
	return &commentaryReader{path: path, byResp: map[string][]commentaryMsg{}, byCall: map[string]string{}}
}

// runtimeEvent is the slice of a session.jsonl record this file reads.
type runtimeEvent struct {
	RecordedAt int64 `json:"recorded_at"` // microseconds since the epoch
	Payload    struct {
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
	kindCommentary = []byte(`"assistant_message_committed"`)
	kindToolCalls  = []byte(`"assistant_tool_calls_committed"`)
)

// take returns the commentary that introduced callID, once: a second call for the same id
// (the tool call's later revisions) returns nothing.
func (r *commentaryReader) take(callID string) []commentaryMsg {
	if r == nil || callID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanLocked()
	resp, ok := r.byCall[callID]
	if !ok {
		return nil
	}
	out := r.byResp[resp]
	delete(r.byResp, resp)
	for c, rid := range r.byCall {
		if rid == resp {
			delete(r.byCall, c)
		}
	}
	return out
}

// scanLocked reads whatever was appended since the last call. A line still being written is
// kept in partial until its newline arrives; a file that shrank (replaced) is read again from
// the start.
func (r *commentaryReader) scanLocked() {
	f, err := os.Open(r.path)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < r.off {
		r.off, r.partial = 0, nil
	}
	if _, err := f.Seek(r.off, io.SeekStart); err != nil {
		return
	}
	br := bufio.NewReaderSize(f, 256*1024)
	for {
		chunk, err := br.ReadBytes('\n')
		r.off += int64(len(chunk))
		if err != nil {
			r.partial = append(r.partial, chunk...)
			return
		}
		line := chunk
		if len(r.partial) > 0 {
			line = append(r.partial, chunk...)
			r.partial = nil
		}
		r.consume(line)
	}
}

func (r *commentaryReader) consume(line []byte) {
	isComm := bytes.Contains(line, kindCommentary)
	if !isComm && !bytes.Contains(line, kindToolCalls) {
		return // the prefilter: almost every line is neither, and is never decoded
	}
	var ev runtimeEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	e := ev.Payload.Event
	switch {
	case e.Kind == "assistant_message_committed" && e.Phase == "commentary":
		if e.ResponseID == "" || e.MessageID == "" || strings.TrimSpace(e.Text) == "" {
			return
		}
		at := ""
		if ev.RecordedAt > 0 {
			at = time.UnixMicro(ev.RecordedAt).UTC().Format(time.RFC3339Nano)
		}
		r.byResp[e.ResponseID] = append(r.byResp[e.ResponseID], commentaryMsg{id: e.MessageID, text: e.Text, at: at})
	case e.Kind == "assistant_tool_calls_committed":
		if _, ok := r.byResp[e.ResponseID]; !ok {
			return
		}
		for _, c := range e.ToolCalls {
			if c.CallID != "" {
				r.byCall[c.CallID] = e.ResponseID
			}
		}
	}
}

// commentaryItems turns the commentary that introduced a tool call into agentMessage items
// placed in that call's turn.
func commentaryItems(r *commentaryReader, call msp.Item) []msp.Item {
	if call.Kind != msp.ItemKindToolCall || call.CallID == nil {
		return nil
	}
	var out []msp.Item
	for _, c := range r.take(*call.CallID) {
		it := msp.Item{
			// The commit's message_id is exactly the itemId the wire gives an agentMessage, so
			// should a host start sending commentary itself, the store folds both into one item
			// rather than showing the text twice. Until then the host never lists the id, and a
			// resume backfill's mergeOrder keeps it where it was seen.
			ItemID:   c.id,
			Kind:     msp.ItemKindAgentMessage,
			Revision: 1,
			Status:   msp.ItemStatusCompleted,
			Text:     strPtr(c.text),
			TurnID:   call.TurnID,
		}
		if c.at != "" {
			it.RecordedAt = strPtr(c.at)
		}
		out = append(out, it)
	}
	return out
}
