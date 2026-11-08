package main

// One check that svn_wiring.go is live: svnx.Configure only catches an unwired field, not one
// wired to the wrong target. Functions are compared by pointer identity, the error code by value.

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/svnx"
)

func TestSvnWiringIsLive(t *testing.T) {
	w := svnx.Wired()
	checks := map[string]func(t *testing.T){
		"RepoJobActive":          func(t *testing.T) { sameGitFunc(t, w.RepoJobActive, repoJobActive) },
		"StartRepoJob":           func(t *testing.T) { sameGitFunc(t, w.StartRepoJob, startGitRepoJob) },
		"ErrCodeSessionsRunning": func(t *testing.T) { sameGitCode(t, w.ErrCodeSessionsRunning, errCodeSessionsRunning) },
	}
	typ := reflect.TypeOf(w)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, ok := checks[name]; !ok {
			t.Errorf("svnx.Deps.%s has no wiring check (add one whenever you add a field)", name)
		}
	}
	for name, check := range checks {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("svnx.Deps has no %s (only the check is stale)", name)
			continue
		}
		t.Run(name, check)
	}
}
