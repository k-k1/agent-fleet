package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// --- fakes for the EC2-pool ports. They model just enough of EC2 to exercise the
// placement rules: tag/state filters, one attachment per device, and a call log so the
// ORDER of destructive operations (umount before detach) is assertable. ---

type fakeEC2 struct {
	mu        sync.Mutex
	volumes   map[string]*ec2types.Volume
	instances map[string]*ec2types.Instance
	subnetAZ  map[string]string
	// attachErr forces AttachVolume to fail for a given instance id, standing in for
	// "another workspace took this slot a moment ago".
	attachErr map[string]bool
	// attachFailures, when > 0, makes attachErr transient: it counts down on each
	// refusal, which is how a slot behaves while it is still giving back its device.
	attachFailures int
	calls          []string
	nextVol        int
	nextInst       int
}

func newFakeEC2() *fakeEC2 {
	return &fakeEC2{
		volumes:   map[string]*ec2types.Volume{},
		instances: map[string]*ec2types.Instance{},
		subnetAZ:  map[string]string{"sub-1a": "ap-northeast-1a", "sub-1c": "ap-northeast-1c"},
		attachErr: map[string]bool{},
	}
}

func (f *fakeEC2) log(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeEC2) addSlot(id, az, itype string, running bool, deviceTaken bool) *ec2types.Instance {
	state := ec2types.InstanceStateNameStopped
	if running {
		state = ec2types.InstanceStateNameRunning
	}
	inst := &ec2types.Instance{
		InstanceId:   aws.String(id),
		InstanceType: ec2types.InstanceType(itype),
		State:        &ec2types.InstanceState{Name: state},
		Placement:    &ec2types.Placement{AvailabilityZone: aws.String(az)},
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagPool), Value: aws.String("clu")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleSlot)},
		},
	}
	if deviceTaken {
		inst.BlockDeviceMappings = []ec2types.InstanceBlockDeviceMapping{
			{DeviceName: aws.String(ec2HomeDevice)},
		}
	}
	f.instances[id] = inst
	return inst
}

func (f *fakeEC2) addHomeVolume(id, membership, workspace, az string) *ec2types.Volume {
	v := &ec2types.Volume{
		VolumeId:         aws.String(id),
		AvailabilityZone: aws.String(az),
		State:            ec2types.VolumeStateAvailable,
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership), Value: aws.String(membership)},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
			{Key: aws.String(ec2TagWorkspace), Value: aws.String(workspace)},
			{Key: aws.String(ec2TagPool), Value: aws.String("clu")},
		},
	}
	f.volumes[id] = v
	return v
}

func (f *fakeEC2) attach(volID, instID string, at time.Time) {
	v := f.volumes[volID]
	v.State = ec2types.VolumeStateInUse
	v.Attachments = []ec2types.VolumeAttachment{{
		InstanceId: aws.String(instID), Device: aws.String(ec2HomeDevice),
		State: ec2types.VolumeAttachmentStateAttached, AttachTime: aws.Time(at),
	}}
	if inst := f.instances[instID]; inst != nil {
		inst.BlockDeviceMappings = []ec2types.InstanceBlockDeviceMapping{{DeviceName: aws.String(ec2HomeDevice)}}
	}
}

