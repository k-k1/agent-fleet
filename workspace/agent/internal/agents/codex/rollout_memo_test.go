package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The memo exists to stop the sessions/*/*/*/ sweep that GET /notifications repeated for every
// codex session (rollout_memo.go). These pin that the sweep stops, and that it never buys speed
// with a wrong answer.

func countGlobs(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := rolloutGlob
	rolloutGlob = func(p string) ([]string, error) { n++; return orig(p) }
	t.Cleanup(func() { rolloutGlob = orig })
	return &n
}

func putRollout(t *testing.T, day, id string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "10", day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-10-"+day+"T00-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const memoID = "01a0ee31-0000-7000-8000-0000000000aa"

func TestRolloutPathGlobsOnceWhileTheAnswerHolds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := putRollout(t, "01", memoID)
	n := countGlobs(t)
	for range 5 {
		if got := rolloutPath(memoID); got != want {
			t.Fatalf("rolloutPath = %q, want %q", got, want)
		}
	}
	if *n != 1 {
		t.Fatalf("globbed %d times, want 1", *n)
	}
}

func TestRolloutPathResearchesAMovedOrDeletedRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := putRollout(t, "01", memoID)
	n := countGlobs(t)
	rolloutPath(memoID)
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if got := rolloutPath(memoID); got != "" {
		t.Fatalf("deleted rollout: got %q, want none", got)
	}
	moved := putRollout(t, "02", memoID)
	if got := rolloutPath(memoID); got != moved {
		t.Fatalf("moved rollout: got %q, want %q", got, moved)
	}
	if *n != 3 {
		t.Fatalf("globbed %d times, want 3", *n)
	}
}

func TestRolloutPathNeverRemembersAMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := countGlobs(t)
	if got := rolloutPath(memoID); got != "" {
		t.Fatalf("got %q before any rollout exists", got)
	}
	want := putRollout(t, "01", memoID)
	if got := rolloutPath(memoID); got != want {
		t.Fatalf("rollout created after a miss: got %q, want %q", got, want)
	}
	if *n != 2 {
		t.Fatalf("globbed %d times, want 2", *n)
	}
}

func TestRolloutPathResearchesAfterTheTTLAndKeepsTheNewest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := putRollout(t, "01", memoID)
	if got := rolloutPath(memoID); got != first {
		t.Fatalf("got %q, want %q", got, first)
	}
	newer := putRollout(t, "02", memoID)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(newer, future, future); err != nil {
		t.Fatal(err)
	}
	// Still inside the TTL: the remembered path is served.
	if got := rolloutPath(memoID); got != first {
		t.Fatalf("inside TTL: got %q, want the remembered %q", got, first)
	}
	key := filepath.Join(os.Getenv("HOME"), ".codex", "sessions") + "\x00" + memoID
	rolloutPaths.mu.Lock()
	h := rolloutPaths.seen[key]
	h.at = time.Now().Add(-2 * rolloutMemoTTL)
	rolloutPaths.seen[key] = h
	rolloutPaths.mu.Unlock()
	if got := rolloutPath(memoID); got != newer {
		t.Fatalf("after TTL: got %q, want the newest %q", got, newer)
	}
}

// A remembered hit that still Lstats must not be re-globbed once the old 60 s TTL has passed;
// only a failed Lstat or forget() triggers a search.
func TestRolloutPathDoesNotReglobAfterTheOldTTLWhileItLstats(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := putRollout(t, "01", memoID)
	n := countGlobs(t)
	rolloutPath(memoID)
	key := filepath.Join(os.Getenv("HOME"), ".codex", "sessions") + "\x00" + memoID
	rolloutPaths.mu.Lock()
	h := rolloutPaths.seen[key]
	h.at = time.Now().Add(-30 * time.Minute)
	rolloutPaths.seen[key] = h
	rolloutPaths.mu.Unlock()
	if got := rolloutPath(memoID); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if *n != 1 {
		t.Fatalf("globbed %d times, want 1 (no re-glob while the path Lstats)", *n)
	}
}

// A new question appended after the cached scan must be seen: the cache key is the file's size
// and mtime, and an append changes both.
func TestPendingQuestionIDNoticesAQuestionAppendedAfterTheCachedScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-memo", Dir: t.TempDir(), Kind: session.KindCodex}
	lines := [][]byte{
		taskStarted("2026-09-30T02:25:41.600Z", "t1"),
		userSays("2026-09-30T02:25:41.644Z", "ask me"),
	}
	writeSlotRollout(t, m, memoID, lines...)
	// Managed route: only the rollout decides.
	m.Driver = "managed"
	if got := PendingQuestionID(m); got != "" {
		t.Fatalf("no question yet: got %q", got)
	}
	_ = PendingQuestionID(m) // second call answered from the cache
	p := rolloutPath(memoID)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(askUser("2026-09-30T02:25:47.331Z", "call_q", "Which animal?"), '\n'))
	f.Close()
	if got := PendingQuestionID(m); got != "call_q" {
		t.Fatalf("question appended: got %q, want call_q", got)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.Write(append(callOutput("2026-09-30T02:25:50Z", "call_q", "cat"), '\n'))
	f.Close()
	if got := PendingQuestionID(m); got != "" {
		t.Fatalf("question answered: got %q, want none", got)
	}
}

// RememberSid is where a resume/fork reports the id; the older remembered rollout must not
// outlive it.
func TestRememberSidDropsTheRememberedRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := putRollout(t, "01", memoID)
	rolloutPath(memoID)
	newer := putRollout(t, "02", memoID)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(newer, future, future); err != nil {
		t.Fatal(err)
	}
	if got := rolloutPath(memoID); got != first {
		t.Fatalf("before RememberSid: got %q, want the remembered %q", got, first)
	}
	RememberSid("slot-"+memoID, memoID)
	if got := rolloutPath(memoID); got != newer {
		t.Fatalf("after RememberSid: got %q, want %q", got, newer)
	}
}

// Pins the cache itself: with size and mtime unchanged the scan is not repeated, so a rewrite
// that keeps both (which an append-only rollout never does) is deliberately not seen.
func TestPendingQuestionIDServesTheCachedScanWhileSizeAndMtimeHold(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-memo2", Dir: t.TempDir(), Kind: session.KindCodex, Driver: "managed"}
	asked := askUser("2026-09-30T02:25:47.331Z", "call_q", "Which animal?")
	writeSlotRollout(t, m, memoID, taskStarted("2026-09-30T02:25:41.600Z", "t1"), asked)
	if got := PendingQuestionID(m); got != "call_q" {
		t.Fatalf("got %q, want call_q", got)
	}
	p := rolloutPath(memoID)
	fi, _ := os.Stat(p)
	// Same length, no question: 'call_q' -> same-size garbage line.
	body := make([]byte, fi.Size())
	for i := range body {
		body[i] = ' '
	}
	body[len(body)-1] = '\n'
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := PendingQuestionID(m); got != "call_q" {
		t.Fatalf("size+mtime unchanged: got %q, want the cached call_q", got)
	}
}
