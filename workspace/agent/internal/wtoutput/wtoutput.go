// Package wtoutput drops regenerable build output (tier A: node_modules, .next, target,
// .gradle) from dormant linked worktrees, keeping the worktree itself (#3202, phase 2 of #3079).
//
// This deletes user files and is not reversible (13G of gz is no trash), so every rule leans
// to refusing:
//
//   - only the four allow-listed names, at the worktree root or in an npm/pnpm workspace
//     package; never build/dist (they hold release artifacts nothing regenerates);
//   - a candidate must be git-ignored and have no tracked file under it, and the worktree must
//     have no uncommitted or untracked change at all;
//   - a symlink is unlinked, never followed: node_modules is often a link to the parent clone's
//     tree and target/build a link onto /scratch, and descending would empty someone else's tree;
//   - nothing that resolves outside the worktree is touched;
//   - no live or deletion-locked session, no process using a path under the candidate, and a
//     quiet period (HEAD, index and session activity all older than the dormant limit).
//
// Which sessions are live and which processes use a path is the caller's (Deps): the session
// store and the process scan live elsewhere.
package wtoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Names is the tier-A allow-list. Matching is by exact directory name.
var Names = []string{"node_modules", ".next", "target", ".gradle"}

// DefaultDormantDays is how long a worktree must have been untouched.
const DefaultDormantDays = 30

const (
	gitTimeout = 20 * time.Second
	// sizeBudget caps the entries one survey stats across all worktrees; past it sizes are
	// lower bounds (Truncated).
	sizeBudget = 3_000_000
)

// Refusal reasons. The Console renders them; the English sentence is for the assistant.
const (
	ReasonNotWorktree = "not_linked_worktree"
	ReasonLocked      = "locked"
	ReasonLive        = "live_session"
	ReasonDormant     = "not_dormant"
	ReasonDirty       = "dirty"
	ReasonGit         = "git_unreadable"
	ReasonInUse       = "in_use"
)

// Deps is what the caller knows and this package must not.
type Deps struct {
	Now func() time.Time
	// Locked says the worktree, or a session whose directory is in it, is deletion-locked.
	Locked func(dir string) bool
	// LiveSessions names the running sessions whose directory is in dir.
	LiveSessions func(dir string) []string
	// LastSession is the newest activity of any session in dir (zero for none).
	LastSession func(dir string) time.Time
	// ProcRoot is /proc; tests point it at a fake tree.
	ProcRoot string
	// Logf records every removal; nil discards.
	Logf func(format string, args ...any)
}

// Dir is one tier-A directory (or link) found in a worktree.
type Dir struct {
	Rel   string `json:"rel"` // slash-separated, relative to the worktree
	Bytes int64  `json:"bytes"`
	// Link = the entry is a symlink: only the link is removed, Bytes is the link's own size.
	Link bool `json:"link,omitempty"`
}

