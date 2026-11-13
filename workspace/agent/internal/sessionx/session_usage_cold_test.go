package sessionx

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// coldUsageFixture gives every session a FIFO for one transcript: reading it blocks until the test
// lets go, which is a disk or network stall no deadline check between files can cut short.
// subagent picks which transcript stalls: a subagent's, or the session's own.
func coldUsageFixture(t *testing.T, n int, subagent bool) (metas []session.Meta, release func()) {
	t.Helper()
	cfg, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	var fifos []string
	for i := 0; i < n; i++ {
		m := session.Meta{Name: "cold3885x" + strconv.Itoa(i) + strconv.FormatBool(subagent), Dir: t.TempDir(),
			Kind: session.KindClaude, CreatedAt: "2026-11-13T09:00:00Z"}
		sid := session.UUID(m.Dir, m.Name)
		main := filepath.Join(cfg, "projects", "p1", sid+".jsonl")
		sub := filepath.Join(cfg, "projects", "p1", sid, "subagents", "agent-a.jsonl")
		if err := os.MkdirAll(filepath.Dir(sub), 0o700); err != nil {
			t.Fatal(err)
		}
		stall, plain := sub, main
		if !subagent {
			stall, plain = main, sub
		}
		if err := os.WriteFile(plain, []byte(mainBody(5, false)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(stall, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		fifos = append(fifos, stall)
		session.WriteMeta(m)
		metas = append(metas, m)
	}
	release = func() {
		// A reader stuck on a FIFO sees EOF once a writer opens and closes it. Nonblocking,
		// because a FIFO nobody is reading yet would block the opener instead (ENXIO here).
		for _, p := range fifos {
			if f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
		}
	}
	return metas, release
}

func callSessionsUsage(t *testing.T, limit time.Duration) (names map[string]bool, pending int, took time.Duration) {
	t.Helper()
	type res struct {
		body []byte
		took time.Duration
	}
	got := make(chan res, 1)
	go func() {
		start := time.Now()
		rec := httptest.NewRecorder()
		HandleSessionsUsage(rec, httptest.NewRequest("GET", "/sessions/usage", nil))
		got <- res{rec.Body.Bytes(), time.Since(start)}
	}()
	var r res
	select {
	case r = <-got:
	case <-time.After(limit):
		t.Fatalf("GET /sessions/usage did not answer within %v", limit)
	}
	var body struct {
		Sessions []sessionUsage `json:"sessions"`
		Pending  int            `json:"pending"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatal(err)
	}
	names = map[string]bool{}
	for _, u := range body.Sessions {
		names[u.Name] = true
	}
	return names, body.Pending, r.took
}

// A cold all-sessions read answers within its budget however slow each session's work is: the
// sessions it could not finish are pending, never a late answer. Slow subagent transcripts are
// the case measured live (28 s against an 8 s budget): their fold ran after the budget check, for
// every session.
func TestHandleSessionsUsageColdCallIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		subagent bool
	}{{"slow subagent transcripts", true}, {"slow main transcripts", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const n = 8
			metas, release := coldUsageFixture(t, n, tc.subagent)
			oldBudget, oldFold := mainUsageColdBudget, deps.MaybeFoldSessionUsage
			deps.MaybeFoldSessionUsage = func() {}
			mainUsageColdBudget = 300 * time.Millisecond
			t.Cleanup(func() {
				for i := 0; i < 50; i++ { // let any reader still on its way in out
					release()
					time.Sleep(10 * time.Millisecond)
				}
				mainUsageColdBudget, deps.MaybeFoldSessionUsage = oldBudget, oldFold
			})

			names, pending, took := callSessionsUsage(t, 3*time.Second)
			if took > mainUsageColdBudget+time.Second {
				t.Fatalf("cold call took %v, budget %v", took, mainUsageColdBudget)
			}
			if pending != n || len(names) != 0 {
				t.Fatalf("cold call: sessions=%v pending=%d, want none and %d", names, pending, n)
			}

			// The stall ends; the background work the cold call left fills the cache, and a later
			// call reports every session.
			release()
			deadline := time.Now().Add(5 * time.Second)
			for {
				names, pending, _ = callSessionsUsage(t, 3*time.Second)
				if pending == 0 && len(names) == n {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("cache never warmed: sessions=%d pending=%d", len(names), pending)
				}
				release()
				time.Sleep(20 * time.Millisecond)
			}
			for _, m := range metas {
				if !names[m.Name] {
					t.Fatalf("%s missing from the warm answer", m.Name)
				}
			}
		})
	}
}
