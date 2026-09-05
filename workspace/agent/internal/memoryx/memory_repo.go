package memoryx

// Version control for agent memory (docs/log/39 / ADR 0022) — the bare repo and the git
// environment it runs in.
//
// The repo is a bare one inside the claude-only mount (/var/lib/af/claude/af-memory.git). That
// mount has the strongest guarantee of surviving recreate / clean-home, so codex's history
// lives alongside it. The live tree gets no .git: the agent itself must not see the repo, and
// claude's memory enumeration must not pick .git up. Staging goes on the same mount, to avoid a
// cross-device copy over EFS and to keep the bare repo's index consistent with what staging
// holds.
//
// ★5: inherit nothing from the user's ~/.gitconfig (signing settings and the like).
// GIT_CONFIG_GLOBAL / GIT_CONFIG_SYSTEM are pointed at /dev/null and a dedicated identity is
// pinned through the environment.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

// memoryBranch is the only branch snapshots are stacked on. Imports land under
// refs/imports/<ts> (P3) as an independent lineage, so this branch shows this environment's
// history and nothing else.
const memoryBranch = "main"

func memoryRepoDir() string    { return filepath.Join(claude.ConfigDir(), "af-memory.git") }
func memoryStagingDir() string { return filepath.Join(claude.ConfigDir(), "af-memory.staging") }

// memoryGit builds a git command bound to the repo. The work tree is always staging and cwd is
// put there too, so that a `git add -A` with no pathspec sees staging and nothing else.
func memoryGit(args ...string) *exec.Cmd {
	_ = os.MkdirAll(memoryStagingDir(), 0o700) // used as cwd, so even read-only calls need it
	cmd := exec.Command("git", args...)
	cmd.Dir = memoryStagingDir()
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+memoryRepoDir(),
		"GIT_WORK_TREE="+memoryStagingDir(),
		// Inherit no user config (cuts out signing, hooksPath, core.autocrlf and friends).
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=af-memory",
		"GIT_AUTHOR_EMAIL=af-memory@agent-fleet.local",
		"GIT_COMMITTER_NAME=af-memory",
		"GIT_COMMITTER_EMAIL=af-memory@agent-fleet.local",
	)
	return cmd
}

// memoryGitRun runs git and returns the trimmed stdout, folding stderr into the error on
// failure. Same style as gitx.Run, kept separate because this one uses the isolated GIT_DIR
// environment.
func memoryGitRun(args ...string) (string, error) {
	out, err := memoryGit(args...).Output()
	s := strings.TrimSpace(string(out))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				err = fmt.Errorf("%v: %s", err, msg)
			}
		}
	}
	return s, err
}