func (f *fakeEC2) setTag(volID, key, value string) {
	v := f.volumes[volID]
	for i := range v.Tags {
		if aws.ToString(v.Tags[i].Key) == key {
			v.Tags[i].Value = aws.String(value)
			return
		}
	}
	v.Tags = append(v.Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
}

func filterMatch(filters []ec2types.Filter, get func(name string) []string) bool {
	for _, fl := range filters {
		name := aws.ToString(fl.Name)
		have := get(name)
		hit := false
		for _, want := range fl.Values {
			for _, h := range have {
				if h == want {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func (f *fakeEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &ec2.DescribeVolumesOutput{}
	for id, v := range f.volumes {
		if len(in.VolumeIds) > 0 {
			found := false
			for _, want := range in.VolumeIds {
				if want == id {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if !filterMatch(in.Filters, func(name string) []string {
			if strings.HasPrefix(name, "tag:") {
				return []string{ec2TagValue(v.Tags, strings.TrimPrefix(name, "tag:"))}
			}
			return nil
		}) {
			continue
		}
		out.Volumes = append(out.Volumes, *v)
	}
	return out, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &ec2.DescribeInstancesOutput{}
	var picked []ec2types.Instance
	for id, inst := range f.instances {
		if len(in.InstanceIds) > 0 {
			found := false
			for _, want := range in.InstanceIds {
				if want == id {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if !filterMatch(in.Filters, func(name string) []string {
			switch {
			case strings.HasPrefix(name, "tag:"):
				return []string{ec2TagValue(inst.Tags, strings.TrimPrefix(name, "tag:"))}
			case name == "instance-state-name":
				return []string{string(inst.State.Name)}
			case name == "instance-type":
				return []string{string(inst.InstanceType)}
			case name == "availability-zone":
				return []string{aws.ToString(inst.Placement.AvailabilityZone)}
			}
			return nil
		}) {
			continue
		}
		picked = append(picked, *inst)
	}
	if len(picked) > 0 {
		out.Reservations = []ec2types.Reservation{{Instances: picked}}
	}
	return out, nil
}

func (f *fakeEC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	out := &ec2.DescribeSubnetsOutput{}
	for _, id := range in.SubnetIds {
		if az, ok := f.subnetAZ[id]; ok {
			out.Subnets = append(out.Subnets, ec2types.Subnet{SubnetId: aws.String(id), AvailabilityZone: aws.String(az)})
		}
	}
	return out, nil
}

func (f *fakeEC2) CreateVolume(_ context.Context, in *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextVol++
	id := fmt.Sprintf("vol-new%d", f.nextVol)
	var tags []ec2types.Tag
	for _, ts := range in.TagSpecifications {
		tags = append(tags, ts.Tags...)
	}
	f.volumes[id] = &ec2types.Volume{
		VolumeId: aws.String(id), AvailabilityZone: in.AvailabilityZone,
		State: ec2types.VolumeStateAvailable, Size: in.Size, Tags: tags,
	}
	f.log("CreateVolume %s az=%s tags=%d", id, aws.ToString(in.AvailabilityZone), len(tags))
	return &ec2.CreateVolumeOutput{
		VolumeId: aws.String(id), AvailabilityZone: in.AvailabilityZone, State: ec2types.VolumeStateAvailable,
	}, nil
}

func (f *fakeEC2) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("DeleteVolume %s", aws.ToString(in.VolumeId))
	delete(f.volumes, aws.ToString(in.VolumeId))
	return &ec2.DeleteVolumeOutput{}, nil
}

func (f *fakeEC2) AttachVolume(_ context.Context, in *ec2.AttachVolumeInput, _ ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := aws.ToString(in.InstanceId)
	f.log("AttachVolume %s -> %s (%s)", aws.ToString(in.VolumeId), inst, aws.ToString(in.Device))
	if f.attachErr[inst] {
		if f.attachFailures > 0 {
			f.attachFailures--
			if f.attachFailures == 0 {
				f.attachErr[inst] = false
			}
		}
		return nil, fmt.Errorf("InvalidParameterValue: Attachment point %s is already in use", aws.ToString(in.Device))
	}
	// The real API refuses a second volume on a device that is already taken; that
	// refusal IS the slot lock, so the fake enforces it too.
	if i := f.instances[inst]; i != nil && deviceInUse(*i, aws.ToString(in.Device)) {
		return nil, fmt.Errorf("InvalidParameterValue: %s is already in use", aws.ToString(in.Device))
	}
	f.attach(aws.ToString(in.VolumeId), inst, time.Now())
	return &ec2.AttachVolumeOutput{}, nil
}

func (f *fakeEC2) DetachVolume(_ context.Context, in *ec2.DetachVolumeInput, _ ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("DetachVolume %s", aws.ToString(in.VolumeId))
	if v := f.volumes[aws.ToString(in.VolumeId)]; v != nil {
		v.Attachments = nil
		v.State = ec2types.VolumeStateAvailable
	}
	if i := f.instances[aws.ToString(in.InstanceId)]; i != nil {
		i.BlockDeviceMappings = nil
	}
	return &ec2.DetachVolumeOutput{}, nil
}

func (f *fakeEC2) CreateTags(_ context.Context, in *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range in.Resources {
		for _, t := range in.Tags {
			if v := f.volumes[r]; v != nil {
				v.Tags = append(v.Tags, t)
			}
		}
		f.log("CreateTags %s", r)
	}
	return &ec2.CreateTagsOutput{}, nil
}

func (f *fakeEC2) DeleteTags(_ context.Context, in *ec2.DeleteTagsInput, _ ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range in.Resources {
		v := f.volumes[r]
		if v == nil {
			continue
		}
		var kept []ec2types.Tag
		for _, t := range v.Tags {
			drop := false
			for _, d := range in.Tags {
				if aws.ToString(d.Key) == aws.ToString(t.Key) {
					drop = true
				}
			}
			if !drop {
				kept = append(kept, t)
			}
		}
		v.Tags = kept
		f.log("DeleteTags %s", r)
	}
	return &ec2.DeleteTagsOutput{}, nil
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextInst++
	id := fmt.Sprintf("i-new%d", f.nextInst)
	f.instances[id] = &ec2types.Instance{
		InstanceId:   aws.String(id),
		InstanceType: in.InstanceType,
		State:        &ec2types.InstanceState{Name: ec2types.InstanceStateNamePending},
		Placement:    &ec2types.Placement{AvailabilityZone: aws.String(f.subnetAZ[aws.ToString(in.SubnetId)])},
	}
	f.log("RunInstances %s type=%s subnet=%s", id, in.InstanceType, aws.ToString(in.SubnetId))
	return &ec2.RunInstancesOutput{Instances: []ec2types.Instance{{InstanceId: aws.String(id)}}}, nil
}

func (f *fakeEC2) StartInstances(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range in.InstanceIds {
		f.log("StartInstances %s", id)
		if i := f.instances[id]; i != nil {
			i.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
		}
	}
	return &ec2.StartInstancesOutput{}, nil
}

func (f *fakeEC2) StopInstances(_ context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range in.InstanceIds {
		f.log("StopInstances %s", id)
		if i := f.instances[id]; i != nil {
			i.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}
		}
	}
	return &ec2.StopInstancesOutput{}, nil
}

type fakeSSMCmd struct {
	mu       sync.Mutex
	commands []string
	fail     map[string]bool // substring of the command -> fail it
	// sink shares the EC2 fake's call log so a test can assert the ORDER of an SSM
	// command against an EC2 call — "umount before detach" spans both.
	sink *fakeEC2
}

func (f *fakeSSMCmd) SendCommand(_ context.Context, in *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := in.Parameters["commands"][0]
	f.commands = append(f.commands, cmd)
	if f.sink != nil {
		f.sink.log("SSM %s", cmd)
	}
	// The command id carries the command text so GetCommandInvocation can decide
	// whether this particular step is the one the test wants to fail.
	return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(cmd)}}, nil
}

func (f *fakeSSMCmd) GetCommandInvocation(_ context.Context, in *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := aws.ToString(in.CommandId)
	for sub := range f.fail {
		if strings.Contains(cmd, sub) {
			return &ssm.GetCommandInvocationOutput{Status: "Failed", StandardErrorContent: aws.String("boom")}, nil
		}
	}
	return &ssm.GetCommandInvocationOutput{Status: "Success"}, nil
}

type fakeContainerInstances struct {
	// registered maps EC2 instance id -> agentConnected
	registered   map[string]bool
	deregistered []string
}

func (f *fakeContainerInstances) ListContainerInstances(_ context.Context, _ *ecs.ListContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.ListContainerInstancesOutput, error) {
	var arns []string
	for id := range f.registered {
		arns = append(arns, "arn:ci/"+id)
	}
	return &ecs.ListContainerInstancesOutput{ContainerInstanceArns: arns}, nil
}

func (f *fakeContainerInstances) DescribeContainerInstances(_ context.Context, in *ecs.DescribeContainerInstancesInput, _ ...func(*ecs.Options)) (*ecs.DescribeContainerInstancesOutput, error) {
	out := &ecs.DescribeContainerInstancesOutput{}
	for _, arn := range in.ContainerInstances {
		id := strings.TrimPrefix(arn, "arn:ci/")
		out.ContainerInstances = append(out.ContainerInstances, ecstypes.ContainerInstance{
			ContainerInstanceArn: aws.String(arn),
			Ec2InstanceId:        aws.String(id),
			Status:               aws.String("ACTIVE"),
			AgentConnected:       f.registered[id],
		})
	}
	return out, nil
}

func (f *fakeContainerInstances) DeregisterContainerInstance(_ context.Context, in *ecs.DeregisterContainerInstanceInput, _ ...func(*ecs.Options)) (*ecs.DeregisterContainerInstanceOutput, error) {
	f.deregistered = append(f.deregistered, aws.ToString(in.ContainerInstance))
	return &ecs.DeregisterContainerInstanceOutput{}, nil
}

// deviceInUse mirrors what EC2 itself enforces: one volume per device name. The adapter
// no longer READS this (instance block-device mappings lag behind a detach on real AWS —
// see freeSlots), but the fake still needs it to refuse a second attach the way the API
// does.
func deviceInUse(inst ec2types.Instance, device string) bool {
	for _, m := range inst.BlockDeviceMappings {
		if aws.ToString(m.DeviceName) == device {
			return true
		}
	}
	return false
}

// --- harness ---

type ec2Harness struct {
	rt   *ecsEC2Runtime
	ec2  *fakeEC2
	ecs  *fakeECS
	efs  *fakeEFS
	ssm  *fakeSSM
	ssmc *fakeSSMCmd
	ci   *fakeContainerInstances
	// deferred collects the background halves instead of racing them; tests run them
	// explicitly when they want to observe convergence.
	deferred []func(context.Context)
}

func newEC2Harness(t *testing.T) *ec2Harness {
	t.Helper()
	h := &ec2Harness{
		ec2:  newFakeEC2(),
		ecs:  &fakeECS{},
		efs:  &fakeEFS{},
		ssm:  &fakeSSM{},
		ssmc: &fakeSSMCmd{fail: map[string]bool{}},
		ci:   &fakeContainerInstances{registered: map[string]bool{}},
	}
	h.ssmc.sink = h.ec2
	base := newTestECS(h.ecs, h.efs, h.ssm)
	base.cfg.subnets = []string{"sub-1a", "sub-1c"}
	h.rt = &ecsEC2Runtime{
		base: base,
		ec2:  h.ec2,
		ssmc: h.ssmc,
		ci:   h.ci,
		pool: ec2PoolConfig{
			launchTemplate: "lt-1",
			pool:           "clu",
			slotSizes:      parseSlotSizes("m7i.large:8192,m7i.xlarge:16384"),
			maxSlots:       4,
			homeGiB:        50,
			tmpfsMiB:       2048,
			tmpfsOpts:      []string{"nosuid", "nodev"},
			claimTTL:       15 * time.Minute,
			releaseGrace:   10 * time.Minute,
			idleStopAfter:  15 * time.Minute,
			waitBudget:     time.Minute,
		},
		instanceType: "m7i.large",
		homeGiB:      50,
		azOfSubnet:   func(context.Context) (map[string]string, error) { return h.ec2.subnetAZ, nil },
		bg:           func(_ context.Context, fn func(context.Context)) { h.deferred = append(h.deferred, fn) },
		now:          time.Now,
		sleep:        func(context.Context, time.Duration) error { return nil },
	}
	return h
}

// factory builds the sweeper's view of this harness. The runtimes it creates inherit the
// harness clients, so a sweep acts on the same fake world the test set up.
func (h *ec2Harness) factory() *ecsEC2Factory {
	return &ecsEC2Factory{
		base: &ecsFactory{cfg: h.rt.base.cfg, ecs: h.ecs, efs: h.efs, ssm: h.ssm},
		ec2:  h.ec2, ssmc: h.ssmc, ci: h.ci, pool: h.rt.pool,
	}
}

func (h *ec2Harness) runDeferred(ctx context.Context) {
	pending := h.deferred
	h.deferred = nil
	for _, fn := range pending {
		fn(ctx)
	}
}

// State() is the whole reason the adapter looks at three resources instead of one: the
// service alone cannot tell a booting slot from a stopped workspace, and every wrong
// answer here is a user-visible bug (a second Start, or a workspace that looks dead).
func TestECSEC2StateMapping(t *testing.T) {
	ctx := context.Background()

	t.Run("no volume is none", func(t *testing.T) {
		h := newEC2Harness(t)
		if got := h.rt.State(ctx); got != "none" {
			t.Fatalf("State = %q, want none", got)
		}
	})

	t.Run("detached volume is stopped", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		if got := h.rt.State(ctx); got != "stopped" {
			t.Fatalf("State = %q, want stopped", got)
		}
	})

	t.Run("live claim is starting", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.setTag("vol-1", ec2TagClaim, "i-pending")
		h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
		if got := h.rt.State(ctx); got != "starting" {
			t.Fatalf("State = %q, want starting", got)
		}
	})

	t.Run("expired claim falls back to stopped", func(t *testing.T) {
		// Without the TTL a failed launch would pin the workspace at `starting`, and
		// Start returns early on `starting` — the user could never recover.
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.setTag("vol-1", ec2TagClaim, "i-pending")
		h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
		if got := h.rt.State(ctx); got != "stopped" {
			t.Fatalf("State = %q, want stopped", got)
		}
	})

	t.Run("attached mid-launch is starting", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		h.ec2.attach("vol-1", "i-hot", time.Now())
		h.ec2.setTag("vol-1", ec2TagClaim, "i-hot")
		h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
		if got := h.rt.State(ctx); got != "starting" {
			t.Fatalf("State = %q, want starting", got)
		}
	})

	t.Run("attached with no service and no claim is stopped", func(t *testing.T) {
		// An abandoned attachment: a launch died between the attach and the
		// CreateService. Calling this `starting` (an earlier revision did, and real AWS
		// caught it) makes the workspace UNSTARTABLE FOREVER — Start returns early on
		// `starting`, so nobody can drive it forward and nobody can retry.
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		h.ec2.attach("vol-1", "i-hot", time.Now())
		h.ci.registered["i-hot"] = true
		if got := h.rt.State(ctx); got != "stopped" {
			t.Fatalf("State = %q, want stopped", got)
		}
		// ...and a Start must actually recover it, reusing the attachment.
		if err := h.rt.Start(ctx); err != nil {
			t.Fatalf("Start on an abandoned attachment: %v", err)
		}
		if len(h.ecs.createCalls) != 1 {
			t.Fatalf("Start did not create the missing service (createCalls=%d)", len(h.ecs.createCalls))
		}
	})

	t.Run("attached with a running task is running", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		h.ec2.attach("vol-1", "i-hot", time.Now())
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
		if got := h.rt.State(ctx); got != "running" {
			t.Fatalf("State = %q, want running", got)
		}
	})

	t.Run("attached at desired 0 is stopped", func(t *testing.T) {
		// Teardown still draining. Reporting `starting` here would block the next Start.
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		h.ec2.attach("vol-1", "i-hot", time.Now())
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
		if got := h.rt.State(ctx); got != "stopped" {
			t.Fatalf("State = %q, want stopped", got)
		}
	})
}

// The hot path is the product claim (22–27s) and the only one allowed to run inside the
// HTTP request, so it must complete end-to-end without deferring anything.
func TestECSEC2StartOnHotSlotIsSynchronous(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.deferred) != 0 {
		t.Fatalf("hot Start deferred %d background steps; it must finish inline", len(h.deferred))
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-hot" {
		t.Fatalf("volume attached to %q, want i-hot", inst)
	}
	if len(h.ssmc.commands) == 0 || !strings.HasPrefix(h.ssmc.commands[0], "af-mount vol-1 /af-home/M-1") {
		t.Fatalf("mount command = %v", h.ssmc.commands)
	}
	if len(h.ecs.createCalls) != 1 {
		t.Fatalf("createCalls = %d, want 1", len(h.ecs.createCalls))
	}
	create := h.ecs.createCalls[0]
	if create.LaunchType != ecstypes.LaunchTypeEc2 {
		t.Errorf("launch type = %s, want EC2", create.LaunchType)
	}
	// The ENI must land in the slot's own AZ, or ECS cannot place the task at all.
	if subs := create.NetworkConfiguration.AwsvpcConfiguration.Subnets; len(subs) != 1 || subs[0] != "sub-1a" {
		t.Errorf("subnets = %v, want [sub-1a]", subs)
	}
	if create.NetworkConfiguration.AwsvpcConfiguration.AssignPublicIp != ecstypes.AssignPublicIpDisabled {
		t.Error("a slot task must never ask for a public IP (ADR 0045 決定 3-3)")
	}
}

// Everything the task definition has to say for the pool to be safe and exclusive.
func TestECSEC2TaskDefinitionShape(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls = %d", len(h.ecs.regCalls))
	}
	td := h.ecs.regCalls[0]

	if len(td.PlacementConstraints) != 1 || aws.ToString(td.PlacementConstraints[0].Expression) != "ec2InstanceId == i-hot" {
		t.Errorf("placement constraint = %+v, want ec2InstanceId == i-hot", td.PlacementConstraints)
	}
	// EC2 reservations are against the instance: reserving here would fence the user
	// out of the box they have exclusively (ADR 0045 決定 8).
	if td.Cpu != nil || td.Memory != nil {
		t.Errorf("task cpu/memory must stay unset on EC2, got %v/%v", aws.ToString(td.Cpu), aws.ToString(td.Memory))
	}
	c := td.ContainerDefinitions[0]
	tmpfs := c.LinuxParameters.Tmpfs
	if len(tmpfs) != 1 || aws.ToString(tmpfs[0].ContainerPath) != "/tmp" || tmpfs[0].Size != 2048 {
		t.Errorf("/tmp must be a size-capped tmpfs, got %+v", tmpfs)
	}
	var home *ecstypes.Volume
	for i := range td.Volumes {
		if aws.ToString(td.Volumes[i].Name) == "home" {
			home = &td.Volumes[i]
		}
	}
	if home == nil || home.Host == nil || aws.ToString(home.Host.SourcePath) != "/af-home/M-1/dev" {
		t.Errorf("home volume = %+v, want a host bind of the mounted EBS", home)
	}
	env := map[string]string{}
	for _, kv := range c.Environment {
		env[aws.ToString(kv.Name)] = aws.ToString(kv.Value)
	}
	if _, ok := env["AF_WS_SCRATCH"]; ok {
		t.Error("AF_WS_SCRATCH must not be injected on EC2 (ADR 0045 決定 10-3): home is already local EBS")
	}
	if env["AF_WS_KEEP"] != ec2KeepPath {
		t.Errorf("AF_WS_KEEP = %q, want %q", env["AF_WS_KEEP"], ec2KeepPath)
	}
}

// Occupancy is derived, never stored: a slot whose home device is taken, and a slot
// another workspace has claimed but not attached yet, are both unavailable.
func TestECSEC2SkipsTakenAndClaimedSlots(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-busy", "ap-northeast-1a", "m7i.large", true, true) // someone's home on /dev/sdf
	h.ec2.addSlot("i-claimed", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)
	other := h.ec2.addHomeVolume("vol-other", "M-2", "af-ws-acme-bob", "ap-northeast-1a")
	_ = other
	h.ec2.setTag("vol-other", ec2TagClaim, "i-claimed")
	h.ec2.setTag("vol-other", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
	for _, id := range []string{"i-busy", "i-claimed", "i-free"} {
		h.ci.registered[id] = true
	}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-free" {
		t.Fatalf("landed on %q, want i-free", inst)
	}
}

// Losing the race for the last hot slot is normal, not an error: AttachVolume is the
// lock, and a refusal just means "try the next slot".
func TestECSEC2AttachRaceFallsThroughToNextSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-b", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-a"] = true
	h.ci.registered["i-b"] = true
	h.ec2.attachErr["i-a"] = true
	h.ec2.attachErr["i-b"] = false

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst == "" || inst == "i-a" {
		t.Fatalf("landed on %q; the loser of a race must fall through", inst)
	}
}

