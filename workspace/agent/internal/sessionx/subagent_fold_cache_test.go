package sessionx

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func foldUserLine(ts string) string {
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":"go"}}` + "\n"
}

func foldUsageLine(ts, req string, in, out int) string {
	return `{"type":"assistant","timestamp":"` + ts + `","uuid":"u-` + req + `","requestId":"` + req +
		`","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],` +
		`"usage":{"input_tokens":` + strconv.Itoa(in) + `,"output_tokens":` + strconv.Itoa(out) + `}}}` + "\n"
}

type foldFixture struct {
	m     session.Meta
	agent string
}

func newFoldFixture(t *testing.T, name string) foldFixture {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, CreatedAt: "2026-10-31T09:00:00Z"}
	agent := filepath.Join(cfg, "projects", "p1", session.UUID(m.Dir, m.Name), "subagents", "agent-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(agent), 0o700); err != nil {
		t.Fatal(err)
	}
	return foldFixture{m: m, agent: agent}
}

func (f foldFixture) write(t *testing.T, body string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(f.agent, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.agent, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func outOf(vals []cumulativeUsage) (n int) {
	for _, v := range vals {
		n += v.OutTok
	}
	return n
}

func foldCum(turns []transcript.Turn) cumulativeUsage { return AggregateUsage(turns).Cumulative }

// An agent whose size and mtime did not change is not parsed again; one that grew is.
// Without the cache both calls would parse, and the same-stamp rewrite below would show 7.
func TestSubagentFoldsReadAnUnchangedAgentOnce(t *testing.T) {
	f := newFoldFixture(t, "fold3080a")
	c := NewSubagentFolds[cumulativeUsage](time.Hour)
	now := time.Now()
	mod := now.Add(-time.Hour).Truncate(time.Second)
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 1, 5), mod)
	if got := outOf(c.Fold(f.m, now, foldCum)); got != 5 {
		t.Fatalf("first read out = %d, want 5", got)
	}
	// Same size, same mtime, other content: only a re-read would see it.
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 1, 7), mod)
	if got := outOf(c.Fold(f.m, now, foldCum)); got != 5 {
		t.Fatalf("unchanged agent was parsed again: out = %d, want the cached 5", got)
	}
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 1, 7)+
		foldUserLine("2026-10-31T09:02:00Z")+foldUsageLine("2026-10-31T09:02:01Z", "r2", 1, 3), mod)
	if got := outOf(c.Fold(f.m, now, foldCum)); got != 10 {
		t.Fatalf("a grown agent must be read again: out = %d, want 10", got)
	}
}

// A recreated session under the same name must never be served its predecessor's values.
func TestSubagentFoldsNeverServeAnotherIncarnation(t *testing.T) {
	f := newFoldFixture(t, "fold3080b")
	c := NewSubagentFolds[cumulativeUsage](time.Hour)
	now := time.Now()
	mod := now.Add(-time.Hour).Truncate(time.Second)
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 1, 5), mod)
	c.Fold(f.m, now, foldCum)
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 1, 7), mod)
	re := f.m
	re.CreatedAt = "2026-10-31T10:00:00Z"
	if got := outOf(c.Fold(re, now, foldCum)); got != 7 {
		t.Fatalf("recreated session got out = %d, want a fresh read of 7", got)
	}
}

// A failed read is not cached as an agent that spent nothing, and idle sessions are dropped.
func TestSubagentFoldsFailureAndIdle(t *testing.T) {
	f := newFoldFixture(t, "fold3080c")
	c := NewSubagentFolds[cumulativeUsage](time.Minute)
	now := time.Now()
	if err := os.MkdirAll(f.agent, 0o700); err != nil { // stat works, reading fails
		t.Fatal(err)
	}
	if got := c.Fold(f.m, now, foldCum); len(got) != 0 {
		t.Fatalf("unreadable agent produced %v", got)
	}
	if c.Len(f.m.Name) != 0 {
		t.Fatal("a failed read was cached")
	}
	if err := os.Remove(f.agent); err != nil {
		t.Fatal(err)
	}
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 1, 5), now.Add(-time.Hour))
	c.Fold(f.m, now, foldCum)
	if s, _ := c.Cached(f.m.Name, ""); !s {
		t.Fatal("session not cached")
	}
	other := f.m
	other.Name = "fold3080d"
	c.Fold(other, now.Add(2*time.Minute), foldCum)
	if s, _ := c.Cached(f.m.Name, ""); s {
		t.Fatal("idle session kept")
	}
}

// The all-sessions read goes through the cache: same figures twice, the second without a parse.
func TestAddSubagentUsageIsCachedAndStable(t *testing.T) {
	f := newFoldFixture(t, "fold3080e")
	mod := time.Now().Add(-time.Hour).Truncate(time.Second)
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 10, 5), mod)
	t.Cleanup(func() {
		subagentUsageFolds.mu.Lock()
		delete(subagentUsageFolds.m, f.m.Name)
		subagentUsageFolds.mu.Unlock()
	})
	var a, b sessionUsage
	addSubagentUsage(&a, f.m)
	f.write(t, foldUserLine("2026-10-31T09:01:00Z")+foldUsageLine("2026-10-31T09:01:01Z", "r1", 10, 9), mod)
	addSubagentUsage(&b, f.m)
	if a.Subagents == nil || b.Subagents == nil || *a.Subagents != *b.Subagents || a.Subagents.OutTok != 5 {
		t.Fatalf("subagents = %+v then %+v, want the cached 5 twice", a.Subagents, b.Subagents)
	}
}

// BenchmarkSubagentUsage measures the subagent part of the all-sessions read for one session
// with 100 agents of ~1 MiB each: cold parses every file, warm only stats them.
func BenchmarkSubagentUsage(b *testing.B) {
	cfg := b.TempDir()
	b.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m := session.Meta{Name: "bench3080", Dir: b.TempDir(), Kind: session.KindClaude, CreatedAt: "2026-10-31T09:00:00Z"}
	dir := filepath.Join(cfg, "projects", "p1", session.UUID(m.Dir, m.Name), "subagents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	var body []byte
	for i := 0; len(body) < 1<<20; i++ {
		body = append(body, foldUserLine("2026-10-31T09:01:00Z")...)
		body = append(body, foldUsageLine("2026-10-31T09:01:01Z", "r"+strconv.Itoa(i), 100, 50)...)
	}
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(dir, "agent-"+strconv.Itoa(i)+".jsonl"), body, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			NewSubagentFolds[cumulativeUsage](time.Hour).Fold(m, time.Now(), foldCum)
		}
	})
	b.Run("warm", func(b *testing.B) {
		c := NewSubagentFolds[cumulativeUsage](time.Hour)
		c.Fold(m, time.Now(), foldCum)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Fold(m, time.Now(), foldCum)
		}
	})
}
