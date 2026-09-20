package harness

// loop.go is segment E's namesake: drive segment D's Client.Send in a loop,
// executing whatever ToolCalls come back and feeding the results back in as new
// messages, until a turn with no ToolCalls comes back (ADR 0093 decision 5 / §4.6
// of docs/log/99). D parses tool_calls out of a response but never runs one (D's
// own doc comment in types.go); this file is where one actually gets run.
//
// 🔴 Context management lived nowhere in this loop until now, and a live trial against
// qwen3-coder-30b-a3b found exactly the hole that leaves open: segment G's own compaction
// judgement (compact.go's PrepareTurn) only ever ran once per top-level turn, at whatever
// boundary a caller called it from — never from inside THIS loop. A single task whose tool
// loop took many Send/tool round trips (reading files, editing, re-running tests) grew hist
// past the real window with nothing checking it in between, and the engine eventually refused
// a request outright: "request (32772 tokens) exceeds the available context size (32768
// tokens)", mid-task, taking down the whole Run call. Run now re-runs that same judgement —
// compact.go's NeedsCompaction/Compact, unmodified, never a second compaction mechanism —
// before every Send this loop makes, not just the caller's own first one, closing exactly that
// gap. See Runtime.Window's own doc comment (tools.go) for the fallback this uses when a
// caller never wires Window up at all, and maybeCompact below for where the boundary is placed
// so it can never split a tool_calls/tool round trip in two.
//
// 🔴🔴 A live A/B (gemma-4-12b-it-q4_k_m, same window=3500, same task list, ONLY the code
// differing — old task-boundary-only compaction vs. this file's own in-loop check) found a
// SECOND, worse failure this same fix introduced: 59 assistant turns total (old code, full
// 5-task session) became 355+ turns without even reaching the 4th task (new code) — 86
// compactions inside a single task alone. The read: compacting mid-loop had been folding the
// model's own MOST RECENT work into prose and discarding the raw detail, so the model kept
// forgetting what it had just verified and re-doing it — the two simple tasks that never
// needed to compact (a few turns each) stayed fine; the two substantial ones, which compacted
// repeatedly, ballooned. Two things fixed this, both required, neither alone sufficient:
//   - maybeCompact's boundary now stops BEFORE the most recent complete tool round trip
//     (compactPreservingLastRoundTrip below) — that round trip rides through RAW, never folded
//     into the summary, so "what did I just do" survives every compaction.
//   - Run now refuses to keep thrashing silently: if compacting fails to buy even one
//     genuinely uncompacted round trip before the budget is exceeded again — the state that
//     produced 86 compactions in one Run call — Run stops with ErrCompactionThrashing instead
//     of continuing to spend Send calls on a task that provably does not fit this window. The
//     threshold is Runtime.MaxConsecutiveCompactions (tools.go); its own <=0 fallback follows
//     the same "zero value stays a real, protective number" posture as Window's own fallback.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// defaultWindowFallback is Runtime.Window's <=0 fallback (tools.go's own doc comment there
// has the full reasoning): deliberately far below every real window this fleet has actually
// run against live (32768 in the incident this file's header describes, 262144 for another
// model — live_manual_test.go's TestManualLiveCompaction), so a caller that forgot to wire
// Window through still gets protective, if overeager, compaction rather than none at all.
const defaultWindowFallback = 8192

// continuationPrompt is appended, as a synthetic user message, ONLY when maybeCompact decides
// to compact and full's own last entry is not already a real user turn (i.e. the loop is mid
// tool round trip, between one Send and the next, not at a caller-supplied top-level turn
// boundary). This borrows compact.go's OWN "pending user turn" preservation (Compact's doc
// comment: a trailing RoleUser message survives, unsummarized, immediately after the new
// boundary) rather than teaching compact.go a second notion of "pending" shaped around a tool
// result — reuse, not a new mechanism. Without it, a mid-loop compaction would summarize
// straight through to the last tool-result message, and BuildSendMessages' next slice would
// end in nothing but the one leading system message: the same "no user query found in
// messages" chat-template rejection compact.go's own doc comment already found live, just
// reached from a different calling shape than the one Compact was written against.
//
// 🔴 This message ends up sitting in Result.Messages/full itself (decision 3's append-only
// record), as an ordinary Role==RoleUser entry indistinguishable, BY ROLE ALONE, from
// something the actual human/caller said — it is NOT stripped back out once its one job
// (keeping the post-compaction request template-valid) is done. A transcript writer or
// mirror reading full later MUST NOT render this as something the user said. It IS reliably
// identifiable: match on Content == continuationPrompt (this exact constant, exported by
// neither name nor value elsewhere) rather than on position or role, since a real user
// message with the same wording is vanishingly unlikely but not impossible to rule out by
// role/position alone.
const continuationPrompt = "Continue with the task."