// An empty pool must still start a workspace: grow by one slot, mark the volume so
// State() can say `starting`, and finish in the background.
func TestECSEC2GrowsPoolAndClaims(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.rt.State(ctx); got != "starting" {
		t.Fatalf("State after growing the pool = %q, want starting", got)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("deferred = %d, want the convergence handed off", len(h.deferred))
	}
	if inst := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagClaim); !strings.HasPrefix(inst, "i-new") {
		t.Fatalf("claim tag = %q, want the new slot", inst)
	}
	// Run the background half: the new slot comes up, gets registered, and the claim
	// must be cleared once the volume is really attached.
	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ci.registered["i-new1"] = true
	h.runDeferred(ctx)
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-new1" {
		t.Fatalf("attached to %q, want i-new1", inst)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagClaim); got != "" {
		t.Errorf("claim still set to %q after attach", got)
	}
	if got := h.rt.State(ctx); got != "running" {
		t.Errorf("State after convergence = %q, want running", got)
	}
}

// A pool cap has to be enforced somewhere; the alternative is an unbounded EC2 bill.
func TestECSEC2PoolCapRefusesToGrow(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-busy", "ap-northeast-1a", "m7i.large", true, true)

	if err := h.rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "pool is full") {
		t.Fatalf("Start error = %v, want the pool cap", err)
	}
}

