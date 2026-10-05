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

// commentaryReader tails one session.jsonl. What it holds is scoped to the turn in flight:
// forgetTurn drops it when the turn ends, and a resume drops what the history already settled
// (forgetSettled), so a long session costs one turn's worth, not its whole history.
type commentaryReader struct {
	mu       sync.Mutex
	path     string
	ident    os.FileInfo // the file the offset belongs to; a replaced file is read from 0
	off      int64
	partial  []byte
	byResp   map[string][]commentaryMsg // response id → commentary not yet attached
	callResp map[string]string          // call id → response id, for every call seen this turn
}

func newCommentaryReader(path string) *commentaryReader {
	r := &commentaryReader{path: path}
	r.resetMaps()
	return r
}

func (r *commentaryReader) resetMaps() {
	r.byResp = map[string][]commentaryMsg{}
	r.callResp = map[string]string{}
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

// lookup reads what was appended and answers for one call. known is false while the log has
// not yet recorded the call (the host writes it around the time the item goes out, so it may
// be late): the caller asks again later. Once known, the commentary is handed out once — a
// sibling call of the same response, or a later revision, gets none.
func (r *commentaryReader) lookup(callID string) (msgs []commentaryMsg, known bool) {
	if r == nil || callID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanLocked()
	resp, ok := r.callResp[callID]
	if !ok {
		return nil, false
	}
	msgs = r.byResp[resp]
	delete(r.byResp, resp)
	return msgs, true
}

// scan reads what was appended without answering anything: a resume's catch-up, so the first
// live item does not pay for the whole history on the MSP reader.
func (r *commentaryReader) scan() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanLocked()
}

// forgetTurn drops everything held: nothing of an ended turn is attached any more.
func (r *commentaryReader) forgetTurn() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetMaps()
}

// forgetSettled keeps only commentary whose tool calls the log has not recorded yet — a
// response still being produced — and drops the rest, which a resume has already attached.
func (r *commentaryReader) forgetSettled() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, resp := range r.callResp {
		delete(r.byResp, resp)
	}
	r.callResp = map[string]string{}
}

// scanLocked reads whatever was appended since the last call. A line still being written is
// kept in partial until its newline arrives. A file that is not the one the offset was taken
// on, or that shrank, is read again from the start with nothing carried over.
func (r *commentaryReader) scanLocked() {
	f, err := os.Open(r.path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if r.ident != nil && (!os.SameFile(r.ident, fi) || fi.Size() < r.off) {
		r.off, r.partial = 0, nil
		r.resetMaps()
	}
	r.ident = fi
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
		for _, c := range e.ToolCalls {
			if c.CallID != "" {
				r.callResp[c.CallID] = e.ResponseID
			}
		}
	}
}

// commentaryRecords turns the commentary that introduced call into store records anchored in
// front of it, and reports whether the log has answered for the call yet.
func commentaryRecords(r *commentaryReader, call msp.Item, model string) ([]record, bool) {
	if call.CallID == nil {
		return nil, true
	}
	msgs, known := r.lookup(*call.CallID)
	var out []record
	for _, c := range msgs {
		it := msp.Item{
			// The commit's message_id is exactly the itemId the wire gives an agentMessage, so
			// should a host start sending commentary itself, the store folds both into one item
			// rather than showing the text twice.
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
		out = append(out, record{Item: it, Model: model, Before: call.ItemID})
	}
	return out, known
}
