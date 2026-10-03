package sessionx

import (
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestMain turns the overview read off for the whole package: every test that builds a wire
// session would otherwise start a detached goroutine reading the real ~/.codex, opencode store
// or copilot log of the machine running the tests. The tests below that need it stub it back.
//
// It also hides a real shared codex app-server the workspace may advertise
// (AF_CODEX_APP_SERVER_ADDR): a codex Terminal launch probes it (codex/release.go), and no test
// may reach it. The ones that need a server start a fake and set the address themselves.
//
// The row links (#1062) are off too: a listed session whose working copy points at github.com
// would start a background refresh that reads the credential store and calls GitHub, and the
// port scan would attribute this machine's real listeners. session_links_test.go stubs them.
func TestMain(m *testing.M) {
	os.Exit(testguard.Run(m, func() {
		overviewFactsRead = func(session.Meta) ([]transcript.Turn, bool) { return nil, false }
		lookupPRs = func([]branchpr.Key, time.Time) map[branchpr.Key]*branchpr.PR { return nil }
		lookupPorts = func(time.Time) map[string][]int { return nil }
		_ = os.Unsetenv("AF_CODEX_APP_SERVER_ADDR")
	}))
}

func userTurn(text string) transcript.Turn { return transcript.Turn{Role: "user", Text: text} }

// TestFoldOverviewFacts pins the mirror's arithmetic (groupTurns / spendOf / latestContext):
// one point per reply, output summed, input and cache taken from the reply's last usage row.
func TestFoldOverviewFacts(t *testing.T) {
	turns := []transcript.Turn{
		userTurn("fix it"),
		// A tool round-trip and the answer: one reply, two rows.
		{Role: "assistant", Model: "gpt-x", InTok: 100, CacheRead: 900, OutTok: 10},
		// Not a person speaking (a bare tool-result row): must not split the reply.
		{Role: "user"},
		{Role: "assistant", InTok: 50, CacheRead: 1000, OutTok: 20, Text: "first\n\nanswer"},
		// A subagent's turn reports its own context and never counts.
		{Role: "assistant", Sidechain: true, InTok: 99999, OutTok: 99999, Text: "subagent"},
		userTurn("again"),
		{Role: "assistant", Model: "gpt-y", InTok: 30, CacheRead: 1200, CacheCreate: 5, OutTok: 7, CtxWindow: 272000, Text: "second"},
		userTurn("and?"),
		// A reply with no usage recorded: no point, and the fill stays the previous reply's.
		{Role: "assistant", Text: "third"},
	}
	f := foldOverviewFacts(turns)
	if want := []int{50 + 10 + 20, 30 + 5 + 7}; !reflect.DeepEqual(f.spends, want) {
		t.Errorf("spends = %v, want %v", f.spends, want)
	}
	want := &session.ContextUsage{Read: 1200, Create: 5, Fresh: 30, Model: "gpt-y", Window: 272000, WindowSource: "recorded"}
	if !reflect.DeepEqual(f.ctx, want) {
		t.Errorf("ctx = %+v, want %+v", f.ctx, want)
	}
	if f.say != "third" {
		t.Errorf("say = %q, want %q", f.say, "third")
	}
}

func TestFoldOverviewFactsEdges(t *testing.T) {
	if f := foldOverviewFacts(nil); f.ctx != nil || f.spends != nil || f.say != "" {
		t.Errorf("empty transcript: %+v", f)
	}
	// No recorded window: left for the Console to guess, never fabricated.
	f := foldOverviewFacts([]transcript.Turn{{Role: "assistant", InTok: 1, Text: "a\n  b"}})
	if f.ctx == nil || f.ctx.Window != 0 || f.ctx.WindowSource != "" {
		t.Errorf("ctx = %+v, want no window", f.ctx)
	}
	if f.say != "a b" {
		t.Errorf("say = %q, want folded to one line", f.say)
	}
	// The trend keeps the newest TokenSpendMax replies.
	var long []transcript.Turn
	for i := 1; i <= claude.TokenSpendMax+5; i++ {
		long = append(long, userTurn("q"), transcript.Turn{Role: "assistant", OutTok: i})
	}
	f = foldOverviewFacts(long)
	if len(f.spends) != claude.TokenSpendMax || f.spends[0] != 6 || f.spends[len(f.spends)-1] != claude.TokenSpendMax+5 {
		t.Errorf("spends = %v, want the newest %d", f.spends, claude.TokenSpendMax)
	}
}

// stubOverviewFacts swaps the transcript source and empties the cache for one test.
func stubOverviewFacts(t *testing.T, read func(session.Meta) ([]transcript.Turn, bool)) {
	t.Helper()
	prev := overviewFactsRead
	overviewFactsRead = read
	overviewFactsMu.Lock()
	overviewFactsCache = map[string]*overviewFactsEntry{}
	overviewFactsMu.Unlock()
	t.Cleanup(func() {
		waitOverviewFactsIdle(t)
		overviewFactsRead = prev
		overviewFactsMu.Lock()
		overviewFactsCache = map[string]*overviewFactsEntry{}
		overviewFactsMu.Unlock()
	})
}

// waitOverviewFactsIdle waits for every in-flight refresh, so no goroutine outlives the test.
func waitOverviewFactsIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		overviewFactsMu.Lock()
		for _, e := range overviewFactsCache {
			busy = busy || e.busy
		}
		overviewFactsMu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("overview facts refresh never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestOverviewFactsForCaching pins the cost rules: the poll never reads synchronously, a live
// session is re-read at most once per interval, a stopped one once per stop, and claude is
// never read here at all.
func TestOverviewFactsForCaching(t *testing.T) {
	var reads atomic.Int32
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) {
		reads.Add(1)
		return []transcript.Turn{{Role: "assistant", InTok: 10, OutTok: 2, Text: "hi"}}, true
	})
	m := session.Meta{Name: "cx", Kind: session.KindCodex}

	if f := overviewFactsFor(m, true); f.ctx != nil {
		t.Fatalf("first poll returned %+v; it must not wait for the read", f)
	}
	waitOverviewFactsIdle(t)
	f := overviewFactsFor(m, true)
	if f.ctx == nil || f.ctx.Fresh != 10 || f.say != "hi" || !reflect.DeepEqual(f.spends, []int{12}) {
		t.Fatalf("second poll = %+v, want the read's facts", f)
	}
	waitOverviewFactsIdle(t)
	if n := reads.Load(); n != 1 {
		t.Fatalf("reads = %d within the refresh interval, want 1", n)
	}

	// Past the interval a live session is read again.
	overviewFactsMu.Lock()
	overviewFactsCache["cx"].at = time.Now().Add(-overviewFactsRefresh)
	overviewFactsMu.Unlock()
	overviewFactsFor(m, true)
	waitOverviewFactsIdle(t)
	if n := reads.Load(); n != 2 {
		t.Fatalf("reads = %d after the interval, want 2", n)
	}

	// Stopping is a change the cache must see at once; after that, a stopped session is never re-read.
	overviewFactsFor(m, false)
	waitOverviewFactsIdle(t)
	overviewFactsMu.Lock()
	overviewFactsCache["cx"].at = time.Now().Add(-time.Hour)
	overviewFactsMu.Unlock()
	overviewFactsFor(m, false)
	waitOverviewFactsIdle(t)
	if n := reads.Load(); n != 3 {
		t.Fatalf("reads = %d for a stopped session, want 3 (one per stop)", n)
	}

	for _, kind := range []string{session.KindClaude, session.KindAgy, session.KindShell, session.KindSSM} {
		overviewFactsFor(session.Meta{Name: "skip-" + kind, Kind: kind}, true)
	}
	waitOverviewFactsIdle(t)
	if n := reads.Load(); n != 3 {
		t.Fatalf("reads = %d, a skipped kind was read", n)
	}
}

