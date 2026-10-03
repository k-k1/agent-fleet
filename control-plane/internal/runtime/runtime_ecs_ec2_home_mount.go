package runtime

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// homeMountLocks serialises, per workspace, a mount of its home against the umount→detach
// half of a release. Without it a Start's background launch and a release (a golden seed's
// teardown, an eviction, Destroy) drive the same slot and the same mountpoint at once, and
// the interleaving "release umounts (nothing mounted yet) → launch mounts → release
// detaches" leaves a mount whose device is gone. That mount answers every later stat of
// /af-home/<id> with EIO, so each later mount of the same membership on that slot fails and
// the slot is quarantined (measured on a sandbox pool: SSM history shows the af-mount
// landing one second after the DetachVolume, twice, on two slots).
//
// Process-local, like startGen: two CP replicas are not serialised by it. The slot-side
// recovery in staleHomeMountScript is what still saves a slot when that happens.
var homeMountLocks sync.Map // workspace name -> *sync.Mutex

func (e *ecsEC2Runtime) homeMountLock() *sync.Mutex {
	v, _ := homeMountLocks.LoadOrStore(e.base.name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// errHomeLeftSlot is a mount that found the home no longer attached to the slot it was
// placed on: a release took it off in the meantime. It says nothing about the slot, so the
// caller must not quarantine for it.
var errHomeLeftSlot = fmt.Errorf("the home is no longer attached to the slot it was placed on")

// homeStillOn reports whether p's volume is still the home and still attached to p's slot.
// An unreadable answer counts as yes: the mount then runs and fails or succeeds on its own
// merits, which is what it did before this check existed.
func (e *ecsEC2Runtime) homeStillOn(ctx context.Context, p ec2Placement) bool {
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return true
	}
	return vol != nil && aws.ToString(vol.VolumeId) == p.volumeID && attachedInstance(vol) == p.instanceID
}

// quarantineUmountBudget bounds quarantineSlot's unmount. af-umount itself takes seconds;
// the budget is for an SSM agent that does not answer at all.
const quarantineUmountBudget = 45 * time.Second

// unclaimIfOurs drops the claim a failed launch placed, and only that one: the claim must
// still name the launch's slot, and no Start may have begun in this process since the
// launch did (gen). A later Start's claim — on this slot or another — stays.
func (e *ecsEC2Runtime) unclaimIfOurs(ctx context.Context, p ec2Placement, gen int64) {
	if e.generation().Load() != gen {
		return
	}
	vol, err := e.homeVolume(ctx)
	if err != nil || vol == nil || aws.ToString(vol.VolumeId) != p.volumeID {
		return
	}
	if ec2TagValue(vol.Tags, EC2TagClaim) != p.instanceID {
		return
	}
	e.unclaim(ctx, p.volumeID)
}

// Paths on the slot that the mount and umount commands read. Parameters rather than
// literals only so the test can run the same script against a fake /proc and /sys.
const (
	slotMountInfo = "/proc/self/mountinfo"
	slotSysBlock  = "/sys/dev/block"
)

// safeHomeMountPoint is the scope of everything below: one directory directly under
// /af-home, named with characters that need no quoting and no mountinfo escaping. A path
// outside it gets the plain helper call and nothing that could unmount anything.
var safeHomeMountPoint = regexp.MustCompile(`^` + ec2HomeMountBase + `/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// staleHomeMountScript lazily unmounts a dead XFS mount at $MP: one whose block device is
// gone from $SYSBLOCK, which is what a volume detached while mounted leaves. Only a device
// that is confirmed gone qualifies. A mount whose device is still there is never lazily
// unmounted, even when its root cannot be read: that is an attached home with an I/O fault,
// possibly still in use, and a lazy umount would hide it from the umount check below while
// the release detaches it. Anything that is not XFS is left alone, and no path other than
// $MP is ever touched. It loops because a dead mount can sit under another one.
//
// The dead mount itself keeps the old NVMe namespace (and so its dev_t) referenced, so a
// newly attached volume cannot take over the dev_t while the dead mount is there.
//
// The slot's own af-mount carries the same lines (40-ec2-pool.yaml; a test keeps the two in
// step), but slots launched from an older launch template keep the af-mount they booted
// with, so the CP sends these lines in front of every mount and after every umount too.
//
// POSIX sh and no "${": the CFN copy sits inside Fn::Sub.
const staleHomeMountScript = `for _ in 1 2 3 4; do
  top=$(awk -v m="$MP" '$5 == m { d = $3; f = ""; for (i = 7; i < NF; i++) if ($i == "-") { f = $(i + 1); break } } END { print d " " f }' "$MOUNTINFO")
  devt=$(echo "$top" | cut -d ' ' -f 1)
  fstype=$(echo "$top" | cut -d ' ' -f 2)
  [ "$fstype" = xfs ] || break
  [ -e "$SYSBLOCK/$devt" ] && break
  echo "dead mount at $MP (device $devt is gone); detaching it lazily"
  umount -l "$MP" || break
done
`

func homeMountVars(mp, mountinfo, sysblock string) string {
	return fmt.Sprintf("MP=%s MOUNTINFO=%s SYSBLOCK=%s\n", mp, mountinfo, sysblock)
}

// homeMountCommand is the SSM script for a mount: clear a dead mount at the mountpoint,
// then the slot's af-mount. af-mount's status is the script's.
func homeMountCommand(volumeID, mp, mountinfo, sysblock string) string {
	mount := fmt.Sprintf("af-mount %s %s --mkfs", volumeID, mp)
	if !safeHomeMountPoint.MatchString(mp) {
		return mount
	}
	return homeMountVars(mp, mountinfo, sysblock) + staleHomeMountScript + mount
}

// homeUmountCommand is the SSM script for an umount, and it succeeds only once nothing at
// all is mounted at the mountpoint — the confirmation releaseSlot's detach rests on. The
// af-umount of an older slot takes off one mount and calls a path whose stat fails "not
// mounted", so on its own it reports success over a second, stacked mount and over a dead
// one; the loop and the dead-mount pass are what close those. Each round clears dead
// mounts first, so a newer af-umount, which refuses to report success while anything is
// mounted, is never asked to unmount one.
func homeUmountCommand(mp, mountinfo, sysblock string) string {
	umount := fmt.Sprintf("af-umount %s", mp)
	if !safeHomeMountPoint.MatchString(mp) {
		return umount
	}
	var b strings.Builder
	b.WriteString(homeMountVars(mp, mountinfo, sysblock))
	clearedOrDone := staleHomeMountScript +
		`awk -v m="$MP" '$5 == m { found = 1 } END { exit !found }' "$MOUNTINFO" || exit 0` + "\n"
	b.WriteString("for _ in 1 2 3 4; do\n")
	b.WriteString(clearedOrDone)
	b.WriteString(umount + " || exit 1\n")
	b.WriteString("done\n")
	// The last af-umount may have taken off the last mount.
	b.WriteString(clearedOrDone)
	b.WriteString(`echo "$MP is still mounted" >&2` + "\n")
	b.WriteString("exit 1")
	return b.String()
}
