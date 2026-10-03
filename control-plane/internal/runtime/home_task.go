// home_task.go — the inside of the one-shot task that operates on an EFS home (ecs,
// ADR 0045 decision 31): `af-cp efs-home-op`, run by the stack's home-ops task definition
// with the file system's root mounted and the operation and membership in its environment.
//
// The task runs as root on the whole file system, because the stack declares one task
// definition for every member and RunTask cannot swap a volume's access point. So the
// confinement is here: the membership id is checked to be one path element, nothing is
// removed unless the root is the NFS mount, and every removal goes through handles on
// /home/<id> or /claude-config/<id> opened without following a link.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// homeWipeDestroy removes both of a member's EFS directories, for Destroy. Only the task
// accepts it; a member's or an administrator's wipe never asks for it.
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

// errHomeOpRefused marks a refusal that happened before anything was removed.
var errHomeOpRefused = errors.New("refused")

// isEFSMount is a seam: tests run against a temporary directory, which is not a mount.
var isEFSMount = efsMount

// homeOpOpened is a seam called once every directory the operation removes from is held
// open, before the first removal; tests swap a path under it to prove the removal follows
// the handle, not the path.
var homeOpOpened = func() {}

// RunEFSHomeOp performs op on membership's directories under root, the EFS file system's
// root as the task mounts it.
//
//   - repos:   remove /home/<id>/repos
//   - clean:   remove everything at the top of /home/<id> except homeKeep
//   - destroy: remove /home/<id> and /claude-config/<id> themselves
//
// Every directory on the way down — home, claude-config, the member's own — is opened
// without following a link (openDir), and everything after that goes through those
// handles (os.Root: openat-based, no resolution that leaves the directory). Checking a
// path and then removing by path would let a rename between the two send the removal into
// another member's home.
//
// A directory that does not exist is a home nobody booted (or one already removed), so it
// is success: the operation is idempotent and a retry after a partial failure is safe.
// An error wrapping errHomeOpRefused means nothing was touched: every check, for every
// directory the operation will touch, runs before the first removal.
func RunEFSHomeOp(ctx context.Context, root, op, membership string) error {
	if !ValidMembershipID(membership) {
		return fmt.Errorf("%w: membership id %q cannot name a home", errHomeOpRefused, membership)
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return fmt.Errorf("%w: file system root %q", errHomeOpRefused, root)
	}
	var parents []string
	switch HomeWipe(op) {
	case HomeWipeRepos, HomeWipeClean:
		parents = []string{"home"}
	case homeWipeDestroy:
		parents = []string{"home", "claude-config"}
	default:
		return fmt.Errorf("%w: unknown home operation %q", errHomeOpRefused, op)
	}
	// The task mounts the file system here. Without the mount the paths below are an empty
	// directory of the container's own disk, every removal succeeds on nothing, and the
	// member is told their home was cleaned while every byte of it stayed.
	if ok, err := isEFSMount(root); err != nil || !ok {
		return fmt.Errorf("%w: %s is not the EFS mount (%v)", errHomeOpRefused, root, err)
	}
	fs, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("%w: open %s: %v", errHomeOpRefused, root, err)
	}
	defer fs.Close()

	// Preflight: hold every directory open before removing anything.
	type held struct{ parent, member *os.Root }
	var dirs []held
	defer func() {
		for _, d := range dirs {
			if d.member != nil {
				d.member.Close()
			}
			d.parent.Close()
		}
	}()
	for _, name := range parents {
		parent, ok, err := openDir(fs, name)
		if err != nil {
			return err
		}
		if !ok {
			continue // no /home at all: nothing of anybody's to remove
		}
		member, ok, err := openDir(parent, membership)
		if err != nil {
			parent.Close()
			return err
		}
		if !ok {
			member = nil
		}
		dirs = append(dirs, held{parent, member})
	}
	homeOpOpened()

	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.member == nil {
			continue
		}
		switch HomeWipe(op) {
		case HomeWipeRepos:
			if err := d.member.RemoveAll("repos"); err != nil {
				return fmt.Errorf("remove repos: %w", err)
			}
		case HomeWipeClean:
			if err := emptyDir(d.member, homeKeep); err != nil {
				return err
			}
		case homeWipeDestroy:
			if err := emptyDir(d.member, nil); err != nil {
				return err
			}
			// Empty now; Remove on the name. Were it swapped for a link meanwhile, what goes
			// is the link, never what it points at.
			if err := d.parent.Remove(membership); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", membership, err)
			}
		}
	}
	return nil
}

// openDir opens name inside r as a root of its own, only if what is there is a real
// directory: false with no error when nothing is there, a refusal for a link or a file.
// The handle is proven to be the directory the Lstat saw (same device and inode), so a
// swap between the two cannot hand back another directory.
func openDir(r *os.Root, name string) (*os.Root, bool, error) {
	fi, err := r.Lstat(name)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: lstat %s: %v", errHomeOpRefused, name, err)
	}
	if !fi.IsDir() {
		return nil, false, fmt.Errorf("%w: %s is not a directory (%s)", errHomeOpRefused, name, fi.Mode().Type())
	}
	sub, err := r.OpenRoot(name)
	if err != nil {
		return nil, false, fmt.Errorf("%w: open %s: %v", errHomeOpRefused, name, err)
	}
	got, err := sub.Stat(".")
	if err != nil || !os.SameFile(fi, got) {
		sub.Close()
		return nil, false, fmt.Errorf("%w: %s changed while it was being opened", errHomeOpRefused, name)
	}
	return sub, true, nil
}

// emptyDir removes every top-level entry of r whose name keep does not hold, through r.
func emptyDir(r *os.Root, keep map[string]bool) error {
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := r.RemoveAll(e.Name()); err != nil {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return nil
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
