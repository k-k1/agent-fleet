package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
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
	defer func(b time.Duration) { homeAttachBudget = b }(homeAttachBudget)
	homeAttachBudget = 10 * time.Second
	ctx := context.Background()
	h := attachingHarness(t)
	h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateAttaching
	err := h.rt.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"})
	if err == nil || errors.Is(err, errHomeLeftSlot) || errors.Is(err, errHomeAttachUnknown) {
		t.Fatalf("mountHome = %v, want a plain error that quarantines", err)
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
	h.ec2.attach("vol-1", "i-hot", time.Now())
	holdAttaching(h, 5)
	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start returned nil although the mount failed")
	}
	if got := ec2TagValue(h.ec2.instances["i-hot"].Tags, EC2TagRole); got != ec2RoleQuarantined {
		t.Errorf("af-role = %q, want quarantined", got)
	}
}

// Throttled reads while the attachment is slow are retried, not mounted through.
func TestECSEC2MountRetriesUnreadableAttachment(t *testing.T) {
	h := attachingHarness(t)
	n := 0
	h.rt.ec2 = &describeFlaky{ec2API: h.ec2, fail: func() bool { n++; return n <= 5 }}
	if err := h.rt.mountHome(context.Background(), ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); err != nil {
		t.Fatalf("mountHome: %v", err)
	}
	if n < 6 {
		t.Errorf("only %d reads; the errors were not retried", n)
	}
}

// Unreadable for the whole budget is no evidence against the slot.
func TestECSEC2MountUnreadableForeverIsNotASlotFailure(t *testing.T) {
	defer func(b time.Duration) { homeAttachBudget = b }(homeAttachBudget)
	homeAttachBudget = 10 * time.Second
	h := attachingHarness(t)
	h.rt.ec2 = &describeFlaky{ec2API: h.ec2, fail: func() bool { return true }}
	err := h.rt.mountHome(context.Background(), ec2Placement{volumeID: "vol-1", instanceID: "i-hot"})
	if !errors.Is(err, errHomeAttachUnknown) {
		t.Fatalf("err = %v, want errHomeAttachUnknown", err)
	}
	if len(h.ssmc.commands) != 0 {
		t.Errorf("af-mount sent without a readable attachment")
	}
}

func TestECSEC2MountCtxErrorFromDescribeIsReturned(t *testing.T) {
	h := attachingHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.rt.ec2 = &describeFlaky{ec2API: h.ec2, fail: func() bool { cancel(); return true }}
	if err := h.rt.mountHome(ctx, ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(h.ssmc.commands) != 0 {
		t.Error("af-mount sent on a dead context")
	}
}

func TestECSEC2MountMissingVolumeOrDetachingIsLeftSlot(t *testing.T) {
	h := attachingHarness(t)
	h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateDetaching
	if err := h.rt.mountHome(context.Background(), ec2Placement{volumeID: "vol-1", instanceID: "i-hot"}); !errors.Is(err, errHomeLeftSlot) {
		t.Fatalf("detaching: err = %v", err)
	}
	if err := h.rt.mountHome(context.Background(), ec2Placement{volumeID: "vol-gone", instanceID: "i-hot"}); !errors.Is(err, errHomeLeftSlot) {
		t.Fatalf("missing volume: err = %v", err)
	}
}

// A launch whose context ended while waiting must not quarantine the slot.
func TestECSEC2LaunchDeadlineDuringAttachWaitDoesNotQuarantine(t *testing.T) {
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ec2.volumes["vol-1"].Attachments[0].State = ec2types.VolumeAttachmentStateAttaching
	ctx, cancel := context.WithCancel(context.Background())
	h.rt.sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	_ = h.rt.Start(ctx)
	h.runDeferred(ctx)
	if got := ec2TagValue(h.ec2.instances["i-hot"].Tags, EC2TagRole); got == ec2RoleQuarantined {
		t.Error("a Start that ran out of time quarantined a healthy slot")
	}
}

// af-mount missing the device is retried before it counts as a mount failure.
func TestECSEC2DeviceNotFoundIsRetriedThenFails(t *testing.T) {
	h := attachingHarness(t)
	h.ssmc.fail["af-mount"] = true
	h.ssmc.statusErr = "device for vol-1 not found"
	err := h.rt.mountHome(context.Background(), ec2Placement{volumeID: "vol-1", instanceID: "i-hot"})
	if err == nil {
		t.Fatal("nil error")
	}
	if got := len(h.ssmc.commands); got != homeDeviceRetries+1 {
		t.Errorf("mount sent %d times, want %d", got, homeDeviceRetries+1)
	}
}

type describeFlaky struct {
	ec2API
	fail func() bool
}

func (d *describeFlaky) DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, o ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if d.fail() {
		return nil, fmt.Errorf("RequestLimitExceeded")
	}
	return d.ec2API.DescribeVolumes(ctx, in, o...)
}
