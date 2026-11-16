package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// ec2EFSMountBase is where a slot mounts a member's EFS access points (claude config and
// the keep-list), one directory per access point: /af-efs/<membership>/<access point id>.
// The task bind-mounts those directories instead of declaring EFS volumes.
//
// Why the slot mounts them and not ECS: an ECS-managed EFS volume of an awsvpc task is
// mounted through the task's own ENI, so the file system's security group has to admit the
// workspace security group, and then any process in any workspace can open its own NFS
// connection and name another member's access point (the file-system policy cannot tell
// members apart: the task role is shared and EFS enforces no tag condition for NFS clients).
// Mounted from the slot's network namespace, only the slot security group needs NFS, and
// the workspace security group can be closed (10-data, EfsWorkspaceIngress). It also lets
// the mount carry nosuid,nodev, which EFSVolumeConfiguration cannot express.
//
// The access point id is part of the path, so a mount found there is the right one: an
// access point recreated after a Destroy gets a new path instead of reusing a stale mount.
const ec2EFSMountBase = "/af-efs"

// efsMountTimeoutSec bounds one mount on the slot. A mount target the slot cannot reach
// (the security group rule is missing) otherwise hangs in the NFS client for minutes.
const efsMountTimeoutSec = 90

// efsUmountBudget bounds the best-effort unmount on a release or a quarantine.
const efsUmountBudget = 45 * time.Second

// errEFSMount is a slot that could not mount the member's access points. It is kept apart
// from a failed home mount on purpose: the usual causes are the deployment's (a security
// group rule, the file-system policy), not the slot's, so the caller must not quarantine.
var errEFSMount = errors.New("the member's EFS access points could not be mounted on the slot")

// Everything below is interpolated into a root shell on the slot, so each value has to be
// of a shape that needs no quoting and no mountinfo escaping.
var (
	safeEFSMountDir   = regexp.MustCompile(`^` + ec2EFSMountBase + `/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeAccessPointID = regexp.MustCompile(`^fsap-[A-Za-z0-9-]+$`)
	safeFileSystemID  = regexp.MustCompile(`^fs-[A-Za-z0-9-]+$`)
)

func (e *ecsEC2Runtime) efsMountDir() string {
	return ec2EFSMountBase + "/" + e.base.membershipID
}

func (e *ecsEC2Runtime) efsMountPoint(apID string) string {
	return e.efsMountDir() + "/" + apID
}

// efsMountCommand is the SSM script that mounts each access point at dir/<id>, over TLS,
// unless something is already mounted there. It is sent whole rather than installed by the
// launch template so that slots booted from an older template get it too; for the same
// reason it installs the EFS mount helper when the image does not carry it.
//
// POSIX sh. The mount is anonymous (no IAM): the file-system policy admits a TLS mount
// that names an access point, and reaching a mount target at all is what the security
// groups decide.
func efsMountCommand(fsID string, apIDs []string, dir, mountinfo string) (string, error) {
	if !safeFileSystemID.MatchString(fsID) {
		return "", fmt.Errorf("file system id %q is not one this script can carry", fsID)
	}
	if !safeEFSMountDir.MatchString(dir) {
		return "", fmt.Errorf("mount directory %q is not one this script can carry", dir)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "set -eu\nFS=%s MOUNTINFO=%s\n", fsID, mountinfo)
	b.WriteString(`{ command -v mount.efs || [ -x /sbin/mount.efs ]; } >/dev/null 2>&1 || dnf install -y -q amazon-efs-utils
mnt() {
  mkdir -p "$2"
  if awk -v m="$2" '$5 == m { found = 1 } END { exit !found }' "$MOUNTINFO"; then return 0; fi
`)
	fmt.Fprintf(&b, `  timeout %d mount -t efs -o tls,accesspoint="$1",nosuid,nodev "$FS:/" "$2" && return 0`+"\n", efsMountTimeoutSec)
	b.WriteString(`  echo "mounting access point $1 at $2 failed; the file system's security group must admit the slot security group on 2049 (40-ec2-pool, SlotEfsIngress)" >&2
  exit 1
}
`)
	for _, id := range apIDs {
		if !safeAccessPointID.MatchString(id) {
			return "", fmt.Errorf("access point id %q is not one this script can carry", id)
		}
		fmt.Fprintf(&b, "mnt %s %s/%s\n", id, dir, id)
	}
	return b.String(), nil
}

// efsUmountCommand is the SSM script that unmounts everything mounted under dir. A mount
// that is busy or whose server no longer answers is detached lazily: unlike the home there
// is no device to pull afterwards, so nothing is corrupted by a mount that lingers.
func efsUmountCommand(dir, mountinfo string) (string, error) {
	if !safeEFSMountDir.MatchString(dir) {
		return "", fmt.Errorf("mount directory %q is not one this script can carry", dir)
	}
	return fmt.Sprintf("EFSDIR=%s MOUNTINFO=%s\n", dir, mountinfo) + `rc=0
for m in $(awk -v p="$EFSDIR/" 'index($5, p) == 1 { print $5 }' "$MOUNTINFO"); do
  umount "$m" 2>/dev/null || umount -l "$m" || { echo "could not unmount $m" >&2; rc=1; }
done
exit $rc`, nil
}

// mountMemberEFS mounts the member's access points on the slot the home is on. It runs
// under homeLockMount like the home's own mount, so a release cannot unmount between this
// and the task's start, and it refuses once the home has left the slot.
func (e *ecsEC2Runtime) mountMemberEFS(ctx context.Context, p ec2Placement, prep ec2Prep) error {
	lctx, unlock, err := e.lockHome(ctx, homeLockMount)
	if err != nil {
		return err
	}
	defer unlock()
	if !e.homeStillOn(lctx, p) {
		return fmt.Errorf("%s on %s: %w", p.volumeID, p.instanceID, errHomeLeftSlot)
	}
	cmd, err := efsMountCommand(e.base.cfg.efsFileSystem, []string{prep.claudeAP, prep.keepAP}, e.efsMountDir(), slotMountInfo)
	if err != nil {
		return fmt.Errorf("%w: %w", errEFSMount, err)
	}
	if err := homeGuardOf(lctx).outcome(e.runOnSlot(lctx, p.instanceID, cmd)); err != nil {
		if errors.Is(err, errHomeLockLost) {
			return err
		}
		return fmt.Errorf("%w: %w", errEFSMount, err)
	}
	return nil
}

// umountMemberEFS takes the member's access points off a slot that is being given up.
// Best-effort and bounded: the home's detach never waits on it, and a mount it leaves
// behind is reachable only by root on the slot, not from the next member's task.
func (e *ecsEC2Runtime) umountMemberEFS(ctx context.Context, instanceID string) {
	cmd, err := efsUmountCommand(e.efsMountDir(), slotMountInfo)
	if err != nil {
		log.Printf("ecs-ec2: not unmounting the EFS access points of %s on %s: %v", e.base.name, instanceID, err)
		return
	}
	uctx, cancel := context.WithTimeout(ctx, efsUmountBudget)
	defer cancel()
	if err := e.runOnSlot(uctx, instanceID, cmd); err != nil {
		log.Printf("ecs-ec2: unmounting the EFS access points of %s on %s: %v", e.base.name, instanceID, err)
	}
}
