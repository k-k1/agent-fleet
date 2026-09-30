// runtime_ecs_ec2_home_wipe.go — a member's Recreate and Clean home on the slot pool
// (ADR 0045 decision 32).
//
// Emptying the home needs its filesystem mounted on a running slot, and a stopped
// workspace's home is usually on a slot that has gone to sleep, detached, or already a
// hibernation snapshot. Waking or attaching does not fit behind the ingress idle timeout,
// so the request only marks the home and the Start that follows it does the removal in
// its background half, between the mount and the task.
package runtime

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// WipeHome marks the home for the next Start and returns; it removes nothing itself.
//
// The volume is marked first and the hibernation snapshots after it. hibernate re-reads
// the volume's mark before it deletes the volume, and only ever deletes it once its
// capture has completed — so a mark that lands after that read is also in time to find the
// snapshot in the listing below. Either way the mark reaches whatever the next Start
// builds the home from.
//
// A member with neither has no home yet; the next Start builds a fresh one, which is what
// either wipe would have left.
func (e *ecsEC2Runtime) WipeHome(ctx context.Context, what HomeWipe) error {
	if what != HomeWipeRepos && what != HomeWipeClean {
		return fmt.Errorf("unknown home wipe %q", what)
	}
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return fmt.Errorf("describe home volume: %w", err)
	}
	if vol != nil {
		if err := e.markHomeWipe(ctx, aws.ToString(vol.VolumeId), vol.Tags, what); err != nil {
			return err
		}
	}
	snaps, err := e.homeSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list hibernation snapshots: %w", err)
	}
	for _, s := range snaps {
		if err := e.markHomeWipe(ctx, aws.ToString(s.SnapshotId), s.Tags, what); err != nil {
			return err
		}
	}
	return nil
}

// markHomeWipe writes what onto one resource of the home, unless the mark already there
// removes at least as much: a Recreate after a pending Clean home must not narrow it. A
// resource that vanished in the meantime is not an error — the volume goes when its
// hibernation completes, and the caller marks the snapshot that replaced it.
func (e *ecsEC2Runtime) markHomeWipe(ctx context.Context, resourceID string, tags []ec2types.Tag, what HomeWipe) error {
	cur := HomeWipe(ec2TagValue(tags, ec2TagHomeWipe))
	if cur == what || cur == HomeWipeClean {
		return nil
	}
	if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{resourceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagHomeWipe), Value: aws.String(string(what))}},
	}); err != nil && !isAWSNotFound(err) {
		return fmt.Errorf("mark %s for %s: %w", resourceID, what, err)
	}
	return nil
}

// homeWipeOf is the pending wipe a volume or snapshot carries, "" for none.
func homeWipeOf(vol *ec2types.Volume) HomeWipe {
	if vol == nil {
		return ""
	}
	return HomeWipe(ec2TagValue(vol.Tags, ec2TagHomeWipe))
}

// homeWipeTags is the mark to copy onto whatever is built from a resource carrying tags:
// the snapshot a hibernation takes, the volume a restore creates.
func homeWipeTags(tags []ec2types.Tag) []ec2types.Tag {
	if v := ec2TagValue(tags, ec2TagHomeWipe); v != "" {
		return []ec2types.Tag{{Key: aws.String(ec2TagHomeWipe), Value: aws.String(v)}}
	}
	return nil
}

// wipeMountedHome performs p.wipe on the mounted home and then drops the mark. launch calls
// it after the mount and before the service is scaled up.
//
// The mark goes only after the removal succeeded, and the task starts only after the mark
// is gone. A failure anywhere in between therefore leaves the mark for the next Start to
// repeat — which is safe, because no task has run since and nothing new is in the home.
// The reverse order would let a Start after a lost DeleteTags remove work the member did
// since.
func (e *ecsEC2Runtime) wipeMountedHome(ctx context.Context, p ec2Placement) error {
	e.setPhase("home: clearing")
	cmd, err := homeWipeCommand(e.homeMountPoint(), p.wipe)
	if err != nil {
		return err
	}
	// Stop only set the desired count to 0; the old task may still be draining, and it
	// holds the home as its bind mount.
	if err := e.waitTasksGone(ctx); err != nil {
		return fmt.Errorf("wait for the old task: %w", err)
	}
	if err := e.runOnSlot(ctx, p.instanceID, cmd); err != nil {
		return err
	}
	// With the value: a DeleteTags that names one deletes only a tag that still has it, so
	// a stronger mark written in the meantime survives for the next Start.
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{p.volumeID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagHomeWipe), Value: aws.String(string(p.wipe))}},
	}); err != nil {
		return fmt.Errorf("drop the %s mark on %s: %w", p.wipe, p.volumeID, err)
	}
	log.Printf("ecs-ec2: cleared the home of %s (%s)", e.base.name, p.wipe)
	return nil
}

// homeWipeCommand is the shell run on the slot. The home is the task's bind-mount source,
// <mount point>/dev.
//
// `mountpoint -q` comes first because the command runs as root on the slot's own disk: if
// the volume were not mounted there, the removal would succeed on an empty directory of
// the root volume and the member would be told the home was cleared while every byte of it
// stayed. A home directory that does not exist yet is a home no task has booted, so there
// is nothing to remove.
//
// Clean home keeps homeKeep by name at the top level, whatever each entry is. Normally
// they are links into EFS, but a keep file a tool replaced since the last boot is a plain
// file here until the entrypoint moves it back, and it has to survive too.
func homeWipeCommand(mountPoint string, what HomeWipe) (string, error) {
	home := mountPoint + "/dev"
	var remove string
	switch what {
	case HomeWipeRepos:
		remove = "rm -rf --one-file-system -- " + shellQuote(home+"/repos")
	case HomeWipeClean:
		keep := make([]string, 0, len(homeKeep))
		for name := range homeKeep {
			keep = append(keep, name)
		}
		slices.Sort(keep)
		var not []string
		for _, name := range keep {
			not = append(not, "! -name "+shellQuote(name))
		}
		remove = fmt.Sprintf("find %s -mindepth 1 -maxdepth 1 %s -exec rm -rf --one-file-system -- {} +",
			shellQuote(home), strings.Join(not, " "))
	default:
		return "", fmt.Errorf("unknown home wipe %q", what)
	}
	mp, h := shellQuote(mountPoint), shellQuote(home)
	return fmt.Sprintf("mountpoint -q %[1]s || { echo %[1]s is not mounted >&2; exit 1; }; "+
		"[ -e %[2]s ] || [ -L %[2]s ] || exit 0; "+
		"[ -d %[2]s ] && [ ! -L %[2]s ] || { echo %[2]s is not a directory >&2; exit 1; }; "+
		"%[3]s", mp, h, remove), nil
}

// shellQuote quotes s as one word for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