// Report is the verdict on one worktree.
type Report struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Status is "candidate" (something can be dropped), "empty" (nothing to drop) or "refused".
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	Dirs   []Dir  `json:"dirs,omitempty"`
	Bytes  int64  `json:"bytes"`
	// Removed is filled by Apply (empty on a dry run); Skipped holds directories that were
	// dropped from the plan at the last minute (became busy, vanished, moved).
	Removed   []Dir    `json:"removed,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) logf(f string, a ...any) {
	if d.Logf != nil {
		d.Logf(f, a...)
	}
}

func git(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return out.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return out.Bytes(), 0, nil
}

// Survey judges one worktree and, when it qualifies, lists what Apply would drop.
// budget counts entries stat-ed for sizes across calls.
func Survey(ctx context.Context, d Deps, name, dir string, dormantDays int, budget *int) Report {
	r := Report{Name: name, Path: dir, Status: "refused"}
	refuse := func(reason, detail string) Report {
		r.Reason, r.Detail = reason, detail
		return r
	}
	if dormantDays < 1 {
		dormantDays = DefaultDormantDays
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return refuse(ReasonGit, err.Error())
	}
	gitDir, ok := linkedGitDir(ctx, root)
	if !ok {
		return refuse(ReasonNotWorktree, "not a linked git worktree")
	}
	if d.Locked != nil && d.Locked(dir) {
		return refuse(ReasonLocked, "the worktree or one of its sessions is deletion-locked")
	}
	if d.LiveSessions != nil {
		if live := d.LiveSessions(dir); len(live) > 0 {
			return refuse(ReasonLive, strings.Join(live, ", "))
		}
	}
	newest := newestOf(
		mtime(filepath.Join(gitDir, "HEAD")), mtime(filepath.Join(gitDir, "index")), mtime(filepath.Join(root, ".git")),
	)
	if d.LastSession != nil {
		newest = newestOf(newest, d.LastSession(dir))
	}
	if limit := d.now().Add(-time.Duration(dormantDays) * 24 * time.Hour); newest.IsZero() || newest.After(limit) {
		return refuse(ReasonDormant, fmt.Sprintf("activity within %d days", dormantDays))
	}
	out, code, err := git(ctx, root, "status", "--porcelain=v1", "-z")
	if err != nil || code != 0 {
		return refuse(ReasonGit, "git status failed")
	}
	if len(out) > 0 {
		return refuse(ReasonDirty, "uncommitted or untracked changes")
	}

	sawBusy := false
	for _, rel := range candidates(ctx, root) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(abs)
		if err != nil {
			continue
		}
		link := fi.Mode()&os.ModeSymlink != 0
		if !link && !fi.IsDir() {
			continue
		}
		if why := notRegenerable(ctx, root, rel, abs, link); why != "" {
			continue
		}
		// A dry run must predict what Apply would refuse.
		if busy, err := inUse(d.ProcRoot, root, abs); err != nil || busy {
			r.Skipped = append(r.Skipped, rel+": "+ReasonInUse)
			sawBusy = true
			continue
		}
		c := Dir{Rel: rel, Link: link}
		if link {
			c.Bytes = allocated(fi)
		} else {
			c.Bytes = treeBytes(abs, budget)
		}
		r.Dirs = append(r.Dirs, c)
		r.Bytes += c.Bytes
	}
	r.Truncated = *budget <= 0
	if len(r.Dirs) == 0 && sawBusy {
		return refuse(ReasonInUse, strings.Join(r.Skipped, "; "))
	}
	r.Reason, r.Detail = "", ""
	if len(r.Dirs) == 0 {
		r.Status = "empty"
	} else {
		r.Status = "candidate"
	}
	return r
}

// Apply re-judges the worktree and, unless dryRun, removes what the survey lists. Every
// directory is re-checked (still where the survey saw it, not in use) right before its delete.
func Apply(ctx context.Context, d Deps, name, dir string, dormantDays int, dryRun bool) Report {
	budget := sizeBudget
	r := Survey(ctx, d, name, dir, dormantDays, &budget)
	if dryRun || r.Status != "candidate" {
		return r
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		r.Status, r.Reason, r.Detail = "refused", ReasonGit, err.Error()
		return r
	}
	plan := r.Dirs
	r.Dirs = nil
	r.Bytes = 0
	for _, c := range plan {
		abs := filepath.Join(root, filepath.FromSlash(c.Rel))
		if why := recheck(ctx, d, root, c, abs); why != "" {
			r.Skipped = append(r.Skipped, c.Rel+": "+why)
			if why == ReasonInUse {
				r.Reason = ReasonInUse
			}
			continue
		}
		var err error
		if c.Link {
			err = os.Remove(abs)
		} else {
			err = removeTree(abs)
		}
		if err != nil {
			r.Skipped = append(r.Skipped, c.Rel+": "+err.Error())
			continue
		}
		d.logf("worktree-output: removed %s (%d bytes, link=%v)", abs, c.Bytes, c.Link)
		r.Removed = append(r.Removed, c)
		r.Bytes += c.Bytes
	}
	if len(r.Removed) == 0 {
		r.Status = "refused"
		if r.Reason == "" {
			r.Reason = ReasonGit
		}
		r.Detail = strings.Join(r.Skipped, "; ")
	}
	return r
}

// recheck is the last look before a delete; "" means go ahead.
func recheck(ctx context.Context, d Deps, root string, c Dir, abs string) string {
	// The parent may have been swapped for a link since the survey: resolve it again and
	// demand it is still inside the worktree.
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil || !within(root, parent) {
		return "path left the worktree"
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return "gone"
	}
	if link := fi.Mode()&os.ModeSymlink != 0; link != c.Link {
		return "changed kind since the survey"
	}
	if why := notRegenerable(ctx, root, c.Rel, abs, c.Link); why != "" {
		return why
	}
	if busy, err := inUse(d.ProcRoot, root, abs); err != nil || busy {
		return ReasonInUse
	}
	return ""
}

// notRegenerable returns why rel is not safe to drop; "" = ignored, nothing tracked under it,
// not a nested repository, and its own enclosing repository is root.
func notRegenerable(ctx context.Context, root, rel, abs string, link bool) string {
	if !link {
		if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
			return "nested repository"
		}
	}
	if top, code, err := git(ctx, filepath.Dir(abs), "rev-parse", "--show-toplevel"); err != nil || code != 0 {
		return "git cannot place it"
	} else if t, err := filepath.EvalSymlinks(strings.TrimSpace(string(top))); err != nil || t != root {
		return "enclosing repository is not the worktree"
	}
	if _, code, err := git(ctx, root, "check-ignore", "-q", "--", rel); err != nil || code != 0 {
		return "not git-ignored"
	}
	out, code, err := git(ctx, root, "ls-files", "-z", "--", rel)
	if err != nil || code != 0 || len(out) > 0 {
		return "tracked files under it"
	}
	return ""
}

// candidates lists the allow-listed names at the root and in each npm/pnpm workspace package,
// as slash-separated paths relative to root. Directories only if they are real folders inside
// root; the entries themselves are judged by the caller.
func candidates(ctx context.Context, root string) []string {
	locs := []string{""}
	for _, g := range workspaceGlobs(root) {
		if g == "" || strings.HasPrefix(g, "!") || strings.Contains(g, "**") || filepath.IsAbs(g) {
			continue
		}
		g = filepath.Clean(filepath.FromSlash(g))
		if g == ".." || strings.HasPrefix(g, ".."+string(filepath.Separator)) {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(root, g))
		for _, m := range matches {
			fi, err := os.Lstat(m)
			if err != nil || !fi.IsDir() { // Lstat: a package that is a link is not entered
				continue
			}
			rel, err := filepath.Rel(root, m)
			if err != nil || rel == "." || !within(root, m) {
				continue
			}
			if real, err := filepath.EvalSymlinks(m); err != nil || !within(root, real) {
				continue
			}
			locs = append(locs, filepath.ToSlash(rel))
		}
	}
	sort.Strings(locs)
	var out []string
	seen := map[string]bool{}
	for _, l := range locs {
		if seen[l] {
			continue
		}
		seen[l] = true
		for _, n := range Names {
			if l == "" {
				out = append(out, n)
			} else {
				out = append(out, l+"/"+n)
			}
		}
	}
	return out
}

// workspaceGlobs reads the package globs of the root package.json ("workspaces" as a list or
// {packages: [...]}) and pnpm-workspace.yaml (the "packages:" list, one "- pattern" per line).
func workspaceGlobs(root string) []string {
	var out []string
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pj struct {
			Workspaces json.RawMessage `json:"workspaces"`
		}
		if json.Unmarshal(b, &pj) == nil && len(pj.Workspaces) > 0 {
			var list []string
			var obj struct {
				Packages []string `json:"packages"`
			}
			if json.Unmarshal(pj.Workspaces, &list) == nil {
				out = append(out, list...)
			} else if json.Unmarshal(pj.Workspaces, &obj) == nil {
				out = append(out, obj.Packages...)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
		in := false
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(t, "packages:"):
				in = true
			case in && strings.HasPrefix(t, "- "):
				out = append(out, strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "- ")), `"'`))
			case in && t != "" && !strings.HasPrefix(t, "#"):
				in = false
			}
		}
	}
	return out
}