// defaultMaxConsecutiveCompactions is Runtime.MaxConsecutiveCompactions's <=0 fallback: the
// live A/B this file's header comment describes hit 86 compactions in a single Run call
// before anything noticed. 3 is chosen the same way defaultRepeatWarnAfter was (repeat.go) —
// well below anything resembling the incident, comfortably above the occasional back-to-back
// pair a real, once-off large tool result can legitimately cause without the task actually
// being unworkable.
const defaultMaxConsecutiveCompactions = 3

// ErrCompactionThrashing is the sentinel behind the error Run returns once
// Runtime.MaxConsecutiveCompactions consecutive iterations have each needed maybeCompact to
// fire, with no intervening iteration that made it under budget without compacting — i.e.
// compacting is no longer buying the loop even one genuinely-uncompacted round trip before the
// budget is exceeded again. Continuing to spin here only spends more Send calls (each one a
// real, billed completion, not a cheap check) on a task that has demonstrated it does not fit
// this window; a caller distinguishes this from any other Run failure via errors.Is, the same
// shape ErrRepeatedToolCall already uses.
var ErrCompactionThrashing = errors.New("harness: context window keeps needing compaction faster than the loop can make progress")

// Result is what Run returns once a turn with no ToolCalls comes back, or once ctx
// is cancelled mid-loop.
type Result struct {
	// Messages is the full history: the caller's original messages, plus every
	// assistant/tool round trip this call made.
	Messages []Message
	// Final is the last turn Send returned. Its ToolCalls is always empty on a
	// successful Run (a non-empty one is what keeps the loop going).
	Final Turn
	// RepeatWarnings counts how many tool calls the repeated-tool-call gate
	// (repeat.go) intercepted at its warn stage and answered with a synthetic
	// error instead of actually running — so a caller (or this package's own
	// tests) can tell whether the gate fired during this Run, not just that Run
	// finished. An abort-stage trip does not add to this count; it surfaces as
	// ErrRepeatedToolCall from Run instead.
	RepeatWarnings int
	// Compactions counts how many times maybeCompact actually ran a summarization turn
	// during this Run call — the in-loop counterpart of RepeatWarnings, for the same
	// reason: a caller (or this package's own tests) needs to tell whether the new
	// mid-loop compaction check in this file ever fired, not just that Run finished
	// without error. Zero is the expected value for an ordinary short conversation (the
	// negative control in loop_test.go); the positive control there wants this above zero.
	Compactions int
}