// A brand-new workspace has no volume, so the pool picks the AZ — and the volume must
// be born tagged, in ONE call: an untagged volume is invisible to every lookup here and
// would be billed forever with nothing pointing at it.
func TestECSEC2CreatesHomeTaggedInOneCall(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-hot", "ap-northeast-1c", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol == nil {
		t.Fatalf("home volume not found by tag: %v", err)
	}
	if aws.ToString(vol.AvailabilityZone) != "ap-northeast-1c" {
		t.Errorf("volume AZ = %q, want the slot's AZ", aws.ToString(vol.AvailabilityZone))
	}
	if aws.ToInt32(vol.Size) != 50 {
		t.Errorf("volume size = %d, want the deployment default", aws.ToInt32(vol.Size))
	}
	created := ""
	for _, call := range h.ec2.calls {
		if strings.HasPrefix(call, "CreateVolume") {
			created = call
		}
	}
	// The identity tags must be part of the creation call itself. A volume that exists
	// for even a moment without them is invisible to every lookup in this adapter — the
	// next Start would make a second one and bill the first forever.
	if !strings.Contains(created, "tags=5") {
		t.Errorf("CreateVolume did not carry the identity tags: %q", created)
	}
}

// Lazy release: Stop must NOT take the home off its slot. That attachment is both the
// affinity ("the same user comes back to the same slot") and the reason the return is
// the 13s path instead of re-attaching and re-mounting.
func TestECSEC2StopKeepsTheHomeAttached(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(h.ecs.updateCalls) != 1 || aws.ToInt32(h.ecs.updateCalls[0].DesiredCount) != 0 {
		t.Fatalf("Stop must scale to zero, got %+v", h.ecs.updateCalls)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-hot" {
		t.Errorf("home was detached on Stop (inst=%q); lazy release means it stays", inst)
	}
	if len(h.ssmc.commands) != 0 {
		t.Errorf("nothing should be unmounted on Stop, got %v", h.ssmc.commands)
	}
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagIdleSince) == "" {
		t.Error("Stop must record when the home went dormant (the sweeper reads it)")
	}
	// ...and the workspace reads as stopped, not starting: nothing is converging.
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	if got := h.rt.State(ctx); got != "stopped" {
		t.Errorf("State after Stop = %q, want stopped", got)
	}
}

