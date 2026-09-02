package gitx

// Submodule sync for a working copy a session is about to start in.
//
// MEASURED (git 2.39): killing `git submodule update` mid-clone — exactly what a hard
// timeout does — leaves the submodule WEDGED, not merely unfinished:
//   - the per-worktree gitdir holds a partial object store whose HEAD is unborn,
//   - the working tree is empty apart from the `.git` link,
//   - every later `git submodule update` (even --force) dies with
//     "fatal: Unable to find current revision in submodule path" — it never resumes,
//   - and nothing surfaces it: `git status` is clean and `git submodule status` prints the
//     recorded sha with the healthy blank prefix.
//
// The session then finds an empty directory where a submodule should be, which is what gets
// reported as "the submodules are broken". A submodule of any real size (the case that
// prompted this: 1.4 GB) hits it on every single launch, because it cannot finish inside the
// launch budget.
//
// So the sync here does three things the old one-shot best-effort update did not:
//  1. it stops WAITING on a slow fetch instead of killing it,
//  2. it says so — log + notification — when a session starts on an incomplete checkout,
//  3. it repairs an already-wedged submodule with the one recipe measured to work: complete
//     the object transfer with `fetch`, then check out the sha the parent records.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

const (
	// submoduleWaitTimeout bounds how long a launch WAITS for the update — not how long the
	// fetch may take. The create request has its own budget (POST 40s + poll 45s) and a big
	// submodule cannot finish inside it, so on expiry the git process is left running and the
	// session starts; the reaper reports the outcome.
	submoduleWaitTimeout = 60 * time.Second
	// submoduleHardTimeout is the backstop for a fetch that is making no progress at all.
	// Deliberately far larger than any healthy clone: reaching it kills git, which wedges the
	// submodule (see the header) — repairable, but only on the next launch.
	submoduleHardTimeout = 60 * time.Minute
)

// submoduleOutcome is what one update attempt did within submoduleWaitTimeout.
type submoduleOutcome int

const (
	submoduleDone    submoduleOutcome = iota // finished, exit 0
	submoduleFailed                          // finished, non-zero
	submoduleRunning                         // still fetching in the background
)

// submoduleEntry is one line of `git submodule status`: the recorded (gitlink) sha, the path,
// and git's leading state byte — ' ' checked out, '-' not initialized, '+' at another sha,
// 'U' conflicted.
type submoduleEntry struct {
	Path  string
	SHA   string
	State byte
}

// gitSubmodulesUpdate fetches/updates submodules of a working copy (after a clone, reuse, or
// branch switch — the .gitmodules set/commits differ per branch).
//
// The workspace has no SSH key, so a submodule pinned to an SSH URL (git@host: / ssh://)
// would fail "Host key verification failed". Following CodeLeaf's JGit client, we
// `submodule init` (expanding .gitmodules into .git/config), rewrite any SSH-form submodule
// URL to HTTPS so the unified credential helper (workspace-agent cred) can authenticate it,
// then `submodule update`. Best-effort throughout — no submodules is a no-op and an
// unreachable submodule is non-fatal so the parent operation still succeeds — but no longer
// SILENT: every failure is logged, because an empty submodule directory is indistinguishable
// from a healthy one to the session that lands in it.
func gitSubmodulesUpdate(dir string) submoduleOutcome {
	if !hasSubmodules(dir) {
		return submoduleDone
	}
	if out, err := gitx.Combined(dir, "submodule", "init"); err != nil { // local: reads .gitmodules, no network
		log.Printf("submodules %s: init failed: %v: %s", dir, err, out)
	}
	rewriteSubmoduleSSHURLs(dir)
	// rewriteSubmoduleSSHURLs only reaches dir's TOP-LEVEL .git/config; a nested submodule
	// (e.g. lib-svc/lib-core) is materialized during --recursive with its own SSH URL and
	// would fail with "Permission denied (publickey)" in a workspace that authenticates over
	// HTTPS+token (no SSH keys). Pass url.insteadOf rewrites derived from the repo's hosts:
	// git exports -c config to the child processes that clone nested submodules (via
	// GIT_CONFIG_PARAMETERS), so SSH→HTTPS applies at every nesting level.
	return runSubmoduleUpdate(dir, submoduleInsteadOfArgs(dir))
}

