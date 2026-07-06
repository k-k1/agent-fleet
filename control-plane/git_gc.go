package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// gitGC periodically repacks the internal bare repositories (docs/reference/
// internal-git-provider, P2). Pushes leave loose objects; without maintenance a
// busy repo grows unbounded and slows clone/fetch. It walks ${DATA_DIR}/git/*/*.git
// and runs `git gc --auto` on each — SEQUENTIALLY and with --auto, so it only does
// real work when a repo has crossed git's loose-object threshold, keeping the memory
// footprint tiny on the shared host (see host-oom-fleet-risk). Off when the interval
// is 0 (AF_GIT_GC_INTERVAL=0).
type gitGC struct {
	dataRoot string
	interval time.Duration
}

func newGitGC(dataRoot string, interval time.Duration) *gitGC {
	return &gitGC{dataRoot: dataRoot, interval: interval}
}

func (g *gitGC) run(ctx context.Context) {
	// A first sweep shortly after boot, then on the interval.
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.sweep(ctx)
			t.Reset(g.interval)
		}
	}
}

// sweep runs `git gc --auto` on every bare under the git tree. Errors are logged
// and skipped so one bad repo never stalls the rest.
func (g *gitGC) sweep(ctx context.Context) {
	root := filepath.Join(g.dataRoot, "git")
	tenants, err := os.ReadDir(root)
	if err != nil {
		return // no git tree yet (nothing created) — nothing to do
	}
	swept, failed := 0, 0
	for _, td := range tenants {
		if !td.IsDir() {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(root, td.Name()))
		if err != nil {
			continue
		}
		for _, rd := range repos {
			if !rd.IsDir() || filepath.Ext(rd.Name()) != ".git" {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			dir := filepath.Join(root, td.Name(), rd.Name())
			cmd := exec.CommandContext(ctx, "git", "--git-dir", dir, "gc", "--auto", "--quiet")
			if out, err := cmd.CombinedOutput(); err != nil {
				failed++
				log.Printf("git-gc: %s: %v: %s", dir, err, out)
				continue
			}
			swept++
		}
	}
	if swept > 0 || failed > 0 {
		log.Printf("git-gc: swept %d bare repo(s), %d failed", swept, failed)
	}
}
