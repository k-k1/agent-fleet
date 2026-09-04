package main

// One end-to-end check that the wiring in git_wiring.go is actually live.
//
// `gitx.Configure` only catches UNWIRED fields (nil / zero value); it cannot catch wiring
// that points at the wrong thing. Three shapes are reachable in practice:
//
//   - `RepoLocked` pinned to `return false`    → the deletion lock disappears entirely
//   - `WorktreeHasSessions` pinned to `false`  → deletes the worktree of a running session
//   - `ErrCodeBranchInUse` spelled differently → the Console shows the raw code (i18n misses)
//
// Each is a one-line edit to the wiring. Coverage did not drop in the extraction (before it
// there was no notion of "wiring" at all), but the surface that can break did grow, so one
// check stops it here.
//
// Two shapes of check:
//
//   - functions are compared by function-pointer identity (a different function or a closure fails)
//   - values must equal the real constants
//
// The set of checks is also cross-checked against Deps' field set, so adding a field without
// adding a check fails here.

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

func TestGitWiringIsLive(t *testing.T) {
	w := gitx.Wired()

	checks := map[string]func(t *testing.T){
		"AbsPath":        func(t *testing.T) { sameGitFunc(t, w.AbsPath, sessionx.AbsPath) },
		"RepoLocked":     func(t *testing.T) { sameGitFunc(t, w.RepoLocked, sessionx.RepoLocked) },
		"LockedRepoDirs": func(t *testing.T) { sameGitFunc(t, w.LockedRepoDirs, sessionx.LockedRepoDirs) },

		"LiveSessionsInDir":   func(t *testing.T) { sameGitFunc(t, w.LiveSessionsInDir, sessionx.LiveSessionsInDir) },
		"LockedSessionsInDir": func(t *testing.T) { sameGitFunc(t, w.LockedSessionsInDir, sessionx.LockedSessionsInDir) },
		"WorktreeHasSessions": func(t *testing.T) { sameGitFunc(t, w.WorktreeHasSessions, sessionx.WorktreeHasSessions) },
		"ManagedAlive":        func(t *testing.T) { sameGitFunc(t, w.ManagedAlive, sessionx.ManagedAlive) },

		"FinalizeSessionUsage": func(t *testing.T) { sameGitFunc(t, w.FinalizeSessionUsage, finalizeSessionUsage) },

		"RepoJobActive": func(t *testing.T) { sameGitFunc(t, w.RepoJobActive, repoJobActive) },
		// StartRepoJob alone is not the real function: it goes through an adapter that
		// repacks the sink (startGitRepoJob in git_wiring.go). Check that it IS that
		// adapter — the bare startRepoJob has an incompatible type so a mix-up cannot
		// happen, but "swapped for a different closure" should still be caught.
		"StartRepoJob": func(t *testing.T) { sameGitFunc(t, w.StartRepoJob, startGitRepoJob) },

		"IsSvnRepo":    func(t *testing.T) { sameGitFunc(t, w.IsSvnRepo, isSvnRepo) },
		"SvnRepoEntry": func(t *testing.T) { sameGitFunc(t, w.SvnRepoEntry, svnRepoEntry) },

		"EnsureCredHelper": func(t *testing.T) { sameGitFunc(t, w.EnsureCredHelper, ensureCredHelper) },
		"InternalGitHost":  func(t *testing.T) { sameGitFunc(t, w.InternalGitHost, internalGitHost) },

		"FirstNonEmpty": func(t *testing.T) {
			sameGitFunc(t, w.FirstNonEmpty, firstNonEmpty)
			// Pin the real precedence right here. The gitx tests use a COPY of this
			// function (they cannot call the real one across packages), so if the copy
			// and the real one drift, gitx stays green while production breaks.
			// Measured: mutating connections.go:808 to ignore the first entry's
			// precedence fails 2 tests on develop (TestApplyGitIdentity +
			// TestParseBitbucketPullRequests) but only 1 after the extraction — the one
			// lost is TestApplyGitIdentity, which incidentally pinned that git identity
			// resolves in the order override > provider > account. This recovers it.
			for _, c := range []struct {
				in   []string
				want string
			}{
				{[]string{"a", "b"}, "a"},     // first wins (why an identity override takes effect)
				{[]string{"", "b", "c"}, "b"}, // empties are skipped
				{[]string{"", ""}, ""},        // all empty = empty
			} {
				if got := firstNonEmpty(c.in...); got != c.want {
					t.Fatalf("firstNonEmpty(%q) = %q, want %q (out of sync with gitx's copy)", c.in, got, c.want)
				}
			}
		},
		"GitConfigGlobal": func(t *testing.T) { sameGitFunc(t, w.GitConfigGlobal, gitConfigGlobal) },
		"GitHosts": func(t *testing.T) {
			if !reflect.DeepEqual(w.GitHosts, gitHosts) {
				t.Fatalf("supported-host table = %v, want %v", w.GitHosts, gitHosts)
			}
		},

		"ScratchAutoRelocate": func(t *testing.T) { sameGitFunc(t, w.ScratchAutoRelocate, scratchAutoRelocate) },

		// Error codes must be spelled exactly as the real constants in errcodes.go.
		// A mismatch means the Console's i18n lookup misses and the raw code reaches
		// the screen.
		"ErrCodeSessionsRunning":       func(t *testing.T) { sameGitCode(t, w.ErrCodeSessionsRunning, errCodeSessionsRunning) },
		"ErrCodeSessionsRunningDelete": func(t *testing.T) { sameGitCode(t, w.ErrCodeSessionsRunningDelete, errCodeSessionsRunningDelete) },
		"ErrCodeBranchInUse":           func(t *testing.T) { sameGitCode(t, w.ErrCodeBranchInUse, errCodeBranchInUse) },
		"ErrCodeWorktreeDirty":         func(t *testing.T) { sameGitCode(t, w.ErrCodeWorktreeDirty, errCodeWorktreeDirty) },
		"ErrCodeWorktreeRemoveFailed":  func(t *testing.T) { sameGitCode(t, w.ErrCodeWorktreeRemoveFailed, errCodeWorktreeRemoveFailed) },
		"ErrCodeHasWorktrees":          func(t *testing.T) { sameGitCode(t, w.ErrCodeHasWorktrees, errCodeHasWorktrees) },
		"ErrCodeLocked":                func(t *testing.T) { sameGitCode(t, w.ErrCodeLocked, errCodeLocked) },
		"ErrCodeLockedSessions":        func(t *testing.T) { sameGitCode(t, w.ErrCodeLockedSessions, errCodeLockedSessions) },
	}

	// Cross-check the set of checks against Deps' field set. A new field always fails
	// here, so "wiring added, check not added" cannot happen.
	typ := reflect.TypeOf(w)
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := checks[name]; !ok {
			t.Errorf("gitx.Deps.%s has no wiring check (add a check whenever you add a field)", name)
		}
	}
	for name := range checks {
		if !seen[name] {
			t.Errorf("gitx.Deps has no %s (only the check is stale)", name)
		}
	}
	for name, run := range checks {
		t.Run(name, run)
	}
}

