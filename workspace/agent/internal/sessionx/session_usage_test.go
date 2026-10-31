package sessionx

import (
	"fmt"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestAggregateUsage pins the fold semantics against the Console's grouping: within
// a logical turn output ACCUMULATES while input/cache snapshots REPLACE (each event
// re-reports the whole context); user turns and sidechain flips break turns; the
// current context is the LAST main-chain snapshot, never a sidechain's.
func TestAggregateUsage(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user"},
		// Turn 1: two events — the second's input snapshot supersedes the first's.
		{Role: "assistant", Model: "claude-fable-5", InTok: 10, CacheRead: 100, CacheCreate: 5, OutTok: 20},
		{Role: "assistant", Model: "claude-fable-5", InTok: 12, CacheRead: 130, CacheCreate: 6, OutTok: 30},
		{Role: "user"},
		// Turn 2: a sidechain (subagent) runs inside the same reply…
		{Role: "assistant", Sidechain: true, InTok: 999, CacheRead: 1, CacheCreate: 1, OutTok: 40},
		// …then the main chain resumes: its snapshot is the session's context.
		{Role: "assistant", Model: "claude-fable-5", InTok: 20, CacheRead: 200, CacheCreate: 8, OutTok: 50},
	}
	u := AggregateUsage(turns)

	// 3 logical turns: [1st main], [sidechain], [2nd main].
	if u.Cumulative.Turns != 3 {
		t.Fatalf("turns = %d, want 3", u.Cumulative.Turns)
	}
	// InTok: 12 (turn1 last snapshot) + 999 (sidechain) + 20 (turn2) — not 10+12.
	if u.Cumulative.InTok != 12+999+20 {
		t.Fatalf("inTok = %d, want %d", u.Cumulative.InTok, 12+999+20)
	}
	if u.Cumulative.OutTok != 20+30+40+50 {
		t.Fatalf("outTok = %d, want %d", u.Cumulative.OutTok, 20+30+40+50)
	}
	if u.Cumulative.CacheRead != 130+1+200 {
		t.Fatalf("cacheRead = %d, want %d", u.Cumulative.CacheRead, 130+1+200)
	}
	// Spend = Σ per-turn (in + create + out): (12+6+50) + (999+1+40) + (20+8+50).
	wantSpend := (12 + 6 + 50) + (999 + 1 + 40) + (20 + 8 + 50)
	if u.Cumulative.Spend != wantSpend {
		t.Fatalf("spend = %d, want %d", u.Cumulative.Spend, wantSpend)
	}

	// Context = last MAIN-chain snapshot (20+200+8), not the sidechain's 999+1+1.
	if u.Context == nil {
		t.Fatal("context missing")
	}
	if u.Context.Tokens != 20+200+8 || u.Context.Fresh != 20 || u.Context.Read != 200 || u.Context.Create != 8 {
		t.Fatalf("context = %+v", u.Context)
	}
	// fable-5 → 1M estimated window.
	if u.Context.Window != 1_000_000 || u.Context.WindowSource != "estimated" {
		t.Fatalf("window = %d (%s), want 1000000 (estimated)", u.Context.Window, u.Context.WindowSource)
	}
	if u.Context.Pct <= 0 || u.Context.Pct > 100 {
		t.Fatalf("pct = %v", u.Context.Pct)
	}
}

// TestAggregateUsageRecordedWindow: an agent-recorded window (codex
// model_context_window) beats the model-name guess.
func TestAggregateUsageRecordedWindow(t *testing.T) {
	u := AggregateUsage([]transcript.Turn{
		{Role: "assistant", Model: "gpt-5.6-terra", CtxWindow: 400_000, InTok: 1000, OutTok: 5},
	})
	if u.Context == nil || u.Context.Window != 400_000 || u.Context.WindowSource != "recorded" {
		t.Fatalf("context = %+v, want recorded 400000", u.Context)
	}
}

// TestAggregateUsageEmpty: a fresh session (no assistant reply yet) has no context
// block and zero cumulative — not an error.
func TestAggregateUsageEmpty(t *testing.T) {
	u := AggregateUsage([]transcript.Turn{{Role: "user"}})
	if u.Context != nil || u.Cumulative.Turns != 0 || u.Cumulative.Spend != 0 {
		t.Fatalf("usage = %+v", u)
	}
}

