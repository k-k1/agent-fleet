package sessionx

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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
	if c.len() != 1 {
		t.Fatalf("idle entry not dropped: len = %d, want 1", c.len())
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
	for i := 0; i < 200 && mainUsageWarming.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	mainUsage.get(m, time.Now()) // the background pass or this call: either way it is cached now
	if n, _ := call(); n != 1 {
		t.Fatalf("warm call: mine=%d, want 1", n)
	}
}