// sameGitFunc checks that the function itself is what is wired. A closure or another
// function has a different code pointer, so the swap fails here.
func sameGitFunc(t *testing.T, got, want any) {
	t.Helper()
	g, w := reflect.ValueOf(got).Pointer(), reflect.ValueOf(want).Pointer()
	if g != w {
		t.Fatalf("wired to the wrong target: got %s, want %s", gitFuncName(g), gitFuncName(w))
	}
}

func gitFuncName(pc uintptr) string {
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	return "?"
}

func sameGitCode(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("error code = %q, want %q", got, want)
	}
}

// TestStartGitRepoJobCarriesTheRealSink checks that startGitRepoJob's repacking reaches
// the real sink.
//
// This is the one runtime seam the extraction created: `tailString()` on `*repoJobSink` is
// unexported, so it can only reach gitx through an adapter (gitRepoJobSink in
// git_wiring.go). Repack it wrong and:
//
//   - Write does not reach the real sink → no progress shows in the Console
//   - Tail() returns empty              → the reason a clone failed disappears
//     (only "clone failed" is shown, git's own output is dropped)
//
// Both compile, and no existing test goes red.
func TestStartGitRepoJobCarriesTheRealSink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetRepoJobs(t)

	done := make(chan string, 1)
	got := startGitRepoJob("git", "probe", t.TempDir(), "https://example.invalid/x.git",
		func(ctx context.Context, sink gitx.RepoJobSink) error {
			if sink == nil {
				done <- ""
				return nil
			}
			if _, err := sink.Write([]byte("Receiving objects: 42%")); err != nil {
				done <- "write: " + err.Error()
				return nil
			}
			done <- sink.Tail()
			return nil
		})

	// The return value stays main's RepoJob (gitx takes it as `any` and only JSON-encodes
	// it). If it turns into another type, the shape of the 202 body silently changes.
	job, ok := got.(RepoJob)
	if !ok {
		t.Fatalf("return type = %T, want main.RepoJob (the shape of the 202 body changes)", got)
	}
	if job.ID == "" || job.Kind != "git" || job.Name != "probe" {
		t.Fatalf("the job's initial snapshot is broken: %+v", job)
	}

	select {
	case tail := <-done:
		if tail != "Receiving objects: 42%" {
			t.Fatalf("what was written through the adapter is not readable from the real sink: %q "+
				"(empty = Tail() is not connected to tailString(); "+
				"different content = Write goes to the wrong destination)", tail)
		}
	case <-time.After(5 * time.Second):
		// Not a bare `<-done`: on a wiring accident CI would time the job out instead of
		// going red, leaving no record of what was being waited for.
		t.Fatal("run was not called within 5s (the repacking handed to startRepoJob is broken)")
	}
}