// linkedGitDir returns the worktree's own git directory (.git/worktrees/<name>), and false for
// a main clone or anything that is not a worktree.
func linkedGitDir(ctx context.Context, root string) (string, bool) {
	out, code, err := git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil || code != 0 {
		return "", false
	}
	g := strings.TrimSpace(string(out))
	if !strings.Contains(filepath.ToSlash(g), "/.git/worktrees/") {
		return "", false
	}
	return g, true
}

func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

func mtime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func newestOf(ts ...time.Time) time.Time {
	var n time.Time
	for _, t := range ts {
		if t.After(n) {
			n = t
		}
	}
	return n
}

func allocated(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// treeBytes sums allocated bytes under dir without following links. budget bounds the entries.
func treeBytes(dir string, budget *int) int64 {
	var n int64
	var walk func(string)
	walk = func(p string) {
		ents, err := os.ReadDir(p)
		if err != nil {
			return
		}
		for _, e := range ents {
			if *budget <= 0 {
				return
			}
			*budget--
			fi, err := e.Info() // lstat
			if err != nil {
				continue
			}
			n += allocated(fi)
			if fi.IsDir() {
				walk(filepath.Join(p, e.Name()))
			}
		}
	}
	walk(dir)
	return n
}

// removeTree is os.RemoveAll that also gets through read-only directories. RemoveAll never
// follows a link it meets inside the tree, and the walk below does not either.
func removeTree(p string) error {
	if os.RemoveAll(p) == nil {
		return nil
	}
	_ = filepath.WalkDir(p, func(q string, de os.DirEntry, werr error) error {
		if werr == nil && de.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}

// inUse says whether a process has its cwd, exe, a mapping or an open file under abs. Errors
// (/proc unreadable) are returned so the caller refuses. A process we may not inspect is
// judged by its argv naming the worktree; one that exited meanwhile is ignored. Best effort:
// a process starting after the scan is not seen.
func inUse(procRoot, root, abs string) (bool, error) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return false, err
	}
	under := func(p string) bool {
		p = strings.TrimSuffix(p, " (deleted)")
		return p == abs || strings.HasPrefix(p, abs+string(filepath.Separator))
	}
	self := os.Getpid()
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		pd := filepath.Join(procRoot, e.Name())
		denied := false
		for _, l := range []string{"cwd", "exe"} {
			t, err := os.Readlink(filepath.Join(pd, l))
			switch {
			case err == nil:
				if under(t) {
					return true, nil
				}
			case os.IsPermission(err):
				denied = true
			}
		}
		if b, err := os.ReadFile(filepath.Join(pd, "maps")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if i := strings.IndexByte(line, '/'); i >= 0 && under(line[i:]) {
					return true, nil
				}
			}
		} else if os.IsPermission(err) {
			denied = true
		}
		fds, err := os.ReadDir(filepath.Join(pd, "fd"))
		if os.IsPermission(err) {
			denied = true
		}
		for _, fd := range fds {
			if t, err := os.Readlink(filepath.Join(pd, "fd", fd.Name())); err == nil && under(t) {
				return true, nil
			}
		}
		if denied {
			if b, err := os.ReadFile(filepath.Join(pd, "cmdline")); (err != nil && !os.IsNotExist(err)) || bytes.Contains(b, []byte(root)) {
				return true, nil // unreadable argv or one that names the worktree: assume busy
			}
		}
	}
	return false, nil
}
