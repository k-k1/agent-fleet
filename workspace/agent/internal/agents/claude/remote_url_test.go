package claude

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// RemoteSessionURL runs per card per poll; these pin that an unchanged transcript is opened
// at most once, and that the memo never buys that with a stale answer.

const bridgeLine = `{"type":"bridge-session","bridgeSessionId":"cse_abc"}` + "\n"

func countOpens(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	prev := openTranscript
	openTranscript = func(p string) (*os.File, error) { n.Add(1); return os.Open(p) }
	t.Cleanup(func() { openTranscript = prev })
	return &n
}

func remoteFixture(t *testing.T, sid, body string) string {
	t.Helper()
	isolateSlot(t) // HOME too: sids.Write lands under the agent state dir
	dir := filepath.Join(ConfigDir(), "projects", "p")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRemoteSessionURLHitOpensOnce(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555551"
	remoteFixture(t, sid, bridgeLine)
	n := countOpens(t)
	for range 5 {
		if got := RemoteSessionURL(sid); got != "https://claude.ai/code/session_abc" {
			t.Fatalf("url = %q", got)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("opens = %d, want 1", n.Load())
	}
}

func TestRemoteSessionURLMissOpensOnceUntilFileChanges(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555552"
	p := remoteFixture(t, sid, `{"type":"user"}`+"\n")
	n := countOpens(t)
	for range 5 {
		if got := RemoteSessionURL(sid); got != "" {
			t.Fatalf("url = %q, want empty", got)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("opens = %d, want 1 for an unchanged miss", n.Load())
	}
	// The bridge line appears later: the file changed, so it must be re-read.
	if err := os.WriteFile(p, []byte(bridgeLine+`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RemoteSessionURL(sid); got != "https://claude.ai/code/session_abc" {
		t.Fatalf("url = %q after the line appeared", got)
	}
}

func TestRemoteSessionURLFailedOpenIsNotRemembered(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555554"
	remoteFixture(t, sid, bridgeLine)
	prev := openTranscript
	openTranscript = func(string) (*os.File, error) { return nil, os.ErrPermission }
	if got := RemoteSessionURL(sid); got != "" {
		t.Fatalf("url = %q with an unreadable log", got)
	}
	openTranscript = prev
	if got := RemoteSessionURL(sid); got == "" {
		t.Fatal("a failed open was cached as a miss")
	}
}

func TestRemoteSessionURLMemoIsBoundedAndConcurrencySafe(t *testing.T) {
	m := &remoteURLMemo{m: map[string]remoteURLEntry{}}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range remoteURLMemoMax {
				k := string(rune('a'+g)) + time.Duration(i).String()
				m.put(k, remoteURLEntry{sig: "x"})
				m.get(k)
			}
		}()
	}
	wg.Wait()
	if len(m.m) > remoteURLMemoMax {
		t.Fatalf("memo grew to %d, cap %d", len(m.m), remoteURLMemoMax)
	}
}

func TestRemoteSessionURLFailedReadIsNotRemembered(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555555"
	remoteFixture(t, sid, bridgeLine)
	prev := openTranscript
	t.Cleanup(func() { openTranscript = prev })
	openTranscript = func(p string) (*os.File, error) {
		f, err := os.Open(p)
		if err == nil {
			f.Close() // Read on a closed file fails, like a transient EIO
		}
		return f, err
	}
	if got := RemoteSessionURL(sid); got != "" {
		t.Fatalf("url = %q from a failed read", got)
	}
	openTranscript = prev
	if got := RemoteSessionURL(sid); got == "" {
		t.Fatal("a failed Read was cached as a miss for the unchanged file")
	}
}

func TestRemoteSessionURLFollowsLiveSIDDrift(t *testing.T) {
	const slot = "11111111-2222-4333-8444-555555555556"
	const live = "11111111-2222-4333-8444-555555555557"
	remoteFixture(t, slot, bridgeLine)
	if got := RemoteSessionURL(slot); got != "https://claude.ai/code/session_abc" {
		t.Fatalf("url = %q", got)
	}
	dir := filepath.Join(ConfigDir(), "projects", "p")
	other := `{"type":"bridge-session","bridgeSessionId":"cse_new"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, live+".jsonl"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	sids.Write(slot, live)
	if got := RemoteSessionURL(slot); got != "https://claude.ai/code/session_new" {
		t.Fatalf("url = %q after drift, want the new conversation's", got)
	}
}
