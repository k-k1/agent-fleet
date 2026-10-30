package main

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func spendNear(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSpendOfTurnsPricesOneWayPerTurn(t *testing.T) {
	born := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return born.Add(d).Format(time.RFC3339Nano) }
	m := session.Meta{Name: "s", Kind: session.KindClaude, Model: "claude-opus-5", CreatedAt: born.Format(time.RFC3339)}
	turns := []transcript.Turn{
		// Copied from the fork source: before this session existed, never charged to it.
		{Role: "user", TS: at(-time.Hour)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 1_000_000, TS: at(-time.Hour)},
		// Estimated: 1M in × $5 + 100k out × $25 = $7.50.
		{Role: "user", TS: at(time.Minute)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 100_000, TS: at(time.Minute)},
		// Reported: the CLI's own $0.40 replaces the estimate of the same turn's tokens.
		{Role: "user", TS: at(2 * time.Minute)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 1_000_000, CostUSD: 0.40, TS: at(2 * time.Minute)},
		// The open turn counts: the hard limit is for a turn still running.
		{Role: "user", TS: at(3 * time.Minute)},
		{Role: "assistant", Model: "claude-opus-5", OutTok: 40_000, TS: at(3 * time.Minute)},
	}
	sp := spendOfTurns(m, turns)
	if want := 7.50 + 0.40 + 1.00; !spendNear(sp.USD, want) {
		t.Fatalf("spend = %v, want %v", sp.USD, want)
	}
	if !sp.Priced || !sp.Reported || sp.Unpriced {
		t.Fatalf("flags = %+v", sp)
	}
}

func TestSpendOfTurnsFlagsUnpricedTokens(t *testing.T) {
	m := session.Meta{Name: "s", Kind: session.KindMuse, CreatedAt: "bad"}
	sp := spendOfTurns(m, []transcript.Turn{
		{Role: "user"},
		{Role: "assistant", Model: "no-such-model-anywhere", InTok: 10, OutTok: 10},
	})
	if sp.USD != 0 || sp.Priced || !sp.Unpriced {
		t.Fatalf("an unpriced model must be flagged, not read as $0 priced: %+v", sp)
	}
	if sp := spendOfTurns(m, nil); sp.Priced || sp.Unpriced {
		t.Fatalf("no turns = nothing measured: %+v", sp)
	}
}

// CreatedAt keeps whole seconds; a fork made late in the second its copied history ended would
// be charged that history if the cut were CreatedAt (review of #1652). SpendFrom keeps the
// sub-second instant the fork was made.
func TestSpendOfTurnsExcludesHistoryCopiedInTheForksOwnSecond(t *testing.T) {
	sec := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	m := session.Meta{Name: "f", Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: sec.Format(time.RFC3339),
		SpendFrom: sec.Add(900 * time.Millisecond).Format(time.RFC3339Nano)}
	turns := []transcript.Turn{
		// The source's expensive turn, ended 0.1 s into the same second, copied into the fork.
		{Role: "user", TS: sec.Add(50 * time.Millisecond).Format(time.RFC3339Nano)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, OutTok: 1_000_000,
			TS: sec.Add(100 * time.Millisecond).Format(time.RFC3339Nano)},
		// The fork's own turn: 1M in × $5 = $5.
		{Role: "user", TS: sec.Add(5 * time.Second).Format(time.RFC3339Nano)},
		{Role: "assistant", Model: "claude-opus-5", InTok: 1_000_000, TS: sec.Add(6 * time.Second).Format(time.RFC3339Nano)},
	}
	if sp := spendOfTurns(m, turns); !spendNear(sp.USD, 5) {
		t.Fatalf("spend = %v, want only the fork's own $5", sp.USD)
	}
}

// claudeUsageLine is one assistant record in claude's own transcript shape, carrying usage.
func claudeUsageLine(ts, req string, in, out int) string {
	return `{"type":"assistant","timestamp":"` + ts + `","uuid":"u-` + req + `","requestId":"` + req +
		`","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],` +
		`"usage":{"input_tokens":` + strconv.Itoa(in) + `,"output_tokens":` + strconv.Itoa(out) + `}}}` + "\n"
}

func claudeUserLine(ts string) string {
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":"go"}}` + "\n"
}