// A mistyped slash command leaves only CLI warning records (claude.IsCLIWarning). They are no
// model response, so a session that has run nothing else reports zero turns, and one landing
// inside a reply does not split it.
func TestAggregateUsageSkipsCLIWarnings(t *testing.T) {
	warn := []byte(`{"type":"system","subtype":"informational","level":"warning","content":"Unknown command: /x"}`)
	only := claude.CollectTurns([][]byte{warn, warn}, 0, 2)
	if len(only) != 2 {
		t.Fatalf("fixture: %d turns, want the 2 warning turns", len(only))
	}
	if u := AggregateUsage(only); u.Cumulative.Turns != 0 || u.Context != nil {
		t.Fatalf("warnings only: usage = %+v, want zero", u)
	}
	mixed := append([]transcript.Turn{
		{Role: "user"},
		{Role: "assistant", Model: "claude-fable-5", InTok: 10, OutTok: 20},
	}, only[0], transcript.Turn{Role: "assistant", Model: "claude-fable-5", InTok: 12, OutTok: 30})
	if u := AggregateUsage(mixed); u.Cumulative.Turns != 1 || u.Cumulative.OutTok != 50 {
		t.Fatalf("warning mid-reply: turns = %d, outTok = %d, want 1 and 50", u.Cumulative.Turns, u.Cumulative.OutTok)
	}
}

// Subagent transcripts add to the cumulative figure and are reported on their own too, each
// agent folded alone: folding two agents together would keep one agent's input snapshot.
// The context fill stays the main thread's (#3051).
func TestAddSubagentUsage(t *testing.T) {
	u := AggregateUsage([]transcript.Turn{
		{Role: "user"},
		{Role: "assistant", Model: "claude-fable-5", InTok: 10, CacheCreate: 5, OutTok: 20},
	})
	ctx := *u.Context
	addSubagentCumulative(&u, []cumulativeUsage{
		AggregateUsage([]transcript.Turn{{Role: "assistant", Sidechain: true, InTok: 100, CacheRead: 7, OutTok: 1}}).Cumulative,
		AggregateUsage([]transcript.Turn{{Role: "assistant", Sidechain: true, InTok: 200, CacheCreate: 3, OutTok: 2}}).Cumulative,
	})
	want := cumulativeUsage{Turns: 2, InTok: 300, OutTok: 3, CacheRead: 7, CacheCreate: 3, Spend: 306}
	if u.Subagents == nil || *u.Subagents != want {
		t.Fatalf("subagents = %+v, want %+v", u.Subagents, want)
	}
	if c := u.Cumulative; c.Turns != 3 || c.InTok != 310 || c.OutTok != 23 || c.Spend != 35+306 {
		t.Fatalf("cumulative = %+v, want the main thread plus every agent", c)
	}
	if *u.Context != ctx {
		t.Fatalf("context = %+v, want the main thread's %+v", *u.Context, ctx)
	}

	none := AggregateUsage(nil)
	addSubagentCumulative(&none, nil)
	if none.Subagents != nil {
		t.Fatalf("a session that delegated nothing must not report a subagents field: %+v", none.Subagents)
	}
}

// A transcript with more than 1 MiB of assistant text: the usage turns cover all of it, so the
// cumulative spend is the sum of the parts, where the chat view's capped parse loses the head.
func TestAggregateUsageOverCapTranscript(t *testing.T) {
	big := strings.Repeat("x", 100<<10)
	var lines [][]byte
	const n, in, out = 30, 50, 7
	for i := 0; i < n; i++ {
		lines = append(lines,
			[]byte(`{"type":"user","message":{"content":"q"}}`),
			[]byte(fmt.Sprintf(`{"type":"assistant","requestId":"r%d","message":{"usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"text","text":"%s"}]}}`, i, in, out, big)),
		)
	}
	want := n * (in + out)
	if got := AggregateUsage(claude.CollectUsageTurns(lines)).Cumulative; got.Turns != n || got.Spend != want {
		t.Errorf("uncapped: turns=%d spend=%d, want %d / %d", got.Turns, got.Spend, n, want)
	}
	if got := AggregateUsage(claude.CollectTurns(lines, 0, len(lines))).Cumulative; got.Spend >= want {
		t.Errorf("fixture does not exceed the chat cap: capped spend=%d", got.Spend)
	}
}
