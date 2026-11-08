package svnx

// svnx's unit tests have no package main, so the outward dependencies are wired here.
//
// Measured on the move: the svnx tests reach none of them. The handlers that call
// RepoJobActive / StartRepoJob (checkout) and ErrCodeSessionsRunning (update) are exercised by
// package main's svn_checkout_job_test.go against the real job registry, so here they panic
// rather than answer with a fake that could go green on a lie.

import (
	"context"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

func init() { Configure(testDeps()) }

func testDeps() Deps {
	return Deps{
		RepoJobActive: func(string) bool { panic("svnx test deps: RepoJobActive reached; test it in package main") },
		StartRepoJob: func(string, string, string, string, func(context.Context, gitx.RepoJobSink) error) any {
			panic("svnx test deps: StartRepoJob reached; test it in package main")
		},
		ErrCodeSessionsRunning: "sessions_running",
	}
}

func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	full := testDeps()
	typ := reflect.TypeOf(full)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		d := full
		reflect.ValueOf(&d).Elem().Field(i).Set(reflect.Zero(typ.Field(i).Type))
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Configure accepted a Deps with %s left unwired", name)
				}
			}()
			Configure(d)
		}()
	}
	Configure(full) // leave the package wired for the tests that follow
}
