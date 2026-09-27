package gitx

// Recreating a deleted worktree at its original path (issue #1040).
//
// A session's identity is session.UUID(dir, name), and claude / opencode / cursor keep their
// own stores per cwd, so a conversation can only be resumed from the exact folder it ran in.
// Putting a worktree back at that path is therefore the whole of "bring the sessions back":
// the ordinary resume path then works for every kind unchanged. EnsureWorktree cannot do it —
// it derives the folder from the parent's name and a branch segment, which does not reproduce
// a nested name such as repo@wip-A@wip-B.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Where a recreated worktree's commits come from, best first. The Console shows the source,
// so these strings are wire values.
const (
	RecreateDeleted = "deleted" // the delete recorded it: its commit, branch and uncommitted work
	RecreateLocal   = "local"   // the local branch still exists
	RecreateRemote  = "remote"  // only a remote-tracking branch is left
	RecreateTrash   = "trash"   // the branch was deleted through the Console, which kept its SHA
	RecreateMerged  = "merged"  // the branch is gone, but a merge commit still names its head
	RecreateNew     = "new"     // nothing is left: a fresh branch of the same name off the parent
)

// RecreateCandidate is one way to put a deleted worktree back.
type RecreateCandidate struct {
	Source string `json:"source"`
	Branch string `json:"branch"`
	// SHA is the commit the worktree would start at; empty for RecreateNew, which starts
	// at Ref.
	SHA string `json:"sha,omitempty"`
	// Ref is the remote-tracking ref for RecreateRemote ("origin/x") and the base branch for
	// RecreateNew ("" = the parent's HEAD).
	Ref string `json:"ref,omitempty"`
	// PR is the pull request number a RecreateMerged merge commit names, when it names one.
	PR int `json:"pr,omitempty"`
	// InUse is the folder of the working copy that has Branch checked out. git holds a
	// branch in one worktree at a time, so the only ways forward are opening that copy or
	// starting a new branch at SHA.
	InUse string `json:"in_use,omitempty"`
	// Snapshot (RecreateDeleted only) is the commit holding the working tree at the delete,
	// laid over the checkout afterwards.
	Snapshot string `json:"snapshot,omitempty"`
	// Moved (RecreateDeleted only) says Branch has since moved off SHA. Checking it out would
	// not be the state that was deleted, so this candidate then needs a new branch at SHA —
	// the branch as it is now is the separate RecreateLocal candidate.
	Moved bool `json:"moved,omitempty"`
}

// NeedsNewBranch reports whether c can only be taken on a new branch: its branch is checked
// out elsewhere, has moved since the delete, or there was none (a detached HEAD).
func (c RecreateCandidate) NeedsNewBranch() bool {
	return c.InUse != "" || c.Moved || c.Branch == ""
}

// ErrRecreatePathExists is returned when something already sits at the target path.
var ErrRecreatePathExists = errors.New("the path already exists")

// RecreateParent finds the main working copy a deleted worktree folder name belonged to. A
// worktree folder is "<parent folder>@<segment>", and the parent may itself have been a
// worktree (repo@wip-A@wip-B was launched from repo@wip-A), so the prefixes are tried
// longest first. A surviving linked worktree resolves to its main working copy, which holds
// the registry `worktree add` writes to.
func RecreateParent(name string) (string, bool) {
	for n := name; ; {
		i := strings.LastIndex(n, "@")
		if i <= 0 {
			return "", false
		}
		n = n[:i]
		dir, ok := ResolveRepoDir(n)
		if !ok || !IsGitRepo(dir) {
			continue
		}
		if IsLinkedWorktree(dir) {
			if p := WorktreeParent(dir); p != "" {
				return p, true
			}
			continue
		}
		return dir, true
	}
}

