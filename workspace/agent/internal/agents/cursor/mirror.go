package cursor

// transcriptBuf accumulates the transcript of a managed (ACP) session in memory
// (docs/log/40 Track A2). cursor's ACP leaves no local trace - not even the JSONL that TUI/-p
// write - so building the transcript from `session/update` notifications is the driver's only
// way in. One state machine handles both a live turn and a `session/load` replay:
//
//   - live turn: the driver calls addUserTurn(prompt) at the top of runTurn, then piles the
//     following agent_message_chunk / agent_thought_chunk / tool_call onto the open assistant
//     turn, and flushAsst at the end of the turn (the session/prompt response). ACP emits no
//     user_message_chunk while live (measured).
//   - replay: while setLoading(true), a user_message_chunk opens a new user turn and the
//     following agent_* build an assistant turn. setLoading(false) flushes the last one.
//
// Turn.Idx increases monotonically: the Console's pendingEcho/MirrorView require it (agy 7354916).
// tool_result (rawOutput) goes into the tool Part's Output, extra information the TUI JSONL does
// not carry - an advantage of ACP.

import (
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// outClip bounds any carried text (a preview — parity with the other parsers).
const bufClip = 4000

func clipBuf(s string) string {
	if len(s) <= bufClip {
		return s
	}
	return s[:bufClip] + "\n…（省略）"
}

type transcriptBuf struct {
	mu      sync.Mutex
	turns   []transcript.Turn // committed turns
	idx     int               // monotonic Idx source
	curAsst *transcript.Turn  // open assistant turn (not yet committed)
	toolIdx map[string]int    // toolCallId → index into curAsst.Parts
	userBuf string            // pending user_message_chunk text (replay only)
	loading bool              // true while replaying a session/load
}

// reset clears everything (called before a session/load replay so history isn't
// double-counted).
func (b *transcriptBuf) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns = nil
	b.idx = 0
	b.curAsst = nil
	b.toolIdx = nil
	b.userBuf = ""
}

// setLoading toggles replay mode. Turning it off flushes the last open assistant turn.
func (b *transcriptBuf) setLoading(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loading = v
	if !v {
		b.flushUserLocked()
		b.flushAsstLocked()
	}
}

// addUserTurn commits a user turn immediately (live Send path).
func (b *transcriptBuf) addUserTurn(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushAsstLocked()
	b.flushUserLocked()
	b.idx++
	b.turns = append(b.turns, transcript.Turn{
		Role: "user", Text: text, Idx: b.idx,
		Parts: []transcript.Part{{Kind: "text", Text: text}},
	})
}

// userChunk appends replayed user text (replay only; live user turns come from
// addUserTurn, and cursor emits no live user_message_chunk).
func (b *transcriptBuf) userChunk(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loading {
		return
	}
	b.flushAsstLocked()
	b.userBuf += text
}

func (b *transcriptBuf) agentChunk(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureAsstLocked()
	b.appendTextLocked("text", text)
}

func (b *transcriptBuf) thoughtChunk(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureAsstLocked()
	b.appendTextLocked("thinking", text)
}

// toolCall records one ACP tool_call. `file`/`verb`/`edits` are the changed-files
// coordinate (docs/log/68) when this call edited something — in the ACP path they come from
// the PROTOCOL's own classification (tool_call.kind / locations), not from the tool's
// display name, so they survive a CLI that renames its tools.
func (b *transcriptBuf) toolCall(id, title, info, file, verb string, edits []transcript.Edit) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureAsstLocked()
	label := title
	if label == "" {
		label = info
	}
	b.curAsst.Parts = append(b.curAsst.Parts,
		transcript.Part{Kind: "tool", Tool: label, Info: clipBuf(info), File: file, Verb: verb, Edits: edits})
	if id != "" {
		if b.toolIdx == nil {
			b.toolIdx = map[string]int{}
		}
		b.toolIdx[id] = len(b.curAsst.Parts) - 1
	}
}

func (b *transcriptBuf) toolOutput(id, out string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.curAsst == nil || b.toolIdx == nil {
		return
	}
	if i, ok := b.toolIdx[id]; ok && i < len(b.curAsst.Parts) {
		b.curAsst.Parts[i].Output = clipBuf(out)
	}
}

// flushAsst commits the open assistant turn (turn end has no notification in ACP;
// the driver calls this when session/prompt returns).
func (b *transcriptBuf) flushAsst() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushAsstLocked()
}

// snapshot returns a chronological copy of the transcript INCLUDING the in-progress
// user/assistant turn, so the mirror shows live streaming. Never mutates committed state.
func (b *transcriptBuf) snapshot() []transcript.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]transcript.Turn, 0, len(b.turns)+1)
	out = append(out, b.turns...)
	// userBuf and curAsst are mutually exclusive (userChunk flushes asst; ensureAsst
	// flushes user), so at most one trailing pending turn.
	if b.userBuf != "" {
		out = append(out, transcript.Turn{
			Role: "user", Text: b.userBuf, Idx: b.idx + 1,
			Parts: []transcript.Part{{Kind: "text", Text: b.userBuf}},
		})
	} else if b.curAsst != nil {
		t := *b.curAsst
		t.Parts = append([]transcript.Part(nil), b.curAsst.Parts...)
		t.Text = combineText(t.Parts)
		out = append(out, t)
	}
	return out
}

// --- locked helpers (caller holds b.mu) --------------------------------------

func (b *transcriptBuf) ensureAsstLocked() {
	b.flushUserLocked()
	if b.curAsst == nil {
		b.idx++
		b.curAsst = &transcript.Turn{Role: "assistant", Idx: b.idx}
		b.toolIdx = map[string]int{}
	}
}

func (b *transcriptBuf) flushUserLocked() {
	if b.userBuf == "" {
		return
	}
	b.idx++
	b.turns = append(b.turns, transcript.Turn{
		Role: "user", Text: b.userBuf, Idx: b.idx,
		Parts: []transcript.Part{{Kind: "text", Text: b.userBuf}},
	})
	b.userBuf = ""
}

func (b *transcriptBuf) flushAsstLocked() {
	if b.curAsst == nil {
		return
	}
	b.curAsst.Text = combineText(b.curAsst.Parts)
	if len(b.curAsst.Parts) > 0 {
		b.turns = append(b.turns, *b.curAsst)
	}
	b.curAsst = nil
	b.toolIdx = nil
}

// appendTextLocked coalesces consecutive chunks of the same kind into one Part so a
// streamed reply is a handful of Parts, not hundreds of one-token fragments.
func (b *transcriptBuf) appendTextLocked(kind, text string) {
	parts := b.curAsst.Parts
	if n := len(parts); n > 0 && parts[n-1].Kind == kind {
		parts[n-1].Text = clipBuf(parts[n-1].Text + text)
		b.curAsst.Parts = parts
		return
	}
	b.curAsst.Parts = append(parts, transcript.Part{Kind: kind, Text: clipBuf(text)})
}

// combineText joins the text Parts into the Turn.Text summary (thinking/tool excluded).
func combineText(parts []transcript.Part) string {
	text := ""
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			if text != "" {
				text += "\n\n"
			}
			text += p.Text
		}
	}
	return text
}
