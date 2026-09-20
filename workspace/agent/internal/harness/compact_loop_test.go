package harness

// compact_loop_test.go is loop.go's own acceptance test for the mid-loop compaction check
// (maybeCompact): the positive control that pins the live incident loop.go's own header
// comment describes (a tool loop whose history grows past the window with nothing checking
// it until the engine refuses the request), and the negative control that pins the opposite
// failure mode (compacting so eagerly that an ordinary short conversation loses history for no
// reason).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// growingClient is a scriptedClient with one difference: InputTokens is not a fixed stub
// answer, it is len(Content) summed across the given messages — a deterministic, easily
// controlled stand-in for "the exact token count", playing the same role a real
// client.InputTokens would (decision 8: this package never estimates instead of asking the
// engine — but a TEST double is not the engine, so growing the messages it is handed is what
// grows what it reports, exactly like the real one growing tokens as history grows). This is
// what lets these two tests actually exercise NeedsCompaction's arithmetic instead of just
// asserting the loop calls it.
type growingClient struct {
	turns    []Turn
	i        int
	summary  string
	gotSends [][]Message
	gotTools [][]ToolDef
}

func (c *growingClient) Send(_ context.Context, messages []Message, tools []ToolDef) (Turn, error) {
	c.gotSends = append(c.gotSends, append([]Message(nil), messages...))
	c.gotTools = append(c.gotTools, tools)
	if tools == nil {
		// Compact's own summarization Send (compact.go) always passes nil tools — the
		// same distinction TestCompactAppendsSummaryWithoutMutatingInput pins ("the
		// summarization Send itself must not carry tools").
		return Turn{Content: c.summary}, nil
	}
	if c.i >= len(c.turns) {
		return Turn{}, errors.New("growingClient: ran out of turns")
	}
	t := c.turns[c.i]
	c.i++
	return t, nil
}

func (c *growingClient) InputTokens(_ context.Context, messages []Message, _ []ToolDef) (int, error) {
	total := 0
	for _, m := range messages {
		total += len(m.Content)
	}
	return total, nil
}

func blobTool(name, blob string) Tool {
	return Tool{
		Def: ToolDef{Name: name},
		Run: func(_ context.Context, _ *Runtime, _ string) (string, error) { return blob, nil },
	}
}