// A dormant slot is stopped, not terminated — the image cache lives on its root volume.
// Waking it must not re-attach anything, and must not look like a fresh launch.
func TestECSEC2StartWakesADormantSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-sleep", "ap-northeast-1a", "m7i.large", false, false) // stopped
	h.ci.registered["i-sleep"] = true                                      // it re-registers with the cluster as it boots
	h.ec2.attach("vol-1", "i-sleep", time.Now().Add(-time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Waking is deferred on purpose: a slot the sweeper just put to sleep is `stopping`,
	// and EC2 refuses to start it from there — so Start must not sit on that wait.
	if got := h.rt.State(ctx); got != "starting" {
		t.Errorf("State while the slot boots = %q, want starting", got)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("waking a dormant slot must be handed off, deferred=%d", len(h.deferred))
	}
	h.runDeferred(ctx)
	started := false
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StartInstances i-sleep") {
			started = true
		}
		if strings.HasPrefix(c, "AttachVolume") || strings.HasPrefix(c, "RunInstances") {
			t.Errorf("waking a dormant slot must not %s — the home is already on it", c)
		}
	}
	if !started {
		t.Error("the dormant slot was never started")
	}
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagIdleSince) != "" {
		t.Error("the idle mark must be cleared when the owner comes back")
	}
}