// memoryEnsureRepo prepares the bare repo and staging (idempotent).
func memoryEnsureRepo() error {
	if err := os.MkdirAll(memoryStagingDir(), 0o700); err != nil {
		return err
	}
	dir := memoryRepoDir()
	if st, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil && !st.IsDir() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	// init alone runs without GIT_DIR/GIT_WORK_TREE, creating the repo by path.
	cmd := exec.Command("git", "init", "--bare", "--quiet", "-b", memoryBranch, dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("init af-memory repo: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// memoryHasCommits reports whether HEAD already points at a commit (false before the first
// snapshot).
func memoryHasCommits() bool {
	_, err := memoryGitRun("rev-parse", "--verify", "--quiet", memoryBranch+"^{commit}")
	return err == nil
}

// memoryProjectRef is one claude project: its slug and the formatted display name of ★6.
type memoryProjectRef struct {
	Slug    string `json:"slug"`
	Display string `json:"display"`
}

// memorySnapshotInfo summarises one snapshot (= commit). The list API returns this shape as is.
type memorySnapshotInfo struct {
	Rev      string             `json:"rev"`
	Short    string             `json:"short"`
	At       string             `json:"at"`       // RFC3339 (author date)
	Subject  string             `json:"subject"`  // first line
	Trigger  string             `json:"trigger"`  // auto | manual | pre-restore | restore | import
	Kinds    []string           `json:"kinds"`    // kinds that changed (claude / codex)
	Projects []memoryProjectRef `json:"projects"` // claude projects that changed
	Files    int                `json:"files"`    // number of changed files
}

// Separators for parsing git log output: \x1e between records, \x1f between fields. Both are
// control characters that cannot occur in a memory body (md), so the split is unambiguous.
const (
	memoryRecSep = "\x1e"
	memoryFldSep = "\x1f"
)

// memoryListSnapshots returns snapshots newest first. A non-empty before starts the list at the
// most recent snapshot at or before that RFC3339 time (what the date-picker UI rests on).
func memoryListSnapshots(limit int, before string) ([]memorySnapshotInfo, error) {
	// Validate input before checking for the repo, so a malformed before is a 400 even with no
	// history at all.
	if before != "" {
		if _, err := time.Parse(time.RFC3339, before); err != nil {
			return nil, fmt.Errorf("before must be RFC3339")
		}
	}
	if !memoryHasCommits() {
		return []memorySnapshotInfo{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args := []string{"log", memoryBranch, "-n", strconv.Itoa(limit),
		"--pretty=format:" + memoryRecSep + "%H" + memoryFldSep + "%aI" + memoryFldSep + "%s" + memoryFldSep + "%(trailers:key=AF-Trigger,valueonly)",
		"--name-only"}
	if before != "" {
		args = append(args, "--before="+before)
	}
	out, err := memoryGitRun(args...)
	if err != nil {
		return nil, err
	}
	list := []memorySnapshotInfo{}
	for _, rec := range strings.Split(out, memoryRecSep) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		head, rest, _ := strings.Cut(rec, "\n")
		fields := strings.Split(head, memoryFldSep)
		if len(fields) < 4 || fields[0] == "" {
			continue
		}
		info := memorySnapshotInfo{
			Rev: fields[0], At: fields[1], Subject: fields[2],
			Trigger: strings.TrimSpace(fields[3]),
		}
		if len(info.Rev) >= 8 {
			info.Short = info.Rev[:8]
		}
		info.Kinds, info.Projects, info.Files = memorySummarizePaths(strings.Split(rest, "\n"))
		list = append(list, info)
	}
	return list, nil
}

// memorySummarizePaths folds a list of changed paths into kinds / claude projects / a count.
func memorySummarizePaths(paths []string) (kinds []string, projects []memoryProjectRef, files int) {
	projects = []memoryProjectRef{}
	kinds = []string{}
	seenKind, seenSlug := map[string]bool{}, map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		files++
		if kind, _, ok := strings.Cut(p, "/"); ok && !seenKind[kind] {
			seenKind[kind] = true
			kinds = append(kinds, kind)
		}
		if slug, ok := memoryScopeSlug(p); ok && !seenSlug[slug] {
			seenSlug[slug] = true
			projects = append(projects, memoryProjectRef{Slug: slug, Display: memorySlugDisplay(slug)})
		}
	}
	return kinds, projects, files
}

// memoryHeadTime is the author time of the newest snapshot (zero value when there is no commit).
func memoryHeadTime() time.Time {
	out, err := memoryGitRun("log", "-1", "--pretty=format:%aI", memoryBranch)
	if err != nil || out == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, out)
	if err != nil {
		return time.Time{}
	}
	return t
}

// memoryResolveRev resolves rev (a sha or ref) or at (RFC3339) to a snapshot sha. at means "the
// most recent snapshot at or before that time" — the semantics of a rollback by date
// (docs/log/39 item 3).
func memoryResolveRev(rev, at string) (string, error) {
	switch {
	case rev != "":
		if !memoryRevSafe(rev) {
			return "", fmt.Errorf("invalid rev")
		}
		sha, err := memoryGitRun("rev-parse", "--verify", "--quiet", rev+"^{commit}")
		if err != nil || sha == "" {
			return "", fmt.Errorf("unknown rev %q", rev)
		}
		return sha, nil
	case at != "":
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			return "", fmt.Errorf("at must be RFC3339")
		}
		sha, err := memoryGitRun("rev-list", "-1", "--before="+at, memoryBranch)
		if err != nil || sha == "" {
			return "", fmt.Errorf("no snapshot at or before %s", at)
		}
		return sha, nil
	}
	return "", fmt.Errorf("rev or at required")
}

// memoryRevSafe checks that a rev string cannot be used for option injection or path escape.
// Git's revision syntax is expressive, so the accepted alphabet is narrowed to alphanumerics
// and a few symbols.
func memoryRevSafe(rev string) bool {
	if rev == "" || len(rev) > 200 || strings.HasPrefix(rev, "-") {
		return false
	}
	for _, r := range rev {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '/', r == '.', r == '~', r == '^':
		default:
			return false
		}
	}
	return !strings.Contains(rev, "..")
}

// memoryPathSafe reports whether a diff / restore path scope stays within a declared prefix of
// the repo.
func memoryPathSafe(p string) bool {
	if p == "" {
		return true // omitted = the whole tree
	}
	if strings.HasPrefix(p, "-") || strings.Contains(p, "..") || filepath.IsAbs(p) {
		return false
	}
	for _, r := range memoryRootDecls() {
		if p == r.RepoPrefix || strings.HasPrefix(p, r.RepoPrefix+"/") {
			return true
		}
	}
	return false
}

// memoryDiff returns a unified diff between two points. An empty from means "the change this
// commit introduced" = the diff against its parent (the first snapshot has no parent, so it is
// diffed against the empty tree).
//
// Both ends are always named as explicit commits. `git diff <rev>` would diff rev against the
// working tree and mix in the staging contents (= the current live state), which is wrong for
// browsing history; parent-dependent shorthands such as `<rev>^!` are avoided for the same
// reason.
func memoryDiff(from, to, path string) (string, error) {
	if !memoryPathSafe(path) {
		return "", fmt.Errorf("invalid path scope")
	}
	base := from
	if base == "" {
		parent, err := memoryGitRun("rev-parse", "--verify", "--quiet", to+"^{commit}^")
		if err != nil || parent == "" {
			// No parent (the first snapshot) — diff against the empty tree.
			empty, eerr := memoryGitRun("hash-object", "-t", "tree", "/dev/null")
			if eerr != nil || empty == "" {
				return "", fmt.Errorf("resolve empty tree: %v", eerr)
			}
			base = empty
		} else {
			base = parent
		}
	}
	args := []string{"diff", "--no-color", "--find-renames", base, to}
	if path != "" {
		args = append(args, "--", path)
	}
	return memoryGitRun(args...)
}