// runSubmoduleUpdate starts `submodule update --recursive` and waits at most
// submoduleWaitTimeout for it. When the wait expires the process is NOT killed — killing it
// is precisely what wedges the checkout (see the header) — it is handed to a reaper goroutine
// that logs the result and files the follow-up notification.
func runSubmoduleUpdate(dir string, insteadOf []string) submoduleOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), submoduleHardTimeout)
	args := append(append([]string{}, insteadOf...), "submodule", "update", "--recursive")
	cmd := gitx.CmdContext(ctx, dir, args...)
	var out bytes.Buffer // read only after Wait returns, on whichever side reaps
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		cancel()
		log.Printf("submodules %s: update could not start: %v", dir, err)
		return submoduleFailed
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		cancel()
		if err != nil {
			log.Printf("submodules %s: update failed: %v: %s", dir, err, strings.TrimSpace(out.String()))
			return submoduleFailed
		}
		return submoduleDone
	case <-time.After(submoduleWaitTimeout):
		go func() {
			defer cancel()
			if err := <-done; err != nil {
				log.Printf("submodules %s: background update failed: %v: %s", dir, err, strings.TrimSpace(out.String()))
			} else {
				log.Printf("submodules %s: background update finished", dir)
			}
			scheduleSubmoduleRepair(dir)
		}()
		return submoduleRunning
	}
}

// gitSubmodulesEnsure is gitSubmodulesUpdate for the paths where a SESSION is about to start
// in dir (worktree launch or relaunch, clone-then-start): the same sync, plus the reporting
// and repair that turn "the checkout is silently incomplete" into something the user can see
// and something that fixes itself on the next launch.
func gitSubmodulesEnsure(dir string) {
	if gitSubmodulesUpdate(dir) == submoduleRunning {
		// The fetch is still running: the session starts NOW, on a tree that is not complete
		// yet. Say so; the reaper files the follow-up when the fetch lands.
		announceSubmoduleGaps(dir, submoduleGaps(dir))
		return
	}
	scheduleSubmoduleRepair(dir)
}

// scheduleSubmoduleRepair reports what the update left missing and repairs it in the
// background — the repair needs its own fetch, which can take minutes, and no launch may wait
// on that. A no-op (and silent) when everything is checked out, which is the normal case.
func scheduleSubmoduleRepair(dir string) {
	gaps := submoduleGaps(dir)
	if len(gaps) == 0 {
		markSubmodulesReady(dir)
		return
	}
	announceSubmoduleGaps(dir, gaps)
	if !repairStart(dir) {
		return // a repair for this working copy is already running
	}
	go func() {
		defer repairDone(dir)
		if left := repairSubmodules(dir, gaps); len(left) == 0 {
			markSubmodulesReady(dir)
		}
	}()
}

// repairSubmodules fixes the submodules a killed clone wedged and returns those still not
// checked out. Only an EMPTY submodule working tree is touched, so this can never overwrite
// local work, and the recipe is the measured one: `fetch` to complete the object transfer,
// then check out the sha the parent records — `git submodule update` itself cannot do this,
// it aborts on the unborn HEAD such a gitdir has.
func repairSubmodules(dir string, gaps []submoduleEntry) []submoduleEntry {
	var left []submoduleEntry
	for _, e := range gaps {
		p := filepath.Join(dir, filepath.FromSlash(e.Path))
		if e.SHA == "" || !isGitRepo(p) {
			// Never initialized at all: there is no partial clone to rescue, and the update
			// above already tried and failed (its error is in the log).
			left = append(left, e)
			continue
		}
		if err := repairSubmodule(p, e.SHA); err != nil {
			log.Printf("submodules %s: repair of %s failed: %v", dir, e.Path, err)
			left = append(left, e)
			continue
		}
		log.Printf("submodules %s: repaired %s at %s", dir, e.Path, e.SHA)
	}
	return left
}

