// home_wipe.go — removing part of a member's home, done by the adapter that knows where
// the home is.
//
// Only the adapter knows that. docker and native keep the home in a directory on the CP's
// own host; ecs keeps it on EFS and ecs-ec2 on a per-user EBS volume, while the CP task's
// data directory is an empty /tmp. A CP that wiped "<data dir>/home" itself would find
// nothing there on AWS, and removing a path that does not exist succeeds — so the member
// would be told their home was cleaned while every byte of it stayed. An adapter that
// cannot reach its home therefore does not claim these ports, the CP refuses before it
// stops anything, and the Console does not offer the button (runtime.go: a button that
// works on one deployment profile and silently does nothing on another is worse than no
// button).
package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// HomeWipe says how much of a member's home a wipe removes.
type HomeWipe string

const (
	// HomeWipeRepos removes ~/repos, the working copies: a member's Recreate.
	HomeWipeRepos HomeWipe = "repos"
	// HomeWipeClean removes everything in the home except homeKeep — the logins,
	// connections and identity: a member's Clean home.
	HomeWipeClean HomeWipe = "clean"
)

// homeWiper is the member's half. The caller has stopped the workspace, calls WipeHome,
// and starts the workspace again at once, all inside one request behind the ingress idle
// timeout (60 s on the AWS deployment). The contract is "the next start does not see what
// was removed", and an adapter claims the port only if it can keep that promise within
// that request together with the Start.
type homeWiper interface {
	WipeHome(ctx context.Context, what HomeWipe) error
}

// homeEraser is the administrator's half, the offboarding step: remove everything in the
// home except homeKeep now, and leave the workspace stopped. "Now" is the contract. The
// member is leaving and nobody will start this workspace again, so a removal that waits
// for the next start never happens. It may take as long as Destroy does.
//
// Copies an adapter keeps outside the home (ecs-ec2's backup snapshots) are not part of
// the home and survive; homeBackupKeeper is how they go.
type homeEraser interface {
	EraseHome(ctx context.Context) error
}

// homeBackupKeeper is claimed by the adapters that keep copies of a home outside it
// (ecs-ec2's periodic snapshots, ADR 0045 decision 17). Clean home leaves them on purpose,
// because a copy that outlives the home is what a backup is for. Deleting them is
// therefore a separate, deliberate step, never a side effect of another action.
type homeBackupKeeper interface {
	HomeBackups(ctx context.Context) (HomeBackups, error)
	// DeleteHomeBackups deletes every copy, including one still being captured, and
	// returns how many it deleted.
	DeleteHomeBackups(ctx context.Context) (int, error)
}

// HomeBackups is what an administrator sees before deleting a member's backups.
type HomeBackups struct {
	// Count includes a copy that is still being captured: it will hold the home once it
	// completes.
	Count int `json:"count"`
	// Newest is when the most recent copy was taken; zero when Count is 0.
	Newest time.Time `json:"newest,omitzero"`
}

// Which adapter claims which port. The claiming direction is pinned here; the adapters
// that must not claim one are pinned in capabilities_test.go.
var (
	_ homeWiper        = (*dockerRuntime)(nil)
	_ homeWiper        = (*nativeRuntime)(nil)
	_ homeEraser       = (*dockerRuntime)(nil)
	_ homeEraser       = (*nativeRuntime)(nil)
	_ homeEraser       = (*ecsEC2Runtime)(nil)
	_ homeBackupKeeper = (*ecsEC2Runtime)(nil)
)

// ErrHomeWipeUnsupported is returned for an adapter that does not claim the port asked
// for. The CP checks CanWipeHome / CanEraseHome before it stops anything, so reaching this
// error means that check was skipped.
var ErrHomeWipeUnsupported = errors.New("this deployment's runtime cannot reach the workspace home")

// CanWipeHome reports whether a member's Recreate and Clean home can run on rt.
func CanWipeHome(rt Runtime) bool {
	_, ok := rt.(homeWiper)
	return ok
}

