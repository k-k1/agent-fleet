package memorelay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// gitTimeout bounds the one git call that decides a memo's repository. A memo is worth more
// than the mapping: when git is slow the memo is relayed as named.
var gitTimeout = 5 * time.Second

// SpacePrefix marks a memo bucket that is a space (~/spaces/<folder>) rather than a repository;
// the Control Plane reads the same prefix (memoSpacePrefix). A folder under ~/repos is one path
// segment and never holds "/", so no repository's bucket collides with a space's.
const SpacePrefix = "spaces/"

// bucketFor is the bucket a memo from the session `from` is filed under for the `repo` the model
// named. A session in a space that names its own space's folder means that space, never a
// repository that happens to share the folder name; anything else goes through baseRepo.
func bucketFor(from, repo string) string {
	if repo != "" && strings.TrimSpace(from) != "" {
		if f := spaceOf(from); f != "" && repo == f {
			return SpacePrefix + f
		}
	}
	return baseRepo(repo)
}

// spaceOf is the space folder the session `from` runs in, "" when it runs in none or has no meta.
// The place stored at create decides; a meta written before places were stored falls back to
// the space marker under its directory, which is what decided the place since.
func spaceOf(from string) string {
	from = strings.TrimSpace(from)
	if !session.ValidName(from) {
		return ""
	}
	m, ok := session.ReadMeta(from)
	if !ok {
		return ""
	}
	if m.Place != nil {
		if m.Place.Kind == session.PlaceSpace {
			return m.Place.ID
		}
		return ""
	}
	return spacex.FolderOf(m.Dir)
}

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