// Run drives client.Send in the tool loop: send, execute any ToolCalls the reply
// asks for (in parallel — a single turn can carry several), append the results as
// RoleTool messages, and send again, until a turn with no ToolCalls comes back or
// ctx is cancelled (which also cancels any bash command currently running,
// exec.CommandContext's ordinary behaviour — tools_bash.go relies on this rather
// than implementing its own cancellation).
//
// messages is the caller-assembled history so far — decision 3's append-only "full"
// shape (the same thing PrepareTurn's own full parameter means), NOT a
// BuildSendMessages-folded "send" slice: Run now does that folding itself, every
// iteration, via maybeCompact below, so it can re-check the budget before every Send
// this loop makes rather than only the caller's first one. Run appends to a COPY and
// never mutates the caller's slice; Result.Messages is that same "full" shape back,
// so it can be handed straight into another Run (or PrepareTurn) call unchanged.
func Run(ctx context.Context, client Client, reg *Registry, rt *Runtime, messages []Message) (Result, error) {
	full := append([]Message(nil), messages...)
	gate := newRepeatGate(rt)
	var tracker repeatTracker
	var repeatWarnings, compactions, consecutiveCompactions int
	maxConsecutiveCompactions := defaultMaxConsecutiveCompactions
	if rt != nil && rt.MaxConsecutiveCompactions > 0 {
		maxConsecutiveCompactions = rt.MaxConsecutiveCompactions
	}
	for {
		if err := ctx.Err(); err != nil {
			return Result{Messages: full, RepeatWarnings: repeatWarnings, Compactions: compactions}, err
		}
		tools := reg.Defs(rt.Plan)
		send, compacted, fired, err := maybeCompact(ctx, client, rt, tools, full)
		if err != nil {
			return Result{Messages: full, RepeatWarnings: repeatWarnings, Compactions: compactions}, err
		}
		full = compacted
		if fired {
			compactions++
			consecutiveCompactions++
			if consecutiveCompactions >= maxConsecutiveCompactions {
				// Stop BEFORE spending another Send on this — see ErrCompactionThrashing's
				// own doc comment: this state is exactly the live incident that spent 86
				// Send calls (each a real completion) finding out the window would never be
				// enough, one iteration at a time.
				return Result{Messages: full, RepeatWarnings: repeatWarnings, Compactions: compactions},
					compactionThrashingErr(consecutiveCompactions)
			}
		} else {
			consecutiveCompactions = 0
		}
		turn, err := client.Send(ctx, send, tools)
		if err != nil {
			return Result{Messages: full, RepeatWarnings: repeatWarnings, Compactions: compactions}, err
		}
		full = append(full, Message{
			Role: RoleAssistant, Content: turn.Content, Reasoning: turn.Reasoning, ToolCalls: turn.ToolCalls,
		})
		if len(turn.ToolCalls) == 0 {
			return Result{Messages: full, Final: turn, RepeatWarnings: repeatWarnings, Compactions: compactions}, nil
		}

		// Decide once per call, in order, BEFORE dispatching any of this turn's
		// calls: the tracker's streak has to be updated in call order for
		// "unbroken row" to mean anything. abortIdx is the first call (if any)
		// whose streak crossed RepeatAbortAfter; the loop keeps deciding the
		// rest anyway (harmless — Run returns right after) rather than bailing
		// out of this inner loop early, so every call in the turn gets a real
		// decision to hand to repeatAbortToolMessage below.
		decisions := make([]repeatDecision, len(turn.ToolCalls))
		abortIdx := -1
		for i, call := range turn.ToolCalls {
			decisions[i] = gate.decide(tracker.note(call))
			if decisions[i].action == repeatAbort && abortIdx == -1 {
				abortIdx = i
			}
		}
		if abortIdx != -1 {
			// Every call in this turn is answered with a gate error and NONE of
			// them actually runs — not just the one whose streak tripped the
			// threshold — so full ends up with no unanswered ToolCalls and stays
			// a sendable history (see ErrRepeatedToolCall's doc comment) rather
			// than the same "assistant asked for tools, nothing answered them"
			// shape the ctx/Send error paths above leave behind (which is fine
			// there — those really cannot continue — but this path is meant to
			// be recoverable).
			aborted := turn.ToolCalls[abortIdx]
			streak := decisions[abortIdx].streak
			abortResults := make([]Message, len(turn.ToolCalls))
			for i, call := range turn.ToolCalls {
				abortResults[i] = toolResult(call, repeatAbortToolMessage(call, aborted, streak))
			}
			full = append(full, abortResults...)
			for _, d := range decisions[:abortIdx] {
				if d.action == repeatWarn {
					repeatWarnings++
				}
			}
			return Result{Messages: full, RepeatWarnings: repeatWarnings, Compactions: compactions}, repeatAbortErr(aborted, streak)
		}
		for _, d := range decisions {
			if d.action == repeatWarn {
				repeatWarnings++
			}
		}

		results, err := runToolCalls(ctx, reg, rt, turn.ToolCalls, decisions)
		if err != nil {
			return Result{Messages: full, RepeatWarnings: repeatWarnings, Compactions: compactions}, err
		}
		full = append(full, results...)
	}
}

