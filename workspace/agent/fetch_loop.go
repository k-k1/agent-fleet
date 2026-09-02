// Auto-fetch — a background loop that keeps origin refs fresh so the Console
// can badge repos whose origin has advanced (and whether a clean fast-forward
// is possible) without the user pressing fetch. Only base clones are fetched:
// linked worktrees share the parent clone's object store and remote refs.
package main

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// autoFetchInterval reads AF_AUTO_FETCH_INTERVAL (a Go duration, e.g. "5m");
// "0"/"off" disables the loop. Default 10m — fresh enough for a badge while
// staying cheap on the shared, memory-constrained host.
func autoFetchInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("AF_AUTO_FETCH_INTERVAL"))
	if v == "" {
		return 10 * time.Minute
	}
	if v == "0" || strings.EqualFold(v, "off") {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute {
		log.Printf("auto-fetch: invalid AF_AUTO_FETCH_INTERVAL %q; using 10m", v)
		return 10 * time.Minute
	}
	return d
}

// startAutoFetch launches the background loop (no-op when disabled). The first
// pass runs shortly after boot so a freshly opened Console gets current badges.
func startAutoFetch() {
	interval := autoFetchInterval()
	if interval == 0 {
		return
	}
	go func() {
		time.Sleep(30 * time.Second) // let startup (cred seeding, network) settle
		for {
			fetchAllRepos()
			time.Sleep(interval)
		}
	}()
}

// lastFetchErr suppresses repeat logging for a repo that keeps failing the same
// way — unreachable remotes are expected here (outbound may be restricted), and
// a 10-minute cadence would otherwise fill the log. Single-goroutine access.
var lastFetchErr = map[string]string{}

// fetchAllRepos fetches origin for every base clone under ~/repos, one at a
// time (the host is shared — no parallel subprocess fan-out).
func fetchAllRepos() {
	entries, err := os.ReadDir(gitx.ReposRoot())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(gitx.ReposRoot(), e.Name())
		if !gitx.IsGitRepo(dir) || gitx.IsLinkedWorktree(dir) {
			continue
		}
		if _, ok := gitx.GitOriginURL(dir); !ok {
			continue // nothing to fetch
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--prune", "--quiet")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if lastFetchErr[e.Name()] != msg {
				lastFetchErr[e.Name()] = msg
				log.Printf("auto-fetch %s: %v: %s", e.Name(), err, msg)
			}
			continue
		}
		delete(lastFetchErr, e.Name())
	}
}
