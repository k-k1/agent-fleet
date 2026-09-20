package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNeedsCompactionThreshold(t *testing.T) {
	cases := []struct {
		name                    string
		input, reserved, window int
		want                    bool
	}{
		{"well under", 100, 50, 1000, false},
		{"just under 90%", 800, 90, 1000, false}, // 890 <= 900
		{"just over 90%", 850, 60, 1000, true},   // 910 > 900
		{"exactly at threshold is not over", 900, 0, 1000, false},
		{"one over threshold", 901, 0, 1000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsCompaction(c.input, c.reserved, c.window); got != c.want {
				t.Fatalf("NeedsCompaction(%d,%d,%d) = %v, want %v", c.input, c.reserved, c.window, got, c.want)
			}
		})
	}
}

// TestNeedsCompactionZeroWindowNeverFires pins ADR 0093 decision 7/8's "window unknown must
// not run away" rule: with no measured/catalogue window, this package must never invent a
// ceiling to compact against. Positive control below.
func TestNeedsCompactionZeroWindowNeverFires(t *testing.T) {
	if NeedsCompaction(1_000_000, 100_000, 0) {
		t.Fatal("NeedsCompaction fired with window=0 — this must never compact against a guessed ceiling")
	}
	if NeedsCompaction(1_000_000, 100_000, -1) {
		t.Fatal("NeedsCompaction fired with a negative window")
	}
}

