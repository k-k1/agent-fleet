package gitx

// deps.go — one page collecting every hand gitx reaches out to the caller (package main).
//
// The git family is "holding the repositories" itself, so its outward dependencies scatter
// across main's other families (the lock ledger, session liveness, import jobs, SVN, the
// credential helper, the error-code table). This does not hide that seam; it gathers it in
// one place so it can be counted (the same shape as internal/mcpx/deps.go):
//
//   - gitx does not import main (it cannot; the dependency already runs the other way)
//   - so "call a function in main" is taken as a function value instead
//   - wiring happens once at boot (the init in main's git_wiring.go), and Configure panics
//     on what is missing. Filling a gap with a default silently would, for instance, make
//     the delete path's lock check read "never locked" so that something locked gets
//     deleted. Crashing beats running quietly.
//
// gitx's own tests have no main, so the fakes are wired by init rather than TestMain
// (see deps_test.go).

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RepoJobSink is a consumer-defined interface (README §1 ③) covering only the face of main's
// `*repoJobSink` that gitx actually uses.
//
// main's type cannot be taken directly (one type from main and the seam stops closing), and
// `io.Writer` alone is not enough: on failure clone returns the tail of the output so far as
// the error body, and losing that leaves the Console with "clone failed" and no reason.
//
// The method on the main side is `tailString()`, which is unexported. An unexported method
// is tied to the package that declares it, so an interface written here with that spelling
// would not be satisfied by main's type. Hence the different name `Tail()`, with a thin
// adapter on the main side (git_wiring.go). repo_jobs.go is not owned by AG-GIT, so adding a
// method there is not an option.
type RepoJobSink interface {
	io.Writer
	// Tail is main's (*repoJobSink).tailString — the tail of the output written so far.
	Tail() string
}

// Deps is the outside world as gitx sees it. It holds no type from main (the moment it does,
// the seam stops closing), so it can grow without adding imports.
type Deps struct {
	// --- Lock ledger (locks.go) ---
	//
	// The only basis on which delete, checkout and worktree pruning decide whether
	// something may be removed. Filling an unwired field with the zero value makes
	// repoLocked always false, which is the same as having no locks at all, so Configure
	// is tipped towards crashing.
	AbsPath        func(p string) string
	RepoLocked     func(dir string) bool
	LockedRepoDirs func() map[string]bool

	// --- Session liveness (session_tmux.go / session_handlers.go) ---
	//
	// "Is anyone still using this worktree?" Here too an unwired field turns into "nobody
	// is using it", which deletes the worktree of a running session.
	LiveSessionsInDir   func(dir string) []string
	LockedSessionsInDir func(metas []session.Meta, dir string) []string
	WorktreeHasSessions func(dir string) bool
	ManagedAlive        func(m session.Meta) bool

	// --- Closing the usage ledger (usage_fold.go) ---
	FinalizeSessionUsage func(m session.Meta)

	// --- Import jobs (repo_jobs.go) ---
	//
	// A clone outlives the request, so it runs as a background job (docs/log/78). The
	// returned `RepoJob` is a type of main's, so it is taken as `any`: gitx only encodes it
	// as JSON and never reads inside it.
	RepoJobActive func(name string) bool
	StartRepoJob  func(kind, name, dir, url string, run func(ctx context.Context, sink RepoJobSink) error) any

	// --- SVN (svn.go) ---
	//
	// ~/repos mixes git and svn. The listing shows both, so the git side has to read the
	// svn side. SvnRepoEntry returns a gitx.Repo, not a type of main's (Repo came over to
	// gitx in the move).
	IsSvnRepo    func(dir string) bool
	SvnRepoEntry func(name, dir string) Repo

	// --- Credential helper (cred_helper.go) ---
	EnsureCredHelper func() error
	InternalGitHost  func() string

	// --- Connections (connections.go) ---
	//
	// GitHosts maps a supported provider's host to the default git user name. The values
	// are not held again on the gitx side: a different table per layer breaks silently the
	// day only one of them grows an entry.
	FirstNonEmpty   func(vals ...string) string
	GitConfigGlobal func(key, val string) error
	GitHosts        map[string]string

	// --- Relocation to /scratch (scratch.go) ---
	ScratchAutoRelocate func(dir string)

	// --- Stable error codes (errcodes.go) ---
	//
	// Strings paired with the Console's i18n catalogue. Not redeclared on the gitx side:
	// with two sources, the day one of them is fixed the screen shows a raw code.
	ErrCodeSessionsRunning       string
	ErrCodeSessionsRunningDelete string
	ErrCodeBranchInUse           string
	ErrCodeWorktreeDirty         string
	ErrCodeWorktreeRemoveFailed  string
	ErrCodeHasWorktrees          string
	ErrCodeLocked                string
	ErrCodeLockedSessions        string
}

