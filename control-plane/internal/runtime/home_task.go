// home_task.go — the inside of the one-shot task that operates on an EFS home (ecs,
// ADR 0045 decision 31): `af-cp efs-home-op`, run by the stack's home-ops task definition
// with the file system's root mounted and the operation and membership in its environment.
//
// The task runs as root on the whole file system, because the stack declares one task
// definition for every member and RunTask cannot swap a volume's access point. So the
// confinement is here: the membership id is checked to be one path element, every path
// removed is built from it and checked to lie strictly under /home, /claude-config or
// /home-keep, and nothing is removed unless the root really is a mount.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// homeWipeDestroy removes every one of a member's EFS directories, for Destroy. Only the
// task accepts it; a member's or an administrator's wipe never asks for it.
const homeWipeDestroy HomeWipe = "destroy"

// membershipIDRe is what a membership id may be for its EFS paths. Store ids are 32 hex
// characters; the wider class only admits ids a test or an older row may carry. No dot,
// no slash: the id is one path element and never "." or "..".
var membershipIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidMembershipID reports whether id can name a member's EFS directories.
func ValidMembershipID(id string) bool { return membershipIDRe.MatchString(id) }

// Exit codes of `af-cp efs-home-op`. The CP reads them through DescribeTasks.
const (
	HomeOpExitOK      = 0
	HomeOpExitFailed  = 1 // the removal itself failed; the home may be partly removed
	HomeOpExitRefused = 2 // bad input or an unsafe file system: nothing was removed
)

// homeTaskDirs are the top-level EFS directories a member's directories live in, the only
// ones the task removes anything under: the Fargate home, the Claude state, and the ecs-ec2
// keep-list (runtime_ecs_ec2.go, ensureKeepAccessPoint).
var homeTaskDirs = []string{"home", "claude-config", "home-keep"}

// errHomeOpRefused marks a refusal that happened before anything was removed.
var errHomeOpRefused = errors.New("refused")

// isMountPoint is a seam: tests run against a temporary directory, which is not a mount.
var isMountPoint = mountPoint

// RunEFSHomeOp performs op on membership's directories under root, the EFS file system's
// root as the task mounts it.
//
//   - repos:   remove /home/<id>/repos
//   - clean:   remove everything at the top of /home/<id> except homeKeep
//   - destroy: remove /home/<id>, /claude-config/<id> and /home-keep/<id> themselves
//     (ecs-ec2 keeps the home on EBS and the keep-list in /home-keep; Fargate has no
//     /home-keep, and a missing directory is success)
//
// A directory that does not exist is a home nobody booted (or one already removed), so it
// is success: the operation is idempotent and a retry after a partial failure is safe.
// An error wrapping errHomeOpRefused means nothing was touched.
func RunEFSHomeOp(ctx context.Context, root, op, membership string) error {
	if !ValidMembershipID(membership) {
		return fmt.Errorf("%w: membership id %q cannot name a home", errHomeOpRefused, membership)
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return fmt.Errorf("%w: file system root %q", errHomeOpRefused, root)
	}
	// The task mounts the file system here. Without the mount the paths below are an empty
	// directory of the container's own disk, every removal succeeds on nothing, and the
	// member is told their home was cleaned while every byte of it stayed.
	if ok, err := isMountPoint(root); err != nil || !ok {
		return fmt.Errorf("%w: %s is not a mount (%v)", errHomeOpRefused, root, err)
	}
	home := filepath.Join(root, "home", membership)
	claude := filepath.Join(root, "claude-config", membership)
	keep := filepath.Join(root, "home-keep", membership)
	for _, p := range []string{home, claude, keep} {
		if err := confinedTo(root, p); err != nil {
			return err
		}
	}
	switch HomeWipe(op) {
	case HomeWipeRepos:
		if ok, err := plainDir(home); err != nil || !ok {
			return err
		}
		return RemoveAllContext(ctx, filepath.Join(home, "repos"))
	case HomeWipeClean:
		if ok, err := plainDir(home); err != nil || !ok {
			return err
		}
		return cleanHomeDir(ctx, home)
	case homeWipeDestroy:
		for _, p := range []string{home, claude, keep} {
			if ok, err := plainDir(p); err != nil {
				return err
			} else if ok {
				if err := RemoveAllContext(ctx, p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return fmt.Errorf("%w: unknown home operation %q", errHomeOpRefused, op)
}

// HomeOpExitCode maps RunEFSHomeOp's error to the task's exit code.
func HomeOpExitCode(err error) int {
	switch {
	case err == nil:
		return HomeOpExitOK
	case errors.Is(err, errHomeOpRefused):
		return HomeOpExitRefused
	default:
		return HomeOpExitFailed
	}
}

// confinedTo refuses p unless it is two elements below root, in one of homeTaskDirs. The id is already validated; this is the check that stays true if
// that validation is ever loosened, because an empty or ".." id would otherwise turn the
// removal into one of the whole /home.
func confinedTo(root, p string) error {
	rel, err := filepath.Rel(root, p)
	parts := strings.Split(rel, string(filepath.Separator))
	if err != nil || len(parts) != 2 || !slices.Contains(homeTaskDirs, parts[0]) ||
		parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return fmt.Errorf("%w: %s is not a member's directory under %s", errHomeOpRefused, p, root)
	}
	return nil
}

// plainDir reports whether p is a real directory: false with no error when it does not
// exist, an error when it is a symlink or a file. A link at /home/<id> pointing elsewhere
// on the file system would otherwise have the removal follow it into another member's home.
func plainDir(p string) (bool, error) {
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%w: %s is not a directory (%s)", errHomeOpRefused, p, fi.Mode().Type())
	}
	return true, nil
}
