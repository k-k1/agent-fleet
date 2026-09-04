package kiro

// transcriptBuf accumulates a managed (ACP) session's transcript in memory
// (docs/log/43 Track A2). Same shape as cursor's mirror.go: one state machine handles both
// a live turn and a `session/load` replay.
//
//   - live turn: the driver calls addUserTurn(prompt) at the top of runTurn, and every
//     following agent_message_chunk / agent_thought_chunk / tool_call piles onto the open
//     assistant turn. flushAsst at the end of the turn (the session/prompt response).
//     kiro's ACP emits no user_message_chunk while live (same as cursor, measured).
//   - replay: while setLoading(true), a user_message_chunk opens a new user turn and the
//     agent_* events that follow build an assistant turn. setLoading(false) flushes the
//     last one. Measured: kiro's session/load replays history as user_message_chunk plus
//     agent_message_chunk (cross-process resume, codeword re-answer PASS).
//
// Unlike cursor, kiro also persists the ACP transcript as v2 JSONL
// (~/.kiro/sessions/cli/<sid>.jsonl), which is what driver.go's managedTranscript falls
// back to once the session is stopped and there is no handle. So this buffer covers the
// live/replay transcript of a LIVE handle.
//
// Turn.Idx increases monotonically: the Console's pendingEcho/MirrorView assume it does
// (the lesson of agy 7354916). A tool_call_update's rawOutput goes into the tool Part's
// Output.

import (
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// bufClip bounds any carried text (a preview — parity with the JSONL parser's outClip).
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

// empty reports whether the buffer holds no committed and no pending turn (used to
// decide the persisted-file fallback).
func (b *transcriptBuf) empty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.turns) == 0 && b.curAsst == nil && b.userBuf == ""
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
// addUserTurn, and kiro emits no live user_message_chunk).
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

func (b *transcriptBuf) toolCall(id, title, info string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureAsstLocked()
	label := title
	if label == "" {
		label = info
	}
	b.curAsst.Parts = append(b.curAsst.Parts, transcript.Part{Kind: "tool", Tool: label, Info: clipBuf(info)})
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