// WipeHome removes what from a stopped workspace's home, for a member's Recreate or Clean
// home.
func WipeHome(ctx context.Context, rt Runtime, what HomeWipe) error {
	w, ok := rt.(homeWiper)
	if !ok {
		return ErrHomeWipeUnsupported
	}
	return w.WipeHome(ctx, what)
}

// CanEraseHome reports whether an administrator's Clean home can run on rt.
func CanEraseHome(rt Runtime) bool {
	_, ok := rt.(homeEraser)
	return ok
}

// EraseHome removes everything but homeKeep from a stopped workspace's home, for an
// administrator's Clean home.
func EraseHome(ctx context.Context, rt Runtime) error {
	e, ok := rt.(homeEraser)
	if !ok {
		return ErrHomeWipeUnsupported
	}
	return e.EraseHome(ctx)
}

// HomeBackupsOf lists the copies rt keeps of its home. ok=false on a runtime that keeps
// none, which is every runtime but the EC2 slot pool.
func HomeBackupsOf(ctx context.Context, rt Runtime) (HomeBackups, bool, error) {
	k, ok := rt.(homeBackupKeeper)
	if !ok {
		return HomeBackups{}, false, nil
	}
	b, err := k.HomeBackups(ctx)
	return b, true, err
}

// DeleteHomeBackups deletes the copies rt keeps of its home. ok=false as for HomeBackupsOf.
func DeleteHomeBackups(ctx context.Context, rt Runtime) (int, bool, error) {
	k, ok := rt.(homeBackupKeeper)
	if !ok {
		return 0, false, nil
	}
	n, err := k.DeleteHomeBackups(ctx)
	return n, true, err
}

// HomeOperations is which of the operations above a deployment's adapter performs. The
// Console needs the answer before it offers a button, and every workspace of a deployment
// is built by the same factory, so it is asked once per deployment rather than per member.
type HomeOperations struct {
	Wipe    bool // a member's Recreate and Clean home
	Erase   bool // an administrator's Clean home
	Backups bool // listing and deleting a member's backup copies
}

// HomeOperationsOf answers HomeOperations from a runtime the factory builds for an empty
// workspace. That is safe because every factory's New only assembles a value; none of
// them talks to Docker or AWS.
func HomeOperationsOf(f RuntimeFactory) HomeOperations {
	rt := f.New(Workspace{}, "", nil)
	_, wipe := rt.(homeWiper)
	_, erase := rt.(homeEraser)
	_, backups := rt.(homeBackupKeeper)
	return HomeOperations{Wipe: wipe, Erase: erase, Backups: backups}
}

// wipeLocalHome is the docker and native wipe: both keep the home at <dataDir>/home, the
// container's bind-mount source (native's process HOME). The caller has stopped the
// workspace, so nothing is writing into it while it is removed.
func wipeLocalHome(ctx context.Context, dataDir string, what HomeWipe) error {
	// An empty dataDir would turn "home/repos" into a path relative to the CP's working
	// directory.
	if dataDir == "" {
		return errors.New("workspace has no data directory")
	}
	switch what {
	case HomeWipeRepos:
		return RemoveAllContext(ctx, filepath.Join(dataDir, "home", "repos"))
	case HomeWipeClean:
		return cleanHomeContext(ctx, dataDir)
	}
	return fmt.Errorf("unknown home wipe %q", what)
}

// WipeHome — docker: see wipeLocalHome.
func (d *dockerRuntime) WipeHome(ctx context.Context, what HomeWipe) error {
	return wipeLocalHome(ctx, d.dataDir, what)
}

// EraseHome — docker: the same removal as a member's Clean home, which already happens
// before anything could start the container again.
func (d *dockerRuntime) EraseHome(ctx context.Context) error {
	return wipeLocalHome(ctx, d.dataDir, HomeWipeClean)
}

// WipeHome — native: see wipeLocalHome.
func (n *nativeRuntime) WipeHome(ctx context.Context, what HomeWipe) error {
	return wipeLocalHome(ctx, n.dataDir, what)
}

// EraseHome — native: as docker.
func (n *nativeRuntime) EraseHome(ctx context.Context) error {
	return wipeLocalHome(ctx, n.dataDir, HomeWipeClean)
}