// ResolveRecreate lists the ways to put the worktree folder name back, best first.
//
// branches are the branch names the folder is known to have held, most recent first (the
// sessions' start branches, which follow a rename done through the Console). With none, the
// folder segment is matched against the existing branches, and failing that used as the new
// branch's name. trashSHA answers the SHA the Console recorded when it deleted a branch, or "".
//
// Each branch yields at most one candidate — the first source that has it — and when no
// branch yields any, the answer is a single RecreateNew for the first name.
//
// tomb, when the delete recorded one for this folder, comes first as RecreateDeleted: it is
// the only candidate that also brings back the uncommitted work.
func ResolveRecreate(parent, name string, branches []string, trashSHA func(branch string) string, tomb *WorktreeTombstone) []RecreateCandidate {
	deleted := deletedCandidate(parent, tomb)
	names := validBranchNames(parent, branches)
	if len(names) == 0 {
		names = branchesForFolderSeg(parent, name)
	}
	if len(names) == 0 {
		return deleted
	}
	inUse := branchOccupants(parent)
	out := deleted
	for _, b := range names {
		if sha := GitBranchSHA(parent, b); sha != "" {
			c := RecreateCandidate{Source: RecreateLocal, Branch: b, SHA: sha}
			if occ := inUse[b]; occ != "" {
				c.InUse = filepath.Base(occ)
			}
			out = append(out, c)
			continue
		}
		if ref := remoteTrackingRef(parent, b); ref != "" {
			if sha, err := Run(parent, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil && sha != "" {
				out = append(out, RecreateCandidate{Source: RecreateRemote, Branch: b, SHA: sha, Ref: ref})
				continue
			}
		}
		if trashSHA != nil {
			if sha := trashSHA(b); sha != "" && commitExists(parent, sha) {
				out = append(out, RecreateCandidate{Source: RecreateTrash, Branch: b, SHA: sha})
				continue
			}
		}
		if sha, pr := mergedBranchHead(parent, b); sha != "" {
			out = append(out, RecreateCandidate{Source: RecreateMerged, Branch: b, SHA: sha, PR: pr})
			continue
		}
	}
	if len(out) > 0 {
		return out
	}
	base := GitCurrentBranch(parent)
	if base == "(detached)" {
		base = ""
	}
	return append(out, RecreateCandidate{Source: RecreateNew, Branch: names[0], Ref: base})
}

// deletedCandidate turns the tombstone into its candidate, or nothing when it belongs to
// another repository or its commit is no longer there. A snapshot gc has taken (its pin was
// dropped by hand) is left out rather than failing the checkout.
func deletedCandidate(parent string, t *WorktreeTombstone) []RecreateCandidate {
	if t == nil || realPath(t.Parent) != realPath(parent) || !commitExists(parent, t.Head) {
		return nil
	}
	c := RecreateCandidate{Source: RecreateDeleted, Branch: t.Branch, SHA: t.Head}
	if t.Snapshot != "" && commitExists(parent, t.Snapshot) {
		c.Snapshot = t.Snapshot
	}
	if c.Branch != "" && !ValidBranchName(parent, c.Branch) {
		c.Branch = ""
	}
	if c.Branch != "" {
		if tip := GitBranchSHA(parent, c.Branch); tip != "" && tip != c.SHA {
			c.Moved = true
		}
		if occ := branchOccupants(parent)[c.Branch]; occ != "" {
			c.InUse = filepath.Base(occ)
		}
	}
	return []RecreateCandidate{c}
}

// branchOccupants maps each branch checked out in parent's repository to the working copy
// holding it, parent included (WorktreeBranches leaves the queried copy out, and git refuses
// the parent's branch just the same). A registration whose folder is gone is not an occupant:
// a worktree deleted behind git's back is still listed with its branch until pruned, and that
// entry is usually the very folder being recreated — RecreateWorktreeAt prunes it first.
func branchOccupants(parent string) map[string]string {
	m := map[string]string{}
	for b, p := range WorktreeBranches(parent) {
		if _, err := os.Stat(p); err == nil {
			m[b] = p
		}
	}
	if b := GitCurrentBranch(parent); b != "" && b != "(detached)" {
		m[b] = parent
	}
	return m
}

// RecreateWorktreeAt adds a worktree of parent at exactly dir from candidate c, or, when
// newBranch is set, on newBranch started at c's commit (the way past a branch that is checked
// out elsewhere). The caller has validated dir and newBranch; dir must not exist.
func RecreateWorktreeAt(parent, dir string, c RecreateCandidate, newBranch string) error {
	if _, err := os.Lstat(dir); err == nil {
		return ErrRecreatePathExists
	}
	// A worktree deleted outside the Console leaves its registration behind, and
	// `worktree add` refuses a path that is "missing but already registered".
	_ = Cmd(parent, "worktree", "prune").Run()
	var args []string
	created := "" // the branch this call creates, taken away again if the recreate fails
	switch {
	case newBranch != "":
		created = newBranch
		start := c.SHA
		if start == "" {
			start = c.Ref
		}
		args = []string{"worktree", "add", "-b", newBranch, dir}
		if start != "" {
			args = append(args, start)
		}
	case c.Source == RecreateDeleted:
		if c.Branch == "" {
			return fmt.Errorf("a new branch is required: HEAD was detached when it was deleted")
		}
		if GitBranchExists(parent, c.Branch) {
			args = []string{"worktree", "add", dir, c.Branch}
		} else {
			args = []string{"worktree", "add", "-b", c.Branch, dir, c.SHA}
			created = c.Branch
		}
	case c.Source == RecreateLocal:
		args = []string{"worktree", "add", dir, c.Branch}
	case c.Source == RecreateRemote:
		args = []string{"worktree", "add", "--track", "-b", c.Branch, dir, c.Ref}
	case c.Source == RecreateTrash || c.Source == RecreateMerged:
		args = []string{"worktree", "add", "-b", c.Branch, dir, c.SHA}
	case c.Source == RecreateNew:
		args = []string{"worktree", "add", "-b", c.Branch, dir}
		if c.Ref != "" {
			args = append(args, c.Ref)
		}
	default:
		return fmt.Errorf("unknown source %q", c.Source)
	}
	if out, err := Combined(parent, args...); err != nil {
		return fmt.Errorf("worktree add: %v: %s", err, out)
	}
	finishNewWorktree(dir, parent)
	if c.Source == RecreateDeleted && c.Snapshot != "" {
		// The working tree as it was deleted, over a checkout of its HEAD. The index stays at
		// HEAD, so every change comes back unstaged — staged-ness is the one thing the
		// snapshot does not keep. Files the snapshot lacks are removed, untracked ones in it
		// come back untracked.
		if out, err := Combined(dir, "restore", "--source="+c.Snapshot, "--worktree", "--", "."); err != nil {
			// Take the checkout away again: left standing, a retry would find the folder back
			// and count as done, and the uncommitted work would never be laid over it. Nothing
			// of the user's is in it yet — it is the recorded commit, fresh.
			_, _ = Combined(parent, "worktree", "remove", "--force", dir)
			if created != "" {
				_, _ = Combined(parent, "branch", "-D", created)
			}
			return fmt.Errorf("its uncommitted work could not be laid over the checkout, so nothing was recreated: %v: %s", err, out)
		}
	}
	switch {
	case newBranch != "":
	case c.Source == RecreateLocal || c.Source == RecreateRemote:
		FastForwardWorktree(dir) // the branch as it is now, same as an existing-branch launch
	case c.Source == RecreateNew:
		FastForwardNewWorktreeToOrigin(dir, c.Ref)
	}
	return nil
}

// ValidBranchName reports whether git accepts name as a branch name. It never starts with
// "-", so it cannot be read as an option either.
func ValidBranchName(dir, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	return OK(dir, "check-ref-format", "--branch", name)
}

func validBranchNames(dir string, branches []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range branches {
		b = strings.TrimSpace(b)
		if seen[b] || b == "(detached)" || !ValidBranchName(dir, b) {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	return out
}

// branchesForFolderSeg names the branch a folder without any recorded branch most likely
// held: an existing local or remote branch whose folder segment is the folder's own, else
// the segment itself. Lossy by nature (sanitizeSeg maps "/" to "-"), so it is only the
// fallback when no session recorded a branch.
func branchesForFolderSeg(parent, name string) []string {
	seg := name[strings.LastIndex(name, "@")+1:]
	if seg == "" {
		return nil
	}
	if out, err := Run(parent, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes"); err == nil {
		for _, ln := range strings.Split(out, "\n") {
			ln = strings.TrimSpace(ln)
			var b string
			switch {
			case strings.HasPrefix(ln, "refs/heads/"):
				b = strings.TrimPrefix(ln, "refs/heads/")
			case strings.HasPrefix(ln, "refs/remotes/"):
				rest := strings.TrimPrefix(ln, "refs/remotes/")
				i := strings.Index(rest, "/")
				if i < 0 || rest[i+1:] == "HEAD" {
					continue
				}
				b = rest[i+1:]
			default:
				continue
			}
			if sanitizeSeg(b) == seg {
				return []string{b}
			}
		}
	}
	if ValidBranchName(parent, seg) {
		return []string{seg}
	}
	return nil
}

// remoteTrackingRef returns the remote-tracking ref holding branch ("origin/x"), preferring
// origin when several remotes have it, or "".
func remoteTrackingRef(dir, branch string) string {
	out, err := Run(dir, "for-each-ref", "--format=%(refname:short)", "refs/remotes/*/"+branch)
	if err != nil {
		return ""
	}
	first := ""
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if ln == "origin/"+branch {
			return ln
		}
		if first == "" {
			first = ln
		}
	}
	return first
}

func commitExists(dir, sha string) bool {
	return OK(dir, "cat-file", "-e", sha+"^{commit}")
}

// The merge-commit subjects that name the merged branch: GitHub, Bitbucket, and git's own
// (which GitLab uses too).
var (
	mergeSubjectGitHub    = regexp.MustCompile(`^Merge pull request #(\d+) from [^/\s]+/(\S+)$`)
	mergeSubjectBitbucket = regexp.MustCompile(`^Merged in (\S+) \(pull request #(\d+)\)`)
	mergeSubjectGit       = regexp.MustCompile(`^Merge (?:remote-tracking )?branch '([^']+)'`)
)

// mergeLogTimeout bounds the history scan; a huge repository answers "not found" rather than
// holding the plan request.
const mergeLogTimeout = 20 * time.Second

// mergedBranchHead finds the newest merge commit whose subject names branch and returns its
// second parent — the branch head that was merged — plus the pull request number when the
// subject carries one. Works offline, which is why it is preferred to asking the forge. A
// squash or rebase merge leaves no such commit and answers "".
func mergedBranchHead(dir, branch string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), mergeLogTimeout)
	defer cancel()
	out, err := CmdContext(ctx, dir, "log", "--branches", "--remotes", "--merges", "-F", "--grep="+branch,
		"-n", "200", "--format=%P%x09%s").Output()
	if err != nil {
		return "", 0
	}
	for _, ln := range strings.Split(string(out), "\n") {
		parents, subject, ok := strings.Cut(ln, "\t")
		if !ok {
			continue
		}
		ps := strings.Fields(parents)
		if len(ps) < 2 {
			continue
		}
		pr := 0
		switch {
		case matchGroup(mergeSubjectGitHub, subject, 2) == branch:
			pr, _ = strconv.Atoi(matchGroup(mergeSubjectGitHub, subject, 1))
		case matchGroup(mergeSubjectBitbucket, subject, 1) == branch:
			pr, _ = strconv.Atoi(matchGroup(mergeSubjectBitbucket, subject, 2))
		case matchGroup(mergeSubjectGit, subject, 1) == branch:
		default:
			continue
		}
		if commitExists(dir, ps[1]) {
			return ps[1], pr
		}
	}
	return "", 0
}

func matchGroup(re *regexp.Regexp, s string, i int) string {
	m := re.FindStringSubmatch(s)
	if len(m) <= i {
		return ""
	}
	return m[i]
}
