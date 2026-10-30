package cloudruns

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A workspace row past its age is gone from Recent, and from disk, without a new run to
// trigger a prune; a run that names a session also applies the workspace's limit.
func TestWorkspaceAgeLimitHoldsWithoutANewWrite(t *testing.T) {
	dir := useRoot(t)
	liveSession(t, "cr-age")
	Begin("aws", "", "p", []string{"aws"})
	if n := len(Recent(10)); n != 1 {
		t.Fatalf("Recent before = %d, want 1", n)
	}
	clock := now()
	now = func() time.Time { return clock.Add(workspaceKeep + 24*time.Hour) }
	if n := len(Recent(10)); n != 0 {
		t.Fatalf("Recent 8 days later = %d, want 0", n)
	}
	if n := len(rowFiles(filepath.Join(dir, workspaceDir))); n != 0 {
		t.Fatalf("workspace spool still holds %d rows", n)
	}

	now = func() time.Time { return clock }
	Begin("aws", "", "p", []string{"aws"})
	now = func() time.Time { return clock.Add(workspaceKeep + 24*time.Hour) }
	Begin("aws", "cr-age", "p", []string{"aws"})
	if n := len(rowFiles(filepath.Join(dir, workspaceDir))); n != 0 {
		t.Fatalf("a session's run left %d expired workspace rows", n)
	}
}

// A run whose row the prune evicted before its identity or refusal was written does not
// write it back, so neither spool grows past its limit, with the updates arriving one by one
// or all at once.
func TestEvictedRunsStayOut(t *testing.T) {
	for _, c := range []struct {
		name, session string
		limit         int
	}{{"session", "cr-evict", perSession}, {"workspace", "", workspaceMax}} {
		t.Run(c.name, func(t *testing.T) {
			dir := useRoot(t)
			if c.session != "" {
				liveSession(t, c.session)
			}
			spool := filepath.Join(dir, c.session)
			if c.session == "" {
				spool = filepath.Join(dir, workspaceDir)
			}
			long := []*Run{}
			for i := 0; i < 5; i++ {
				long = append(long, Begin("aws", c.session, "p", []string{"aws"}))
			}
			for i := 0; i < c.limit; i++ {
				Begin("aws", c.session, "p", []string{"aws"})
			}
			if n := len(rowFiles(spool)); n != c.limit {
				t.Fatalf("before the late ends: %d rows, want %d", n, c.limit)
			}
			long[0].Resolved(Identity{Account: "123456789012"})
			long[1].Refuse(RefusedOther)
			var wg sync.WaitGroup
			for _, r := range long[2:] {
				wg.Add(2)
				go func(r *Run) { defer wg.Done(); r.Resolved(Identity{Region: "us-east-1"}) }(r)
				go func() { defer wg.Done(); Begin("aws", c.session, "p", []string{"aws"}) }()
			}
			wg.Wait()
			if n := len(rowFiles(spool)); n != c.limit {
				t.Fatalf("after the late ends: %d rows, want %d", n, c.limit)
			}
			for _, r := range long {
				for _, f := range rowFiles(spool) {
					if f == filepath.Base(r.path) {
						t.Fatalf("evicted row %s was written back", f)
					}
				}
			}
		})
	}
}