// TestOverviewFactsKeepOnFailedRead: an unreadable store says nothing about the conversation,
// so the card keeps what it showed rather than blanking.
func TestOverviewFactsKeepOnFailedRead(t *testing.T) {
	var fail atomic.Bool
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) {
		if fail.Load() {
			return nil, false
		}
		return []transcript.Turn{{Role: "assistant", Text: "kept"}}, true
	})
	m := session.Meta{Name: "oc", Kind: session.KindOpencode}
	overviewFactsFor(m, true)
	waitOverviewFactsIdle(t)
	fail.Store(true)
	overviewFactsMu.Lock()
	overviewFactsCache["oc"].at = time.Now().Add(-overviewFactsRefresh)
	overviewFactsMu.Unlock()
	overviewFactsFor(m, true)
	waitOverviewFactsIdle(t)
	if f := overviewFactsFor(m, true); f.say != "kept" {
		t.Fatalf("say = %q after a failed read, want the previous one", f.say)
	}
}

func TestPruneOverviewFacts(t *testing.T) {
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) { return nil, true })
	overviewFactsFor(session.Meta{Name: "keep", Kind: session.KindCodex}, false)
	overviewFactsFor(session.Meta{Name: "gone", Kind: session.KindCodex}, false)
	waitOverviewFactsIdle(t)
	pruneOverviewFacts(func(name string) bool { return name == "keep" })
	overviewFactsMu.Lock()
	defer overviewFactsMu.Unlock()
	var names []string
	for n := range overviewFactsCache {
		names = append(names, n)
	}
	if strings.Join(names, ",") != "keep" {
		t.Fatalf("cache = %v, want only keep", names)
	}
}

// TestOverviewFactsStoppedFailedReadRetried: a stopped session is read once per stop only when
// that read succeeded; a failed one is retried after the interval rather than left empty.
func TestOverviewFactsStoppedFailedReadRetried(t *testing.T) {
	var fail atomic.Bool
	var reads atomic.Int32
	fail.Store(true)
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) {
		reads.Add(1)
		if fail.Load() {
			return nil, false
		}
		return []transcript.Turn{{Role: "assistant", Text: "late"}}, true
	})
	m := session.Meta{Name: "st", Kind: session.KindCopilot}
	overviewFactsFor(m, false)
	waitOverviewFactsIdle(t)
	overviewFactsFor(m, false) // within the interval: not yet
	waitOverviewFactsIdle(t)
	if n := reads.Load(); n != 1 {
		t.Fatalf("reads = %d within the interval, want 1", n)
	}
	fail.Store(false)
	overviewFactsMu.Lock()
	overviewFactsCache["st"].at = time.Now().Add(-overviewFactsRefresh)
	overviewFactsMu.Unlock()
	overviewFactsFor(m, false)
	waitOverviewFactsIdle(t)
	if f := overviewFactsFor(m, false); f.say != "late" {
		t.Fatalf("say = %q, want the retried read's", f.say)
	}
}

