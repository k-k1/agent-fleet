package main

// git_wiring.go — wires `internal/gitx`'s outward dependencies (gitx → main) in one place.
//
// The wiring lives apart from the aliases of the opposite direction (main → gitx) on
// purpose: aliases are peeled off wholesale at a wave boundary, while the wiring stays
// (gitx calling main's families is a relationship a move does not remove). In one file the
// two would have been mixed up at reclaim time.
//
// Never give the wiring defaults. `gitx.Configure` panics on anything left unwired; a zero
// value accepted here would make e.g. `RepoLocked` answer "always false" and a working copy
// the user locked would be deleted. Crashing beats running quietly.

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

func init() { gitx.Configure(gitDeps()) }

// gitDeps is the production wiring. gitx's own exhaustiveness check
// (internal/gitx/deps_test.go) uses fakes, so this is the only place the real values
// are written.
func gitDeps() gitx.Deps {
	return gitx.Deps{
		AbsPath:        sessionx.AbsPath,
		RepoLocked:     sessionx.RepoLocked,
		LockedRepoDirs: sessionx.LockedRepoDirs,

		LiveSessionsInDir:   sessionx.LiveSessionsInDir,
		LockedSessionsInDir: sessionx.LockedSessionsInDir,
		WorktreeHasSessions: sessionx.WorktreeHasSessions,
		ManagedAlive:        sessionx.ManagedAlive,

		FinalizeSessionUsage: finalizeSessionUsage,

		RepoJobActive: repoJobActive,
		StartRepoJob:  startGitRepoJob,

		IsSvnRepo:    isSvnRepo,
		SvnRepoEntry: svnRepoEntry,

		EnsureCredHelper: ensureCredHelper,
		InternalGitHost:  internalGitHost,

		FirstNonEmpty:   firstNonEmpty,
		GitConfigGlobal: gitConfigGlobal,
		GitHosts:        gitHosts,

		ScratchAutoRelocate: scratchAutoRelocate,

		ErrCodeSessionsRunning:       errCodeSessionsRunning,
		ErrCodeSessionsRunningDelete: errCodeSessionsRunningDelete,
		ErrCodeBranchInUse:           errCodeBranchInUse,
		ErrCodeWorktreeDirty:         errCodeWorktreeDirty,
		ErrCodeWorktreeRemoveFailed:  errCodeWorktreeRemoveFailed,
		ErrCodeHasWorktrees:          errCodeHasWorktrees,
		ErrCodeLocked:                errCodeLocked,
		ErrCodeLockedSessions:        errCodeLockedSessions,
	}
}

// gitRepoJobSink is a thin adapter presenting `*repoJobSink` as gitx.RepoJobSink.
//
// It exists for one reason: the method that reads the tail is `tailString()`, which is
// unexported. An unexported method is bound to the package that declares it, so
// `*repoJobSink` would not satisfy an identically spelled interface written in gitx.
// repo_jobs.go is outside AG-GIT's ownership, so the wrapper goes here rather than a
// `TailString()` added there.
type gitRepoJobSink struct{ *repoJobSink }

func (s gitRepoJobSink) Tail() string { return s.repoJobSink.tailString() }

// startGitRepoJob is startRepoJob with only the sink repackaged.
//
// Never wrap a nil: `gitRepoJobSink{nil}` is a non-nil interface holding nil, which makes
// gitx's `if sink != nil` true (clone then runs with `--progress` and `Stream` writes to a
// nil Writer and crashes). A nil is passed through as a nil.
func startGitRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink gitx.RepoJobSink) error) any {
	return startRepoJob(kind, name, dir, url, func(ctx context.Context, sink *repoJobSink) error {
		if sink == nil {
			return run(ctx, nil)
		}
		return run(ctx, gitRepoJobSink{sink})
	})
}
