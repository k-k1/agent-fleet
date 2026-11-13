package sessionx

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func mainFixture(t *testing.T, name string) (session.Meta, func(body string, mod time.Time)) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, CreatedAt: "2026-11-11T09:00:00Z"}
	path := filepath.Join(cfg, "projects", "p1", session.UUID(m.Dir, m.Name)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return m, func(body string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

func mainBody(out int, extra bool) string {
	b := foldUserLine("2026-11-11T09:01:00Z") + foldUsageLine("2026-11-11T09:01:01Z", "r1", 1, out)
	if extra {
		b += foldUserLine("2026-11-11T09:02:00Z") + foldUsageLine("2026-11-11T09:02:01Z", "r2", 1, 3)
	}
	return b
}

// An unchanged transcript is parsed once; a grown or rewritten one (new stamp) is parsed again.
// The same-stamp rewrite shows the cache is what answered: only a parse would see 7.
func TestMainUsageCacheKeyedByStamp(t *testing.T) {
	m, write := mainFixture(t, "mainusage3885a")
	c := newMainUsageCache(time.Hour)
	now := time.Now()
	mod := now.Add(-time.Hour).Truncate(time.Second)
	write(mainBody(5, false), mod)
	if got := c.get(m, now).Cumulative.OutTok; got != 5 {
		t.Fatalf("first read out = %d, want 5", got)
	}
	write(mainBody(7, false), mod) // same size, same mtime
	if got := c.get(m, now).Cumulative.OutTok; got != 5 {
		t.Fatalf("unchanged transcript was parsed again: out = %d, want cached 5", got)
	}
	write(mainBody(7, true), mod) // grown
	if got := c.get(m, now).Cumulative.OutTok; got != 10 {
		t.Fatalf("grown transcript served stale: out = %d, want 10", got)
	}
	write(mainBody(9, true), mod.Add(time.Second)) // same size, newer mtime
	if got := c.get(m, now).Cumulative.OutTok; got != 12 {
		t.Fatalf("rewritten transcript served stale: out = %d, want 12", got)
	}
	// A recreated name never sees its predecessor's figure.
	m.CreatedAt = "2026-11-11T10:00:00Z"
	write(mainBody(1, false), mod.Add(time.Second))
	if got := c.get(m, now).Cumulative.OutTok; got != 1 {
		t.Fatalf("recreated session served old figure: out = %d", got)
	}
}

// Caller mutation of the returned Context must not reach the cache.
func TestMainUsageCacheReturnsACopy(t *testing.T) {
	m, write := mainFixture(t, "mainusage3885b")
	c := newMainUsageCache(time.Hour)
	now := time.Now()
	write(mainBody(5, false), now.Add(-time.Hour))
	a := c.get(m, now)
	if a.Context == nil {
		t.Fatal("no context")
	}
	a.Context.Tokens = -1
	if b := c.get(m, now); b.Context.Tokens == -1 {
		t.Fatal("cache shares the Context pointer with callers")
	}
}

// A session nobody reads any more (deleted, archived) leaves the cache.
func TestMainUsageCacheDropsIdleSessions(t *testing.T) {
	m, write := mainFixture(t, "mainusage3885c")
	c := newMainUsageCache(time.Minute)
	now := time.Now()
	write(mainBody(5, false), now.Add(-time.Hour))
	c.get(m, now)
	if c.len() != 1 {
		t.Fatalf("len = %d, want 1", c.len())
	}
	other, write2 := mainFixture(t, "mainusage3885d")
	write2(mainBody(5, false), now.Add(-time.Hour))
	c.get(other, now.Add(2*time.Minute))
	c.mu.Lock()
	_, hasOld := c.m[m.Name]
	_, hasNew := c.m[other.Name]
	c.mu.Unlock()
	if hasOld || !hasNew {
		t.Fatalf("idle entry not dropped: old=%v new=%v, want false and true", hasOld, hasNew)
	}
}

func TestMainUsageCacheConcurrent(t *testing.T) {
	m, write := mainFixture(t, "mainusage3885e")
	c := newMainUsageCache(time.Hour)
	write(mainBody(5, true), time.Now().Add(-time.Hour))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if got := c.get(m, time.Now()).Cumulative.OutTok; got != 8 {
					t.Errorf("out = %d, want 8", got)
				}
			}
		}()
	}
	wg.Wait()
}

// Past the deadline an uncached session is not parsed (ok=false), a cached one still answers.
func TestMainUsageCacheDeadline(t *testing.T) {
	m, write := mainFixture(t, "mainusage3885f")
	c := newMainUsageCache(time.Hour)
	write(mainBody(5, false), time.Now().Add(-time.Hour))
	past := time.Now().Add(-time.Second)
	if _, ok := c.getBy(m, time.Now(), past); ok {
		t.Fatal("uncached session was parsed past the deadline")
	}
	c.get(m, time.Now())
	if u, ok := c.getBy(m, time.Now(), past); !ok || u.Cumulative.OutTok != 5 {
		t.Fatalf("cached session must answer past the deadline: ok=%v out=%d", ok, u.Cumulative.OutTok)
	}
}