// maybeCompact is decision 7's judgement (compact.go's NeedsCompaction/Compact,
// unmodified), re-run before every Send this loop makes instead of only the caller's
// first one — the gap this file's own header comment describes. It always returns a
// send slice (BuildSendMessages(rt.SystemPrompt, full), the same folding PrepareTurn
// does at a turn boundary), the (possibly compacted) full history to keep tracking
// going forward, and fired (whether a summarization Send actually ran this call, for
// Run's own Compactions bookkeeping above — NOT inferrable from len(send) vs len(full)
// alone, since decision 3's append-only full stays longer than send forever after the
// FIRST compaction, whether or not a later iteration compacts again).
//
// This is only ever called between complete round trips — once before the loop's
// first Send, and again only after runToolCalls has appended every one of a turn's
// tool results (Run's own call sites, above) — never with full ending mid-turn on an
// assistant message whose ToolCalls have not all been answered yet. That placement is
// what keeps this from ever splitting a tool_calls message from its own tool results
// across the new summary boundary (constraint checked live already, twice, by
// compact.go's own two chat-template bugs — see this function's own continuationPrompt
// handling below for the third shape neither of those fixes covered).
func maybeCompact(ctx context.Context, client Client, rt *Runtime, tools []ToolDef, full []Message) (send, newFull []Message, fired bool, err error) {
	sysPrompt := ""
	window := defaultWindowFallback
	reserved := 0
	if rt != nil {
		sysPrompt = rt.SystemPrompt
		if rt.WindowDisabled {
			return BuildSendMessages(sysPrompt, full), full, false, nil
		}
		if rt.Window > 0 {
			window = rt.Window
		}
		if rt.ReservedOutput > 0 {
			reserved = rt.ReservedOutput
		}
	}

	send = BuildSendMessages(sysPrompt, full)
	tokens, err := client.InputTokens(ctx, send, tools)
	if err != nil {
		return nil, full, false, err
	}
	if !NeedsCompaction(tokens, reserved, window) {
		return send, full, false, nil
	}

	compacted, err := compactPreservingLastRoundTrip(ctx, client, sysPrompt, full)
	if err != nil {
		// Same contract as Compact's own: a failed summarization Send returns full
		// UNCHANGED. Propagate the error rather than silently sending the over-budget
		// request anyway — decision 7/8's own stance (compact.go) is that a real
		// failure must surface, not be swallowed.
		return nil, full, false, err
	}
	return BuildSendMessages(sysPrompt, compacted), compacted, true, nil
}