// requireNoOrphanToolCalls walks messages and fails if any RoleAssistant message's ToolCalls
// are not immediately answered, one RoleTool message per call ID, before the next
// RoleAssistant message — the structural half of the driving task's constraint 1 ("never
// separate a tool_calls message from its own tool results"). Checked against BOTH
// Result.Messages (decision 3's own record) and every request this loop actually sent
// (growingClient.gotSends) — the record can only stay correct by construction if the wire
// payloads derived from it are also correct, but asserting both catches a bug in the
// derivation (BuildSendMessages) that a record-only check would miss.
func requireNoOrphanToolCalls(t *testing.T, label string, messages []Message) {
	t.Helper()
	pending := map[string]bool{}
	for i, m := range messages {
		switch m.Role {
		case RoleAssistant:
			if len(pending) != 0 {
				t.Fatalf("%s: message %d is an assistant turn but %d tool call(s) from an earlier turn were never answered: %+v", label, i, len(pending), pending)
			}
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		case RoleTool:
			if !pending[m.ToolCallID] {
				t.Fatalf("%s: message %d is a tool result for %q, which no preceding assistant message asked for (in this same request)", label, i, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatalf("%s: %d tool call(s) at the end of the message list were never answered: %+v", label, len(pending), pending)
	}
}

// TestRunCompactsMidLoopWhenHistoryGrowsPastWindow is this file's positive control: a tool
// loop whose OWN history (not something the caller pre-loaded into `messages`) grows past a
// small Window, entirely within a single Run call, with no top-level turn boundary in between
// for a caller to have hung a PrepareTurn call off of. Before loop.go's own maybeCompact
// existed, nothing inside Run ever re-checked the budget after the first Send, which is
// exactly the live incident loop.go's header comment describes (a real 32768-token window,
// blown through mid-task). Mutating the fix out (see this test's own comment block below, kept
// for the review record) turns this red.
func TestRunCompactsMidLoopWhenHistoryGrowsPastWindow(t *testing.T) {
	blob := strings.Repeat("x", 300) // one tool result; several of these is what grows hist past Window
	var turns []Turn
	for i := 0; i < 10; i++ {
		// Arguments vary per call (a distinct "n") purely so the UNRELATED repeated-tool-call
		// gate (repeat.go) never mistakes this for its own failure mode and aborts first —
		// this test is about maybeCompact, not repeat.go.
		turns = append(turns, Turn{ToolCalls: []ToolCall{{ID: string(rune('a' + i)), Name: "blob", Arguments: fmt.Sprintf(`{"n":%d}`, i)}}})
	}
	turns = append(turns, Turn{Content: "done"})
	client := &growingClient{turns: turns, summary: "summary of the blob-fetching so far"}
	reg := NewRegistry(blobTool("blob", blob))
	rt := &Runtime{Cwd: t.TempDir(), Window: 1000} // deliberately small: ~4 blobs already exceeds 1000*0.9

	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Final.Content != "done" {
		t.Fatalf("Final.Content = %q, want %q — the loop must still finish normally after compacting", res.Final.Content, "done")
	}
	if res.Compactions < 1 {
		t.Fatal("Compactions = 0, want at least 1 — history grew well past Window*0.9 and nothing compacted it")
	}

	// Find the first request sent with fewer messages than the one before it — proof that an
	// actual shrink happened at some point, not just that the counter above was incremented
	// without anything really changing what got sent.
	shrankAt := -1
	for i := 1; i < len(client.gotSends); i++ {
		if len(client.gotSends[i]) < len(client.gotSends[i-1]) {
			shrankAt = i
			break
		}
	}
	if shrankAt < 0 {
		t.Fatal("no request ever got smaller than the one before it — Compactions fired but nothing was actually trimmed")
	}
	shrunk := client.gotSends[shrankAt]
	// The request right after a mid-loop compaction must end in the synthetic continuation
	// turn (continuationPrompt), not bare on the one leading system message — the exact chat
	// template rejection ("no user query found in messages") compact.go's own doc comment
	// already found live once, reached here from a different calling shape (a tool result
	// last, not a caller-supplied user turn).
	last := shrunk[len(shrunk)-1]
	if last.Role != RoleUser || last.Content != continuationPrompt {
		t.Fatalf("post-compaction request ends in %+v, want the synthetic continuation user turn", last)
	}
	sysCount := 0
	for i, m := range shrunk {
		if m.Role == RoleSystem {
			sysCount++
			if i != 0 {
				t.Fatalf("post-compaction request has a system-role message at index %d, want only at index 0", i)
			}
		}
	}
	if sysCount != 1 {
		t.Fatalf("post-compaction request has %d system-role messages, want exactly 1 (Qwen's chat template rejects a second one, per compact.go's own doc comment)", sysCount)
	}

	// The last complete round trip before compaction fired must ride through RAW — a
	// RoleTool message whose Content is the literal blob, not folded into the (distinct,
	// fixed) canned summary text — the fix for the live A/B regression (loop.go's own header
	// comment: 86 compactions in one Run call once the model lost track of its own most
	// recent action and kept re-doing it).
	sawRawBlob := false
	for _, m := range shrunk {
		if m.Role == RoleTool && m.Content == blob {
			sawRawBlob = true
		}
		if m.Role == RoleSystem && strings.Contains(m.Content, blob) {
			t.Fatalf("the last round trip's own blob content ended up folded into the summary/system message instead of surviving raw: %+v", m)
		}
	}
	if !sawRawBlob {
		t.Fatalf("post-compaction request has no raw tool-result message matching the last round trip's own blob content: %+v", shrunk)
	}

	requireNoOrphanToolCalls(t, "Result.Messages", res.Messages)
	for i, sent := range client.gotSends {
		if client.gotTools[i] == nil {
			continue // the internal compaction summarization Send never carries tool_calls
		}
		requireNoOrphanToolCalls(t, "a sent request", sent)
	}
}

// TestRunDoesNotCompactShortConversation is this file's negative control: an ordinary short
// tool loop, measured with the SAME len(Content) InputTokens stand-in the positive control
// above uses (not scriptedClient's fixed 0, which would pass trivially regardless of whether
// the check ever really ran), must never compact. A loop-internal check that fired on every
// iteration regardless of size would defeat the point of compaction — trading a short,
// perfectly answerable conversation for a lossy summary for no reason (the driving task's own
// negative-control requirement).
func TestRunDoesNotCompactShortConversation(t *testing.T) {
	client := &growingClient{turns: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Arguments: `{"x":1}`}}},
		{Content: "done"},
	}}
	reg := NewRegistry(echoTool("echo"))
	rt := &Runtime{Cwd: t.TempDir()} // Window left at its zero value — defaultWindowFallback applies
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Compactions != 0 {
		t.Fatalf("Compactions = %d, want 0 — this conversation is nowhere near defaultWindowFallback", res.Compactions)
	}
	if len(client.gotSends) != 2 {
		t.Fatalf("Send called %d times, want 2 (no extra summarization turn)", len(client.gotSends))
	}
}

