package harness

// compact.go is segment G's window half (ADR 0093 decision 7 / docs/log/99 §4.11): the
// judgement of whether the next real Send risks overflowing the engine's context window, and
// the one-shot summarization turn that runs when it does. There is no growth ladder here on
// purpose (decision 7's background, docs/log/99 §4.11): hermes-agent's own compressor grows
// the SERVER's window right before compacting, which only works because one local process
// owns both the llama-server child and the conversation; here the window is a fleet-wide GPU
// box (an ECS task swap, minutes, not hermes's local-restart seconds — and Tokyo's on-demand
// GPU capacity can be exhausted even for a same-class replacement). The window this package is
// handed is decided once, with headroom, and never grown.

import (
	"context"
	"strings"
)

// CompactionThreshold is decision 7's "閾値" in `(input_tokens + reserved output) > window *
// threshold`. The ADR pins the FORMULA but not a number, so 0.9 is this segment's own choice:
// it leaves slack above the reserved-output headroom a caller already counted, for
// llama-server's own chat-template overhead and a reservedOutput guess that runs a little
// short. Not exposed as a per-call knob on purpose — decision 7 wants exactly one judgement,
// not a tunable surface that could reintroduce the "13% shown, compacts every turn" bug this
// design replaces (window.go's own postmortem, cited in the ADR's background).
const CompactionThreshold = 0.9

// summaryPrefix marks a system message BuildSendMessages treats as a compaction boundary.
// Kept unexported: nothing outside this file needs to recognise the marker, only produce
// (Compact) or consume (BuildSendMessages) it.
const summaryPrefix = "[lcpp compaction summary]\n"

// NeedsCompaction is decision 7's judgement, given the EXACT pre-send token count
// Client.InputTokens already measured (never an estimate — decision 8 forbids this kind from
// calling usagex.WindowGuess) and the window EngineWindow reported.
//
// window <= 0 — EngineWindow's own documented "nothing is known" value (types.go) — always
// answers false. With no ceiling to compare against, inventing one to compact against would be
// exactly the guess decision 8 rules out, and a caller that compacted anyway on a made-up
// number would run the summarization turn on every single send rather than never — the
// runaway behaviour decision 7's background exists to prevent, not a safer default.
func NeedsCompaction(inputTokens, reservedOutput, window int) bool {
	if window <= 0 {
		return false
	}
	return inputTokens+reservedOutput > int(float64(window)*CompactionThreshold)
}

// BuildSendMessages is segment G's own seam (types.go: "G is what BUILDS the []Message a call
// passes to Client.Send/Client.InputTokens"): systemPrompt first, then every entry of full FROM
// THE LAST COMPACTION SUMMARY ONWARD — decision 3's compaction is append-only, so full itself
// is never trimmed; only the slice actually handed to the engine narrows. Every carried-over
// message's own Reasoning is dropped (decision 7 / docs/log/99 §4.11: llama-server's
// --reasoning-preserve default would otherwise replay every past turn's chain-of-thought on
// every future request) — the RECORD a higher layer keeps for display is a separate copy of
// full that this function never touches, so the reasoning is dropped from the wire, not lost.
//
// 🔴 Confirmed live against the dev deployment: the compaction summary is FOLDED into the one
// leading system message rather than sent as its own separate system-role entry later in the
// list. Qwen's own chat template rejects the latter outright — "System message must be at the
// beginning" — the moment a summary boundary sits anywhere past index 0 alongside the
// system-prompt message BuildSendMessages already puts there. full itself still carries the
// summary as its own Message (decision 3's record), this function just never re-emits it as a
// second system-role wire entry.
//
// full is read only; the returned slice is a fresh one, so a caller may safely keep sending
// full itself to a transcript writer unmodified.
func BuildSendMessages(systemPrompt string, full []Message) []Message {
	sys := systemPrompt
	rest := full
	if idx := lastSummaryIndex(full); idx >= 0 {
		summary := strings.TrimPrefix(full[idx].Content, summaryPrefix)
		switch {
		case sys != "" && summary != "":
			sys = sys + "\n\n" + summary
		case summary != "":
			sys = summary
		}
		rest = full[idx+1:]
	}
	out := make([]Message, 0, len(rest)+1)
	out = append(out, Message{Role: RoleSystem, Content: sys})
	for _, m := range rest {
		m.Reasoning = ""
		out = append(out, m)
	}
	return out
}

