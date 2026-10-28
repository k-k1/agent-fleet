package runtime

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// --- mount / umount (SSM) ---

func (e *ecsEC2Runtime) homeMountPoint() string {
	return ec2HomeMountBase + "/" + e.base.membershipID
}

// mountHome runs `af-mount <volume-id> <mountpoint> --mkfs` on the slot. The helper is
// placed by the launch template's user-data, resolves the NVMe device from the volume
// id (the device name we asked for is not what the kernel shows on Nitro), and only
// formats when blkid finds no filesystem — which is why --mkfs can be passed
// unconditionally and a retried mount never eats a home.
//
// It holds homeLockMount, and under it first confirms the home is still on the slot: a
// release that ran while this launch was on its way has detached it, and mounting then
// would either wait out af-mount's device search and fail, or — had the release not yet
// detached — mount a filesystem the release is about to pull (see homeMountLocks).
func (e *ecsEC2Runtime) mountHome(ctx context.Context, p ec2Placement) error {
	lctx, unlock, err := e.lockHome(ctx, homeLockMount)
	if err != nil {
		return err
	}
	defer unlock()
	return homeGuardOf(lctx).outcome(e.mountHomeLocked(lctx, p))
}

func (e *ecsEC2Runtime) mountHomeLocked(ctx context.Context, p ec2Placement) error {
	if !e.homeStillOn(ctx, p) {
		return fmt.Errorf("%s on %s: %w", p.volumeID, p.instanceID, errHomeLeftSlot)
	}
	return e.runOnSlot(ctx, p.instanceID, homeMountCommand(p.volumeID, e.homeMountPoint(), slotMountInfo, slotSysBlock))
}

// umountHome succeeds only when nothing is left mounted at the home's mountpoint
// (homeUmountCommand). Callers that detach afterwards hold homeLockMount across both.
func (e *ecsEC2Runtime) umountHome(ctx context.Context, instanceID string) error {
	return e.runOnSlot(ctx, instanceID, homeUmountCommand(e.homeMountPoint(), slotMountInfo, slotSysBlock))
}

// runOnSlot sends one shell command through SSM and waits for it. SendCommand is
// retried for a while because a freshly booted slot registers with SSM a little after
// it registers with ECS, and an InvalidInstanceId there is a timing artifact rather
// than a failure.
//
// Under a home lock (homeGuardOf) each send is preceded by the lock's check, and the
// command is recorded on the lock until it is seen ending, so the lock is not let go while
// it may still run.
func (e *ecsEC2Runtime) runOnSlot(ctx context.Context, instanceID, command string) error {
	g := homeGuardOf(ctx)
	var cmdID string
	for attempt := 1; ; attempt++ {
		if err := g.check(); err != nil {
			return fmt.Errorf("ssm send %q to %s: %w", command, instanceID, err)
		}
		out, err := e.ssmc.SendCommand(ctx, &ssm.SendCommandInput{
			DocumentName: aws.String("AWS-RunShellScript"),
			InstanceIds:  []string{instanceID},
			Parameters:   map[string][]string{"commands": {command}},
			Comment:      aws.String(slotVolumeCommandComment),
		})
		if err == nil && out.Command != nil {
			cmdID = aws.ToString(out.Command.CommandId)
			g.sent(cmdID, instanceID)
			break
		}
		if err == nil || !ssmSendRefused(err) {
			g.sendUnknown()
		}
		// SAY SOMETHING. A slot whose SSM agent never came back swallows every mount and
		// unmount silently, and the workspace just sits at `starting` with no clue why —
		// which is exactly how a live run burned ten minutes before anyone could tell
		// that SSM, not ECS, was the thing that was stuck.
		if attempt%5 == 1 {
			log.Printf("ecs-ec2: slot %s is not answering SSM yet (attempt %d, %q): %v",
				instanceID, attempt, command, err)
		}
		if err := e.sleep(ctx, 3*time.Second); err != nil {
			return fmt.Errorf("ssm send %q to %s: %w", command, instanceID, err)
		}
	}
	// Poll on a ramp rather than a flat 2s, and ask EARLY. Measured on the production
	// deployment's pool (docs/log/64 §64.38, n=37 mounts over 5 days): `af-mount` itself runs
	// in 0.6s median / 3.0s max, and SSM queues it in another 0.2–0.7s — so the command
	// is almost always finished before the CP has even asked once. A flat 2s pre-sleep
	// therefore spent ~1.5s per call doing nothing, twice on the swap path (umount on the
	// old slot, mount on the new one). The ramp keeps the tail cheap: the poll widens to
	// the same 2s within a few rounds, so a mount that genuinely takes a minute (mkfs on
	// a fresh 50 GiB home) costs the same number of API calls it always did.
	//
	// ⚠️ This is a small win on purpose. The measurement it comes from is that the mount
	// was NEVER the 10–30s the earlier estimate charged it (§64.17.5); it is ~2–4s out of
	// a 110–147s Start. Do not expect a Start to get noticeably faster from this.
	delay := 300 * time.Millisecond
	for {
		if err := e.sleep(ctx, delay); err != nil {
			return err
		}
		if delay < 2*time.Second {
			if delay *= 2; delay > 2*time.Second {
				delay = 2 * time.Second
			}
		}
		inv, err := e.ssmc.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(cmdID),
			InstanceId: aws.String(instanceID),
		})
		if err != nil {
			continue // InvocationDoesNotExist right after SendCommand is normal
		}
		if ssmCommandEnded(inv.Status) {
			g.ended(cmdID)
		}
		switch inv.Status {
		case "Success":
			return nil
		case "Failed", "Cancelled", "TimedOut":
			return fmt.Errorf("%q on %s: %s: %s", command, instanceID, inv.Status,
				strings.TrimSpace(aws.ToString(inv.StandardErrorContent)))
		}
	}
}
