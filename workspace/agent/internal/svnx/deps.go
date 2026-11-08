package svnx

// deps.go — every hand svnx reaches out to package main with, on one sheet.
//
// svnx does not import main (the dependency already runs the other way), so "call something
// in main" arrives as a value wired once at boot (main's svn_wiring.go). Configure panics on a
// gap: an unwired StartRepoJob would run a checkout outside the job table, and an empty
// ErrCodeSessionsRunning would send the Console a blank error code.
//
// The repo-job sink type is gitx.RepoJobSink: gitx already owns the consumer-defined face of
// main's `*repoJobSink` (io.Writer plus Tail), and svnx uses exactly that face.

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// Deps is the outside world as svnx sees it. It holds no type from main.
type Deps struct {
	// Repo import jobs (repo_jobs.go). RepoJob is main's type and svnx only encodes it as JSON,
	// so StartRepoJob returns it as `any`.
	RepoJobActive func(name string) bool
	StartRepoJob  func(kind, name, dir, url string, run func(ctx context.Context, sink gitx.RepoJobSink) error) any

	// ErrCodeSessionsRunning is errcodes.go's machine token for "sessions are running in this
	// working copy"; the Console keys its localized text on it.
	ErrCodeSessionsRunning string
}

var deps Deps

// Configure is called once at boot (main's svn_wiring.go, or svnx's own tests). Completeness
// is taken with reflect so a field added later cannot be forgotten; the value-typed
// ErrCodeSessionsRunning would otherwise run on quietly as "".
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if v.Field(i).IsZero() {
			missing = append(missing, t.Field(i).Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("svnx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
}

// Wired returns the current wiring, for a caller checking end to end that it is live.
// Configure catches only what is unwired, never what is wired wrong.
func Wired() Deps { return deps }

func repoJobActive(name string) bool { return deps.RepoJobActive(name) }

func startRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink gitx.RepoJobSink) error) any {
	return deps.StartRepoJob(kind, name, dir, url, run)
}