// The sweeper is what keeps lazy release from becoming hoarding: after the idle window
// it STOPS the slot (cheap, keeps the cache) instead of releasing it (which would throw
// away the affinity and make the owner re-attach).
func TestECSEC2SweeperStopsDormantSlotInsteadOfReleasing(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	f := h.factory()
	f.sweepVolume(ctx, h.ec2.volumes["vol-1"])

	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-hot" {
		t.Error("the sweeper released a merely dormant home; it should only put the slot to sleep")
	}
	stopped := false
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances i-hot") {
			stopped = true
		}
	}
	if !stopped {
		t.Error("the dormant slot was never stopped — the deployment keeps paying for it")
	}
}

func TestECSEC2SweeperLeavesFreshlyDormantSlotsAlone(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances") {
			t.Error("a slot dormant for a minute must stay hot — the owner is probably coming back")
		}
	}
}

// At the cap the only way to serve a new user is to take a slot back. It must be the
// LONGEST-dormant one, and never one that is in use.
func TestECSEC2EvictsLongestDormantAtTheCap(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 2
	h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-b", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-a"] = true
	h.ci.registered["i-b"] = true
	// bob has been dormant for 2h, carol for 10m; dave wants a slot.
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-a", time.Now().Add(-3*time.Hour))
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ec2.addHomeVolume("vol-carol", "M-CAROL", "af-ws-carol", "ap-northeast-1a")
	h.ec2.attach("vol-carol", "i-b", time.Now().Add(-3*time.Hour))
	h.ec2.setTag("vol-carol", ec2TagIdleSince, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-carol"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-dave"]); inst != "i-a" {
		t.Fatalf("dave landed on %q, want i-a (bob was dormant longest)", inst)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-carol"]); inst != "i-b" {
		t.Error("carol was evicted even though bob had been dormant far longer")
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "RunInstances") {
			t.Error("grew the pool past its cap instead of reclaiming a dormant slot")
		}
	}
}