var deps Deps

// Configure is called exactly once at boot (main's git_wiring.go, or the init in gitx's own
// tests). Nothing runs with a gap left in it: a lock check or a session-liveness check that
// happens to run on the zero value tips towards deleting what must not be deleted.
//
// Completeness is taken with reflect, never a hand-written list: a hand-written map misses
// a field that was added later, and nothing happens when it does. The dangerous ones are
// the value types. An unwired func dies on a nil dereference, but a string like
// `ErrCodeLocked` runs on quietly as empty and the Console receives `""` as the code. This
// struct already holds 9 value-typed fields.
//
// To make an exception, tag the field `gitx:"optional"` — there is no separate list, so an
// exception is always visible at the declaration.
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("gitx") == "optional" {
			continue
		}
		if unwired(v.Field(i)) {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("gitx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
	gitHosts = d.GitHosts
	errCodeSessionsRunning = d.ErrCodeSessionsRunning
	errCodeSessionsRunningDelete = d.ErrCodeSessionsRunningDelete
	errCodeBranchInUse = d.ErrCodeBranchInUse
	errCodeWorktreeDirty = d.ErrCodeWorktreeDirty
	errCodeWorktreeRemoveFailed = d.ErrCodeWorktreeRemoveFailed
	errCodeHasWorktrees = d.ErrCodeHasWorktrees
	errCodeLocked = d.ErrCodeLocked
	errCodeLockedSessions = d.ErrCodeLockedSessions
}

// unwired decides what counts as "not wired". Besides the zero value, an empty map counts
// too: `map[string]string{}` is not the zero value, but as a dependency it means the same
// thing as unwired.
func unwired(v reflect.Value) bool {
	if v.Kind() == reflect.Map {
		return v.Len() == 0
	}
	return v.IsZero()
}

// Wired returns the current wiring. It is a read port for a caller checking end to end that
// the wiring is live; gitx itself does not use it.
//
// Configure catches only what is unwired, never what is wired wrong. Making `RepoLocked`
// always false passes quietly, and since the wiring is one line it is the spot most likely
// to be touched by a future tidy-up.
func Wired() Deps { return deps }

// Taken by value. Configure writes them once; everything after that only reads.
var (
	gitHosts                     map[string]string
	errCodeSessionsRunning       string
	errCodeSessionsRunningDelete string
	errCodeBranchInUse           string
	errCodeWorktreeDirty         string
	errCodeWorktreeRemoveFailed  string
	errCodeHasWorktrees          string
	errCodeLocked                string
	errCodeLockedSessions        string
)

// What follows are thin delegations under the same names the code used before the move, so
// that the 3,871 lines that came over need no edits. This is the only outward window.
func absPath(p string) string { return deps.AbsPath(p) }

func repoLocked(dir string) bool { return deps.RepoLocked(dir) }

func lockedRepoDirs() map[string]bool { return deps.LockedRepoDirs() }

func liveSessionsInDir(dir string) []string { return deps.LiveSessionsInDir(dir) }

func lockedSessionsInDir(metas []session.Meta, dir string) []string {
	return deps.LockedSessionsInDir(metas, dir)
}

func worktreeHasSessions(dir string) bool { return deps.WorktreeHasSessions(dir) }

func managedAlive(m session.Meta) bool { return deps.ManagedAlive(m) }

func finalizeSessionUsage(m session.Meta) { deps.FinalizeSessionUsage(m) }

func repoJobActive(name string) bool { return deps.RepoJobActive(name) }

func startRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink RepoJobSink) error) any {
	return deps.StartRepoJob(kind, name, dir, url, run)
}

func isSvnRepo(dir string) bool { return deps.IsSvnRepo(dir) }

func svnRepoEntry(name, dir string) Repo { return deps.SvnRepoEntry(name, dir) }

func ensureCredHelper() error { return deps.EnsureCredHelper() }

func internalGitHost() string { return deps.InternalGitHost() }

func firstNonEmpty(vals ...string) string { return deps.FirstNonEmpty(vals...) }

func gitConfigGlobal(key, val string) error { return deps.GitConfigGlobal(key, val) }

func scratchAutoRelocate(dir string) { deps.ScratchAutoRelocate(dir) }

// A thin skin over a pure internal package is not wired: it has no behaviour, so there is no
// room for a copy to go stale. main's homeDir is the same one line.
func homeDir() string { return paths.HomeDir() }