// Claude writes subagent and Workflow-agent transcripts beside the session's own and never
// into it (#3051): a budget priced from the main transcript alone never stopped a session whose
// work ran in subagents.
func TestSessionSpendCountsSubagentTranscripts(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	born := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return born.Add(d).Format(time.RFC3339Nano) }
	m := session.Meta{Name: "sub3051", Dir: t.TempDir(), Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: born.Format(time.RFC3339)}
	sid := session.UUID(m.Dir, m.Name)
	proj := filepath.Join(cfg, "projects", "p1")
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The main thread: 1M in × $5 = $5.
	write(filepath.Join(proj, sid+".jsonl"), claudeUserLine(at(0))+claudeUsageLine(at(time.Minute), "r1", 1_000_000, 0))
	// A subagent: two calls, each re-reporting its own 1M context = $10.
	agent := filepath.Join(proj, sid, "subagents", "agent-a1.jsonl")
	write(agent, claudeUserLine(at(2*time.Minute))+claudeUsageLine(at(3*time.Minute), "a1", 1_000_000, 0)+
		claudeUserLine(at(4*time.Minute))+claudeUsageLine(at(5*time.Minute), "a2", 1_000_000, 0))
	// A Workflow agent, finished before the subagent's first call: 100k out × $25 = $2.50.
	write(filepath.Join(proj, sid, "subagents", "workflows", "wf_x", "agent-b1.jsonl"),
		claudeUserLine(at(2*time.Minute))+claudeUsageLine(at(150*time.Second), "b1", 0, 100_000))

	sp := sessionSpend(m)
	if want := 5 + 10 + 2.50; !spendNear(sp.USD, want) {
		t.Fatalf("spend = %v, want %v (main + subagent + Workflow agent)", sp.USD, want)
	}
	for i := 1; i < len(sp.Marks); i++ {
		if sp.Marks[i].End.Before(sp.Marks[i-1].End) {
			t.Fatalf("marks out of order at %d: %+v", i, sp.Marks)
		}
	}
	// The crossing bound is the end of the last turn under the cap, in time order across the
	// files: $5 (main) + $2.50 (Workflow agent) stays under $12, the subagent's first call
	// crosses it.
	if got := session.SpendCrossingBound(sp, 12, born, born.Add(10*time.Minute)); !got.Equal(born.Add(150 * time.Second)) {
		t.Fatalf("crossing bound = %v, want the end of the Workflow agent's call", got)
	}

	// A running agent's transcript grows; the cached fold of it must not hide that.
	f, err := os.OpenFile(agent, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(claudeUserLine(at(7*time.Minute)) + claudeUsageLine(at(8*time.Minute), "a3", 1_000_000, 0))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sp := sessionSpend(m); !spendNear(sp.USD, 22.50) {
		t.Fatalf("spend after the agent grew = %v, want 22.50", sp.USD)
	}

	// An agent transcript that cannot be read is not cached as an agent that spent nothing.
	unreadable := filepath.Join(proj, sid, "subagents", "agent-z.jsonl")
	if err := os.MkdirAll(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	if sp := sessionSpend(m); !spendNear(sp.USD, 22.50) {
		t.Fatalf("spend with an unreadable agent = %v, want 22.50", sp.USD)
	}
	subagentRowsCache.Lock()
	_, cached := subagentRowsCache.m[m.Name].agents[unreadable]
	subagentRowsCache.Unlock()
	if cached {
		t.Fatal("a failed read was cached; the agent would never be read again")
	}
}

// A session nobody prices any more (stopped, archived, deleted) must not hold its folded rows
// until the Agent restarts.
func TestSubagentRowsCacheDropsIdleSessions(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	meta := func(name string) session.Meta {
		m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, CreatedAt: now.Format(time.RFC3339)}
		p := filepath.Join(cfg, "projects", "p1", session.UUID(m.Dir, m.Name), "subagents", "agent-a.jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(claudeUsageLine(now.Format(time.RFC3339), "r", 1, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		return m
	}
	gone, live := meta("idle3051a"), meta("idle3051b")
	subagentSpendRowsAt(gone, now)
	subagentSpendRowsAt(live, now.Add(subagentRowsIdle+time.Minute))
	subagentRowsCache.Lock()
	_, kept := subagentRowsCache.m[gone.Name]
	_, livePresent := subagentRowsCache.m[live.Name]
	subagentRowsCache.Unlock()
	if kept || !livePresent {
		t.Fatalf("idle session kept=%v, live session present=%v; want false, true", kept, livePresent)
	}
}

// A copy of an agent that cannot be read must not freeze the agent's spend at what the cache
// last held while another copy keeps growing; the partial figure is used and not cached.
func TestSessionSpendUsesReadableCopiesOfAnAgent(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	born := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	m := session.Meta{Name: "copies3051", Dir: t.TempDir(), Kind: session.KindClaude, Model: "claude-opus-5",
		CreatedAt: born.Format(time.RFC3339)}
	sid := session.UUID(m.Dir, m.Name)
	broken := filepath.Join(cfg, "projects", "p1", sid, "subagents", "agent-a.jsonl")
	if err := os.MkdirAll(broken, 0o700); err != nil { // stat works, reading fails
		t.Fatal(err)
	}
	good := filepath.Join(cfg, "projects", "p2", sid, "subagents", "agent-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(good), 0o700); err != nil {
		t.Fatal(err)
	}
	ts := born.Add(time.Minute).Format(time.RFC3339Nano)
	if err := os.WriteFile(good, []byte(claudeUserLine(ts)+claudeUsageLine(ts, "g1", 1_000_000, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if sp := sessionSpend(m); !spendNear(sp.USD, 5) {
		t.Fatalf("spend = %v, want the readable copy's $5", sp.USD)
	}
	subagentRowsCache.Lock()
	n := len(subagentRowsCache.m[m.Name].agents)
	subagentRowsCache.Unlock()
	if n != 0 {
		t.Fatal("a partial read was cached as complete")
	}
}