// TestRunWindowDisabledSkipsInputTokensEntirely pins Runtime.WindowDisabled's own contract
// (tools.go): the opt-out really does skip the check (and its InputTokens round trip), not
// just skip acting on the result — a caller that set this expects zero extra engine calls per
// iteration, not one that always comes back "under budget".
func TestRunWindowDisabledSkipsInputTokensEntirely(t *testing.T) {
	client := &countingInputTokensClient{growingClient: growingClient{turns: []Turn{{Content: "done"}}}}
	reg := NewRegistry()
	rt := &Runtime{Cwd: t.TempDir(), WindowDisabled: true}
	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Compactions != 0 {
		t.Fatalf("Compactions = %d, want 0", res.Compactions)
	}
	if client.calls != 0 {
		t.Fatalf("InputTokens was called %d times, want 0 (WindowDisabled must skip the check entirely)", client.calls)
	}
}

// TestRunStopsCompactionThrashingWithSentinelError is the live A/B regression's own positive
// control (loop.go's header comment: 86 compactions in a single Run call, gemma-4-12b-it-q4_k_m,
// window=3500, real deployment): a single tool result too big to ever fit under a small Window,
// even alone and even preserved as the one thing a compaction keeps raw, forces every
// subsequent iteration to compact again immediately — Run must notice this and stop loudly
// (ErrCompactionThrashing) well before anything like 86 Send calls, rather than spending an
// unbounded number of real completions finding out the window will never be enough.
func TestRunStopsCompactionThrashingWithSentinelError(t *testing.T) {
	blob := strings.Repeat("y", 2000) // bigger than the window on its own, even alone post-compaction
	var turns []Turn
	for i := 0; i < 20; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{{ID: string(rune('a' + i)), Name: "blob", Arguments: fmt.Sprintf(`{"n":%d}`, i)}}})
	}
	client := &growingClient{turns: turns, summary: "summary of the blob-fetching so far"}
	reg := NewRegistry(blobTool("blob", blob))
	rt := &Runtime{Cwd: t.TempDir(), Window: 500} // one blob alone already exceeds 500*0.9

	res, err := Run(context.Background(), client, reg, rt, nil)
	if !errors.Is(err, ErrCompactionThrashing) {
		t.Fatalf("err = %v, want an error wrapping ErrCompactionThrashing", err)
	}
	if res.Compactions > 2*defaultMaxConsecutiveCompactions {
		t.Fatalf("Compactions = %d, want at most roughly %d — the live incident this pins spent 86 finding this out one iteration at a time", res.Compactions, defaultMaxConsecutiveCompactions)
	}
	if client.i >= len(turns) {
		t.Fatalf("consumed all %d scripted turns before stopping — Run kept spinning instead of stopping early", len(turns))
	}
	requireNoOrphanToolCalls(t, "Result.Messages", res.Messages)
}

// TestRunConsecutiveCompactionsResetsAfterARealRoundTrip pins the "consecutive" half of
// MaxConsecutiveCompactions's own contract: a compaction that DOES buy the loop a real,
// under-budget round trip before the next one fires must not count toward the same streak as
// an immediately-repeating one — otherwise a long conversation that legitimately compacts many
// times over its life (never back-to-back) would eventually trip ErrCompactionThrashing for no
// reason, exactly the false-positive TestRunCompactsMidLoopWhenHistoryGrowsPastWindow's own
// window/blob sizing was chosen to avoid triggering.
func TestRunConsecutiveCompactionsResetsAfterARealRoundTrip(t *testing.T) {
	blob := strings.Repeat("x", 300)
	var turns []Turn
	for i := 0; i < 20; i++ {
		turns = append(turns, Turn{ToolCalls: []ToolCall{{ID: string(rune('a' + i)), Name: "blob", Arguments: fmt.Sprintf(`{"n":%d}`, i)}}})
	}
	turns = append(turns, Turn{Content: "done"})
	client := &growingClient{turns: turns, summary: "summary of the blob-fetching so far"}
	reg := NewRegistry(blobTool("blob", blob))
	rt := &Runtime{Cwd: t.TempDir(), Window: 1000, MaxConsecutiveCompactions: 2}

	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run: %v, want no error — each compaction here is followed by several real round trips before the next one, never back-to-back", err)
	}
	if res.Final.Content != "done" {
		t.Fatalf("Final.Content = %q, want %q", res.Final.Content, "done")
	}
	if res.Compactions < 2 {
		t.Fatalf("Compactions = %d, want at least 2 (this Window is small enough to need several over 20 rounds)", res.Compactions)
	}
}

type countingInputTokensClient struct {
	growingClient
	calls int
}

func (c *countingInputTokensClient) InputTokens(ctx context.Context, messages []Message, tools []ToolDef) (int, error) {
	c.calls++
	return c.growingClient.InputTokens(ctx, messages, tools)
}
