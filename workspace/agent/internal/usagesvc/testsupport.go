package usagesvc

// testsupport.go — fixtures that tests in this package and in package main share (main's model
// catalog tests read the same price catalog and ledger directory). They live in a non-test file
// only because a _test.go helper cannot be imported across packages; nothing in production calls
// them. It deliberately does not import "testing" (the production binary would link it): TB is
// the slice of testing.TB these helpers use, and a *testing.T satisfies it.

import (
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// UseTempUsageDir points the ledger at a temp directory and clears the prune throttle state
// (it is process-global, so carrying it across tests makes prune either not run or run too often).
//
// Collect the running bulk fold before swapping the ledger. The fold runs in a goroutine and
// reads its destination (`AF_USAGE_DIR`) from the env at the moment it writes; the env is
// process-wide, so a fold an earlier test started and left alive writes into this test's ledger
// — a red like "ledger = 39 rows, want 4" whose cause is nowhere inside that test. On a real
// machine folding 200 sessions takes hundreds of ms to tens of seconds, so a single test that
// does not wait is enough to leak (CI's HOME is empty and has no sessions, so it is green only
// there).
//
// Clearing `usageFoldRunning` instead of waiting does not fix the leak, it erases its traces —
// the goroutine keeps running, and the double-start guard is dropped so a second one may run.
// Waiting is the correct fix.
func UseTempUsageDir(t TB) string {
	t.Helper()
	waitUsageFoldIdle(t) // collect a fold an earlier test started, before swapping the ledger
	dir := t.TempDir()
	t.Setenv("AF_USAGE_DIR", dir)
	// Push this Cleanup after t.TempDir(). Cleanup is LIFO, so this one runs before the temp
	// directory is removed — the other order leaves the writer writing into a directory that
	// disappeared while we were waiting (memory: tempdir-cleanup-lifo-detached-writer).
	t.Cleanup(func() { waitUsageFoldIdle(t) })
	usagex.ResetPruneClock() // usageMu / usagePrunedAt are unexported state inside usagex
	usageFoldGate.Lock()
	usageFoldedAt = time.Time{}
	usageFoldGate.Unlock()
	return dir
}

// UseIsolatedUsageDir swaps HOME in addition to the ledger. Aggregation API tests go through the
// handler and hit fold-on-read, so without a separate HOME they fold the real workspace's
// sessions and the expected values break against real data (hit for real — the total came to
// millions of tokens). Live tests that fire the real CLI cannot use it (auth lives under HOME),
// so those stay on UseTempUsageDir. HOME alone is not enough (memory: ci-runner-xdg-not-home):
// the price catalog also reads the opencode cache under XDG_CACHE_HOME, so unless that is swapped
// too the expected values move with the real catalog on the dev machine (i.e. the test ends up
// inspecting the environment).
func UseIsolatedUsageDir(t TB) string {
	t.Helper()
	dir := UseTempUsageDir(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AF_USAGE_CATALOG", "")
	ResetUsageCatalogCache(t)
	// UseTempUsageDir's wait runs after the swaps above are undone (LIFO): a fold still running
	// then would read the real HOME's sessions into this ledger. Collect it before that too.
	t.Cleanup(func() { waitUsageFoldIdle(t) })
	return dir
}

// ResetUsageCatalogCache drops the in-process cache so an earlier test's catalog does not leak
// into the next one. Also registered as a Cleanup so it takes effect on both sides.
func ResetUsageCatalogCache(t TB) {
	t.Helper()
	clear := func() {
		usageCatalogMu.Lock()
		usageCatalogCache, usageCatalogSrc = nil, ""
		usageCatalogAt, usageCatalogChecked = time.Time{}, time.Time{}
		usageCatalogMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// UseUsageCatalog installs the given JSON as the catalog (the real machine's catalog is
// never visible to the test).
func UseUsageCatalog(t TB, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	t.Setenv("AF_USAGE_CATALOG", p)
	ResetUsageCatalogCache(t)
}

// waitUsageFoldIdle collects the running bulk fold. It shares globals (the running flag, the
// throttle timestamp, the destination env), so leaking one across tests makes the next test fail
// or skip for no reason of its own. If the wait cannot be completed, fail instead of moving on
// quietly — tolerating it lets the leaked writer write into another test's ledger and turn that
// test red with no visible cause.
//
// The limit is chosen to cover a bulk fold on a real machine (a dev box with hundreds of sessions
// in HOME). Measured 2026-09-02: 0.58 s for 200 synthetic sessions of one transcript each. Real
// transcripts are an order of magnitude larger (measured on the code side: 158 sessions, ~20 s).
func waitUsageFoldIdle(t TB) {
	t.Helper()
	if !waitForIdle(usageFoldRunning.Load, usageFoldWaitBudget, time.Now, time.Sleep) {
		t.Fatalf("the bulk fold did not finish in %s (cannot move on leaving a running writer behind)",
			usageFoldWaitBudget)
	}
}

// usageFoldWaitBudget is a HANG detector, not a speed limit.
//
// What the wait is for is the real HOME's sessions: CI's is empty and the fold is instant there,
// a developer's holds a couple of hundred and takes tens of seconds. 🔴 At 60 seconds that made
// the budget a measurement of the MACHINE rather than of the code -- on this shared, memory
// constrained host three test suites at once pushed an ordinary fold past it and killed whichever
// test happened to be holding the wait (twice: the runs on PRs #494 and #498; the same suite on
// its own is green both times). A fold that is genuinely stuck still fails here, five minutes
// later instead of one, and that is the only case the budget exists for.
const usageFoldWaitBudget = 5 * time.Minute

// waitForIdle spins until `busy` reports false, or until `budget` has elapsed ON THE INJECTED
// CLOCK. Returns false only in the second case.
//
// The clock is a parameter so the timeout can be PROVEN without being spent: a test passes a
// `now` that only moves when `sleep` is called, and gets the same decisions in microseconds
// (TestWaitForIdleSpendsItsBudgetOnTheInjectedClock). A wall-clock deadline can only be checked
// by waiting for it, which is why the one it replaced was never covered and could drift into a
// load measurement without anybody noticing.
func waitForIdle(busy func() bool, budget time.Duration, now func() time.Time, sleep func(time.Duration)) bool {
	deadline := now().Add(budget)
	for busy() {
		if !now().Before(deadline) {
			return false
		}
		sleep(10 * time.Millisecond)
	}
	return true
}

// TB is the part of testing.TB the fixtures in this file use.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Setenv(key, value string)
	TempDir() string
	Cleanup(func())
}