// The all-sessions read answers within its budget on an empty cache: sessions it could not parse
// in time are counted as pending instead of delaying the answer, and the next read has them.
func TestHandleSessionsUsagePendingPastBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, write := mainFixture(t, "mainusage3885g")
	write(mainBody(5, false), time.Now().Add(-time.Hour))
	session.WriteMeta(m)
	old, oldFold := mainUsageColdBudget, deps.MaybeFoldSessionUsage
	deps.MaybeFoldSessionUsage = func() {} // the ledger fold is not what is under test
	t.Cleanup(func() {
		mainUsageColdBudget, deps.MaybeFoldSessionUsage = old, oldFold
		mainUsage.mu.Lock()
		delete(mainUsage.m, m.Name)
		mainUsage.mu.Unlock()
	})

	mainUsageColdBudget = -time.Second
	call := func() (mine, pending int) {
		rec := httptest.NewRecorder()
		HandleSessionsUsage(rec, httptest.NewRequest("GET", "/sessions/usage", nil))
		var body struct {
			Sessions []sessionUsage `json:"sessions"`
			Pending  int            `json:"pending"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, u := range body.Sessions {
			if u.Name == m.Name {
				mine++
			}
		}
		return mine, body.Pending
	}
	// Other tests' sessions may share the state dir, so only this session is counted.
	if n, p := call(); n != 0 || p < 1 {
		t.Fatalf("cold call past budget: mine=%d pending=%d, want 0 and >=1", n, p)
	}
	// Only the background pass the cold call kicked off can fill the cache now.
	for i := 0; i < 500; i++ {
		mainUsage.mu.Lock()
		_, cached := mainUsage.m[m.Name]
		mainUsage.mu.Unlock()
		if cached {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A spent budget answers nothing at all, so the warm call gets a real one.
	mainUsageColdBudget = 5 * time.Second
	if n, _ := call(); n != 1 {
		t.Fatalf("warm call: mine=%d, want 1", n)
	}
}

// Not cached (no stamp, or a read that may be stale) means never stored.
func TestMainUsageCacheSkipsUnstampedAndPartialReads(t *testing.T) {
	m := session.Meta{Name: "mainusage3885h", CreatedAt: "2026-11-11T09:00:00Z"}
	c := newMainUsageCache(time.Hour)
	turnsOf := func(out int) []transcript.Turn {
		return []transcript.Turn{{Role: "user"}, {Role: "assistant", OutTok: out}}
	}
	c.sourceOf = func(session.Meta) (string, func() ([]transcript.Turn, bool)) {
		return "", func() ([]transcript.Turn, bool) { return turnsOf(4), true }
	}
	if got := c.get(m, time.Now()).Cumulative.OutTok; got != 4 || c.len() != 0 {
		t.Fatalf("unstamped: out=%d len=%d, want 4 and 0", got, c.len())
	}
	c.sourceOf = func(session.Meta) (string, func() ([]transcript.Turn, bool)) {
		return "S", func() ([]transcript.Turn, bool) { return turnsOf(4), false }
	}
	if got := c.get(m, time.Now()).Cumulative.OutTok; got != 4 || c.len() != 0 {
		t.Fatalf("partial read: out=%d len=%d, want 4 and 0", got, c.len())
	}
}

// Past the budget an uncached session must not even be stamped (the disk reads are the cost).
func TestMainUsageCachePastDeadlineDoesNoSourceRead(t *testing.T) {
	m := session.Meta{Name: "mainusage3885i", CreatedAt: "2026-11-11T09:00:00Z"}
	c := newMainUsageCache(time.Hour)
	calls := 0
	c.sourceOf = func(session.Meta) (string, func() ([]transcript.Turn, bool)) {
		calls++
		return "S", func() ([]transcript.Turn, bool) { return nil, true }
	}
	if _, ok := c.getBy(m, time.Now(), time.Now().Add(-time.Second)); ok || calls != 0 {
		t.Fatalf("ok=%v source reads=%d, want false and 0", ok, calls)
	}
}

// Two parses overlap: the one that took its stamp later keeps its figure when the older, slower
// one finishes last, and the idle clock never moves backwards.
func TestMainUsageCacheLaterStampWinsAndUsedNeverRewinds(t *testing.T) {
	m := session.Meta{Name: "mainusage3885j", CreatedAt: "2026-11-11T09:00:00Z"}
	c := newMainUsageCache(time.Hour)
	release, started := make(chan struct{}), make(chan struct{})
	n := 0
	c.sourceOf = func(session.Meta) (string, func() ([]transcript.Turn, bool)) {
		n++
		if n == 1 { // the slow parse of the older stamp
			return "S1", func() ([]transcript.Turn, bool) {
				close(started)
				<-release
				return []transcript.Turn{{Role: "assistant", OutTok: 1}}, true
			}
		}
		return "S2", func() ([]transcript.Turn, bool) {
			return []transcript.Turn{{Role: "assistant", OutTok: 2}}, true
		}
	}
	t0 := time.Now()
	done := make(chan struct{})
	go func() { c.get(m, t0); close(done) }()
	<-started
	c.get(m, t0.Add(10*time.Second))
	close(release)
	<-done
	c.mu.Lock()
	e := c.m[m.Name]
	c.mu.Unlock()
	if e.stamp != "S2" || e.val.Cumulative.OutTok != 2 {
		t.Fatalf("stale parse overwrote the newer entry: stamp=%s out=%d", e.stamp, e.val.Cumulative.OutTok)
	}
	if !e.used.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("used rewound to %v", e.used)
	}
}

// Concurrent callers of one session and stamp share a single parse.
func TestMainUsageCacheSharesOneParse(t *testing.T) {
	m := session.Meta{Name: "mainusage3885k", CreatedAt: "2026-11-11T09:00:00Z"}
	c := newMainUsageCache(time.Hour)
	var parses atomic.Int32
	release := make(chan struct{})
	c.sourceOf = func(session.Meta) (string, func() ([]transcript.Turn, bool)) {
		return "S", func() ([]transcript.Turn, bool) {
			parses.Add(1)
			<-release
			return []transcript.Turn{{Role: "assistant", OutTok: 3}}, true
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := c.get(m, time.Now()).Cumulative.OutTok; got != 3 {
				t.Errorf("out = %d, want 3", got)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if p := parses.Load(); p != 1 {
		t.Fatalf("parses = %d, want 1", p)
	}
}