func TestECSEC2NeverEvictsALiveWorkspace(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-a"] = true
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-a", time.Now().Add(-3*time.Hour))
	// Marked dormant, but bob's service came back up in the meantime.
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	err := h.rt.Start(ctx)
	if err == nil {
		t.Fatal("Start must fail rather than evict a running workspace")
	}
	if inst := attachedInstance(h.ec2.volumes["vol-bob"]); inst != "i-a" {
		t.Fatal("bob lost his slot while his task was running")
	}
	if len(h.ssmc.commands) != 0 {
		t.Errorf("nothing should have been unmounted, got %v", h.ssmc.commands)
	}
}

// The ordering that can destroy user data. Reached now by eviction, Destroy and drift
// repair rather than by Stop.
func TestECSEC2ReleaseUnmountsBeforeDetach(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot: %v", err)
	}
	if len(h.ssmc.commands) != 1 || !strings.HasPrefix(h.ssmc.commands[0], "af-umount") {
		t.Fatalf("expected an umount, got %v", h.ssmc.commands)
	}
	umount, detach := -1, -1
	for i, c := range h.ec2.calls {
		if strings.HasPrefix(c, "SSM af-umount") {
			umount = i
		}
		if strings.HasPrefix(c, "DetachVolume") {
			detach = i
		}
	}
	if umount < 0 || detach < 0 || umount > detach {
		t.Fatalf("umount must come before detach, calls = %v", h.ec2.calls)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "" {
		t.Error("volume still attached after release")
	}
}

// A stopped slot cannot be reached over SSM, and has nothing mounted: the instance stop
// unmounted it on the way down. Waiting for an umount that can never run would make
// dormant slots impossible to reclaim.
func TestECSEC2ReleaseOfADormantSlotSkipsUnmount(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-sleep", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.attach("vol-1", "i-sleep", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot on a stopped slot: %v", err)
	}
	if len(h.ssmc.commands) != 0 {
		t.Errorf("no SSM command can run on a stopped slot, got %v", h.ssmc.commands)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "" {
		t.Error("the dormant slot was not reclaimed")
	}
}

// If the unmount fails the volume must stay put: forcing the detach off a mounted
// filesystem is how a home gets corrupted.
func TestECSEC2FailedUnmountBlocksDetach(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ssmc.fail["af-umount"] = true
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.releaseSlot(ctx); err == nil {
		t.Fatal("releaseSlot must fail when the umount fails")
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "DetachVolume") {
			t.Fatal("detached despite a failed umount")
		}
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-hot" {
		t.Error("volume was released anyway")
	}
}

// A release must never run while the workspace is meant to be up — the sweeper calls
// the same routine from a completely different context.
func TestECSEC2ReleaseRefusesLiveService(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	if err := h.rt.releaseSlot(ctx); err == nil {
		t.Fatal("releaseSlot must refuse a service at desired 1")
	}
	if len(h.ssmc.commands) != 0 {
		t.Errorf("no umount should have been attempted, got %v", h.ssmc.commands)
	}
}

