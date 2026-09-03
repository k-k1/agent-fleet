package sessionx

import (
	"testing"

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
