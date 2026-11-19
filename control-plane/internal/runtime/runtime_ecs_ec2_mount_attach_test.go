package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// holdAttaching leaves vol-1's attachment `attaching` until DescribeVolumes has been
// called `reads` more times, the way a slow EBS attach surfaces.
func holdAttaching(h *ec2Harness, reads int) {
	h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateAttaching
	n := 0
	h.ec2.afterDescribeVolumes = func() {
		h.ec2.mu.Lock()
		defer h.ec2.mu.Unlock()
		if n++; n >= reads {
			h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateAttached
		}
	}
}

func attachingHarness(t *testing.T) *ec2Harness {
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	return h
}

// A slow attach must not reach af-mount: it waits 30s for the device, then the slot is
// quarantined although nothing is wrong with it.
func TestECSEC2MountWaitsForASlowAttachment(t *testing.T) {
	ctx := context.Background()
	h := attachingHarness(t)
	holdAttaching(h, 20) // far more polls than a 30s af-mount window would cover
	h.ssmc.onSend = func(cmd string) {
		if strings.Contains(cmd, "af-mount") {
			if st := h.ec2.volumes["vol-1"].Attachments[0].State; st != ec2types.VolumeAttachmentStateAttached {
				t.Errorf("af-mount sent while the attachment is %s", st)
			}
		}
	}
	if err := h.rt.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); err != nil {
		t.Fatalf("mountHome: %v", err)
	}
	if len(h.ssmc.commands) == 0 {
		t.Fatal("af-mount was never sent")
	}
}

// An attachment that never leaves `attaching` is a broken slot: bounded wait, then an
// error the caller quarantines on.
func TestECSEC2MountGivesUpOnAnAttachmentStuckAttaching(t *testing.T) {
	ctx := context.Background()
	h := attachingHarness(t)
	h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateAttaching
	if err := h.rt.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); err == nil {
		t.Fatal("mountHome returned nil for an attachment stuck attaching")
	}
	if len(h.ssmc.commands) != 0 {
		t.Errorf("af-mount sent: %v", h.ssmc.commands)
	}
}

// The wait ends with the Start's context.
func TestECSEC2MountAttachWaitHonoursContext(t *testing.T) {
	h := attachingHarness(t)
	h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateAttaching
	ctx, cancel := context.WithCancel(context.Background())
	h.rt.sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	if err := h.rt.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); err == nil {
		t.Fatal("mountHome ignored the cancelled context")
	}
}

// A slot whose mount really fails is still quarantined, after a normal attach.
func TestECSEC2SlowAttachThenRealMountFailureStillQuarantines(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.ssmc.fail["af-mount"] = true
	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start returned nil although the mount failed")
	}
	if got := ec2TagValue(h.ec2.instances["i-hot"].Tags, EC2TagRole); got != ec2RoleQuarantined {
		t.Errorf("af-role = %q, want quarantined", got)
	}
}
