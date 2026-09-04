package gitx

// gitx unit tests have no package main, so the outward dependencies are wired here.
//
// Why this is not just a row of fakes: before the move these tests ran against main's real
// implementations. Replacing them with fakes leaves the assertions and the branches taken
// unchanged while shrinking only the set of bugs that can be caught (the first pitfall in
// README §4). So which dependencies are actually reached was measured first:
//
//	measured (wiring replaced by one that only counts with `p(name)`, one full run of the
//	gitx tests) ->
//	  ScratchAutoRelocate=9 / InternalGitHost=8 / FirstNonEmpty=2, and 0 for the rest
//
// The 3 that are reached copy main's implementation verbatim (below); all three are nothing
// but env lookups and pure computation and touch no state in main, so the copies behave as
// the real ones do. The 15 that are not reached panic: a fake return value would silently go
// green on a lie once a future test does reach here, so this errs on the side of noise.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func init() { Configure(testDeps()) }

// unreached wires a dependency that was measured never to be reached from the gitx tests.
// Reaching it stops the run: prefer failing over running quietly, fakes included.
func unreached(name string) {
	panic("gitx test deps: " + name + " was never reached in the measurement taken at the " +
		"time of the move. Arriving here means a new check needs main's implementation: " +
		"before putting a fake return value here, decide whether to copy the behaviour of " +
		"main's git_wiring.go, or to put the test in package main")
}

// testDeps is the whole wiring for the gitx unit tests, kept in one place because the
// exhaustiveness check below uses the same thing.
func testDeps() Deps {
	return Deps{
		// --- the 3 reached in the measurement (copies of main's implementation) ---

		// A copy of scratch.go: with no env set it does nothing. Written as an empty function
		// on the grounds that "it is a no-op in tests", it would differ from the real one
		// exactly in an environment where AF_WS_SCRATCH is set. Copied, every environment gets
		// what it got before the move.
		ScratchAutoRelocate: func(dir string) {
			if dir == "" || os.Getenv("AF_WS_SCRATCH") == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "af-scratch", "--auto", dir).CombinedOutput()
			msg := strings.TrimSpace(string(out))
			if err != nil {
				log.Printf("scratch: auto relocate %s failed: %v: %s", dir, err, msg)
				return
			}
			if msg != "" {
				log.Printf("scratch: %s", msg)
			}
		},
		// A copy of cred_helper.go (one line, env only).
		InternalGitHost: func() string { return strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_HOST")) },
		// A copy of connections.go (pure).
		FirstNonEmpty: func(vals ...string) string {
			for _, v := range vals {
				if v != "" {
					return v
				}
			}
			return ""
		},

		// --- the 15 not reached in the measurement ---
		AbsPath:              func(s string) string { unreached("AbsPath"); return s },
		RepoLocked:           func(string) bool { unreached("RepoLocked"); return false },
		LockedRepoDirs:       func() map[string]bool { unreached("LockedRepoDirs"); return nil },
		LiveSessionsInDir:    func(string) []string { unreached("LiveSessionsInDir"); return nil },
		LockedSessionsInDir:  func([]session.Meta, string) []string { unreached("LockedSessionsInDir"); return nil },
		WorktreeHasSessions:  func(string) bool { unreached("WorktreeHasSessions"); return false },
		ManagedAlive:         func(session.Meta) bool { unreached("ManagedAlive"); return false },
		FinalizeSessionUsage: func(session.Meta) { unreached("FinalizeSessionUsage") },
		RepoJobActive:        func(string) bool { unreached("RepoJobActive"); return false },
		StartRepoJob: func(string, string, string, string, func(context.Context, RepoJobSink) error) any {
			unreached("StartRepoJob")
			return nil
		},
		IsSvnRepo:        func(string) bool { unreached("IsSvnRepo"); return false },
		SvnRepoEntry:     func(string, string) Repo { unreached("SvnRepoEntry"); return Repo{} },
		EnsureCredHelper: func() error { unreached("EnsureCredHelper"); return nil },
		GitConfigGlobal:  func(string, string) error { unreached("GitConfigGlobal"); return nil },

		// Taken by value. Do not copy the real values in: doing so makes this a second source
		// of truth (the real ones are wired by main's git_wiring.go). Confirmed that the gitx
		// tests read neither - the only reader of gitHosts is HandleGitProviderIdentityPut,
		// which the gitx tests never call.
		GitHosts: map[string]string{"gitx-test.invalid": "gitx-test"},

		ErrCodeSessionsRunning:       "gitx-test-sessions_running",
		ErrCodeSessionsRunningDelete: "gitx-test-sessions_running_delete",
		ErrCodeBranchInUse:           "gitx-test-branch_in_use",
		ErrCodeWorktreeDirty:         "gitx-test-worktree_dirty",
		ErrCodeWorktreeRemoveFailed:  "gitx-test-worktree_remove_failed",
		ErrCodeHasWorktrees:          "gitx-test-has_worktrees",
		ErrCodeLocked:                "gitx-test-locked",
		ErrCodeLockedSessions:        "gitx-test-locked_sessions",
	}
}

// TestConfigureRejectsEveryUnwiredField checks that Configure fails when any single field of
// Deps is left unwired.
//
// The check itself walks the struct by reflection, so a new field is covered automatically.
// A hand-written list (in the implementation or in the test) is what gets missed when a field
// is added, and missing it costs nothing visible: what breaks is the silent side, like the
// deletion lock check quietly always answering false.
func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	good := testDeps()
	v := reflect.ValueOf(good)
	typ := v.Type()
	if typ.NumField() < 20 {
		t.Fatalf("Deps has only %d fields (wrong struct)", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			// Always restore the correct wiring: deps is shared by the whole package.
			defer Configure(good)
			broken := reflect.New(typ).Elem()
			broken.Set(v)
			broken.Field(i).Set(reflect.Zero(f.Type))
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("Configure accepted %s left unwired (a missing wire slips through silently)", f.Name)
				}
				if !strings.Contains(fmt.Sprint(r), f.Name) {
					t.Fatalf("the panic does not name %s: %v", f.Name, r)
				}
			}()
			Configure(broken.Interface().(Deps))
		})
	}
}

// TestConfigureRejectsHollowGitHosts checks that a wiring which is non-zero but empty is
// rejected too (the map branch of `unwired`). Looking only at the zero value, a
// `map[string]string{}` passes as wired and the supported-provider table runs empty, i.e.
// every host counts as unsupported.
func TestConfigureRejectsHollowGitHosts(t *testing.T) {
	defer Configure(testDeps())
	d := testDeps()
	d.GitHosts = map[string]string{}
	defer func() {
		if recover() == nil {
			t.Fatal("Configure accepted an empty GitHosts")
		}
	}()
	Configure(d)
}
