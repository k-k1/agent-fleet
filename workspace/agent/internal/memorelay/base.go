package memorelay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// gitTimeout bounds the one git call that decides a memo's repository. A memo is worth more
// than the mapping: when git is slow the memo is relayed as named.
var gitTimeout = 5 * time.Second

// baseRepo is the repository bucket a memo is filed under for the folder `repo` the model
// named: a worktree folder (`app@feature`) is filed under its base repository (`app`), so a
// queue set on the base's category sees the note (ADR 0137 §2). "" stays "", and a name that
// is not a single folder under ~/repos is returned as it came, for the Control Plane to judge,
// before it is joined to any path.
//
// The decision comes from git while the folder exists: only a linked worktree maps, to the
// folder of its main working copy, so a plain clone that happens to be named `a@b` is left
// alone. Once the folder is gone, git cannot say and the name rule the Console uses applies
// (everything before the first "@"). A git failure on a folder that exists relays the name
// unchanged: a guess could file a plain clone under a repository it is not part of, while an
// unmapped note is one drag away.
func baseRepo(repo string) string {
	if repo == "" {
		return ""
	}
	dir, ok := gitx.ResolveRepoDir(repo)
	if !ok {
		return repo
	}
	if _, err := os.Lstat(dir); err != nil {
		if os.IsNotExist(err) {
			return baseByName(repo)
		}
		return repo
	}
	if base, ok := worktreeBase(dir); ok {
		return base
	}
	return repo
}

// baseByName is the Console's rule for a folder that cannot be asked: `<base>@<slug>` is
// filed under <base>.
func baseByName(repo string) string {
	base, _, found := strings.Cut(repo, "@")
	if !found || base == "" {
		return repo
	}
	return base
}

// worktreeBase answers with the main working copy's folder name when dir is a linked
// worktree whose main working copy sits directly under ~/repos. A worktree of a worktree ends
// at the same main one, because git's common dir is shared by all of them.
func worktreeBase(dir string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := gitx.CmdContext(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	cmd.Env = withoutGitLocation(cmd.Env)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] == lines[1] {
		return "", false // not a linked worktree
	}
	// …/repos/app/.git -> …/repos/app; a bare main working copy has no ".git" to strip.
	main := lines[1]
	if filepath.Base(main) == ".git" {
		main = filepath.Dir(main)
	}
	root, err := filepath.EvalSymlinks(gitx.ReposRoot())
	if err != nil {
		return "", false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(main))
	if err != nil || parent != root {
		return "", false
	}
	name := filepath.Base(main)
	if _, ok := gitx.ResolveRepoDir(name); !ok {
		return "", false
	}
	return name, true
}

// withoutGitLocation drops the variables that point git somewhere other than the folder it is
// asked about, so the answer is about that folder whatever the Agent was started with.
func withoutGitLocation(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		switch name, _, _ := strings.Cut(e, "="); name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR":
			continue
		}
		out = append(out, e)
	}
	return out
}