// compactPreservingLastRoundTrip is maybeCompact's own boundary placement — the live A/B this
// file's header comment describes (86 compactions in one Run call) traced back to Compact
// folding EVERYTHING, including the round trip that just finished, into the summary: the model
// lost track of its own most recent action and re-did it, repeatedly. Compact itself
// (compact.go) is unmodified; this only decides WHERE its boundary sits.
//
// The last complete round trip — the most recent RoleAssistant message and every RoleTool
// message answering it — is carved off BEFORE calling Compact and reattached, VERBATIM, after
// the new boundary: Compact only ever sees full[:lastAssistant], never the round trip itself,
// so there is nothing for it to fold that content into. This can never split a tool_calls
// message from its own results (the driving constraint this whole file is built around): the
// carve point is the start of that last round trip, never somewhere inside it.
//
// full's last entry not already being a real user turn is what tells the caller (maybeCompact)
// this is the mid-loop shape rather than a caller-supplied top-level turn boundary; when it
// IS already a user turn, Compact's own pending-user-turn preservation (compact.go's doc
// comment) already does the right thing on its own, so this defers to it unchanged.
func compactPreservingLastRoundTrip(ctx context.Context, client Client, sysPrompt string, full []Message) ([]Message, error) {
	if n := len(full); n == 0 || full[n-1].Role == RoleUser {
		return Compact(ctx, client, sysPrompt, full)
	}

	lastAssistant := -1
	for i := len(full) - 1; i >= 0; i-- {
		if full[i].Role == RoleAssistant {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		// No assistant turn at all yet to preserve (full is ONLY tool-shaped messages,
		// which Run's own call sites never actually produce, but nothing here depends on
		// that) — fall back to the plain synthetic-nudge shape with nothing held back.
		toCompact := append(append([]Message(nil), full...), Message{Role: RoleUser, Content: continuationPrompt})
		return Compact(ctx, client, sysPrompt, toCompact)
	}

	compactedPrefix, err := Compact(ctx, client, sysPrompt, full[:lastAssistant])
	if err != nil {
		return full, err
	}
	preserved := full[lastAssistant:] // the last complete round trip, unmodified
	// 🔴 The synthetic continuationPrompt turn appended here survives into
	// full/Result.Messages permanently — see its own doc comment for why a future
	// transcript writer must never render it as something the human/caller actually said.
	out := make([]Message, 0, len(compactedPrefix)+len(preserved)+1)
	out = append(out, compactedPrefix...)
	out = append(out, preserved...)
	out = append(out, Message{Role: RoleUser, Content: continuationPrompt})
	return out, nil
}

// compactionThrashingErr is the error Run returns once ErrCompactionThrashing's threshold
// trips, worded with the actual streak length so a caller sees a real count, not just a name.
func compactionThrashingErr(consecutive int) error {
	return fmt.Errorf("compaction fired %d times in a row with no round trip fitting under budget in between: %w", consecutive, ErrCompactionThrashing)
}

// runToolCalls executes every call in calls concurrently (parallel tool_calls,
// docs/log/99 §4.6), preserving the original order in the returned messages so the
// history reads left-to-right regardless of which finished first. A
// context.Canceled/DeadlineExceeded from any one call aborts the whole batch
// (returned, not folded into a message) — every other in-flight call was already
// cancelled by the same ctx, so their own errors are redundant noise.
//
// decisions is Run's repeat-gate verdict for each call, same index as calls
// (computed up front so the tracker's streak reflects call order even though
// execution itself is concurrent). A repeatWarn call never reaches executeOne at
// all — its message is the gate's own synthetic error, not a real tool result,
// because the point of the warn stage is that the call does NOT run again.
func runToolCalls(ctx context.Context, reg *Registry, rt *Runtime, calls []ToolCall, decisions []repeatDecision) ([]Message, error) {
	out := make([]Message, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		if decisions[i].action == repeatWarn {
			out[i] = toolResult(call, repeatWarnMessage(call, decisions[i].streak))
			continue
		}
		wg.Add(1)
		go func(i int, call ToolCall) {
			defer wg.Done()
			out[i], errs[i] = executeOne(ctx, reg, rt, call)
		}(i, call)
	}
	wg.Wait()
	for _, e := range errs {
		if isCancellation(e) {
			return nil, e
		}
	}
	return out, nil
}

func isCancellation(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// executeOne resolves and runs a single ToolCall, turning every non-cancellation
// outcome (unknown tool, plan-mode refusal, declined approval, a failed Run) into
// a RoleTool message rather than an error — the same "report the failure back to
// the model" shape every CLI-driven kind already uses for a failed shell command.
func executeOne(ctx context.Context, reg *Registry, rt *Runtime, call ToolCall) (Message, error) {
	tool, ok := reg.lookup(call.Name)
	if !ok {
		return toolResult(call, fmt.Sprintf("error: unknown tool %q", call.Name)), nil
	}
	if tool.Mutates && rt.Plan {
		return toolResult(call, fmt.Sprintf("error: %s is unavailable in plan mode", call.Name)), nil
	}
	if tool.Mutates {
		if err := approve(ctx, rt, call, tool); err != nil {
			if isCancellation(err) {
				return Message{}, err
			}
			var declined *declinedError
			if errors.As(err, &declined) {
				return toolResult(call, "declined: "+declined.Error()), nil
			}
			return toolResult(call, "error: approval failed: "+err.Error()), nil
		}
	}
	out, err := tool.Run(ctx, rt, call.Arguments)
	if err != nil {
		if isCancellation(err) {
			return Message{}, err
		}
		out = "error: " + err.Error()
	}
	return toolResult(call, truncateOutput(out, rt.outputLimit())), nil
}

func toolResult(call ToolCall, content string) Message {
	return Message{Role: RoleTool, Content: content, ToolCallID: call.ID}
}