// lastSummaryIndex returns the index of the most recent compaction-boundary message, or -1
// when there has never been one — distinct from 0, which is a real position a summary can sit
// at (a compaction that runs before any other history has been added).
func lastSummaryIndex(full []Message) int {
	for i := len(full) - 1; i >= 0; i-- {
		if full[i].Role == RoleSystem && strings.HasPrefix(full[i].Content, summaryPrefix) {
			return i
		}
	}
	return -1
}

// compactionRequest is the one-shot instruction Compact appends before asking the model to
// summarize everything BuildSendMessages would otherwise send.
const compactionRequest = "Summarize this conversation so far for your own future reference: " +
	"the goal, decisions already made, files touched, and anything still unfinished. Be " +
	"concise — this summary REPLACES the conversation above for the rest of this session, so " +
	"keep anything you would otherwise need to remember to keep working."

// Compact runs decision 7's "要約ターンを1回": one extra, tool-free Send asking the model to
// summarize everything currently in the active window, then appends the answer to full as a
// new compaction-boundary system message.
//
// 🔴 Confirmed live against the dev deployment (ADR 0093 segment G's own acceptance test): when
// full's last message is the pending user turn that triggered NeedsCompaction in the first
// place (PrepareTurn's usual calling shape — the new question is appended before the budget
// check, matching decision 7's "the messages about to be SENT" wording), it must not be
// swallowed into the summary. Folding it in left the boundary system message as the last
// entry BuildSendMessages' next slice would carry, which llama-server's own chat template
// (Qwen's Jinja template, at least) rejects outright — "No user query found in messages": a
// template that expects the LAST message to be a user turn, not two system messages back to
// back. So the pending user message (if any) is excluded from what gets summarized and
// re-appended AFTER the new boundary, keeping it exactly what it always was: the next thing to
// answer, not a claim inside the summary's own prose.
//
// full is never mutated in place, and the returned slice's backing array is always a fresh
// allocation, never full's own — decision 3's append-only compaction means a caller that
// stores full as (or alongside) a growing transcript must not see an in-place append silently
// alias and then reallocate out from under it later.
func Compact(ctx context.Context, client Client, systemPrompt string, full []Message) ([]Message, error) {
	pending := 0
	if n := len(full); n > 0 && full[n-1].Role == RoleUser {
		pending = 1
	}
	toSummarize := full[:len(full)-pending]

	send := BuildSendMessages(systemPrompt, toSummarize)
	send = append(send, Message{Role: RoleUser, Content: compactionRequest})
	turn, err := client.Send(ctx, send, nil)
	if err != nil {
		return full, err
	}
	summary := strings.TrimSpace(turn.Content)

	out := make([]Message, len(toSummarize), len(full)+1)
	copy(out, toSummarize)
	out = append(out, Message{Role: RoleSystem, Content: summaryPrefix + summary})
	out = append(out, full[len(toSummarize):]...) // the pending turn, unchanged, after the boundary
	return out, nil
}

// PrepareTurn is the entry point a caller (a chatx provider, or a future kind driver) uses
// before starting a new top-level turn: it measures the exact token cost BuildSendMessages'
// current slice would carry, compacts once if decision 7's judgement fires, and returns both
// the (possibly larger, per decision 3) full history to persist and the ready-to-send slice
// for the real Client.Send/Run call that follows.
//
// A failed compaction (the summarization Send itself erroring — e.g. the engine is asleep)
// returns full UNCHANGED alongside the uncompacted send slice: a caller that goes ahead and
// sends it anyway gets exactly today's behaviour (the engine's own error, or a Send that still
// fits), rather than this function silently swallowing a real failure.
func PrepareTurn(ctx context.Context, client Client, tools []ToolDef, systemPrompt string, full []Message, reservedOutput, window int) (newFull, send []Message, err error) {
	send = BuildSendMessages(systemPrompt, full)
	tokens, err := client.InputTokens(ctx, send, tools)
	if err != nil {
		return full, send, err
	}
	if !NeedsCompaction(tokens, reservedOutput, window) {
		return full, send, nil
	}
	compacted, err := Compact(ctx, client, systemPrompt, full)
	if err != nil {
		return full, send, err
	}
	return compacted, BuildSendMessages(systemPrompt, compacted), nil
}
