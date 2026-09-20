package harness

// filelock_test.go is the positive/negative control pair for the read-modify-write
// race described in the spawn task that added filelock.go: runEdit/runWrite used to
// do os.ReadFile -> mutate -> os.WriteFile with no synchronization, so N concurrent
// edits to the SAME file that all landed inside the same read/write window collapsed
// to whichever write happened last — every other edit's change silently vanished.
//
// TestConcurrentEditsToSameFileAllLand is the reproduction: fire many concurrent,
// non-overlapping edits at one file and assert every one of them survives. Checked
// out at the parent commit (before filelock.go), this test fails — most edits are
// lost, not merely slow. With the fix, runEdit's read-modify-write is serialized per
// resolved path, so the edits interleave safely (order doesn't matter since each
// targets a disjoint substring) and all land.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentEditsToSameFileAllLand(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()

	// Fixed-width, prefixed markers so no marker is ever a substring of another
	// (e.g. "line1" would wrongly match inside "line10") — that would make
	// strings.Contains lie about which edits actually landed.
	const n = 20
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("MARK%02d", i))
	}
	initial := strings.Join(lines, "\n") + "\n"
	if _, err := runWrite(ctx, rt, mustJSON(t, writeArgs{Path: "f.txt", Content: initial})); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			old := fmt.Sprintf("MARK%02d", i)
			neu := fmt.Sprintf("MARK%02d-edited", i)
			_, errs[i] = runEdit(ctx, rt, mustJSON(t, editArgs{Path: "f.txt", OldString: old, NewString: neu}))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("edit %d returned an error instead of applying: %v", i, err)
		}
	}

	full, err := resolvePath(rt.Cwd, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	var missing []string
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("MARK%02d-edited", i)
		if !strings.Contains(content, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d/%d concurrent edits were silently lost (missing %v); final content:\n%s", len(missing), n, missing, content)
	}
}

// TestFileLocksAreIndependentPerPath proves the fix does not fall back to a single
// global lock: a write to a.txt must not wait on a lock held for b.txt, or parallel
// tool_calls to different files would serialize for no reason (ADR 0093 decision 5's
// parallel tool_calls would lose most of their benefit).
func TestFileLocksAreIndependentPerPath(t *testing.T) {
	rt := &Runtime{Cwd: t.TempDir()}
	ctx := context.Background()
	if _, err := runWrite(ctx, rt, `{"path":"a.txt","content":"a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runWrite(ctx, rt, `{"path":"b.txt","content":"b"}`); err != nil {
		t.Fatal(err)
	}

	fullB, err := resolvePath(rt.Cwd, "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	muB := rt.lockPath(fullB)
	muB.Lock()
	defer muB.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := runWrite(ctx, rt, `{"path":"a.txt","content":"a2"}`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write to a.txt failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write to a.txt blocked on b.txt's lock — different-file writes must not serialize")
	}
}

// TestLockPathResolvesEquivalentPaths proves the lock is keyed by the RESOLVED
// absolute path, not the caller's raw argument — "sub/../f.txt" and "f.txt" must
// share a lock, or the whole scheme is bypassable by spelling the same file two ways.
func TestLockPathResolvesEquivalentPaths(t *testing.T) {
	cwd := t.TempDir()
	rt := &Runtime{Cwd: cwd}
	if err := os.MkdirAll(filepath.Join(cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	full1, err := resolvePath(cwd, "sub/../f.txt")
	if err != nil {
		t.Fatal(err)
	}
	full2, err := resolvePath(cwd, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if rt.lockPath(full1) != rt.lockPath(full2) {
		t.Fatal("lockPath returned different mutexes for the same resolved path")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
