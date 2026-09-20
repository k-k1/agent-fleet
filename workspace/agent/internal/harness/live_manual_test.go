//go:build manuallive

package harness

// Opt-in live check against a real llama.cpp engine (docs/log/99 §4.14's own suggested
// "実エンジンに対する契約テスト", opt-in shape) — this is what actually drove ADR 0093 segment
// G's own acceptance requirement: a real conversation against the dev deployment's "llm"
// engine, grown until decision 7's compaction fires once, with the conversation still
// answering afterward. NOT part of `go test ./...` (build-tagged out, so it never touches a
// real, billed GPU box by accident) — run explicitly with:
//
//   go test ./internal/harness/ -tags manuallive -run TestManualLiveCompaction -v -timeout 20m
//
// with AF_LCPP_LIVE_BASE (the engine's own /v1-mount base URL, e.g.
// https://<cp>/engine/llm/v1) and AF_LCPP_LIVE_TOKEN (a session/engine-scoped bearer minted via
// POST /internal/engine/token — never the CP's own AF_ENGINE_ISSUE_TOKEN) set. This is exactly
// how two real chat-template incompatibilities were found and fixed in compact.go: Qwen's own
// template rejects a system-role message anywhere but the first, and rejects a request that
// does not end in a user turn — neither is exercisable with a scripted Client stub, only a
// real engine's own template actually raises them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// retryOnWake calls fn, retrying only while it fails with an *EngineError whose own
// Retryable() says asking again can plausibly help (ADR 0093 decision 4: a true cold start is
// 4-5 minutes) — deferring to that judgement rather than this file inventing a broader one.
// A prior version of this helper retried every kind except EngineUnavailable/EngineOff, which
// silently spent 10 minutes retrying a plain "model 'x' not found" (a running llama-server that
// was never told about a model added after it started, not a wake in progress at all) — paying
// for an awake GPU box the whole time and, worse, preventing the box from ever going idle and
// cycling to a fresh process that WOULD know about the new model. Retryable() alone avoids that:
// only EngineWaking is retried, everything else fails fast.
func retryOnWake(t *testing.T, ctx context.Context, label string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		err := fn()
		if err == nil {
			return
		}
		var ee *EngineError
		if errors.As(err, &ee) && ee.Retryable() && time.Now().Before(deadline) {
			t.Logf("engine waking during %s, retrying in 15s: %v", label, err)
			select {
			case <-time.After(15 * time.Second):
				continue
			case <-ctx.Done():
				t.Fatalf("%s: context done while waiting for the engine to wake: %v", label, ctx.Err())
			}
		}
		t.Fatalf("%s: %v", label, err)
	}
}

func TestManualLiveCompaction(t *testing.T) {
	base := os.Getenv("AF_LCPP_LIVE_BASE")
	token := os.Getenv("AF_LCPP_LIVE_TOKEN")
	if base == "" || token == "" {
		t.Skip("AF_LCPP_LIVE_BASE / AF_LCPP_LIVE_TOKEN not set")
	}
	model := os.Getenv("AF_LCPP_LIVE_MODEL")
	if model == "" {
		model = "qwen3.8-27b-uncensored-q4_k_m"
	}
	client := NewClient(EngineConn{BaseURL: base, Token: token}, model)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	sys := "You are a terse test assistant used for an automated harness check. Reply in one short sentence."
	var full []Message
	reservedOutput := 128
	// Deliberately far below the model's real window (262144, confirmed live via
	// GET /engine/llm/props for this same deployment) — actually filling the real window would
	// take a very large number of turns and a long time on a GPU box shared with other
	// sessions, for no more evidence than a small forced window already gives: PrepareTurn's
	// judgement only cares about the ARITHMETIC (input_tokens + reserved > window*threshold),
	// and input_tokens itself is measured for real either way.
	window := 900

	compactedAtTurn := -1
	var lastInputTokens int
	for i := 0; i < 40 && compactedAtTurn < 0; i++ {
		full = append(full, Message{Role: RoleUser, Content: fmt.Sprintf(
			"Turn %d: repeat the number %d back to me and name one short unique fact about it.", i, i)})

		newFull, send, err := PrepareTurn(ctx, client, nil, sys, full, reservedOutput, window)
		if err != nil {
			t.Fatalf("PrepareTurn at turn %d: %v", i, err)
		}
		if len(newFull) > len(full) {
			compactedAtTurn = i
			t.Logf("compaction fired at turn %d: full grew %d -> %d entries; summary=%q",
				i, len(full), len(newFull), newFull[len(newFull)-1].Content)
		}
		full = newFull

		turn, err := client.Send(ctx, send, nil)
		if err != nil {
			t.Fatalf("Send at turn %d: %v", i, err)
		}
		tokens, err := client.InputTokens(ctx, send, nil)
		if err != nil {
			t.Fatalf("InputTokens at turn %d: %v", i, err)
		}
		lastInputTokens = tokens
		t.Logf("turn %d: input_tokens=%d assistant=%q", i, tokens, turn.Content)
		full = append(full, Message{Role: RoleAssistant, Content: turn.Content, Reasoning: turn.Reasoning})
	}
	if compactedAtTurn < 0 {
		t.Fatalf("never compacted after 40 turns (last input_tokens=%d, window=%d)", lastInputTokens, window)
	}

	// One more turn AFTER compaction, to prove the conversation still answers coherently and
	// BuildSendMessages' post-compaction slice (system + summary + this turn) is accepted.
	full = append(full, Message{Role: RoleUser, Content: "What number did I mention in my very first message to you?"})
	_, send, err := PrepareTurn(ctx, client, nil, sys, full, reservedOutput, window)
	if err != nil {
		t.Fatalf("PrepareTurn after compaction: %v", err)
	}
	turn, err := client.Send(ctx, send, nil)
	if err != nil {
		t.Fatalf("Send after compaction: %v", err)
	}
	t.Logf("post-compaction reply: %q", turn.Content)
	if turn.Content == "" {
		t.Fatal("post-compaction reply was empty — conversation looks broken after compaction")
	}
}