func TestBuildSendMessagesLeadsWithSystemPrompt(t *testing.T) {
	full := []Message{{Role: RoleUser, Content: "hi"}}
	got := BuildSendMessages("be nice", full)
	if len(got) != 2 || got[0].Role != RoleSystem || got[0].Content != "be nice" {
		t.Fatalf("got %+v", got)
	}
	if got[1].Content != "hi" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

// TestBuildSendMessagesSendsFromLastSummaryOnwardButKeepsFullHistory pins decision 3's
// append-only compaction: BuildSendMessages narrows what gets SENT, but must never be handed
// a full slice that has had anything removed from it — the caller's own copy of full is the
// only place truncation could happen, and this function must not do it either.
func TestBuildSendMessagesSendsFromLastSummaryOnwardButKeepsFullHistory(t *testing.T) {
	full := []Message{
		{Role: RoleUser, Content: "turn 1"},
		{Role: RoleAssistant, Content: "reply 1"},
		{Role: RoleSystem, Content: summaryPrefix + "everything up to turn 1 happened"},
		{Role: RoleUser, Content: "turn 2"},
		{Role: RoleAssistant, Content: "reply 2"},
	}
	original := append([]Message(nil), full...)

	send := BuildSendMessages("sys", full)

	// (a) full itself must be untouched — the past rows are still there, in order.
	if len(full) != len(original) {
		t.Fatalf("full was mutated in length: got %d want %d", len(full), len(original))
	}
	for i := range full {
		if !reflect.DeepEqual(full[i], original[i]) {
			t.Fatalf("full[%d] mutated: got %+v want %+v", i, full[i], original[i])
		}
	}

	// the SEND slice starts at the summary boundary, not turn 1 — and the summary is FOLDED
	// into the one leading system message (Qwen's chat template rejects a second, later
	// system-role entry outright: "System message must be at the beginning", confirmed live),
	// not sent as its own separate entry.
	wantContents := []string{"sys\n\neverything up to turn 1 happened", "turn 2", "reply 2"}
	if len(send) != len(wantContents) {
		t.Fatalf("send = %+v, want %d entries", send, len(wantContents))
	}
	for i, want := range wantContents {
		if send[i].Content != want {
			t.Fatalf("send[%d].Content = %q, want %q", i, send[i].Content, want)
		}
	}
	if send[0].Role != RoleSystem {
		t.Fatalf("send[0].Role = %q, want %q", send[0].Role, RoleSystem)
	}
	for _, m := range send[1:] {
		if m.Role == RoleSystem {
			t.Fatalf("a second system-role message rode along: %+v", send)
		}
	}
	if strings.Contains(send[1].Content, "turn 1") {
		t.Fatal("send slice leaked pre-summary content")
	}
}

// TestBuildSendMessagesDropsPastReasoning pins decision 7: replaying old chain-of-thought into
// a future request must not happen, because llama-server's own --reasoning-preserve default
// would otherwise make every past turn's thinking ride every future prompt.
func TestBuildSendMessagesDropsPastReasoning(t *testing.T) {
	full := []Message{
		{Role: RoleUser, Content: "question"},
		{Role: RoleAssistant, Content: "answer", Reasoning: "long chain of thought nobody should resend"},
	}
	send := BuildSendMessages("sys", full)
	for _, m := range send {
		if m.Reasoning != "" {
			t.Fatalf("send message carries Reasoning: %+v", m)
		}
	}
	// full itself must still carry it — this is a record, not a second place to lose data.
	if full[1].Reasoning == "" {
		t.Fatal("full's own Reasoning was dropped — only the SEND copy should lose it")
	}
}

// budgetClient is a minimal Client stub for Compact/PrepareTurn tests: Send always answers
// with a fixed summary, InputTokens answers a scripted value.
type budgetClient struct {
	inputTokens    int
	inputTokensErr error
	sendTurn       Turn
	sendErr        error
	gotSends       [][]Message
}

func (c *budgetClient) Send(_ context.Context, messages []Message, _ []ToolDef) (Turn, error) {
	c.gotSends = append(c.gotSends, append([]Message(nil), messages...))
	if c.sendErr != nil {
		return Turn{}, c.sendErr
	}
	return c.sendTurn, nil
}

func (c *budgetClient) InputTokens(context.Context, []Message, []ToolDef) (int, error) {
	return c.inputTokens, c.inputTokensErr
}

func TestCompactAppendsSummaryWithoutMutatingInput(t *testing.T) {
	full := []Message{
		{Role: RoleUser, Content: "turn 1"},
		{Role: RoleAssistant, Content: "reply 1"},
	}
	original := append([]Message(nil), full...)
	client := &budgetClient{sendTurn: Turn{Content: "  the summary  "}}

	got, err := Compact(context.Background(), client, "sys", full)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// (a) input slice untouched.
	if len(full) != len(original) {
		t.Fatalf("full mutated in length: %dvs%d", len(full), len(original))
	}
	for i := range full {
		if !reflect.DeepEqual(full[i], original[i]) {
			t.Fatalf("full[%d] mutated", i)
		}
	}

	if len(got) != len(full)+1 {
		t.Fatalf("got %d messages, want %d", len(got), len(full)+1)
	}
	last := got[len(got)-1]
	if last.Role != RoleSystem || last.Content != summaryPrefix+"the summary" {
		t.Fatalf("last message = %+v", last)
	}

	// Compact must not have aliased full's backing array: mutating got must not touch full.
	got[0].Content = "tampered"
	if full[0].Content == "tampered" {
		t.Fatal("Compact's return value shares full's backing array")
	}

	// the summarization Send itself must not carry tools.
	if len(client.gotSends) != 1 {
		t.Fatalf("Send called %d times, want 1", len(client.gotSends))
	}
}

// TestCompactKeepsPendingUserTurnAfterTheBoundary pins a bug found live against the dev
// deployment (ADR 0093 segment G's own acceptance run): PrepareTurn's usual calling shape
// appends the NEW user question to full before checking the budget, so full's last message is
// often that still-unanswered turn when NeedsCompaction fires. Folding it into the summary
// left the boundary system message last, and BuildSendMessages' next slice then ended in TWO
// system-role messages with no trailing user turn at all — which llama-server's own chat
// template (Qwen's Jinja template) rejected outright: "No user query found in messages". The
// pending user message must survive, unsummarized, immediately after the new boundary.
func TestCompactKeepsPendingUserTurnAfterTheBoundary(t *testing.T) {
	full := []Message{
		{Role: RoleUser, Content: "turn 1"},
		{Role: RoleAssistant, Content: "reply 1"},
		{Role: RoleUser, Content: "turn 2 — still unanswered"},
	}
	client := &budgetClient{sendTurn: Turn{Content: "summary of turn 1"}}

	got, err := Compact(context.Background(), client, "sys", full)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// full's own two rows (turn 1 + reply 1), plus the new summary, plus the pending turn.
	if len(got) != 4 {
		t.Fatalf("got %+v, want 4 entries (turn 1 + reply 1 + summary + the pending turn)", got)
	}
	if got[2].Role != RoleSystem || got[2].Content != summaryPrefix+"summary of turn 1" {
		t.Fatalf("got[2] = %+v, want the summary boundary", got[2])
	}
	if got[3].Role != RoleUser || got[3].Content != "turn 2 — still unanswered" {
		t.Fatalf("got[3] = %+v, want the pending user turn preserved", got[3])
	}

	// The summarization Send itself must NOT have included the pending turn — it has not
	// happened yet as far as "what to summarize" is concerned.
	if len(client.gotSends) != 1 {
		t.Fatalf("Send called %d times, want 1", len(client.gotSends))
	}
	for _, m := range client.gotSends[0] {
		if m.Content == "turn 2 — still unanswered" {
			t.Fatal("the pending turn leaked into the summarization request")
		}
	}

	// What BuildSendMessages hands the NEXT real Send must end in a user message (one of the
	// two live template errors this pins) and must carry exactly one system-role message,
	// with the summary folded into it rather than riding as its own later entry (the other).
	send := BuildSendMessages("sys", got)
	if last := send[len(send)-1]; last.Role != RoleUser {
		t.Fatalf("post-compaction send ends in role %q, want %q: %+v", last.Role, RoleUser, send)
	}
	if send[0].Role != RoleSystem || send[0].Content != "sys\n\nsummary of turn 1" {
		t.Fatalf("send[0] = %+v, want the fleet+summary folded system message", send[0])
	}
	for _, m := range send[1:] {
		if m.Role == RoleSystem {
			t.Fatalf("a second system-role message rode along: %+v", send)
		}
	}
}

func TestCompactFailureLeavesFullUnchanged(t *testing.T) {
	full := []Message{{Role: RoleUser, Content: "turn 1"}}
	client := &budgetClient{sendErr: errors.New("engine asleep")}
	got, err := Compact(context.Background(), client, "sys", full)
	if err == nil {
		t.Fatal("want an error")
	}
	if len(got) != 1 || got[0].Content != "turn 1" {
		t.Fatalf("got = %+v, want full unchanged", got)
	}
}

func TestPrepareTurnSkipsCompactionUnderBudget(t *testing.T) {
	full := []Message{{Role: RoleUser, Content: "hi"}}
	client := &budgetClient{inputTokens: 10}
	newFull, send, err := PrepareTurn(context.Background(), client, nil, "sys", full, 5, 1000)
	if err != nil {
		t.Fatalf("PrepareTurn: %v", err)
	}
	if len(newFull) != len(full) {
		t.Fatalf("compacted when it should not have: %+v", newFull)
	}
	if len(client.gotSends) != 0 {
		t.Fatal("a summarization Send happened despite being under budget")
	}
	if len(send) != 2 { // system + the one user message
		t.Fatalf("send = %+v", send)
	}
}

func TestPrepareTurnCompactsOverBudgetThenSendsTrimmed(t *testing.T) {
	// The realistic shape (and the one that broke live): the newest, still-unanswered user
	// turn is already appended to full by the time PrepareTurn measures the budget — decision
	// 7's "the messages about to be SENT" includes it.
	full := []Message{
		{Role: RoleUser, Content: "turn 1"},
		{Role: RoleAssistant, Content: "reply 1"},
		{Role: RoleUser, Content: "turn 2"},
	}
	client := &budgetClient{inputTokens: 950, sendTurn: Turn{Content: "summary text"}}
	newFull, send, err := PrepareTurn(context.Background(), client, nil, "sys", full, 0, 1000)
	if err != nil {
		t.Fatalf("PrepareTurn: %v", err)
	}
	if len(newFull) != len(full)+1 {
		t.Fatalf("newFull = %+v, want one inserted summary", newFull)
	}
	if len(client.gotSends) != 1 {
		t.Fatalf("Send called %d times, want exactly 1 (the compaction turn)", len(client.gotSends))
	}
	// the ready-to-send slice must already reflect the new boundary (folded into the one
	// leading system message — Qwen's chat template rejects a later, second system-role
	// entry) AND still end in the pending user turn (the other live template error this
	// pins).
	wantContents := []string{"sys\n\nsummary text", "turn 2"}
	if len(send) != len(wantContents) {
		t.Fatalf("send = %+v, want %d entries", send, len(wantContents))
	}
	for i, want := range wantContents {
		if send[i].Content != want {
			t.Fatalf("send[%d].Content = %q, want %q", i, send[i].Content, want)
		}
	}
	if send[len(send)-1].Role != RoleUser {
		t.Fatalf("send ends in role %q, want %q: %+v", send[len(send)-1].Role, RoleUser, send)
	}
}

func TestPrepareTurnInputTokensErrorPropagates(t *testing.T) {
	full := []Message{{Role: RoleUser, Content: "hi"}}
	client := &budgetClient{inputTokensErr: errors.New("engine unreachable")}
	newFull, _, err := PrepareTurn(context.Background(), client, nil, "sys", full, 0, 1000)
	if err == nil {
		t.Fatal("want an error")
	}
	if len(newFull) != len(full) {
		t.Fatal("full changed despite an InputTokens error")
	}
}