// TestGitRepoJobSinkHasOneConstructionSite mechanically pins the premise that the
// `if sink == nil` guard in `startGitRepoJob` is unreachable (the RECLAIM-C debt).
//
// Two parts to that premise: (1) `*repoJobSink` is created in exactly one place,
// `startRepoJob`; (2) `gitRepoJobSink{…}` is built in exactly one place. Add another and a
// route appears that passes a non-nil interface holding nil (`gitRepoJobSink{nil}`), making
// gitx's `if sink != nil` true and sending writes to a nil Writer.
//
// The guard itself is covered by no test (being unreachable, a mutation there stays green),
// so what is actually protected is the NUMBER of construction sites — read from the source.
// The number of files scanned is verified too, so that "found nothing, checked nothing"
// cannot pass silently.
func TestGitRepoJobSinkHasOneConstructionSite(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned, sinkNew, wrapNew := 0, 0, 0
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		scanned++
		ast.Inspect(f, func(node ast.Node) bool {
			lit, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			switch id := lit.Type.(type) {
			case *ast.Ident:
				switch id.Name {
				case "repoJobSink":
					sinkNew++
				case "gitRepoJobSink":
					wrapNew++
				}
			}
			return true
		})
	}
	if scanned < 50 {
		t.Fatalf("only %d non-test .go files were read = this check has gone silent", scanned)
	}
	if sinkNew != 1 {
		t.Errorf("repoJobSink is constructed in %d places (want 1). "+
			"If you add one, guarantee at every site that nil is never passed", sinkNew)
	}
	if wrapNew != 1 {
		t.Errorf("gitRepoJobSink is constructed in %d places (want 1). "+
			"gitRepoJobSink{nil} is a non-nil interface holding nil, "+
			"and walks straight past the nil check on the gitx side", wrapNew)
	}
}
