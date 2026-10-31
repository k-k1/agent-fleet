package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// cacheFixture is n claude sessions, each with one closed turn in its transcript.
func cacheFixture(t *testing.T, n int) []session.Meta {
	t.Helper()
	useTempUsageDir(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dropUsageStateCache()
	t.Cleanup(dropUsageStateCache)
	born := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return born.Add(d).Format(time.RFC3339Nano) }
	var ms []session.Meta
	for i := 0; i < n; i++ {
		m := session.Meta{Name: fmt.Sprintf("cache%03d", i), Dir: t.TempDir(), Kind: session.KindClaude,
			Model: "claude-opus-5", CreatedAt: born.Format(time.RFC3339)}
		p := filepath.Join(cfg, "projects", "p1", session.UUID(m.Dir, m.Name)+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		body := claudeUserLine(at(0)) + claudeUsageLine(at(time.Minute), fmt.Sprintf("r%d", i), 1000, 10) + claudeUserLine(at(2*time.Minute))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ms = append(ms, m)
	}
	return ms
}

// cachedState is what the next commit would be handed, and puts it back.
func cachedState() usageFoldState {
	usageFoldMu.Lock()
	defer usageFoldMu.Unlock()
	st, l := loadUsageFoldStateLocked()
	releaseUsageFoldStateLocked(st, l)
	return *st
}

func commitAll(t *testing.T, ms []session.Meta) {
	t.Helper()
	for _, m := range ms {
		if _, err := commitSessionUsageFold(m, false); err != nil {
			t.Fatal(err)
		}
	}
}

// One pass over N sessions decodes state.json once, not once per session (#3115).
func TestFoldPassDecodesStateOnce(t *testing.T) {
	ms := cacheFixture(t, 25)
	commitAll(t, ms) // the first pass writes the file
	before := usageStateDecodes.Load()
	commitAll(t, ms)
	commitAll(t, ms)
	if got := usageStateDecodes.Load() - before; got > 1 {
		t.Fatalf("two unchanged passes over %d sessions decoded state.json %d times", len(ms), got)
	}
	if n := len(readUsageFoldState().Sessions); n != len(ms) {
		t.Fatalf("state holds %d sessions, want %d", n, len(ms))
	}
}

// The cached value is the file's content after every kind of commit.
func TestFoldStateCacheMatchesDisk(t *testing.T) {
	ms := cacheFixture(t, 6)
	commitAll(t, ms)
	cached := cachedState()
	if disk := readUsageFoldState(); !reflect.DeepEqual(cached, disk) {
		t.Fatalf("cache differs from disk:\n cache %+v\n disk  %+v", cached, disk)
	}
	finalizeSessionUsage(ms[0])
	cached = cachedState()
	if _, ok := cached.Sessions[ms[0].Name]; ok {
		t.Fatal("finalize left the deleted session's mark in the cache")
	}
	if disk := readUsageFoldState(); !reflect.DeepEqual(cached, disk) {
		t.Fatal("cache differs from disk after finalize")
	}
}

// A failed write must not leave the cache claiming the new state.
func TestFoldStateCacheDroppedOnFailedWrite(t *testing.T) {
	ms := cacheFixture(t, 3)
	commitAll(t, ms[:2])
	if err := os.Mkdir(usageStatePath()+".tmp", 0o700); err != nil { // the write cannot create its temp file
		t.Fatal(err)
	}
	if _, err := commitSessionUsageFold(ms[2], false); err == nil {
		t.Fatal("the write was meant to fail")
	}
	if err := os.Remove(usageStatePath() + ".tmp"); err != nil {
		t.Fatal(err)
	}
	got := cachedState()
	if _, ok := got.Sessions[ms[2].Name]; ok {
		t.Fatal("the cache kept a mark whose write failed")
	}
	if !reflect.DeepEqual(got, readUsageFoldState()) {
		t.Fatal("cache differs from disk after a failed write")
	}
}

// A state.json changed from outside is read again, and a caller of readUsageFoldState cannot
// reach the cache through what it was given.
func TestFoldStateCacheValidatedAndNotAliased(t *testing.T) {
	ms := cacheFixture(t, 3)
	commitAll(t, ms)
	mine := readUsageFoldState()
	mark := mine.Sessions[ms[0].Name]
	mark.Groups = 999
	mark.OrphanKeys = append(mark.OrphanKeys, "x")
	mine.Sessions[ms[0].Name] = mark
	cached := cachedState().Sessions[ms[0].Name]
	if cached.Groups == 999 || len(cached.OrphanKeys) != 0 {
		t.Fatal("a value from readUsageFoldState aliases the cache")
	}

	// An outside edit (here: drop one mark) is noticed through size and mtime.
	edit := readUsageFoldState()
	delete(edit.Sessions, ms[1].Name)
	if err := writeUsageFoldState(edit); err != nil {
		t.Fatal(err)
	}
	_, stale := cachedState().Sessions[ms[1].Name]
	if stale {
		t.Fatal("the cache served a state.json that changed under it")
	}
	if err := os.WriteFile(usageStatePath(), []byte("{\"sessions\":{}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := len(cachedState().Sessions)
	if n != 0 {
		t.Fatalf("cache served %d sessions after the file was replaced", n)
	}
}

// Measurement for the PR (AF_MEASURE_USAGE_STATE=1 go test -run TestMeasureUsageStateSize -v): how big state.json gets and
// what decoding it costs; the fold used to pay this once per session per pass.
func TestMeasureUsageStateSize(t *testing.T) {
	if os.Getenv("AF_MEASURE_USAGE_STATE") == "" {
		t.Skip("measurement, no assertions: set AF_MEASURE_USAGE_STATE=1")
	}
	useTempUsageDir(t)
	dropUsageStateCache()
	t.Cleanup(dropUsageStateCache)
	const sessions, agentsPer = 1400, 3
	st := usageFoldState{Sessions: map[string]usageFoldMark{}, Subagents: map[string]usageAgentMark{}}
	for i := 0; i < sessions; i++ {
		name := fmt.Sprintf("session-%04d", i)
		st.Sessions[name] = usageFoldMark{Groups: 40, LastTS: "2026-10-30T09:02:00.123456789Z", LastIdx: 812, Uncapped: true,
			Stamp: "0123456789abcdef"}
		for a := 0; a < agentsPer; a++ {
			key := subagentMarkKey(session.Meta{Name: name}, fmt.Sprintf("/subagents/agent-%016x.jsonl", i*10+a))
			if a == 0 { // one still active: per-event hashed ids
				st.Subagents[key] = usageAgentMark{Seq: 6, Stamp: "1234\x005678", Turns: []usageAgentTurn{
					{IDs: []string{"0123456789abcdef", "fedcba9876543210"}, In: 1000, Out: 10, CacheRead: 5000, CostUSD: 0.01},
					{IDs: []string{"0123456789abcde0"}, In: 1200, Out: 20, CacheRead: 5000, CostUSD: 0.02},
				}}
				continue
			}
			st.Subagents[key] = usageAgentMark{Seq: 6, Stamp: "1234\x005678", Total: &usageAgentTurn{In: 9000, Out: 90, CacheRead: 40000, CostUSD: 0.2}}
		}
	}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(usageStatePath())
	start := time.Now()
	const reps = 20
	for i := 0; i < reps; i++ {
		readUsageFoldState()
	}
	per := time.Since(start) / reps
	t.Logf("%d sessions + %d agent marks: state.json %d bytes; one decode %v; one pass of %d sessions: %.1f MB read, %v decoding (cache: one decode, then %d Stats)",
		sessions, len(st.Subagents), fi.Size(), per, sessions, float64(fi.Size())*sessions/1e6, per*sessions, sessions)
}

// A commit that fails part-way (here: a panic inside the fold) leaves no cached state behind, so
// the next session decodes the file instead of persisting a half-mutated value.
func TestFoldStateCacheNotReturnedAfterPanic(t *testing.T) {
	ms := cacheFixture(t, 2)
	commitAll(t, ms)
	orig := foldUsageSource
	defer func() { foldUsageSource = orig }()
	foldUsageSource = func(session.Meta) (string, func() ([]transcript.Turn, bool)) { panic("boom") }
	func() {
		defer func() { _ = recover() }()
		_, _ = commitSessionUsageFold(ms[0], true) // includeTrailing: never skipped
	}()
	if usageStateCache.st != nil {
		t.Fatal("a commit that panicked left its state in the cache")
	}
}

// The cache lives for one pass.
func TestFoldAllSessionsDropsCache(t *testing.T) {
	ms := cacheFixture(t, 2)
	commitAll(t, ms)
	if usageStateCache.st == nil {
		t.Fatal("setup: nothing cached")
	}
	foldAllSessions(false)
	if usageStateCache.st != nil {
		t.Fatal("the decoded state outlived the pass")
	}
}