// TestManualLiveAgenticSession drives a real read/edit/bash/todo_write tool loop against a
// real llama-server model, for ADR 0093 open question 1 (segment G report, docs/log/99 §4.11):
// which model family keeps `tool_calls` JSON well-formed across ~20 real turns of coding work,
// including parallel tool calls and one forced compaction. Unlike TestManualLiveCompaction
// (synthetic "repeat this number" turns, no tools), this one runs an actual multi-file Go
// project with real bugs through E's own Registry/Runtime/Run — the shape phase 1's kind is
// actually meant to carry.
//
//	go test ./internal/harness/ -tags manuallive -run TestManualLiveAgenticSession -v -timeout 60m
//
// Env: AF_LCPP_LIVE_BASE / AF_LCPP_LIVE_TOKEN (same as TestManualLiveCompaction),
// AF_LCPP_LIVE_MODEL (catalogue id), AF_LCPP_LIVE_CWD (a pre-seeded scratch Go project — this
// test does not create one, so a family/model comparison can each get an untouched copy), and
// optionally AF_LCPP_LIVE_WINDOW (default 3500 — deliberately far below the real window so
// decision 7's compaction judgement actually fires within the turn budget below, matching
// TestManualLiveCompaction's own reasoning for using a small forced window).
func TestManualLiveAgenticSession(t *testing.T) {
	base := os.Getenv("AF_LCPP_LIVE_BASE")
	token := os.Getenv("AF_LCPP_LIVE_TOKEN")
	cwd := os.Getenv("AF_LCPP_LIVE_CWD")
	if base == "" || token == "" || cwd == "" {
		t.Skip("AF_LCPP_LIVE_BASE / AF_LCPP_LIVE_TOKEN / AF_LCPP_LIVE_CWD not set")
	}
	model := os.Getenv("AF_LCPP_LIVE_MODEL")
	if model == "" {
		model = "qwen3.8-27b-uncensored-q4_k_m"
	}
	window := 3500
	if w := os.Getenv("AF_LCPP_LIVE_WINDOW"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			window = n
		}
	}
	client := NewClient(EngineConn{BaseURL: base, Token: token}, model)
	reg := NewRegistry(BuiltinTools()...)
	rt := &Runtime{Cwd: cwd, Approve: AutoApprove, MaxOutputBytes: 8000}

	sys := "You are a careful coding agent working in a real Go project at " + cwd + ". " +
		"You have read/write/edit/glob/grep/ls/bash tools and a todo_write tool. " +
		"Use todo_write to track multi-step work. When a step lets you look at several " +
		"independent things at once (e.g. reading multiple files), issue those tool calls " +
		"in the SAME turn rather than one at a time. Always verify your work by actually " +
		"running `go build ./...` and `go test ./...` with the bash tool before declaring " +
		"something fixed — do not just claim success without having run it."

	tasks := []string{
		"This Go project currently fails `go build ./...` and, once it builds, has failing " +
			"tests under `go test ./...` because of real bugs. Start by reading every .go file " +
			"under this directory (in parallel — one tool call per file, all in the same turn) " +
			"to see the whole project before changing anything. Then find and fix every bug so " +
			"that both `go build ./...` and `go test ./...` succeed. Use todo_write to track " +
			"each bug as you find and fix it.",
		"Now add two new exported functions to the mathutil package: `Max(nums []int) (int, " +
			"error)` and `Min(nums []int) (int, error)`, each returning an error for an empty " +
			"slice. Add table-driven tests for both, including the empty-slice case. Run `go " +
			"test ./...` to confirm they pass.",
		"Run `go vet ./...` and fix anything it flags. Then run `gofmt -l .` and fix anything " +
			"it lists.",
		"Add a short README.md at the project root (a few lines) describing what the " +
			"mathutil and stack packages do and how to run the tests.",
		"One more round: add a `Sum(nums []int) int` function to mathutil (empty slice sums " +
			"to 0, no error), plus a test. Run the full test suite once more and report the " +
			"final pass/fail counts.",
	}
	// Asked regardless of whether compaction actually fired (logged either way) — decision 3's
	// claim is that history before a summary boundary is still answerable from the summary
	// alone, so this is the one question in the whole session that must NOT be answered by
	// reading files again.
	recallQuestion := "Without reading any files again, from memory: what was the exact bug " +
		"you found and fixed in the stack package's Pop function at the very start of this " +
		"session, and what was the original (buggy) divisor bug in mathutil's Average " +
		"function? Be specific."

	var full []Message
	reservedOutput := 512
	compactedAtTurn := -1
	invalidToolCallJSON := 0
	unknownToolCalls := 0
	badArgShape := 0
	parallelToolCallTurns := 0
	turnCount := 0

	logAdded := func(label string, before, after []Message) {
		for i := len(before); i < len(after); i++ {
			m := after[i]
			if m.Role != RoleAssistant {
				continue
			}
			turnCount++
			if len(m.ToolCalls) > 1 {
				parallelToolCallTurns++
			}
			for _, tc := range m.ToolCalls {
				if !json.Valid([]byte(tc.Arguments)) {
					invalidToolCallJSON++
					t.Logf("🔴 turn %d (%s): INVALID tool_call JSON for %q: %s", turnCount, label, tc.Name, tc.Arguments)
					continue
				}
				tool, ok := reg.lookup(tc.Name)
				if !ok {
					unknownToolCalls++
					t.Logf("🔴 turn %d (%s): unknown tool name %q, args=%s", turnCount, label, tc.Name, tc.Arguments)
					continue
				}
				if missing := missingRequiredArgs(tool, tc.Arguments); len(missing) > 0 {
					badArgShape++
					t.Logf("🔴 turn %d (%s): %q call missing required arg(s) %v, args=%s",
						turnCount, label, tc.Name, missing, tc.Arguments)
				}
			}
			t.Logf("turn %d (%s): content=%q tool_calls=%d names=%v", turnCount, label,
				truncateForLog(m.Content), len(m.ToolCalls), toolCallNames(m.ToolCalls))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	runTurn := func(label, userMsg string) {
		full = append(full, Message{Role: RoleUser, Content: userMsg})
		var newFull, send []Message
		retryOnWake(t, ctx, "PrepareTurn at "+label, func() error {
			var err error
			newFull, send, err = PrepareTurn(ctx, client, reg.Defs(false), sys, full, reservedOutput, window)
			return err
		})
		if len(newFull) > len(full) && compactedAtTurn < 0 {
			compactedAtTurn = turnCount
			summary := ""
			// lastSummaryIndex (compact.go) is the same lookup BuildSendMessages itself uses —
			// reused rather than re-derived so this log line cannot disagree with what the
			// engine actually got sent.
			if idx := lastSummaryIndex(newFull); idx >= 0 {
				summary = strings.TrimPrefix(newFull[idx].Content, summaryPrefix)
			}
			t.Logf("🔴 compaction fired before %s, after %d assistant turns so far; summary=%q",
				label, turnCount, truncateForLog(summary))
		}
		full = newFull
		before := full
		var result Result
		retryOnWake(t, ctx, "Run at "+label, func() error {
			var err error
			result, err = Run(ctx, client, reg, rt, send)
			return err
		})
		// Run's Messages starts with `send` (systemPrompt/summary + trimmed history, decision
		// 7), not `full` (decision 3's untrimmed record) — reconstitute full by appending only
		// what Run actually added past send, matching PrepareTurn's own doc comment on the two
		// slices' relationship.
		added := result.Messages[len(send):]
		full = append(full, added...)
		logAdded(label, before, full)
	}

	for i, task := range tasks {
		runTurn(fmt.Sprintf("task-%d", i), task)
	}
	if compactedAtTurn < 0 {
		t.Logf("⚠️ compaction never fired across %d assistant turns (window=%d) — the recall "+
			"question below will not actually exercise post-compaction recall", turnCount, window)
	}
	runTurn("recall", recallQuestion)
	t.Logf("=== SUMMARY model=%s window=%d total_assistant_turns=%d compacted_at_turn=%d "+
		"parallel_tool_call_turns=%d invalid_tool_call_json=%d unknown_tool_calls=%d bad_arg_shape=%d ===",
		model, window, turnCount, compactedAtTurn, parallelToolCallTurns, invalidToolCallJSON, unknownToolCalls, badArgShape)
}

func truncateForLog(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500] + "…"
	}
	return s
}

func toolCallNames(calls []ToolCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Name
	}
	return out
}

// missingRequiredArgs reads tool.Def.Parameters' own JSON-Schema "required" list and reports
// which of those keys argsJSON's top-level object is missing — catching the "right tool, wrong
// argument keys" failure mode separately from invalid JSON or an unknown tool name.
func missingRequiredArgs(tool Tool, argsJSON string) []string {
	var schema struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(tool.Def.Parameters, &schema) != nil || len(schema.Required) == 0 {
		return nil
	}
	var args map[string]json.RawMessage
	s := strings.TrimSpace(argsJSON)
	if s != "" {
		_ = json.Unmarshal([]byte(s), &args)
	}
	var missing []string
	for _, k := range schema.Required {
		if _, ok := args[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}
