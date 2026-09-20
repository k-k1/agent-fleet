package harness

// loop.go is segment E's namesake: drive segment D's Client.Send in a loop,
// executing whatever ToolCalls come back and feeding the results back in as new
// messages, until a turn with no ToolCalls comes back (ADR 0093 decision 5 / §4.6
// of docs/log/99). D parses tool_calls out of a response but never runs one (D's
// own doc comment in types.go); this file is where one actually gets run.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

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
}

// Run drives client.Send in the tool loop: send, execute any ToolCalls the reply
// asks for (in parallel — a single turn can carry several), append the results as
// RoleTool messages, and send again, until a turn with no ToolCalls comes back or
// ctx is cancelled (which also cancels any bash command currently running,
// exec.CommandContext's ordinary behaviour — tools_bash.go relies on this rather
// than implementing its own cancellation). messages is the caller-assembled
// history so far (segment G's job, per types.go); Run appends to a COPY and never
// mutates the caller's slice.
func Run(ctx context.Context, client Client, reg *Registry, rt *Runtime, messages []Message) (Result, error) {
	hist := append([]Message(nil), messages...)
	gate := newRepeatGate(rt)
	var tracker repeatTracker
	var repeatWarnings int
	for {
		if err := ctx.Err(); err != nil {
			return Result{Messages: hist, RepeatWarnings: repeatWarnings}, err
		}
		turn, err := client.Send(ctx, hist, reg.Defs(rt.Plan))
		if err != nil {
			return Result{Messages: hist, RepeatWarnings: repeatWarnings}, err
		}
		hist = append(hist, Message{
			Role: RoleAssistant, Content: turn.Content, Reasoning: turn.Reasoning, ToolCalls: turn.ToolCalls,
		})
		if len(turn.ToolCalls) == 0 {
			return Result{Messages: hist, Final: turn, RepeatWarnings: repeatWarnings}, nil
		}

		// Decide once per call, in order, BEFORE dispatching any of this turn's
		// calls: the tracker's streak has to be updated in call order for
		// "unbroken row" to mean anything, and an abort must stop the loop
		// without running anything else this turn — including calls after the
		// one that tripped it.
		decisions := make([]repeatDecision, len(turn.ToolCalls))
		for i, call := range turn.ToolCalls {
			decisions[i] = gate.decide(tracker.note(call))
			if decisions[i].action == repeatAbort {
				return Result{Messages: hist, RepeatWarnings: repeatWarnings}, repeatAbortErr(call, decisions[i].streak)
			}
		}
		for _, d := range decisions {
			if d.action == repeatWarn {
				repeatWarnings++
			}
		}

		results, err := runToolCalls(ctx, reg, rt, turn.ToolCalls, decisions)
		if err != nil {
			return Result{Messages: hist, RepeatWarnings: repeatWarnings}, err
		}
		hist = append(hist, results...)
	}
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