// TestOverviewFactsPanicRecovered: a kind's parser panicking must neither take the Agent down
// (an unrecovered goroutine panic exits the process) nor wedge the entry or the semaphore.
func TestOverviewFactsPanicRecovered(t *testing.T) {
	var reads atomic.Int32
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) {
		if reads.Add(1) <= int32(cap(overviewFactsSem))+1 {
			panic("parser bug")
		}
		return []transcript.Turn{{Role: "assistant", Text: "after"}}, true
	})
	m := session.Meta{Name: "pn", Kind: session.KindKiro}
	// More panics than the semaphore has slots: a leaked slot would block the last read forever.
	for i := 0; i <= cap(overviewFactsSem)+1; i++ {
		overviewFactsFor(m, true)
		waitOverviewFactsIdle(t)
		overviewFactsMu.Lock()
		overviewFactsCache["pn"].at = time.Now().Add(-overviewFactsRefresh)
		overviewFactsMu.Unlock()
	}
	overviewFactsFor(m, true)
	waitOverviewFactsIdle(t)
	if f := overviewFactsFor(m, true); f.say != "after" {
		t.Fatalf("say = %q after recovered panics, want the next read's", f.say)
	}
}

// TestOverviewFactsRefreshWritesItsOwnEntry: a refresh that outlives a prune and a re-add must
// not mark the new entry idle or overwrite it.
func TestOverviewFactsRefreshWritesItsOwnEntry(t *testing.T) {
	rel1, rel2, in1 := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) {
		if first.CompareAndSwap(true, false) {
			close(in1)
			<-rel1
			return []transcript.Turn{{Role: "assistant", Text: "old"}}, true
		}
		<-rel2
		return []transcript.Turn{{Role: "assistant", Text: "new"}}, true
	})
	m := session.Meta{Name: "re", Kind: session.KindCodex}
	overviewFactsFor(m, true) // R1 starts and blocks
	<-in1
	overviewFactsMu.Lock()
	e1 := overviewFactsCache["re"]
	overviewFactsMu.Unlock()
	pruneOverviewFacts(func(string) bool { return false })
	overviewFactsFor(m, true) // a new entry; R2 starts and blocks
	overviewFactsMu.Lock()
	e2 := overviewFactsCache["re"]
	overviewFactsMu.Unlock()
	close(rel1)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		overviewFactsMu.Lock()
		done := !e1.busy
		overviewFactsMu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("R1 never finished")
		}
	}
	overviewFactsMu.Lock()
	busy, say := e2.busy, e2.facts.say
	overviewFactsMu.Unlock()
	if say != "" || !busy {
		t.Fatalf("successor entry = busy %v, say %q; the pruned entry's refresh wrote into it", busy, say)
	}
	close(rel2)
	waitOverviewFactsIdle(t)
	if f := overviewFactsFor(m, true); f.say != "new" {
		t.Fatalf("say = %q, want the successor's own read", f.say)
	}
}

// fakeLiveAgent answers WireLive with fixed facts, for the agent-fill-wins rule.
type fakeLiveAgent struct {
	agents.Agent
	li agents.LiveInfo
}

func (f fakeLiveAgent) WireLive(session.Meta, bool) agents.LiveInfo { return f.li }

// TestWireSessionAgentFactsWin: wireSession only fills what WireLive left empty — lcpp knows
// its window exactly, and a derived one must not replace it.
func TestWireSessionAgentFactsWin(t *testing.T) {
	withTempHome(t)
	own := &session.ContextUsage{Fresh: 7, Window: 4096, WindowSource: "recorded"}
	withFakeAgent(t, session.KindLcpp, fakeLiveAgent{li: agents.LiveInfo{Context: own}})
	stubOverviewFacts(t, func(session.Meta) ([]transcript.Turn, bool) {
		return []transcript.Turn{{Role: "assistant", InTok: 99, OutTok: 1, Text: "derived"}}, true
	})
	m := session.Meta{Name: "lc", Kind: session.KindLcpp, Dir: t.TempDir()}
	wireSession(m, false)
	waitOverviewFactsIdle(t)
	got := wireSession(m, false)
	if got.Context != own {
		t.Errorf("context = %+v, want the agent's own", got.Context)
	}
	if got.LastSay != "derived" || !reflect.DeepEqual(got.TokenSpends, []int{100}) {
		t.Errorf("lastSay/tokenSpends = %q/%v, want the derived ones filling the gaps", got.LastSay, got.TokenSpends)
	}
}