func repairSubmodule(path, sha string) error {
	ctx, cancel := context.WithTimeout(context.Background(), submoduleHardTimeout)
	defer cancel()
	if out, err := gitx.CmdContext(ctx, path, "fetch", "origin").CombinedOutput(); err != nil {
		return fmt.Errorf("fetch: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// --force because a wedged gitdir's index can disagree with the (empty) working tree; the
	// caller only ever passes submodules whose working tree holds no files, so there is
	// nothing of the user's to discard.
	if out, err := gitx.Combined(path, "checkout", "--detach", "--force", sha); err != nil {
		return fmt.Errorf("checkout: %v: %s", err, out)
	}
	return nil
}

// submoduleGaps returns the submodules of dir a session would trip over: not initialized at
// all ('-'), or "initialized" with an empty working tree — the wedge a killed clone leaves,
// which git itself reports as healthy.
func submoduleGaps(dir string) []submoduleEntry {
	if !hasSubmodules(dir) {
		return nil
	}
	out, err := gitx.Run(dir, "submodule", "status", "--recursive")
	if err != nil {
		log.Printf("submodules %s: status failed: %v", dir, err)
		return nil
	}
	var gaps []submoduleEntry
	for _, e := range parseSubmoduleStatus(out) {
		if e.State == '-' || submodulePathEmpty(filepath.Join(dir, filepath.FromSlash(e.Path))) {
			gaps = append(gaps, e)
		}
	}
	return gaps
}

// parseSubmoduleStatus reads `git submodule status` output. Each line is "<state><sha> <path>"
// with the state byte glued to the sha ("-<sha> libs/x", " <sha> libs/x (v1.2)").
func parseSubmoduleStatus(out string) []submoduleEntry {
	var items []submoduleEntry
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		state := byte(' ')
		if line[0] != ' ' {
			state = line[0]
		}
		items = append(items, submoduleEntry{Path: f[1], SHA: strings.TrimLeft(f[0], "-+U"), State: state})
	}
	return items
}

// submodulePathEmpty reports whether a submodule directory holds nothing but its `.git` link
// (or is absent). A checked-out submodule always has at least one tracked file, so this is the
// signal that separates a wedged submodule from a healthy one — git's own status does not.
func submodulePathEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return false
		}
	}
	return true
}

// hasSubmodules keeps every step above off the overwhelming majority of working copies, which
// have no .gitmodules at all.
func hasSubmodules(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".gitmodules"))
	return err == nil
}

// One repair at a time per working copy: a relaunch during a long repair must not start a
// second fetch into the same submodule.
var submoduleRepairs = struct {
	mu      sync.Mutex
	running map[string]bool
	warned  map[string]bool // dirs we have already told the user about (for the "ready" follow-up)
}{running: map[string]bool{}, warned: map[string]bool{}}

func repairStart(dir string) bool {
	submoduleRepairs.mu.Lock()
	defer submoduleRepairs.mu.Unlock()
	if submoduleRepairs.running[dir] {
		return false
	}
	submoduleRepairs.running[dir] = true
	return true
}

func repairDone(dir string) {
	submoduleRepairs.mu.Lock()
	defer submoduleRepairs.mu.Unlock()
	delete(submoduleRepairs.running, dir)
}

// announceSubmoduleGaps notifies (once per dir+missing set) that a working copy is in use with
// submodules that are not checked out, and names them.
func announceSubmoduleGaps(dir string, gaps []submoduleEntry) {
	if len(gaps) == 0 {
		return
	}
	paths := make([]string, 0, len(gaps))
	for _, e := range gaps {
		paths = append(paths, e.Path)
	}
	log.Printf("submodules %s: not checked out: %s", dir, strings.Join(paths, ", "))
	submoduleRepairs.mu.Lock()
	submoduleRepairs.warned[dir] = true
	submoduleRepairs.mu.Unlock()
	ev := submoduleNotice(dir, "incomplete")
	ev.Payload["paths"] = paths
	ev.Payload["count"] = len(paths)
	// Keyed by the missing set: relaunching into the same broken worktree must not add a row
	// every time, but a DIFFERENT submodule going missing later is news.
	_ = notice.PutOnce("submodules:"+dir+":"+strings.Join(paths, ","), ev)
}

// markSubmodulesReady closes the loop only for a working copy we actually warned about — an
// ordinary complete checkout says nothing at all.
func markSubmodulesReady(dir string) {
	submoduleRepairs.mu.Lock()
	warned := submoduleRepairs.warned[dir]
	delete(submoduleRepairs.warned, dir)
	submoduleRepairs.mu.Unlock()
	if !warned {
		return
	}
	log.Printf("submodules %s: complete", dir)
	_ = notice.Put(submoduleNotice(dir, "ready"))
}

// submoduleNotice builds the shared event. It carries no session (the sync runs before any
// session exists); the Console's click target is the working copy's Source Control view, which
// lists submodules and their fetched state.
func submoduleNotice(dir, state string) notice.Event {
	repo := filepath.Base(dir)
	ev := notice.New("submodule-sync", "", "", repo)
	ev.Payload["state"] = state
	ev.Payload["repo"] = repo
	return ev
}
