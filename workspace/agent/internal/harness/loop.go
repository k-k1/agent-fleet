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
	for {
		if err := ctx.Err(); err != nil {
			return Result{Messages: hist}, err
		}
		turn, err := client.Send(ctx, hist, reg.Defs(rt.Plan))
		if err != nil {
			return Result{Messages: hist}, err
		}
		hist = append(hist, Message{
			Role: RoleAssistant, Content: turn.Content, Reasoning: turn.Reasoning, ToolCalls: turn.ToolCalls,
		})
		if len(turn.ToolCalls) == 0 {
			return Result{Messages: hist, Final: turn}, nil
		}
		results, err := runToolCalls(ctx, reg, rt, turn.ToolCalls)
		if err != nil {
			return Result{Messages: hist}, err
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
func runToolCalls(ctx context.Context, reg *Registry, rt *Runtime, calls []ToolCall) ([]Message, error) {
	out := make([]Message, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
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
