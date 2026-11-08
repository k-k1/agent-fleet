package main

// svn_wiring.go — wires `internal/svnx`'s outward dependencies (svnx → main) in one place.
//
// Never give the wiring defaults: `svnx.Configure` panics on anything left unwired, because an
// empty ErrCodeSessionsRunning would reach the Console as a blank error code.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/svnx"

func init() { svnx.Configure(svnDeps()) }

// svnDeps is the production wiring. startGitRepoJob is startRepoJob with the sink repackaged
// as gitx.RepoJobSink, which is the same face svnx uses.
func svnDeps() svnx.Deps {
	return svnx.Deps{
		RepoJobActive:          repoJobActive,
		StartRepoJob:           startGitRepoJob,
		ErrCodeSessionsRunning: errCodeSessionsRunning,
	}
}