// A Start that finds its volume already attached reuses the slot instead of detaching
// and re-attaching — this is the "Start right after Stop" path.
func TestECSEC2StartReusesExistingAttachment(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ci.registered["i-hot"] = true
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "AttachVolume") || strings.HasPrefix(c, "DetachVolume") {
			t.Errorf("unnecessary %s on a slot that already holds the home", c)
		}
	}
	if len(h.ecs.updateCalls) != 1 || aws.ToInt32(h.ecs.updateCalls[0].DesiredCount) != 1 {
		t.Fatalf("expected the service to be brought to 1, got %+v", h.ecs.updateCalls)
	}
}

// Sizing on EC2 is a choice of instance type, not one of Fargate's 74 discrete pairs.
func TestECSEC2SlotTypeFor(t *testing.T) {
	p := ec2PoolConfig{slotSizes: parseSlotSizes("m7i.large:8192,m7i.xlarge:16384,m7i.2xlarge:32768")}
	for _, c := range []struct {
		bytes int64
		want  string
	}{
		{0, "m7i.large"},
		{4 * gib, "m7i.large"},
		{8 * gib, "m7i.large"},
		{9 * gib, "m7i.xlarge"},
		{16 * gib, "m7i.xlarge"},
		{30 * gib, "m7i.2xlarge"},
		{999 * gib, "m7i.2xlarge"}, // above the pool: the biggest slot, not a failure
	} {
		if got := p.slotTypeFor(c.bytes); got != c.want {
			t.Errorf("slotTypeFor(%d GiB) = %s, want %s", c.bytes/gib, got, c.want)
		}
	}
}

func TestECSEC2ParseSlotSizesIgnoresJunk(t *testing.T) {
	got := parseSlotSizes("m7i.2xlarge:32768, m7i.large:8192 ,broken,:0,x:abc")
	if len(got) != 2 || got[0].instanceType != "m7i.large" || got[1].instanceType != "m7i.2xlarge" {
		t.Fatalf("parseSlotSizes = %+v, want the two valid entries in ascending order", got)
	}
}

// recreate / clean-home call Stop and then Start straight away, so a teardown can still
// be draining when the workspace comes back up. Releasing the slot then would leave the
// task running with no home mounted — and the entrypoint would write into the slot's
// shared root disk instead of the user's volume.
func TestECSEC2ReleaseAbortsWhenStartRacesIt(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.name = "af-ws-race-alice" // own generation counter, isolated from other tests
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-race-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ci.registered["i-hot"] = true
	h.ecs.services["af-ws-race-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// The task is gone, but the workspace is being started again before the deferred
	// release gets its turn.
	h.ecs.services["af-ws-race-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.ecs.services["af-ws-race-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.runDeferred(ctx)

	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "DetachVolume") {
			t.Fatal("detached a home that a concurrent Start had just claimed")
		}
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-hot" {
		t.Error("home was released despite the restart")
	}
}

// A slot handed back seconds ago still refuses an attach for a few seconds (measured on
// real AWS: 7s after DetachVolume answered and the volume already read `available`).
// Treating that as "no free slot" makes the next Start buy an instance to avoid a
// five-second wait — the pool would defeat its own purpose on every quick swap.
func TestECSEC2AttachRetriesWhileTheDeviceIsStillHeld(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.base.name = "af-ws-retry-alice"
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-retry-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	// The first two attaches fail the way EC2 does mid-release, then the device frees.
	h.ec2.attachErr["i-hot"] = true
	h.ec2.attachFailures = 2

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-hot" {
		t.Fatalf("landed on %q; the adapter must wait out the release instead of growing the pool", inst)
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "RunInstances") {
			t.Error("grew the pool despite a slot that was about to free up")
		}
	}
}

// Stopping a slot while ECS still has a task ENI on it is how an instance comes back
// multi-ENI, loses its auto-assigned public IPv4 and — without NAT — its egress; the
// agent then never reconnects. Reproduced through this code path against real AWS, so
// the guard is not theoretical.
func TestECSEC2SweeperWaitsForTaskENIsBeforeStopping(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	inst := h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	inst.NetworkInterfaces = []ec2types.InstanceNetworkInterface{
		{Description: aws.String("")},
		{Description: aws.String("arn:aws:ecs:ap-northeast-1:1234:attachment/abc")},
	}
	h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	f := h.factory()
	f.sweepVolume(ctx, h.ec2.volumes["vol-1"])
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances") {
			t.Fatal("stopped a slot that still had a task ENI attached")
		}
	}

	// Once ECS has cleaned the ENI up, the same sweep puts the slot to sleep.
	inst.NetworkInterfaces = []ec2types.InstanceNetworkInterface{{Description: aws.String("")}}
	f.sweepVolume(ctx, h.ec2.volumes["vol-1"])
	stopped := false
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances i-hot") {
			stopped = true
		}
	}
	if !stopped {
		t.Error("the slot was never stopped after its task ENI went away")
	}
}
