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
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
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
	// ranAMI is the ImageId of each RunInstances, in order — "" when the request did
	// not override the launch template's own.
	ranAMI    []string
	nextVol   int
	nextInst  int
	nextSnap  int
	snapshots map[string]*ec2types.Snapshot
	// snapshotState overrides the state CreateSnapshot reports, so a test can hold a
	// snapshot at `pending` and drive the wait.
	snapshotState ec2types.SnapshotState
	// snapshotStart overrides the StartTime stamped on a new snapshot; hibernation
	// compares it against the af-hibernating mark, so a test that wants a "stale"
	// snapshot backdates it here.
	snapshotStart *time.Time
	// runErr forces RunInstances to fail for a given SUBNET, standing in for an AZ that
	// has no capacity for the slot type right now.
	runErr map[string]error
	// describeVolumesErr makes the volume lookup fail, for the paths that must keep
	// working (degraded) when it does rather than failing a Start.
	describeVolumesErr error
}

func newFakeEC2() *fakeEC2 {
	return &fakeEC2{
		volumes:   map[string]*ec2types.Volume{},
		instances: map[string]*ec2types.Instance{},
		subnetAZ:  map[string]string{"sub-1a": "ap-northeast-1a", "sub-1c": "ap-northeast-1c"},
		attachErr: map[string]bool{},
		snapshots: map[string]*ec2types.Snapshot{},
		runErr:    map[string]error{},
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

// giveTaskENI puts the pair of interfaces a slot running a task actually has on it: its
// own primary, and the one ECS attached for the awsvpc task.
func (f *fakeEC2) giveTaskENI(instID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst := f.instances[instID]; inst != nil {
		inst.NetworkInterfaces = []ec2types.InstanceNetworkInterface{
			{Description: aws.String("")},
			{Description: aws.String("arn:aws:ecs:ap-northeast-1:1234:attachment/abc")},
		}
	}
}

func (f *fakeEC2) dropTaskENIs(instID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := f.instances[instID]
	if inst == nil {
		return
	}
	var kept []ec2types.InstanceNetworkInterface
	for _, ni := range inst.NetworkInterfaces {
		if !strings.Contains(aws.ToString(ni.Description), ":ecs:") {
			kept = append(kept, ni)
		}
	}
	inst.NetworkInterfaces = kept
}

// setInstanceTag is setTag's counterpart for a SLOT. The free-pool dormancy mark
// (af-slot-idle-since) lives on the instance, because a free slot has no volume.
func (f *fakeEC2) setInstanceTag(instID, key, value string) {
	i := f.instances[instID]
	for n := range i.Tags {
		if aws.ToString(i.Tags[n].Key) == key {
			i.Tags[n].Value = aws.String(value)
			return
		}
	}
	i.Tags = append(i.Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
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
	if f.describeVolumesErr != nil {
		return nil, f.describeVolumesErr
	}
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
		SnapshotId: in.SnapshotId,
	}
	f.log("CreateVolume %s az=%s tags=%d", id, aws.ToString(in.AvailabilityZone), len(tags))
	// Tags come back on the response, like the real API. Dropping them made the fake
	// say "this volume has no tags" for a call that had just tagged it — which is the
	// wrong direction of the usual fake bug, but still a fake that does not model AWS.
	return &ec2.CreateVolumeOutput{
		VolumeId: aws.String(id), AvailabilityZone: in.AvailabilityZone,
		State: ec2types.VolumeStateAvailable, Tags: tags,
	}, nil
}

func (f *fakeEC2) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("DeleteVolume %s", aws.ToString(in.VolumeId))
	delete(f.volumes, aws.ToString(in.VolumeId))
	return &ec2.DeleteVolumeOutput{}, nil
}

func (f *fakeEC2) DescribeSnapshots(_ context.Context, in *ec2.DescribeSnapshotsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &ec2.DescribeSnapshotsOutput{}
	for _, s := range f.snapshots {
		if !filterMatch(in.Filters, func(name string) []string {
			if strings.HasPrefix(name, "tag:") {
				return []string{ec2TagValue(s.Tags, strings.TrimPrefix(name, "tag:"))}
			}
			if name == "volume-id" {
				return []string{aws.ToString(s.VolumeId)}
			}
			return nil
		}) {
			continue
		}
		out.Snapshots = append(out.Snapshots, *s)
	}
	return out, nil
}

func (f *fakeEC2) CreateSnapshot(_ context.Context, in *ec2.CreateSnapshotInput, _ ...func(*ec2.Options)) (*ec2.CreateSnapshotOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSnap++
	id := fmt.Sprintf("snap-%d", f.nextSnap)
	f.log("CreateSnapshot %s -> %s", aws.ToString(in.VolumeId), id)
	var tags []ec2types.Tag
	for _, ts := range in.TagSpecifications {
		tags = append(tags, ts.Tags...)
	}
	// completed straight away: the fake models the API's bookkeeping, not the hours a
	// real snapshot takes. Tests that care about the wait drive snapshotState instead.
	state := ec2types.SnapshotStateCompleted
	if f.snapshotState != "" {
		state = f.snapshotState
	}
	started := time.Now()
	if f.snapshotStart != nil {
		started = *f.snapshotStart
	}
	s := &ec2types.Snapshot{
		SnapshotId: aws.String(id), VolumeId: in.VolumeId, State: state, Tags: tags,
		StartTime: aws.Time(started),
	}
	f.snapshots[id] = s
	return &ec2.CreateSnapshotOutput{SnapshotId: aws.String(id), State: state, Tags: tags}, nil
}

func (f *fakeEC2) DeleteSnapshot(_ context.Context, in *ec2.DeleteSnapshotInput, _ ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("DeleteSnapshot %s", aws.ToString(in.SnapshotId))
	delete(f.snapshots, aws.ToString(in.SnapshotId))
	return &ec2.DeleteSnapshotOutput{}, nil
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
	// The FORCE flag is part of what a test needs to see: a forced detach is the yanked
	// cable, only correct once the box has been proved unstoppable (abandonLostSlot).
	if aws.ToBool(in.Force) {
		f.log("DetachVolume %s force", aws.ToString(in.VolumeId))
	} else {
		f.log("DetachVolume %s", aws.ToString(in.VolumeId))
	}
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
		// Volumes AND instances: quarantining a slot re-stamps af-role on the INSTANCE
		// (決定 20), and a fake that only knew about volumes reported "tag written" while
		// the box stayed in the pool — the implementation looked correct and was not.
		var tags *[]ec2types.Tag
		if v := f.volumes[r]; v != nil {
			tags = &v.Tags
		} else if i := f.instances[r]; i != nil {
			tags = &i.Tags
		} else {
			continue
		}
		for _, t := range in.Tags {
			// OVERWRITE, like the real API. Appending left two tags with the same key and
			// ec2TagValue reads the first, so re-stamping a mark silently kept the old
			// value — a fake that made a broken implementation look correct.
			replaced := false
			for i := range *tags {
				if aws.ToString((*tags)[i].Key) == aws.ToString(t.Key) {
					(*tags)[i].Value = t.Value
					replaced = true
				}
			}
			if !replaced {
				*tags = append(*tags, t)
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
		// Instances as well as volumes, for the same reason CreateTags above handles
		// both: the slot's owner tags (af-membership / af-tenant, ADR 0048 決定 3) are
		// removed from the INSTANCE on release, and a volumes-only fake would report
		// "cleared" while the box kept billing its last user forever.
		var tags *[]ec2types.Tag
		if v := f.volumes[r]; v != nil {
			tags = &v.Tags
		} else if i := f.instances[r]; i != nil {
			tags = &i.Tags
		} else {
			continue
		}
		var kept []ec2types.Tag
		for _, t := range *tags {
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
		*tags = kept
		f.log("DeleteTags %s", r)
	}
	return &ec2.DeleteTagsOutput{}, nil
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.runErr[aws.ToString(in.SubnetId)]; err != nil {
		f.log("RunInstances REFUSED subnet=%s (%v)", aws.ToString(in.SubnetId), err)
		return nil, err
	}
	f.nextInst++
	id := fmt.Sprintf("i-new%d", f.nextInst)
	f.instances[id] = &ec2types.Instance{
		InstanceId:   aws.String(id),
		InstanceType: in.InstanceType,
		State:        &ec2types.InstanceState{Name: ec2types.InstanceStateNamePending},
		Placement:    &ec2types.Placement{AvailabilityZone: aws.String(f.subnetAZ[aws.ToString(in.SubnetId)])},
	}
	f.ranAMI = append(f.ranAMI, aws.ToString(in.ImageId))
	f.log("RunInstances %s type=%s subnet=%s ami=%s", id, in.InstanceType, aws.ToString(in.SubnetId), aws.ToString(in.ImageId))
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

// TerminateInstances models the two things about a real terminate that the sweeper's
// safety argument rests on: the box leaves every instance-state filter, and its HOME
// survives as an unattached volume (the launch template sets DeleteOnTermination only on
// the root device, so a home left attached would come back `available`, not disappear).
// A fake that only flipped the state would let a test "prove" a release-then-terminate
// ordering that AWS does not actually give us.
func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range in.InstanceIds {
		f.log("TerminateInstances %s", id)
		if i := f.instances[id]; i != nil {
			i.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated}
			i.BlockDeviceMappings = nil
		}
		for _, v := range f.volumes {
			if attachedInstance(v) != id {
				continue
			}
			v.Attachments = nil
			v.State = ec2types.VolumeStateAvailable
		}
	}
	return &ec2.TerminateInstancesOutput{}, nil
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
	registered map[string]bool
	// tasks maps EC2 instance id -> running task count. The free-slot sweep asks ECS
	// "is anything on this box" as well as asking EC2 "is a home on it", because a task
	// can be running on a slot no home is attached to at all.
	tasks        map[string]int32
	deregistered []string
	// sink lets a forced deregistration do to the fake world what ECS does to the real
	// one: once ECS gives up on the task, it takes the task's ENI off the instance.
	// abandonLostSlot's whole ordering argument rests on that happening, so the fake has
	// to model it rather than leave the ENI there forever.
	sink *fakeEC2
	// stickyENI models the opposite: ECS acknowledges the deregistration but the ENI
	// stays attached. The teardown must then refuse to stop the box (決定 3-3).
	stickyENI bool
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
			RunningTasksCount:    f.tasks[id],
		})
	}
	return out, nil
}

func (f *fakeContainerInstances) DeregisterContainerInstance(_ context.Context, in *ecs.DeregisterContainerInstanceInput, _ ...func(*ecs.Options)) (*ecs.DeregisterContainerInstanceOutput, error) {
	arn := aws.ToString(in.ContainerInstance)
	f.deregistered = append(f.deregistered, arn)
	id := strings.TrimPrefix(arn, "arn:ci/")
	delete(f.registered, id)
	if f.sink != nil && !f.stickyENI {
		f.sink.dropTaskENIs(id)
	}
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
	// lastTaskDef and startGen/startPhase are process-local scratch keyed by
	// workspace name (runtime_ecs_ec2.go) — every test in this package reuses the
	// same "af-ws-acme-alice" name, so without a reset an earlier test's cache
	// entry leaks into a later one and its Start silently skips
	// RegisterTaskDefinition. Production never resets it (a real CP process only
	// serves one instance ID per real slot lifetime); tests must, since they don't.
	lastTaskDef.Delete("af-ws-acme-alice")
	startGen.Delete("af-ws-acme-alice")
	startPhase.Delete("af-ws-acme-alice")
	h := &ec2Harness{
		ec2:  newFakeEC2(),
		ecs:  &fakeECS{},
		efs:  &fakeEFS{},
		ssm:  &fakeSSM{},
		ssmc: &fakeSSMCmd{fail: map[string]bool{}},
		ci:   &fakeContainerInstances{registered: map[string]bool{}, tasks: map[string]int32{}},
	}
	h.ssmc.sink = h.ec2
	h.ci.sink = h.ec2
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
			classes:        parseSlotClasses("m7i.large:8192,m7i.xlarge:16384"),
			defaultClass:   "default",
			maxSlots:       4,
			homeGiB:        50,
			tmpfsMiB:       2048,
			tmpfsOpts:      []string{"nosuid", "nodev"},
			claimTTL:       15 * time.Minute,
			releaseGrace:   10 * time.Minute,
			slotSleepAfter: 15 * time.Minute,
			waitBudget:     time.Minute,
			// The production default, so the ordinary paths here are the ordinary paths
			// there. `sleep` is a no-op in this harness, so the 100 polls it works out to
			// cost nothing; the lost-slot tests shorten it only to keep their logs small.
			slotLostAfter: 5 * time.Minute,
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

// A re-wake of the SAME slot with nothing changed must not register a second task
// definition or force a new deployment — that pair is what retires the running
// task and starts a fresh one, and the ~1-2 minutes Service Connect spends routing
// to both is what silently dropped an in-flight Claude Code OAuth flow_id
// (confirmed 2026-08-19 on the dev deployment). A no-op Stop→Start on one slot is the
// common case (every idle-timeout return), so it must take the cheap path.
func TestECSEC2ReWakingTheSameSlotReusesTheTaskDefinition(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after first Start = %d, want 1", len(h.ecs.regCalls))
	}

	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// The fake does not model ECS retiring the task on its own; mirror what a real
	// desiredCount-0 service looks like once its task has actually stopped (same
	// simulation TestECSEC2StopKeepsTheHomeAttached uses).
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after re-waking the same slot = %d, want 1 (still just the first)", len(h.ecs.regCalls))
	}
	if len(h.ecs.updateCalls) != 2 {
		t.Fatalf("updateCalls = %d, want 2 (Stop, then the second Start)", len(h.ecs.updateCalls))
	}
	second := h.ecs.updateCalls[1]
	if aws.ToInt32(second.DesiredCount) != 1 {
		t.Errorf("second Start's UpdateService DesiredCount = %d, want 1", aws.ToInt32(second.DesiredCount))
	}
	if second.ForceNewDeployment {
		t.Error("re-waking the same slot with nothing changed must not force a new deployment")
	}
	if want := "arn:task/" + aws.ToString(h.ecs.regCalls[0].Family) + ":1"; aws.ToString(second.TaskDefinition) != want {
		t.Errorf("second Start's TaskDefinition = %q, want the reused first-registration ARN %q", aws.ToString(second.TaskDefinition), want)
	}
}

// Anything that actually changes the task definition (here: the slot changes, so the
// placement constraint must point at the new instance) must register a fresh revision —
// reuse must never paper over a real change — and it must reach the service as TWO
// calls: the revision first, desiredCount last.
//
// One call carrying both is what ECS answers by scaling the deployment it just demoted,
// so a task from the OLD revision either runs alongside the real one for a minute or
// stalls the real one for ~41s on a MemberOf that no longer matches anything
// (docs/log/64 §64.39). ForceNewDeployment on the second call would put it straight back.
func TestECSEC2MovingSlotsSplitsTheServiceUpdate(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	// Simulate the home moving to a different slot (eviction/new-AZ repair): the
	// same volume now attaches to a second, distinct instance.
	h.ec2.addSlot("i-hot-2", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot-2"] = true
	h.ec2.attach("vol-1", "i-hot-2", time.Now())

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if len(h.ecs.regCalls) != 2 {
		t.Fatalf("regCalls after moving slots = %d, want 2 (a new instance needs a new placement constraint)", len(h.ecs.regCalls))
	}
	if len(h.ecs.updateCalls) != 3 {
		t.Fatalf("updateCalls = %d, want 3 (Stop, then the revision, then desiredCount) — %+v",
			len(h.ecs.updateCalls), h.ecs.updateCalls)
	}
	wantArn := "arn:task/" + aws.ToString(h.ecs.regCalls[1].Family) + ":2"
	point := h.ecs.updateCalls[1]
	if aws.ToString(point.TaskDefinition) != wantArn {
		t.Errorf("first Start-side update should carry the NEW revision %q, got %q", wantArn, aws.ToString(point.TaskDefinition))
	}
	if point.DesiredCount != nil {
		t.Errorf("the revision update must not touch desiredCount (that is what makes ECS scale the old deployment), got %d", aws.ToInt32(point.DesiredCount))
	}
	if point.ForceNewDeployment {
		t.Error("the revision update must not force: changing the revision already starts the deployment")
	}
	up := h.ecs.updateCalls[2]
	if aws.ToInt32(up.DesiredCount) != 1 {
		t.Errorf("second Start-side update DesiredCount = %d, want 1", aws.ToInt32(up.DesiredCount))
	}
	if up.ForceNewDeployment {
		t.Error("scaling up must never force — that spawns exactly the ACTIVE deployment the split just retired")
	}
	if up.TaskDefinition != nil {
		t.Errorf("scaling up should send desiredCount only, got TaskDefinition %q", aws.ToString(up.TaskDefinition))
	}
}

// lastTaskDef is process-local, so it is empty after every CP restart and on whichever
// replica did not serve the last Start. That miss must not turn into a redundant
// revision: the fingerprint is stamped on the revision itself, so AWS can answer
// "already registered exactly this" (docs/log/64 §64.39.6). Before this, every workspace's
// FIRST Start after every deploy rolled the service for a task definition that had not
// changed at all.
func TestECSEC2ReuseSurvivesACPRestart(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after first Start = %d, want 1", len(h.ecs.regCalls))
	}
	if got := h.ecs.regCalls[0].ContainerDefinitions[0].DockerLabels[afTaskDefFingerprintLabel]; got == "" {
		t.Fatal("the revision was registered without its fingerprint label; nothing can read it back")
	}
	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// A stopped service still POINTS at the revision it last ran — that is the whole
	// thing being read back — so drop only the desired count.
	stopped := h.ecs.services["af-ws-acme-alice"]
	stopped.DesiredCount, stopped.RunningCount = 0, 0
	h.ecs.services["af-ws-acme-alice"] = stopped

	// The CP restarts: same AWS, empty process cache.
	lastTaskDef.Delete("af-ws-acme-alice")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls after a restart with nothing changed = %d, want 1 — a redundant revision rolls the service for nothing", len(h.ecs.regCalls))
	}
	last := h.ecs.updateCalls[len(h.ecs.updateCalls)-1]
	if aws.ToInt32(last.DesiredCount) != 1 || last.ForceNewDeployment {
		t.Errorf("the reused path must just scale up, got %+v", last)
	}
}

// …but only when it really is the same thing. A changed input must still register,
// restart or no restart: reuse that papers over a change would launch the wrong image.
func TestECSEC2ReuseAfterRestartStillNoticesAChange(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stopped := h.ecs.services["af-ws-acme-alice"]
	stopped.DesiredCount, stopped.RunningCount = 0, 0
	h.ecs.services["af-ws-acme-alice"] = stopped
	lastTaskDef.Delete("af-ws-acme-alice")

	// The home moved to another slot, so the placement constraint — and therefore the
	// fingerprint — must differ.
	h.ec2.addSlot("i-hot-2", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot-2"] = true
	h.ec2.attach("vol-1", "i-hot-2", time.Now())

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if len(h.ecs.regCalls) != 2 {
		t.Fatalf("regCalls = %d, want 2 — a different slot needs a different placement constraint", len(h.ecs.regCalls))
	}
}

// The scale-up must wait out the deployment the revision change demoted. While one is
// still ACTIVE, ECS satisfies a desiredCount increase from it — the whole bug — so the
// split only helps if the second call is held until it is gone (docs/log/64 §64.39.4).
func TestECSEC2ScaleUpWaitsForTheDemotedDeployment(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ec2.addSlot("i-hot-2", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot-2"] = true
	h.ec2.attach("vol-1", "i-hot-2", time.Now())

	// Three answers still carry the demoted deployment; the poll has to outlast them.
	h.ecs.activeDeploymentPolls = 3
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if h.ecs.activeDeploymentPolls != 0 {
		t.Errorf("scale-up did not wait: %d ACTIVE answers were never consumed", h.ecs.activeDeploymentPolls)
	}
	last := h.ecs.updateCalls[len(h.ecs.updateCalls)-1]
	if aws.ToInt32(last.DesiredCount) != 1 {
		t.Errorf("the workspace must still end up at desiredCount 1, got %+v", last)
	}
}

// A Start that dies at the mount has already pointed the service at the new revision.
// That is safe ONLY because the revision update leaves desiredCount alone: the service
// stays at 0, so nothing runs on the slot that just got quarantined, and the next Start
// recomputes the revision anyway.
func TestECSEC2FailedMountLeavesTheServiceStopped(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ec2.addSlot("i-hot-2", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot-2"] = true
	h.ec2.attach("vol-1", "i-hot-2", time.Now())
	h.ssmc.fail["af-mount"] = true

	// Start surfaces the mount failure on this (hot-slot) path; what matters here is
	// what it left behind.
	_ = h.rt.Start(ctx)
	pointed := false
	for i, c := range h.ecs.updateCalls {
		if aws.ToInt32(c.DesiredCount) == 1 {
			t.Fatalf("updateCalls[%d] scaled a workspace up whose mount failed: %+v", i, c)
		}
		if c.TaskDefinition != nil && c.DesiredCount == nil {
			pointed = true
		}
	}
	if !pointed {
		t.Error("the revision update should already have gone out before the mount — that is what buys the overlap")
	}
}

// --- a slot whose OS has died (docs/log/64 §64.40) ---

// wedge is the shape of the incident: the workspace's own slot is EC2-`running`, its
// task's ENI is still bolted to it, and the cluster cannot reach its agent. Every AWS
// status check passes; the box is simply gone.
func wedgedSlotHarness(t *testing.T) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	// Three polls is plenty to prove the point and keeps the intent readable.
	h.rt.pool.slotLostAfter = 3 * slotRegisterPoll
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-dead", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-dead", time.Now().Add(-time.Hour))
	h.ec2.giveTaskENI("i-dead")
	// Registered with the cluster, but the agent stopped answering — which is exactly
	// what DescribeContainerInstances reports for a box whose OS has wedged.
	h.ci.registered["i-dead"] = false
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	return h
}

// 🔥 The 504. Start runs in an HTTP handler behind a 60s-idle ingress, so it may only do
// work measured in seconds inline — and reusing the home's existing slot is normally the
// FASTEST path there is, which is why this branch used to run inline unconditionally.
// It asked EC2 "is the box running" and never asked the cluster "can you reach it", so a
// wedged slot sent the whole convergence down the synchronous path, where launch()'s first
// act is a wait that could not finish. The user got a 504 per attempt, for hours.
func TestECSEC2StartDoesNotRunInlineOnAnUnreachableSlot(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlotHarness(t)

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.deferred) != 1 {
		t.Fatalf("deferred %d background steps, want 1: a slot the cluster cannot reach must never be waited on inline", len(h.deferred))
	}
	// Nothing may have been torn down yet either — Start's job here is to get off the
	// request thread, and the decision about the box belongs to the convergence.
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances") || strings.HasPrefix(c, "DetachVolume") {
			t.Fatalf("Start tore down %q on the request thread", c)
		}
	}
}

// The recovery, end to end: the box is declared lost, taken out of the world in an order
// that is safe for a machine whose kernel is not answering, and the workspace comes up on
// a different slot — without the user pressing anything a second time.
func TestECSEC2LostSlotIsAbandonedAndTheWorkspaceMovesOn(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlotHarness(t)
	h.ec2.addSlot("i-good", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-good"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)

	// 1. the box is out of the pool, with the reason recorded on it
	if role := ec2TagValue(h.ec2.instances["i-dead"].Tags, ec2TagRole); role != ec2RoleQuarantined {
		t.Errorf("af-role on the lost slot = %q, want %q — it is still in freeSlots", role, ec2RoleQuarantined)
	}
	if why := ec2TagValue(h.ec2.instances["i-dead"].Tags, ec2TagQuarantineReason); !strings.Contains(why, "cluster") {
		t.Errorf("af-quarantine-reason = %q, want it to say why the box was abandoned", why)
	}
	// 2. ECS was forced to give up on the ghost task — the step that releases the ENI and
	//    the only thing that can break the deadlock
	if len(h.ci.deregistered) != 1 || h.ci.deregistered[0] != "arn:ci/i-dead" {
		t.Fatalf("deregistered = %v, want [arn:ci/i-dead]", h.ci.deregistered)
	}
	// 3. …before the box was stopped, and stopped forcibly (nothing in it is listening)
	stopIdx, deregDone := -1, false
	for i, c := range h.ec2.calls {
		if c == "StopInstances i-dead" {
			stopIdx = i
		}
	}
	deregDone = len(h.ci.deregistered) > 0
	if stopIdx < 0 || !deregDone {
		t.Fatalf("the lost slot was never stopped (calls=%v)", h.ec2.calls)
	}
	// 4. the home came off cleanly — the box was already powered down, so no forced
	//    detach was needed — and landed on the healthy slot
	for _, c := range h.ec2.calls {
		if c == "DetachVolume vol-1 force" {
			t.Error("forced the home off a box that had already stopped; the clean detach is the one that cannot corrupt it")
		}
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-good" {
		t.Fatalf("home ended up on %q, want i-good", inst)
	}
	// 5. and the workspace actually came up there, with no second Start from the user
	if !strings.HasPrefix(h.ssmc.commands[len(h.ssmc.commands)-1], "af-mount vol-1 /af-home/M-1") {
		t.Errorf("home was never mounted on the replacement slot: %v", h.ssmc.commands)
	}
	up := false
	for _, c := range h.ecs.updateCalls {
		if aws.ToInt32(c.DesiredCount) == 1 {
			up = true
		}
	}
	if !up && len(h.ecs.createCalls) == 0 {
		t.Error("the service was never scaled up on the replacement slot")
	}
	// 6. the claim is gone: the workspace is `running` now, and a claim left behind would
	//    keep answering `starting`
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagClaim) != "" {
		t.Error("the claim outlived the recovery")
	}
}

// ⚠️ The one thing this teardown may NOT do. A slot stopped while ECS still holds a task
// ENI comes back MULTI-ENI and silently loses its egress (ADR 0045 決定 3-3) — the same
// trap the sweeper refuses to go near. If the deregistration does not free the ENI, the
// box stays up and only the HOME is taken back, forcibly, because leaving it bolted to a
// machine that will never run anything again is the worse outcome.
func TestECSEC2LostSlotIsLeftRunningWhileItHoldsATaskENI(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlotHarness(t)
	h.ci.stickyENI = true
	h.ec2.addSlot("i-good", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-good"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)

	for _, c := range h.ec2.calls {
		if c == "StopInstances i-dead" {
			t.Fatal("stopped a slot that still had a task ENI attached")
		}
	}
	forced := false
	for _, c := range h.ec2.calls {
		if c == "DetachVolume vol-1 force" {
			forced = true
		}
	}
	if !forced {
		t.Errorf("the home was never forced off the unstoppable slot: %v", h.ec2.calls)
	}
	// It is still out of the pool, and the user still gets their workspace.
	if role := ec2TagValue(h.ec2.instances["i-dead"].Tags, ec2TagRole); role != ec2RoleQuarantined {
		t.Errorf("af-role = %q, want %q", role, ec2RoleQuarantined)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-good" {
		t.Errorf("home ended up on %q, want i-good", inst)
	}
}

// The bound. "The slot was lost, take another" is only safe because it is budgeted: a
// pool that is failing for a reason this code does not model would otherwise walk every
// box into quarantine, one Start at a time, and turn a degraded deployment into an empty
// one. Two boxes tried, then it stops and leaves a state the user can retry from.
func TestECSEC2LostSlotRecoveryQuarantinesAtMostOneSpare(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlotHarness(t)
	// The "healthy" spare is just as dead — a cluster-wide failure, not a bad box.
	h.ec2.addSlot("i-dead-2", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-dead-2"] = false
	h.ec2.addSlot("i-dead-3", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-dead-3"] = false

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)

	quarantined := 0
	for id, inst := range h.ec2.instances {
		if ec2TagValue(inst.Tags, ec2TagRole) == ec2RoleQuarantined {
			quarantined++
			if id == "i-dead-3" {
				t.Error("a third box was taken out; the retry budget is one")
			}
		}
	}
	if quarantined != 2 {
		t.Errorf("quarantined %d slots, want exactly 2 (the original and one replacement)", quarantined)
	}
	// Giving up must leave the workspace STARTABLE. The claim abandonLostSlot keeps alive
	// for the hand-off has no replacement coming now, and a claim that outlives its launch
	// pins State() at `starting` — which Start early-returns on, so nobody could recover.
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagClaim) != "" {
		t.Error("the claim was left behind: the workspace is stuck at `starting` until it expires")
	}
	if st := h.rt.State(ctx); st != "stopped" {
		t.Errorf("State after giving up = %q, want stopped", st)
	}
}

// A slot that has not registered YET is not a slot that is gone. The grace only starts a
// teardown for a box EC2 says is `running`; anything still coming up gets the benefit of
// the doubt, because tearing down a mid-boot box turns a slow Start into a destroyed one.
func TestECSEC2SlotStillBootingIsNeverAbandoned(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotLostAfter = 3 * slotRegisterPoll
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	inst := h.ec2.addSlot("i-booting", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-booting", time.Now())
	inst.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNamePending}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.runDeferred(ctx)

	if role := ec2TagValue(h.ec2.instances["i-booting"].Tags, ec2TagRole); role != ec2RoleSlot {
		t.Errorf("af-role = %q: a slot that is still `pending` was thrown away", role)
	}
	if len(h.ci.deregistered) != 0 {
		t.Errorf("deregistered %v while the box was still booting", h.ci.deregistered)
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

// ⚠️ The eviction path has to check the instance type, and for a while it did not.
// freeSlots filters on instance-type because a slot of the wrong size cannot run the
// task — but evictLongestIdle only filtered by AZ and by "not me", so at the cap a
// member on a bigger rung could take a SMALLER box and land on it. ECS then refuses to
// place the task ("no container instance met all of its requirements") and the service
// sits at desiredCount 1 / runningCount 0 forever. Across architectures placeHome's own
// header records the same symptom measured on a live deployment (docs/log/70 §70.14.5).
//
// It only ever shows at the cap, which is why it survived: below the cap growPool runs
// and the new box is the right size by construction.
func TestECSEC2NeverEvictsASlotOfTheWrongType(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	// The only box in the pool is a large, and it is dormant — a perfect victim by every
	// test the eviction path used to apply.
	h.ec2.addSlot("i-large", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-large"] = true
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-large", time.Now().Add(-3*time.Hour))
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	// dave asked for more memory, so he is on the xlarge rung.
	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The large is taken OUT of the pool rather than handed to dave: reuse cannot turn it
	// into an xlarge, and while it exists it holds the only place under the cap.
	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-large" {
		t.Fatalf("TerminateInstances = %v, want [i-large] — the cap was held by a box dave cannot run on", got)
	}
	// ⚠️ dave must NOT be attached to it. That is the bug this test was written for.
	if inst := attachedInstance(h.ec2.volumes["vol-dave"]); inst == "i-large" {
		t.Fatal("dave's home was attached to an m7i.large — ECS will never place his task there")
	}
	// The freed place is spent on a box of the size he actually needs.
	built := ""
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "RunInstances ") && strings.Contains(c, "type=m7i.xlarge") {
			built = c
		}
	}
	if built == "" {
		t.Fatalf("no m7i.xlarge was built after making room; calls = %v", h.ec2.calls)
	}
	// bob's home survives the box it was on — released first, and never deleted.
	if v := h.ec2.volumes["vol-bob"]; v == nil || v.State != ec2types.VolumeStateAvailable {
		t.Fatal("bob's home did not survive the terminate as an available volume")
	}
}

// The other half: when there is nothing to take out either, the Start still fails — and
// says the pool is full, which is the one thing the operator can act on. Making room must
// not turn into "terminate something, anything".
func TestECSEC2FailsAtTheCapWhenNothingCanBeFreed(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	// One large, and its owner's workspace is RUNNING. Not evictable (wrong size), and not
	// removable either (live service).
	h.ec2.addSlot("i-large", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-large"] = true
	h.ci.tasks["i-large"] = 1
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-large", time.Now().Add(-3*time.Hour))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	err := h.rt.Start(ctx)
	if err == nil {
		t.Fatal("Start must fail rather than disturb a running workspace")
	}
	if !strings.Contains(err.Error(), "full") {
		t.Errorf("Start error = %v, want it to say the pool is full", err)
	}
	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("TerminateInstances = %v — that box is running somebody's workspace", got)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-bob"]); inst != "i-large" {
		t.Error("bob lost his slot while his task was running")
	}
}

// An EMPTY box of the wrong size is taken before somebody's dormant one: it spends nobody's
// affinity at all, so the choice is free.
func TestECSEC2MakesRoomFromAnEmptyBoxFirst(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 2
	// bob's large has been dormant for days; the other large is empty but only minutes free.
	h.ec2.addSlot("i-bobs", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.addSlot("i-empty", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-bobs"] = true
	h.ci.registered["i-empty"] = true
	h.ec2.setInstanceTag("i-empty", ec2TagSlotIdleSince, time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339))
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-bobs", time.Now().Add(-96*time.Hour))
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-empty" {
		t.Fatalf("TerminateInstances = %v, want [i-empty] — the empty box costs nobody anything", got)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-bob"]); inst != "i-bobs" {
		t.Error("bob's home was released although an empty box could have been taken instead")
	}
}

// ⚠️ Not gated on Ec2SlotTerminateAfterSec. That knob is about idle COST and its 0 means
// "keep the boxes"; this only ever runs when the alternative is a member who cannot start,
// and the operator's stated rule is that slower is acceptable and unusable is not. Reading
// the cost knob as "leave the deadlock in place" would answer a question it was never
// asked — and the default IS 0, which is exactly the deployment most likely to sit at the
// cap, because nothing ever removes a box there.
func TestECSEC2MakesRoomEvenWithTheTerminateTimerOff(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.rt.pool.slotTerminateAfter = 0
	h.ec2.addSlot("i-large", "ap-northeast-1a", "m7i.large", false, false)
	h.ci.registered["i-large"] = true
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-large", time.Now().Add(-3*time.Hour))
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 1 {
		t.Fatalf("TerminateInstances = %v, want one — the cost knob must not gate a deadlock", got)
	}
}

// A box a Start is landing on right now holds no attachment yet — only the claim says so.
// Taking it would strand that launch, and the claim is the only evidence there is.
func TestECSEC2MakingRoomNeverTakesAClaimedBox(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.ec2.addSlot("i-large", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-large"] = true
	h.ec2.addHomeVolume("vol-carol", "M-CAROL", "af-ws-carol", "ap-northeast-1a")
	h.ec2.setTag("vol-carol", ec2TagClaim, "i-large")
	h.ec2.setTag("vol-carol", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start must fail rather than take a box another launch is landing on")
	}
	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("TerminateInstances = %v — carol's launch was landing on that box", got)
	}
}

// The other side of the same check: a victim of the RIGHT type is still taken. Without
// this the fix above could quietly turn every eviction into a failure.
func TestECSEC2StillEvictsASlotOfTheRightType(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 2
	// Longest-dormant is the large; dave is on the xlarge rung and must skip past it to
	// the xlarge, even though that one has been dormant for less time.
	h.ec2.addSlot("i-large", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-xl", "ap-northeast-1a", "m7i.xlarge", true, false)
	h.ci.registered["i-large"] = true
	h.ci.registered["i-xl"] = true
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-large", time.Now().Add(-4*time.Hour))
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ec2.addHomeVolume("vol-carol", "M-CAROL", "af-ws-carol", "ap-northeast-1a")
	h.ec2.attach("vol-carol", "i-xl", time.Now().Add(-4*time.Hour))
	h.ec2.setTag("vol-carol", ec2TagIdleSince, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-carol"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.rt.base.name = "af-ws-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-dave", "ap-northeast-1a")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-dave"]); inst != "i-xl" {
		t.Fatalf("dave landed on %q, want i-xl — the longest-dormant box is the wrong size for him", inst)
	}
}

// Eviction crosses TENANT boundaries, and that is a decision rather than an oversight
// (ADR 0045 決定 24). The pool is one deployment-wide resource: forbidding the crossing
// would mean tenant A's dormant box cannot serve tenant B even when nothing else can,
// which produces the one outcome the operator ruled out — a member who cannot start at
// all. What bounds a tenant is max_workspaces (how many of its people run AT ONCE), not
// which physical box they land on; nothing of the victim's is exposed, because releaseSlot
// unmounts and detaches their home first and the root volume is shared with whoever had
// the slot before by design.
//
// This test exists so the crossing cannot be removed by accident, and so that anyone who
// wants to remove it on purpose has to read why.
func TestECSEC2EvictionCrossesTenantsOnPurpose(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-a"] = true
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-other-bob", "ap-northeast-1a")
	h.ec2.setTag("vol-bob", ec2TagTenant, "other-corp")
	h.ec2.attach("vol-bob", "i-a", time.Now().Add(-3*time.Hour))
	h.ec2.setTag("vol-bob", ec2TagIdleSince, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-other-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.rt.base.name = "af-ws-acme-dave"
	h.rt.base.membershipID = "M-DAVE"
	h.rt.base.tenantSlug = "acme"
	h.ec2.addHomeVolume("vol-dave", "M-DAVE", "af-ws-acme-dave", "ap-northeast-1a")
	h.ec2.setTag("vol-dave", ec2TagTenant, "acme")

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-dave"]); inst != "i-a" {
		t.Fatalf("dave landed on %q; a dormant box in another tenant is still a box", inst)
	}
	// The victim's home is off the box before dave's goes on, and unmounted before that.
	umounted := false
	for _, c := range h.ssmc.commands {
		if strings.Contains(c, "af-umount") {
			umounted = true
		}
	}
	if !umounted {
		t.Error("the other tenant's home was detached without being unmounted first")
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
	// Two calls now, not one: the revision first, desiredCount last (docs/log/64 §64.39).
	// Asserted as an invariant rather than a count so this does not re-break the day a
	// reused task definition makes the first call unnecessary.
	last := h.ecs.updateCalls[len(h.ecs.updateCalls)-1]
	if aws.ToInt32(last.DesiredCount) != 1 {
		t.Fatalf("expected the service to be brought to 1, got %+v", h.ecs.updateCalls)
	}
	for i, c := range h.ecs.updateCalls {
		if c.TaskDefinition != nil && c.DesiredCount != nil {
			t.Errorf("updateCalls[%d] carries a revision AND a desiredCount; that pair is what makes ECS scale the deployment it just demoted: %+v", i, c)
		}
	}
}

// Sizing on EC2 is a choice of instance type, not one of Fargate's 74 discrete pairs.
func TestECSEC2SlotTypeFor(t *testing.T) {
	p := ec2PoolConfig{classes: parseSlotClasses("m7i.large:8192,m7i.xlarge:16384,m7i.2xlarge:32768"), defaultClass: "default"}
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
		got, arch := p.slotTypeFor("", c.bytes)
		if got != c.want {
			t.Errorf("slotTypeFor(%d GiB) = %s, want %s", c.bytes/gib, got, c.want)
		}
		if arch != ec2ArchX86 {
			t.Errorf("a bare ladder is x86_64, got %q", arch)
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

// A Start is in flight: placeHome clears the dormancy marks first and upsertService
// raises desiredCount last, with the mount (an SSM round trip) in between. The home walk
// used to read that window as "dormant" — see docs/log/64 §64.31.6.
func TestECSEC2SweeperLeavesAHomeAloneWhileItsStartIsInFlight(t *testing.T) {
	ctx := context.Background()

	// The claim is what says "a launch owns this home right now".
	claim := func(h *ec2Harness, at time.Time) {
		h.ec2.setTag("vol-1", ec2TagClaim, "i-hot")
		h.ec2.setTag("vol-1", ec2TagClaimAt, at.UTC().Format(time.RFC3339))
	}

	t.Run("does not re-stamp the dormancy mark a Start just cleared", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
		claim(h, time.Now())
		// The service is still at the value Stop left; upsertService has not run yet.
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

		h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])

		if v := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagIdleSince); v != "" {
			t.Errorf("af-idle-since = %q on a home whose Start is in flight; the mark would "+
				"outlive the launch and make a RUNNING workspace an eviction victim", v)
		}
	})

	t.Run("does not release the slot before the service exists", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		// Attached longer ago than releaseGrace, so only the claim can save it.
		h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
		claim(h, time.Now())

		h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])

		if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "i-hot" {
			t.Error("the sweeper released a home mid-launch; the Start it interrupted has no service yet")
		}
	})

	// …but an EXPIRED claim must not pin the walk, or a launch that died would keep its
	// slot forever. claimLive reads af-claim-at, so the tag's mere presence is not enough.
	t.Run("an expired claim still falls through", func(t *testing.T) {
		h := newEC2Harness(t)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
		h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
		claim(h, time.Now().Add(-2*time.Hour)) // claimTTL is 15m in the harness
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

		h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])

		if v := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagIdleSince); v == "" {
			t.Error("a home whose claim had expired was never stamped; its slot would never sleep")
		}
	})
}

// --- free slots (docs/log/64 §64.31, ADR 0045 決定 22) ---
//
// A slot whose home has been released leaves the home walk entirely, so before
// sweepFreeSlots existed NOTHING stopped it. Measured on the live deployment: three
// m*.large with zero tasks, running for over 24 hours, at ~$95/month each — while
// Ec2SlotSleepSec's own description promised ~$9.6.

// stoppedInstances is what every test below actually asserts on.
func stoppedInstances(h *ec2Harness) []string {
	var ids []string
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "StopInstances ") {
			ids = append(ids, strings.TrimPrefix(c, "StopInstances "))
		}
	}
	return ids
}

// freeSlotHarness builds the exact shape of the live incident: a slot in the pool, no
// home attached to it, nothing running on it.
func freeSlotHarness(t *testing.T, freeFor time.Duration) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-free"] = true
	h.ec2.setInstanceTag("i-free", ec2TagSlotIdleSince,
		time.Now().Add(-freeFor).UTC().Format(time.RFC3339))
	return h
}

func TestECSEC2SweeperStopsAFreeSlot(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, 30*time.Minute)

	f := h.factory()
	if err := f.sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	got := stoppedInstances(h)
	if len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("StopInstances = %v, want [i-free] — an empty slot ran for 30m and nothing stopped it", got)
	}
}

// The mark is what makes the decision reproducible across a CP restart and across the two
// replicas every rolling deploy overlaps (ADR 0012). A slot that predates this code — or
// one whose CP died between the detach and the tag — has none, so the sweeper stamps it
// and acts only on the NEXT pass. That is also the grace: no slot is ever stopped by the
// first sweep that sees it.
func TestECSEC2SweeperStampsAnUnmarkedFreeSlotBeforeStoppingIt(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-free"] = true

	f := h.factory()
	if err := f.sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 0 {
		t.Fatalf("StopInstances = %v, want none — the first sighting must only stamp the slot", got)
	}
	stamp := ec2TagValue(h.ec2.instances["i-free"].Tags, ec2TagSlotIdleSince)
	if stamp == "" {
		t.Fatal("the sweeper neither stamped nor stopped the free slot; it will never be stopped")
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
		t.Fatalf("af-slot-idle-since = %q, not RFC3339: %v", stamp, err)
	}

	// Backdate the mark the sweeper itself wrote and sweep again: now it is due.
	h.ec2.setInstanceTag("i-free", ec2TagSlotIdleSince,
		time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339))
	if err := f.sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("StopInstances = %v, want [i-free] on the sweep after the mark aged out", got)
	}
}

func TestECSEC2SweeperLeavesAFreshlyFreedSlotAlone(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, time.Minute)

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 0 {
		t.Fatalf("StopInstances = %v, want none — a slot free for a minute is inside the grace", got)
	}
}

// ⚠️ The one guard that must never be skipped. A slot stopped while ECS still has a task
// ENI on it comes back MULTI-ENI and silently loses its auto-assigned public IPv4 and, on
// a deployment without NAT, its egress (ADR 0045 決定 3-3 — reproduced on real hardware
// through the occupied path). The free path can reach the same window.
func TestECSEC2SweeperWaitsForTaskENIsOnAFreeSlot(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, 30*time.Minute)
	h.ec2.instances["i-free"].NetworkInterfaces = []ec2types.InstanceNetworkInterface{
		{Description: aws.String("")},
		{Description: aws.String("arn:aws:ecs:ap-northeast-1:1234:attachment/abc")},
	}

	f := h.factory()
	if err := f.sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 0 {
		t.Fatalf("StopInstances = %v — stopped a free slot that still carried a task ENI", got)
	}

	// ECS detaches it a little after the task is gone; the next sweep stops the slot.
	h.ec2.instances["i-free"].NetworkInterfaces = []ec2types.InstanceNetworkInterface{{Description: aws.String("")}}
	if err := f.sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("StopInstances = %v, want [i-free] once the task ENI was gone", got)
	}
}

// A task can be running on a slot that no home is attached to — the golden baker's probe,
// or plain drift. "No home" is therefore not the same statement as "nothing on it", and
// asking only EC2 would kill a live container.
func TestECSEC2SweeperLeavesAFreeSlotThatStillRunsATask(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, 30*time.Minute)
	h.ci.tasks["i-free"] = 1

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 0 {
		t.Fatalf("StopInstances = %v — stopped a slot ECS still had a task on", got)
	}
}

// Occupancy comes from the volumes, and both of its forms have to count: a home already
// attached, and a home whose Start has only got as far as the claim.
func TestECSEC2SweeperNeverStopsAnOccupiedOrClaimedSlot(t *testing.T) {
	ctx := context.Background()

	t.Run("a home is attached", func(t *testing.T) {
		h := freeSlotHarness(t, 30*time.Minute) // stale mark left over from a past tenancy
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.attach("vol-1", "i-free", time.Now())
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

		if err := h.factory().sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := stoppedInstances(h); len(got) != 0 {
			t.Fatalf("StopInstances = %v — stopped a slot with a live workspace on it", got)
		}
		// …and the stale mark is cleared, or the slot would sleep with no grace at all the
		// next time it is released.
		if v := ec2TagValue(h.ec2.instances["i-free"].Tags, ec2TagSlotIdleSince); v != "" {
			t.Errorf("af-slot-idle-since = %q on an occupied slot; want it cleared", v)
		}
	})

	t.Run("a Start has claimed it", func(t *testing.T) {
		h := freeSlotHarness(t, 30*time.Minute)
		h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
		h.ec2.setTag("vol-1", ec2TagClaim, "i-free")
		h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))

		if err := h.factory().sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := stoppedInstances(h); len(got) != 0 {
			t.Fatalf("StopInstances = %v — stopped a slot a Start was landing on", got)
		}
	})
}

// releaseSlot is where a slot BECOMES free, and it is the only place that knows the exact
// moment. Before this it handed the box back to the pool with no clock on it at all —
// which is precisely how the live deployment ended up with three of them.
func TestECSEC2ReleaseStartsTheSlotsOwnDormancyClock(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
	h.ec2.setInstanceTag("i-hot", ec2TagMembership, "M-1")
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot: %v", err)
	}
	stamp := ec2TagValue(h.ec2.instances["i-hot"].Tags, ec2TagSlotIdleSince)
	if stamp == "" {
		t.Fatal("the released slot carries no af-slot-idle-since; nothing will ever stop it")
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("af-slot-idle-since = %q, not RFC3339: %v", stamp, err)
	}
	if d := time.Since(at); d > time.Minute {
		t.Errorf("af-slot-idle-since is %s old right after the release; it must be now", d)
	}
}

// 0 means OFF for every kind of slot, which is what Ec2SlotSleepSec has always been
// documented to mean. The bare `idle < slotSleepAfter` test made it mean the exact
// opposite — stop at the first sweep — the same trap AF_WS_IDLE_TIMEOUT=0 sprang in
// §64.26.
func TestECSEC2SlotSleepZeroKeepsEverySlotRunning(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, 30*time.Minute)
	h.rt.pool.slotSleepAfter = 0
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stoppedInstances(h); len(got) != 0 {
		t.Fatalf("StopInstances = %v with Ec2SlotSleepSec=0, which means never sleep", got)
	}
}

func terminatedInstances(h *ec2Harness) []string {
	var ids []string
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "TerminateInstances ") {
			ids = append(ids, strings.TrimPrefix(c, "TerminateInstances "))
		}
	}
	return ids
}

// --- slotTerminateAfter: the stage that gives the ROOT volume back (docs/log/64 §64.32) ---
//
// Before this existed nothing ever removed a box, so the number of retained roots only
// grew and its ceiling was maxSlots — raising maxSlots to 30 to serve more people also
// signed the deployment up for 30 × 40 GiB of gp3, permanently. On the live deployment the
// only cure was an operator terminating boxes by hand.

// The home comes off through releaseSlot BEFORE the terminate, and the order is the
// assertion: placeHome derives placement from the attachment, so a home still attached to
// a shutting-down box reads as "your slot is right here" to the owner's next Start.
func TestECSEC2SweeperTerminatesALongDormantSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotTerminateAfter = 4 * time.Hour
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-cold", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.attach("vol-1", "i-cold", time.Now().Add(-9*time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-5*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])

	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-cold" {
		t.Fatalf("TerminateInstances = %v, want [i-cold] — a box dormant 5h still bills its root volume", got)
	}
	detach, terminate := -1, -1
	for i, c := range h.ec2.calls {
		switch {
		case strings.HasPrefix(c, "DetachVolume vol-1"):
			detach = i
		case strings.HasPrefix(c, "TerminateInstances i-cold"):
			terminate = i
		}
	}
	if detach < 0 || detach > terminate {
		t.Fatalf("calls = %v — the home must be detached BEFORE the box is terminated", h.ec2.calls)
	}
	// The home is the user's and survives either way (DeleteOnTermination=false); what
	// must not survive is any belief that it is still on a slot.
	if v := h.ec2.volumes["vol-1"]; attachedInstance(v) != "" || v.State != ec2types.VolumeStateAvailable {
		t.Fatalf("home is %s attached to %q; the next Start would try to wake a terminated box",
			v.State, attachedInstance(v))
	}
}

// A terminated instance stays in the cluster as ACTIVE with agentConnected=false, and a
// ghost that looks ACTIVE still satisfies placement constraints — ECS picks it for a task
// that then never starts (ADR 0045 決定 3-2, seen again when the three live boxes were
// terminated by hand). sweepGhostInstances is the repair path; when the sweeper is itself
// the one removing the box there is no reason to leave the window open at all.
func TestECSEC2SweeperDeregistersTheBoxItTerminates(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotTerminateAfter = 4 * time.Hour
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-cold", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.attach("vol-1", "i-cold", time.Now().Add(-9*time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-5*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ci.registered["i-cold"] = true

	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])

	if got := h.ci.deregistered; len(got) != 1 || got[0] != "arn:ci/i-cold" {
		t.Fatalf("DeregisterContainerInstance = %v, want [arn:ci/i-cold]", got)
	}
}

// The sleep stage keeps working, and terminate does not swallow it: inside the window a
// dormant box is stopped and KEPT, which is the 110s-instead-of-135s the image cache buys.
func TestECSEC2SweeperOnlyStopsADormantSlotInsideTheTerminateWindow(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotTerminateAfter = 4 * time.Hour
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now().Add(-time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-1"])

	if got := stoppedInstances(h); len(got) != 1 || got[0] != "i-hot" {
		t.Fatalf("StopInstances = %v, want [i-hot]", got)
	}
	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("TerminateInstances = %v — 30m dormant is inside the 4h window", got)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-hot" {
		t.Error("the home was released although only the sleep stage was due")
	}
}

// ⚠️ The free-slot walk used to list only `running` boxes, which was consistent while
// stopping was the last thing that could happen to one. With a terminate stage that filter
// means a slot leaves the walk forever the moment it is stopped — exactly the boxes this
// timer exists to collect.
func TestECSEC2SweeperTerminatesALongFreeSlotEvenWhenAlreadyStopped(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotTerminateAfter = 4 * time.Hour
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", false /* stopped */, false)
	h.ci.registered["i-free"] = true
	h.ec2.setInstanceTag("i-free", ec2TagSlotIdleSince,
		time.Now().Add(-5*time.Hour).UTC().Format(time.RFC3339))

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("TerminateInstances = %v, want [i-free] — a stopped free box bills its root forever", got)
	}
}

// Occupancy is re-read immediately before acting for the stop stage; terminate is
// irreversible, so it runs behind the same three guards (a home attached, an ECS task, a
// live claim) rather than a weaker set.
func TestECSEC2SweeperNeverTerminatesAnOccupiedOrClaimedSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotTerminateAfter = 4 * time.Hour
	old := time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339)

	// A box someone's home sits on, marked free by a stale tag.
	h.ec2.addSlot("i-taken", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-taken", time.Now().Add(-9*time.Hour))
	h.ec2.setInstanceTag("i-taken", ec2TagSlotIdleSince, old)
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	// A box a Start is landing on right now: no attachment yet, only the claim says so.
	h.ec2.addSlot("i-claimed", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.addHomeVolume("vol-carol", "M-CAROL", "af-ws-carol", "ap-northeast-1a")
	h.ec2.setTag("vol-carol", ec2TagClaim, "i-claimed")
	h.ec2.setTag("vol-carol", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
	h.ec2.setInstanceTag("i-claimed", ec2TagSlotIdleSince, old)

	// A box with no home but a task ECS still counts.
	h.ec2.addSlot("i-busy", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-busy"] = true
	h.ci.tasks["i-busy"] = 1
	h.ec2.setInstanceTag("i-busy", ec2TagSlotIdleSince, old)

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("TerminateInstances = %v — every one of those boxes is in use", got)
	}
}

// 0 is the default and it has to mean OFF, not "terminate at the first sweep" — the same
// trap Ec2SlotSleepSec fell into (§64.31.4) and AF_WS_IDLE_TIMEOUT before it (§64.26).
func TestECSEC2SlotTerminateZeroNeverTerminatesAnything(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, 30*24*time.Hour)
	h.rt.pool.slotTerminateAfter = 0
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-cold", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.attach("vol-1", "i-cold", time.Now().Add(-60*24*time.Hour))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("TerminateInstances = %v with the timer off, after a month of dormancy", got)
	}
}

// With sleeping switched off there is no stopped stage to pass through, so the terminate
// stage has to be reachable from a RUNNING box or a deployment that sets only this one
// reclaims nothing.
func TestECSEC2SlotTerminateWorksWithSleepingOff(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.slotSleepAfter = 0
	h.rt.pool.slotTerminateAfter = 4 * time.Hour
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-free"] = true
	h.ec2.setInstanceTag("i-free", ec2TagSlotIdleSince,
		time.Now().Add(-5*time.Hour).UTC().Format(time.RFC3339))

	if err := h.factory().sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("TerminateInstances = %v, want [i-free] with Ec2SlotSleepSec=0", got)
	}
}

// P0's Destroy deleted the EBS home and stopped there, leaving the ECS service, both EFS
// access points and both SSM secrets alive for a membership that no longer exists
// (ADR 0045 決定 13, docs/log/64 §64.18.1). Everything the adapter created has to go — and the
// hibernation snapshot with it, or a "deleted" home stays restorable and keeps billing.
func TestECSEC2DestroyFoldsEveryResourceItCreated(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ec2.snapshots["snap-old"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-old"), VolumeId: aws.String("vol-1"),
		State: ec2types.SnapshotStateCompleted,
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.efs.aps = []efstypes.AccessPointDescription{
		{AccessPointId: aws.String("fsap-home"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}, {Key: aws.String("af-role"), Value: aws.String("home")}}},
		{AccessPointId: aws.String("fsap-keep"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/claude-config/M-1")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-1")}, {Key: aws.String("af-role"), Value: aws.String("claude")}}},
		// Somebody else's access point on the same filesystem must survive.
		{AccessPointId: aws.String("fsap-other"), RootDirectory: &efstypes.RootDirectory{Path: aws.String("/home/M-2")},
			Tags: []efstypes.Tag{{Key: aws.String("af-membership"), Value: aws.String("M-2")}, {Key: aws.String("af-role"), Value: aws.String("home")}}},
	}

	leftovers, err := h.rt.Destroy(ctx)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Error("home volume survived Destroy")
	}
	if _, ok := h.ec2.snapshots["snap-old"]; ok {
		t.Error("hibernation snapshot survived Destroy — the home is still restorable and still billed")
	}
	if _, ok := h.ecs.services["af-ws-acme-alice"]; ok {
		t.Error("ECS service survived Destroy")
	}
	if len(h.ecs.deleteCalls) != 1 || !aws.ToBool(h.ecs.deleteCalls[0].Force) {
		t.Errorf("DeleteService must be forced (a draining task refuses otherwise), got %#v", h.ecs.deleteCalls)
	}
	if len(h.efs.aps) != 1 || aws.ToString(h.efs.aps[0].AccessPointId) != "fsap-other" {
		t.Errorf("wrong access points deleted, left = %v", h.efs.aps)
	}
	if len(h.ssm.deletes) != 2 {
		t.Errorf("both SSM secrets must be deleted, got %v", h.ssm.deletes)
	}
	// The EFS directories are the one thing that cannot be removed from the API. They
	// come back as leftovers so the caller can record them rather than believe the data
	// is gone (docs/log/64 §64.18.4).
	if len(leftovers) != 2 {
		t.Errorf("expected the two EFS directories reported as leftovers, got %v", leftovers)
	}
	for _, want := range []string{"/home/M-1", "/claude-config/M-1"} {
		found := false
		for _, l := range leftovers {
			if strings.HasSuffix(l, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("leftover %s not reported, got %v", want, leftovers)
		}
	}
}

// Destroy runs after a crash mid-teardown as often as it runs on a whole workspace, so
// every step has to treat "already gone" as success.
func TestECSEC2DestroyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	if _, err := h.rt.Destroy(ctx); err != nil {
		t.Fatalf("Destroy of a workspace that never started: %v", err)
	}
	if _, err := h.rt.Destroy(ctx); err != nil {
		t.Fatalf("second Destroy: %v", err)
	}
}

// --- hibernation (ADR 0045 決定 4 + 決定 13, docs/log/64 §64.18.2) ---

func hibernateHarness(t *testing.T, dormantFor time.Duration) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.rt.pool.hibernateAfter = 30 * 24 * time.Hour
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-sleep", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.attach("vol-1", "i-sleep", time.Now().Add(-dormantFor))
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-dormantFor).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	return h
}

// A snapshot of a 45 GiB home takes 30–40 minutes, so hibernation cannot be one call.
// Each sweep advances it by one step and the state lives in AWS, not in the CP.
func TestECSEC2HibernationAdvancesOneStepPerSweep(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour)
	h.ec2.snapshotState = ec2types.SnapshotStatePending

	// Step 1: the slot goes back to the pool and the capture starts. The volume must
	// still be here — it is the only copy until the snapshot completes.
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate step 1: %v", err)
	}
	if attachedInstance(h.ec2.volumes["vol-1"]) != "" {
		t.Error("the slot was not released before the snapshot; the capture would be of a live mount")
	}
	if len(h.ec2.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(h.ec2.snapshots))
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Fatal("the volume was deleted while its snapshot was still pending — that is the home, gone")
	}
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHibernating) == "" {
		t.Error("no hibernation mark: the next sweep cannot tell this snapshot from an older one")
	}

	// Step 2: still pending, so still nothing to do — and no second snapshot.
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate step 2: %v", err)
	}
	if len(h.ec2.snapshots) != 1 {
		t.Errorf("a second capture was started while the first was running: %d", len(h.ec2.snapshots))
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Fatal("volume deleted on a pending snapshot")
	}

	// Step 3: completed → the home is now the snapshot, so the volume can go.
	for _, s := range h.ec2.snapshots {
		s.State = ec2types.SnapshotStateCompleted
	}
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate step 3: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; ok {
		t.Error("the volume is still billing after its home was captured")
	}
	if len(h.ec2.snapshots) != 1 {
		t.Errorf("the capture must survive: %d snapshots", len(h.ec2.snapshots))
	}
}

// The data-loss case. A snapshot from an EARLIER dormancy is a snapshot of the same
// volume in the same state field — and acting on it deletes a volume holding everything
// the owner did after coming back.
func TestECSEC2HibernationIgnoresASnapshotOlderThanTheMark(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour)
	stale := time.Now().Add(-90 * 24 * time.Hour)
	h.ec2.snapshots["snap-stale"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-stale"), VolumeId: aws.String("vol-1"),
		State: ec2types.SnapshotStateCompleted, StartTime: aws.Time(stale),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Fatal("the volume was deleted on the strength of a snapshot taken before this dormancy")
	}
	if _, ok := h.ec2.snapshots["snap-stale"]; ok {
		t.Error("the superseded snapshot must be dropped, not left to confuse the next sweep")
	}
	if len(h.ec2.snapshots) != 1 {
		t.Errorf("a fresh capture should have been started: %v", h.ec2.snapshots)
	}
}

// A failed capture must not pin the home forever: never completed, so the volume never
// goes; never absent, so nothing new is ever started.
func TestECSEC2HibernationRetriesAFailedSnapshot(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour)
	h.ec2.snapshotState = ec2types.SnapshotStateError
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	if err := h.rt.hibernate(ctx); err != nil { // sees the error state, discards it
		t.Fatalf("hibernate retry: %v", err)
	}
	if len(h.ec2.snapshots) != 0 {
		t.Errorf("the failed capture must be discarded, got %v", h.ec2.snapshots)
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Error("the volume must survive a failed capture")
	}
}

// The owner coming back beats the sweeper: clearIdle drops the hibernation mark, and the
// snapshot that was already running is then treated as superseded rather than acted on.
func TestECSEC2ReturningOwnerCancelsHibernation(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour)
	h.ec2.snapshotState = ec2types.SnapshotStatePending
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	h.rt.clearDormancy(ctx, "vol-1") // what every Start path does
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHibernating) != "" {
		t.Fatal("the hibernation mark survived the owner's return")
	}
	// That capture completes anyway. It must NOT be mistaken for a new hibernation's.
	for _, s := range h.ec2.snapshots {
		s.State = ec2types.SnapshotStateCompleted
	}
	if err := h.rt.hibernate(ctx); err != nil {
		t.Fatalf("hibernate after the return: %v", err)
	}
	if _, ok := h.ec2.volumes["vol-1"]; !ok {
		t.Error("the volume was deleted using a capture from the interrupted dormancy")
	}
}

// The sweeper never STARTS a hibernation any more: "how long may this tenant's homes sit
// before they are put away" is a database answer, and this loop has no database (ADR 0012).
// It still carries an in-flight one to the end, which is what makes the operation survive a
// CP restart — and what stops a switched-off deployment from billing for a snapshot AND a
// volume forever.
func TestECSEC2SweepNeverStartsAHibernation(t *testing.T) {
	ctx := context.Background()

	t.Run("not even a home dormant for two months", func(t *testing.T) {
		h := hibernateHarness(t, 60*24*time.Hour)
		f := h.factory()
		f.pool.hibernateAfter = 30 * 24 * time.Hour // the deployment default, no longer a trigger
		if err := f.sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if len(h.ec2.snapshots) != 0 {
			t.Errorf("the sweeper started a hibernation on its own: %v", h.ec2.calls)
		}
	})

	t.Run("but resumes one already under way even after it is switched off", func(t *testing.T) {
		h := hibernateHarness(t, 60*24*time.Hour)
		f := h.factory()
		h.ec2.snapshotState = ec2types.SnapshotStatePending
		// The reaper's step: mark, release the slot, start the capture.
		if err := h.rt.BeginHibernate(ctx); err != nil {
			t.Fatalf("BeginHibernate: %v", err)
		}
		if len(h.ec2.snapshots) != 1 {
			t.Fatalf("the capture never started: %v", h.ec2.calls)
		}
		for _, s := range h.ec2.snapshots {
			s.State = ec2types.SnapshotStateCompleted
		}
		// Turned off between sweeps. Leaving the home half-way would bill for BOTH the
		// snapshot and the volume, forever.
		f.pool.hibernateAfter = 0
		if err := f.sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if _, ok := h.ec2.volumes["vol-1"]; ok {
			t.Error("a hibernation in flight was stranded, leaving both a snapshot and a volume")
		}
	})
}

// BeginHibernate is the seam between the reaper (which knows the tenant's window) and the
// sweeper (which finishes the job). Both of its guards exist because the alternative costs
// money or work.
func TestECSEC2BeginHibernateGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("starts the capture", func(t *testing.T) {
		h := hibernateHarness(t, 60*24*time.Hour)
		h.ec2.snapshotState = ec2types.SnapshotStatePending
		if err := h.rt.BeginHibernate(ctx); err != nil {
			t.Fatalf("BeginHibernate: %v", err)
		}
		if len(h.ec2.snapshots) != 1 {
			t.Fatalf("snapshots = %d, want 1", len(h.ec2.snapshots))
		}
		if attachedInstance(h.ec2.volumes["vol-1"]) != "" {
			t.Error("the slot was not released before the capture")
		}
	})

	t.Run("does not start a second capture once it is marked", func(t *testing.T) {
		h := hibernateHarness(t, 60*24*time.Hour)
		h.ec2.snapshotState = ec2types.SnapshotStatePending
		if err := h.rt.BeginHibernate(ctx); err != nil {
			t.Fatalf("BeginHibernate 1: %v", err)
		}
		// The sweeper is advancing this one now. A reaper pass landing in the same window
		// must not fire CreateSnapshot again — the second copy is orphaned and bills.
		if err := h.rt.BeginHibernate(ctx); err != nil {
			t.Fatalf("BeginHibernate 2: %v", err)
		}
		if len(h.ec2.snapshots) != 1 {
			t.Errorf("snapshots = %d, want 1 — the reaper raced the sweeper into a duplicate", len(h.ec2.snapshots))
		}
	})

	t.Run("leaves a workspace that is running alone", func(t *testing.T) {
		h := hibernateHarness(t, 60*24*time.Hour)
		h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
		if err := h.rt.BeginHibernate(ctx); err != nil {
			t.Fatalf("BeginHibernate: %v", err)
		}
		if len(h.ec2.snapshots) != 0 {
			t.Error("hibernated a workspace whose owner had come back")
		}
		if attachedInstance(h.ec2.volumes["vol-1"]) == "" {
			t.Error("released the slot out from under a running task")
		}
	})

	t.Run("does nothing when the home is already a snapshot", func(t *testing.T) {
		h := newEC2Harness(t)
		if err := h.rt.BeginHibernate(ctx); err != nil {
			t.Fatalf("BeginHibernate with no home: %v", err)
		}
		if len(h.ec2.snapshots) != 0 {
			t.Error("captured something although there is no home volume")
		}
	})
}

// The other half: a hibernated home has to come back, and the workspace must not be able
// to tell the difference.
func TestECSEC2StartRestoresAHibernatedHome(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.snapshots["snap-old"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-old"), VolumeId: aws.String("vol-gone"),
		State: ec2types.SnapshotStateCompleted, StartTime: aws.Time(time.Now().Add(-24 * time.Hour)),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	vol, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	created := h.ec2.volumes[aws.ToString(vol.VolumeId)]
	if created == nil || aws.ToString(created.SnapshotId) != "snap-old" {
		t.Fatalf("the home was created empty instead of restored: %#v", created)
	}
	// And the snapshot goes once the volume is usable — otherwise the user pays for both.
	h.runDeferred(ctx)
	if _, ok := h.ec2.snapshots["snap-old"]; ok {
		t.Error("the restored-from snapshot is still billing")
	}
}

// A Start while the capture is still running must fail loudly. Answering "no snapshot"
// would hand the user an empty home while their real one is mid-flight — data loss
// dressed up as a fast start.
func TestECSEC2StartRefusesWhileTheHomeIsBeingCaptured(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.snapshots["snap-run"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-run"), VolumeId: aws.String("vol-gone"),
		State: ec2types.SnapshotStatePending, StartTime: aws.Time(time.Now()),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	if _, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a"); err == nil {
		t.Fatal("createHomeVolume made an empty home while the real one was being captured")
	}
}

// --- golden snapshot (ADR 0045 決定 9) ---

func (f *fakeEC2) addGolden(id, pool, image string, state ec2types.SnapshotState, started time.Time) {
	f.snapshots[id] = &ec2types.Snapshot{
		SnapshotId: aws.String(id), State: state, StartTime: aws.Time(started),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagPool), Value: aws.String(pool)},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleGolden)},
			{Key: aws.String(ec2TagImage), Value: aws.String(image)},
		},
	}
}

func TestECSEC2GoldenSnapshotSeedsANewHome(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addGolden("snap-golden", "clu", h.rt.base.cfg.workspaceImage, ec2types.SnapshotStateCompleted, time.Now())

	vol, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	if got := aws.ToString(h.ec2.volumes[aws.ToString(vol.VolumeId)].SnapshotId); got != "snap-golden" {
		t.Errorf("new home built from %q, want the golden snapshot", got)
	}
	// The golden is shared by everyone. The post-restore cleanup must not touch it.
	h.runDeferred(ctx)
	if _, ok := h.ec2.snapshots["snap-golden"]; !ok {
		t.Fatal("the golden snapshot was deleted — every future user just lost their fast first start")
	}
}

// Re-baking is a manual step tied to a release, and forgetting it is invisible: only NEW
// users are affected, and only by getting old CLIs. So a mismatch is refused, not used.
func TestECSEC2StaleGoldenSnapshotIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addGolden("snap-old", "clu", "ecr/af-workspace:0.7.0", ec2types.SnapshotStateCompleted, time.Now())

	vol, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	if got := h.ec2.volumes[aws.ToString(vol.VolumeId)].SnapshotId; got != nil {
		t.Errorf("a golden baked from another image was used: %q", aws.ToString(got))
	}
}

// The user's own home always wins. Handing somebody the golden image because their real
// home was merely not found yet would be silent data loss.
func TestECSEC2HibernatedHomeBeatsTheGolden(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addGolden("snap-golden", "clu", h.rt.base.cfg.workspaceImage, ec2types.SnapshotStateCompleted, time.Now())
	h.ec2.snapshots["snap-mine"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-mine"), VolumeId: aws.String("vol-gone"),
		State: ec2types.SnapshotStateCompleted, StartTime: aws.Time(time.Now().Add(-time.Hour)),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagMembership), Value: aws.String("M-1")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
		},
	}
	vol, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	if got := aws.ToString(h.ec2.volumes[aws.ToString(vol.VolumeId)].SnapshotId); got != "snap-mine" {
		t.Errorf("built from %q, want the user's own hibernated home", got)
	}
	h.runDeferred(ctx)
	if _, ok := h.ec2.snapshots["snap-golden"]; !ok {
		t.Error("the restore cleanup deleted the shared golden snapshot")
	}
	if _, ok := h.ec2.snapshots["snap-mine"]; ok {
		t.Error("the user's superseded snapshot is still billing")
	}
}

// Destroy walks per-membership resources; the golden belongs to the pool, not to anyone.
func TestECSEC2DestroyLeavesTheGoldenAlone(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addGolden("snap-golden", "clu", h.rt.base.cfg.workspaceImage, ec2types.SnapshotStateCompleted, time.Now())
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	if _, err := h.rt.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := h.ec2.snapshots["snap-golden"]; !ok {
		t.Error("destroying one workspace took the pool's golden snapshot with it")
	}
}

// --- pool status for the admin UI (docs/log/64 §64.18.6) ---

// The screen exists to answer three questions no other runtime raises: how many boxes am
// I paying for, which are asleep, and whose home is where. A hibernated home has NO
// volume, so it is only visible if the snapshot is folded back in — otherwise the user
// simply disappears from the list, which reads as "their home is gone".
func TestECSEC2PoolStatus(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	f := h.factory()
	f.pool.hibernateAfter = 30 * 24 * time.Hour

	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-zzz", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ec2.setTag("vol-1", ec2TagIdleSince, time.Now().Add(-90*time.Minute).UTC().Format(time.RFC3339))
	// Carol's home is already a snapshot: the volume is gone.
	h.ec2.snapshots["snap-carol"] = &ec2types.Snapshot{
		SnapshotId: aws.String("snap-carol"), State: ec2types.SnapshotStateCompleted,
		StartTime: aws.Time(time.Now()),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagPool), Value: aws.String("clu")},
			{Key: aws.String(ec2TagRole), Value: aws.String(ec2RoleHome)},
			{Key: aws.String(ec2TagWorkspace), Value: aws.String("af-ws-acme-carol")},
		},
	}
	h.ec2.addGolden("snap-golden", "clu", "ecr/af-workspace:0.7.0", ec2types.SnapshotStateCompleted, time.Now())

	st, err := f.PoolStatus(ctx)
	if err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if st.Runtime != "ecs-ec2" || len(st.Slots) != 2 {
		t.Fatalf("runtime=%q slots=%d, want ecs-ec2 / 2", st.Runtime, len(st.Slots))
	}
	byID := map[string]ec2SlotView{}
	for _, s := range st.Slots {
		byID[s.InstanceID] = s
	}
	if got := byID["i-hot"]; got.State != "running" || got.Workspace != "af-ws-acme-alice" {
		t.Errorf("i-hot = %+v, want running and occupied", got)
	}
	if got := byID["i-zzz"]; got.State != "stopped" || got.Workspace != "" {
		t.Errorf("i-zzz = %+v, want a stopped, free slot", got)
	}
	if got := byID["i-hot"].IdleMinutes; got < 85 || got > 95 {
		t.Errorf("dormant minutes = %d, want ~90", got)
	}
	var alice, carol *ec2HomeView
	for i := range st.Homes {
		switch st.Homes[i].Workspace {
		case "af-ws-acme-alice":
			alice = &st.Homes[i]
		case "af-ws-acme-carol":
			carol = &st.Homes[i]
		}
	}
	if alice == nil || alice.AttachedTo != "i-hot" {
		t.Errorf("alice = %+v, want her home on i-hot", alice)
	}
	if carol == nil {
		t.Fatal("a hibernated home vanished from the list — it reads as 'their home is gone'")
	}
	if !carol.Hibernating || carol.SnapshotID != "snap-carol" || carol.VolumeID != "" {
		t.Errorf("carol = %+v, want a hibernated home with no volume", carol)
	}
	if !st.GoldenStale || st.GoldenImage != "ecr/af-workspace:0.7.0" {
		t.Errorf("golden = %q/%q stale=%v, want it reported as stale against %q",
			st.GoldenID, st.GoldenImage, st.GoldenStale, st.RunningImage)
	}
}

// Nothing else in the product has a pool, and an empty table on a Fargate deployment
// reads as "my slots all vanished".
func TestPoolStatusIsAbsentOnOtherRuntimes(t *testing.T) {
	m := &manager{rtFactory: &dockerFactory{}}
	if _, ok, err := m.poolStatus(context.Background()); ok || err != nil {
		t.Errorf("poolStatus on the docker runtime = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// --- AZ placement (docs/log/64 §64.20.4, ADR 0045「未解決 — AZ の選び方」を閉じる) ---

// countCalls counts the calls whose text starts with prefix — used where the POINT is how
// many times something happened, not that it happened.
func (f *fakeEC2) countCalls(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

var errNoCapacity = fmt.Errorf("api error InsufficientInstanceCapacity: We currently do not have sufficient m7i.large capacity in the Availability Zone you requested")

// One AZ running out of the slot type used to stop every NEW user in the deployment:
// anyAZ() is deterministic, so the one AZ the adapter ever picked was the one that had no
// room, and nothing tried anywhere else. Everybody already placed kept working, so it did
// not look like an outage.
func TestECSEC2NewHomeMovesToAnAZWithRoom(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.runErr["sub-1a"] = errNoCapacity // 1a is the AZ a new home would take

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol == nil {
		t.Fatalf("home volume: %v", err)
	}
	if got := aws.ToString(vol.AvailabilityZone); got != "ap-northeast-1c" {
		t.Errorf("the home was created in %q; 1a had no capacity, so it belongs in 1c", got)
	}
	// The whole reason the volume is created last: one attempt, one volume. Creating it
	// up front and deleting it to try elsewhere is what this replaced.
	if n := h.ec2.countCalls("CreateVolume"); n != 1 {
		t.Errorf("CreateVolume called %d times, want 1", n)
	}
	if n := h.ec2.countCalls("DeleteVolume"); n != 0 {
		t.Errorf("a home volume was deleted to retry (%d DeleteVolume calls) — on a restored "+
			"home that is the home, gone", n)
	}
}

// The other half of the same rule: an EBS home cannot move, so a slot in another AZ is
// useless to it. Wandering off would produce a slot nothing can ever attach to.
func TestECSEC2ExistingHomeNeverFollowsCapacityToAnotherAZ(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.runErr["sub-1a"] = errNoCapacity

	err := h.rt.Start(ctx)
	if err == nil {
		t.Fatal("Start succeeded although the home's own AZ had no capacity")
	}
	if !strings.Contains(err.Error(), "InsufficientInstanceCapacity") {
		t.Errorf("the capacity error was swallowed: %v", err)
	}
	for _, c := range h.ec2.calls {
		if strings.Contains(c, "subnet=sub-1c") {
			t.Errorf("a slot was launched in another AZ for a home pinned to 1a: %q", c)
		}
	}
}

// Only "no room here" is worth asking elsewhere. A bad launch template or an exhausted
// quota fails the same way in every AZ, and retrying buries the real message.
func TestECSEC2NonCapacityFailuresAreNotRetriedInEveryAZ(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	boom := fmt.Errorf("api error InvalidLaunchTemplateId.NotFound: the launch template does not exist")
	h.ec2.runErr["sub-1a"] = boom
	h.ec2.runErr["sub-1c"] = boom

	err := h.rt.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "InvalidLaunchTemplateId") {
		t.Fatalf("Start error = %v, want the launch template failure surfaced as-is", err)
	}
	if n := h.ec2.countCalls("RunInstances REFUSED"); n != 1 {
		t.Errorf("RunInstances was attempted %d times; a broken launch template is not an AZ problem", n)
	}
}

// No room ANYWHERE is a failure, and it must be a failure with nothing left behind: the
// home is not created, so there is no empty volume billing for a user who never started.
func TestECSEC2NoRoomAnywhereLeavesNoHomeBehind(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.runErr["sub-1a"] = errNoCapacity
	h.ec2.runErr["sub-1c"] = errNoCapacity

	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start succeeded with no capacity in any AZ")
	}
	if n := h.ec2.countCalls("CreateVolume"); n != 0 {
		t.Errorf("a home was created for a start that could not happen (%d CreateVolume calls)", n)
	}
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol != nil {
		t.Errorf("home volume = %v (err %v), want none", vol, err)
	}
}

// At the cap a new home has no AZ yet, so the victim should be the longest-dormant one in
// the WHOLE pool. It used to be the longest-dormant one in an AZ chosen before anybody
// looked — which could evict somebody who had been away ten minutes while leaving a
// week-old occupant of the other AZ alone.
func TestECSEC2EvictionForANewHomeLooksAtEveryAZ(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 2
	h.ec2.addSlot("i-1a", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-1c", "ap-northeast-1c", "m7i.large", true, false)
	h.ci.registered["i-1a"], h.ci.registered["i-1c"] = true, true
	// Both slots are taken. The 1c occupant has been gone far longer.
	h.ec2.addHomeVolume("vol-a", "M-A", "af-ws-acme-a", "ap-northeast-1a")
	h.ec2.attach("vol-a", "i-1a", time.Now())
	h.ec2.setTag("vol-a", ec2TagIdleSince, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	h.ec2.addHomeVolume("vol-c", "M-C", "af-ws-acme-c", "ap-northeast-1c")
	h.ec2.attach("vol-c", "i-1c", time.Now())
	h.ec2.setTag("vol-c", ec2TagIdleSince, time.Now().Add(-7*24*time.Hour).UTC().Format(time.RFC3339))

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol == nil {
		t.Fatalf("home volume: %v", err)
	}
	if got := aws.ToString(vol.AvailabilityZone); got != "ap-northeast-1c" {
		t.Errorf("the new home landed in %q; the week-old occupant is in 1c", got)
	}
	if attachedInstance(h.ec2.volumes["vol-a"]) == "" {
		t.Error("evicted the occupant who had been away ten minutes instead of the one away a week")
	}
}

// Moving a user to another AZ. An EBS home cannot be moved and the adapter has no
// "move" operation — but it does not need one: hibernation turns the home into a
// snapshot, and a snapshot has no AZ. The next Start rebuilds it wherever a slot is,
// which is the whole runbook (docs/log/64 §64.20.7).
//
// This is a real path with real consequences, so it is pinned: the home has to come back
// FROM THE SNAPSHOT (not as a fresh empty volume) and in the NEW AZ.
func TestECSEC2HibernateThenStartMovesTheHomeToAnotherAZ(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour) // home on vol-1, slot i-sleep, both in 1a

	// Step 1–3 of the runbook: the workspace is already stopped; put the home away.
	live := func() { // the sweeper finishes what BeginHibernate starts
		if err := h.factory().sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	}
	if err := h.rt.BeginHibernate(ctx); err != nil {
		t.Fatalf("BeginHibernate: %v", err)
	}
	live()
	if v, _ := h.rt.homeVolume(ctx); v != nil {
		t.Fatalf("the home is still a volume in %s", aws.ToString(v.AvailabilityZone))
	}

	// The old AZ has nothing free any more; the only slot is in the other one.
	h.ec2.instances["i-sleep"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated}
	h.ec2.addSlot("i-elsewhere", "ap-northeast-1c", "m7i.large", true, false)
	h.ci.registered["i-elsewhere"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start after hibernation: %v", err)
	}
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol == nil {
		t.Fatalf("home volume after the move: %v", err)
	}
	if got := aws.ToString(vol.AvailabilityZone); got != "ap-northeast-1c" {
		t.Errorf("the home came back in %q, want the AZ the free slot is in", got)
	}
	if aws.ToString(vol.SnapshotId) == "" {
		t.Error("the home came back EMPTY — a move that loses the contents is not a move")
	}
}

// An AZ having a bad day is exactly when the reaper's hibernation step fails: releaseSlot
// unmounts over SSM, and the slot is unreachable. What must NOT happen is that the mark
// stays behind — a home that reads as "hibernating" while it is attached and fine, an
// error that repeats every sweep with no way to tell it from progress, and a stale
// timestamp that the first snapshot taken after the outage would be judged against.
func TestECSEC2HibernateTakesItsMarkBackWhenTheSlotIsUnreachable(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour)
	// The shape an AZ outage has: EC2 still says the instance is running, so releaseSlot
	// insists on unmounting first (a detach of a mounted filesystem is how a home gets
	// corrupted) — and nothing answers over SSM. A STOPPED slot would not reproduce this:
	// there is nothing to unmount and the release goes straight through.
	h.ec2.instances["i-sleep"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ssmc.fail["umount"] = true

	err := h.rt.BeginHibernate(ctx)
	if err == nil {
		t.Fatal("BeginHibernate reported success although the home could not be released")
	}
	vol := h.ec2.volumes["vol-1"]
	if mark := ec2TagValue(vol.Tags, ec2TagHibernating); mark != "" {
		t.Errorf("the hibernation mark %q was left on a home that is still attached", mark)
	}
	if attachedInstance(vol) == "" {
		t.Error("the home was detached even though the unmount failed — that is how a home gets corrupted")
	}
	if len(h.ec2.snapshots) != 0 {
		t.Errorf("a capture was started without releasing the slot: %v", h.ec2.snapshots)
	}

	// And when the AZ comes back, the next pass starts cleanly rather than resuming a
	// hibernation that never began.
	delete(h.ssmc.fail, "umount")
	if err := h.rt.BeginHibernate(ctx); err != nil {
		t.Fatalf("BeginHibernate after the slot came back: %v", err)
	}
	if len(h.ec2.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(h.ec2.snapshots))
	}
	for _, s := range h.ec2.snapshots {
		mark, _ := time.Parse(time.RFC3339, ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHibernating))
		if s.StartTime != nil && s.StartTime.Before(mark) {
			t.Error("the capture predates its own mark — it would be dropped as superseded forever")
		}
	}
}

// A mark that was already there belongs to a hibernation that is genuinely under way, and
// the snapshot it validates may already exist. A later failure must not remove it.
func TestECSEC2HibernateKeepsAMarkItDidNotWrite(t *testing.T) {
	ctx := context.Background()
	h := hibernateHarness(t, 60*24*time.Hour)
	earlier := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	h.ec2.setTag("vol-1", ec2TagHibernating, earlier)
	h.ec2.instances["i-sleep"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ssmc.fail["umount"] = true

	if err := h.rt.hibernate(ctx); err == nil {
		t.Fatal("hibernate reported success although the slot could not be released")
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagHibernating); got != earlier {
		t.Errorf("mark = %q, want the existing %q — a hibernation in flight lost its timestamp", got, earlier)
	}
}

// New homes used to follow one deterministic first choice, so everybody ended up in the
// same AZ and losing it took out the whole deployment rather than half of it. An EBS home
// cannot be evacuated, so the only lever is not putting everyone in one place (docs/log/64
// §64.21).
func TestECSEC2NewSlotsSpreadAcrossAZs(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	// Two homes already in 1a and none in 1c, and no free slot to reuse.
	h.ec2.addHomeVolume("vol-x", "M-X", "af-ws-acme-x", "ap-northeast-1a")
	h.ec2.addHomeVolume("vol-y", "M-Y", "af-ws-acme-y", "ap-northeast-1a")
	h.ec2.addSlot("i-x", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addSlot("i-y", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-x", "i-x", time.Now())
	h.ec2.attach("vol-y", "i-y", time.Now())

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	vol, err := h.rt.homeVolume(ctx)
	if err != nil || vol == nil {
		t.Fatalf("home volume: %v", err)
	}
	if got := aws.ToString(vol.AvailabilityZone); got != "ap-northeast-1c" {
		t.Errorf("the new home went to %q; 1a already holds two and 1c none", got)
	}
}

// Spreading decides where a NEW slot goes. It must never talk anybody out of reusing a
// free one: that preference is what keeps the pool small, and a home can only ever attach
// to a slot in its own AZ anyway.
func TestECSEC2SpreadingNeverBeatsAFreeSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-x", "M-X", "af-ws-acme-x", "ap-northeast-1a")
	h.ec2.addSlot("i-busy", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-x", "i-busy", time.Now())
	// 1a holds the only home AND the only free slot. Balance says 1c, reuse says 1a.
	h.ec2.addSlot("i-free", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-free"] = true

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	vol, _ := h.rt.homeVolume(ctx)
	if got := aws.ToString(vol.AvailabilityZone); got != "ap-northeast-1a" {
		t.Errorf("the home was created in %q instead of on the free slot in 1a", got)
	}
	if n := h.ec2.countCalls("RunInstances"); n != 0 {
		t.Errorf("grew the pool (%d RunInstances) with a free slot sitting there", n)
	}
}

// Balancing is an optimisation. If the count cannot be read, placement still has to
// happen — on the fixed order, not on an error.
func TestECSEC2SpreadingFallsBackToTheFixedOrder(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.describeVolumesErr = fmt.Errorf("api error RequestLimitExceeded")
	azs, err := h.rt.spreadAZs(ctx)
	if err != nil {
		t.Fatalf("spreadAZs surfaced a counting failure as a placement failure: %v", err)
	}
	if len(azs) != 2 || azs[0] != "ap-northeast-1a" {
		t.Errorf("AZ order = %v, want the fixed poolAZs order", azs)
	}
}

// --- periodic backups (ADR 0045 決定 17) ---

func backupHarness(t *testing.T) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.rt.pool.backupKeep = 2
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.attach("vol-1", "i-hot", time.Now())
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	return h
}

// A home is in one AZ and cannot be evacuated, so the copy has to be taken BEFORE the bad
// day — while the workspace is running, mounted and in use. That is the whole feature.
func TestECSEC2BacksUpAHomeThatIsInUse(t *testing.T) {
	ctx := context.Background()
	h := backupHarness(t)

	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("BackupHome: %v", err)
	}
	if len(h.ec2.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(h.ec2.snapshots))
	}
	for _, s := range h.ec2.snapshots {
		if got := ec2TagValue(s.Tags, ec2TagRole); got != ec2RoleBackup {
			t.Errorf("backup tagged af-role=%q; a backup that looks like a home would be "+
				"restored from, or deleted as a superseded capture", got)
		}
		if ec2TagValue(s.Tags, ec2TagBackupAt) == "" {
			t.Error("no af-backup-at: the next sweep cannot tell whether one is due")
		}
	}
	// Not detached, not unmounted, nobody disturbed.
	if attachedInstance(h.ec2.volumes["vol-1"]) != "i-hot" {
		t.Error("the home left its slot to be backed up")
	}
	for _, c := range h.ssmc.commands {
		if strings.Contains(c, "umount") {
			t.Error("the home was unmounted for a backup — that is taking a working person's desk away on a timer")
		}
	}
}

// The schedule is read from AWS, not from anything the CP remembers, so a restart or a
// second replica cannot double the bill.
func TestECSEC2BackupWaitsForTheInterval(t *testing.T) {
	ctx := context.Background()
	h := backupHarness(t)
	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	for _, s := range h.ec2.snapshots {
		s.State = ec2types.SnapshotStateCompleted
	}
	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n := len(h.ec2.snapshots); n != 1 {
		t.Errorf("snapshots = %d; a second copy was taken minutes after the first", n)
	}
	// A day later it is due again.
	base := time.Now()
	h.rt.now = func() time.Time { return base.Add(25 * time.Hour) }
	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if n := len(h.ec2.snapshots); n != 2 {
		t.Errorf("snapshots = %d, want 2 once the interval had passed", n)
	}
}

// One capture at a time: a 45 GiB home takes 30–40 minutes, and stacking them pays for
// the same home several times over.
func TestECSEC2BackupDoesNotStackOnAPendingOne(t *testing.T) {
	ctx := context.Background()
	h := backupHarness(t)
	h.ec2.snapshotState = ec2types.SnapshotStatePending
	if err := h.rt.BackupHome(ctx, time.Hour); err != nil {
		t.Fatalf("first: %v", err)
	}
	base := time.Now()
	h.rt.now = func() time.Time { return base.Add(48 * time.Hour) } // long overdue
	if err := h.rt.BackupHome(ctx, time.Hour); err != nil {
		t.Fatalf("second: %v", err)
	}
	if n := len(h.ec2.snapshots); n != 1 {
		t.Errorf("snapshots = %d; a second capture started while the first was still running", n)
	}
}

// Retention: keep N completed copies. Pruning a PENDING one throws away work already paid
// for, and dropping below N while a replacement is still running leaves a window with
// fewer copies than the operator asked for.
func TestECSEC2BackupRetention(t *testing.T) {
	ctx := context.Background()
	h := backupHarness(t)
	base := time.Now()
	for i := 0; i < 3; i++ {
		h.rt.now = func() time.Time { return base.Add(time.Duration(i) * 25 * time.Hour) }
		if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		for _, s := range h.ec2.snapshots {
			s.State = ec2types.SnapshotStateCompleted
		}
	}
	if n := len(h.ec2.snapshots); n != 2 {
		t.Errorf("kept %d copies, want backupKeep=2", n)
	}

	// A pending fourth must not push a completed one out before it is usable.
	h.ec2.snapshotState = ec2types.SnapshotStatePending
	h.rt.now = func() time.Time { return base.Add(100 * time.Hour) }
	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("fourth: %v", err)
	}
	completed := 0
	for _, s := range h.ec2.snapshots {
		if s.State == ec2types.SnapshotStateCompleted {
			completed++
		}
	}
	if completed != 2 {
		t.Errorf("completed copies = %d while a replacement was still running, want 2", completed)
	}
}

// The three kinds of snapshot must never be mistaken for one another. A backup that a
// restore picked up would hand somebody an older home in silence; one that hibernation
// counted would delete a volume that had not been captured.
func TestECSEC2BackupsAreInvisibleToRestoreAndHibernation(t *testing.T) {
	ctx := context.Background()
	h := backupHarness(t)
	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("BackupHome: %v", err)
	}
	for _, s := range h.ec2.snapshots {
		s.State = ec2types.SnapshotStateCompleted
	}
	if got, err := h.rt.restoreSnapshot(ctx); err != nil || got != "" {
		t.Errorf("restoreSnapshot picked up a backup (%q, err %v) — that is a silently older home", got, err)
	}
	snaps, err := h.rt.homeSnapshots(ctx)
	if err != nil || len(snaps) != 0 {
		t.Errorf("homeSnapshots saw %d backups; hibernation would treat one as its own capture", len(snaps))
	}
	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Errorf("goldenSnapshot picked up a backup: %q", got)
	}
}

// A backup outlives the home on purpose — but not the person. Destroy has to take them,
// or an offboarded member keeps billing forever with nothing pointing at it.
func TestECSEC2DestroyTakesTheBackupsToo(t *testing.T) {
	ctx := context.Background()
	h := backupHarness(t)
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	if err := h.rt.BackupHome(ctx, 24*time.Hour); err != nil {
		t.Fatalf("BackupHome: %v", err)
	}
	if _, err := h.rt.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if n := len(h.ec2.snapshots); n != 0 {
		t.Errorf("%d snapshot(s) survived the workspace being destroyed", n)
	}
}

// Two cases where taking a copy is wrong rather than merely unnecessary.
func TestECSEC2BackupSkipsWhatItShould(t *testing.T) {
	ctx := context.Background()

	t.Run("a home that is being hibernated", func(t *testing.T) {
		h := backupHarness(t)
		h.ec2.setTag("vol-1", ec2TagHibernating, time.Now().UTC().Format(time.RFC3339Nano))
		if err := h.rt.BackupHome(ctx, time.Hour); err != nil {
			t.Fatalf("BackupHome: %v", err)
		}
		if len(h.ec2.snapshots) != 0 {
			t.Error("backed up a volume that is being captured and deleted — paying twice for one moment")
		}
	})

	t.Run("a home that is already a snapshot", func(t *testing.T) {
		h := newEC2Harness(t) // no volume at all
		if err := h.rt.BackupHome(ctx, time.Hour); err != nil {
			t.Fatalf("BackupHome: %v", err)
		}
		if len(h.ec2.snapshots) != 0 {
			t.Error("took a backup with no home volume to back up")
		}
	})

	t.Run("backups turned off", func(t *testing.T) {
		h := backupHarness(t)
		if err := h.rt.BackupHome(ctx, 0); err != nil {
			t.Fatalf("BackupHome: %v", err)
		}
		if len(h.ec2.snapshots) != 0 {
			t.Error("took a backup although the tenant asked for none")
		}
	})
}

func TestECSEC2QuarantinesASlotThatCannotMount(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-bad"] = true
	h.ssmc.fail["af-mount"] = true

	err := h.rt.Start(ctx)
	if err == nil {
		t.Fatal("Start returned nil although the home could not be mounted")
	}
	bad := h.ec2.instances["i-bad"]
	if got := ec2TagValue(bad.Tags, ec2TagRole); got != ec2RoleQuarantined {
		t.Errorf("slot af-role = %q, want %q — a slot left tagged `slot` is offered again", got, ec2RoleQuarantined)
	}
	if ec2TagValue(bad.Tags, ec2TagQuarantineReason) == "" || ec2TagValue(bad.Tags, ec2TagQuarantineAt) == "" {
		t.Errorf("quarantine left no reason/time on %s: %v", "i-bad", bad.Tags)
	}
	if bad.State == nil || bad.State.Name != ec2types.InstanceStateNameStopped {
		t.Errorf("a quarantined slot must be stopped (it cannot run tasks and bills by the hour), state = %v", bad.State)
	}
	if inst := attachedInstance(h.ec2.volumes["vol-1"]); inst != "" {
		t.Errorf("the home is still attached to %s; it has to be free to go somewhere that works", inst)
	}
	if ec2TagValue(h.ec2.volumes["vol-1"].Tags, ec2TagClaim) != "" {
		t.Error("the claim survived the failure, so the owner would wait out the claim TTL before retrying")
	}
}

// …and the very next Start must go somewhere else. Quarantine that only relabels the box
// would be cosmetic; what matters is that placement stops choosing it.
func TestECSEC2StartAfterQuarantineGrowsAFreshSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-bad"] = true
	h.ssmc.fail["af-mount"] = true
	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("the first Start must fail; the mount does")
	}

	// The box is repaired for nobody: the mount still fails on it. The second Start has
	// to create a slot instead of reusing the quarantined one.
	delete(h.ssmc.fail, "af-mount")
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if h.ec2.instances["i-new1"] == nil {
		t.Fatalf("the second Start did not create a slot; instances = %v", h.ec2.calls)
	}
	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ci.registered["i-new1"] = true
	h.runDeferred(ctx)
	got := attachedInstance(h.ec2.volumes["vol-1"])
	if got == "i-bad" {
		t.Fatal("the second Start landed on the quarantined slot again")
	}
	if got == "" {
		t.Fatal("the second Start attached the home nowhere")
	}
	if n := len(h.ec2.instances); n != 2 {
		t.Errorf("pool holds %d instances, want 2 (the quarantined one plus a fresh slot)", n)
	}
}

// A quarantined box still bills. It is out of the pool for placement but must stay on the
// operator's screen, with the reason — otherwise the only symptom is a bill.
func TestECSEC2PoolStatusShowsQuarantinedSlots(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-bad"] = true
	h.ssmc.fail["af-mount"] = true
	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("the mount fails, so Start must")
	}

	st, err := h.factory().PoolStatus(ctx)
	if err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	var seen *ec2SlotView
	for i := range st.Slots {
		if st.Slots[i].InstanceID == "i-bad" {
			seen = &st.Slots[i]
		}
	}
	if seen == nil {
		t.Fatalf("the quarantined slot vanished from the pool screen while still billing: %+v", st.Slots)
	}
	if !seen.Quarantined || seen.QuarantineReason == "" {
		t.Errorf("slot view = %+v, want quarantined with a reason", *seen)
	}
}

// A destroyed home lingers in `deleting` for a while — measured at ~40 minutes on a
// volume whose slot had wedged. Until then the pool screen listed it as a home, so a
// workspace that had been destroyed still looked present to the operator.
func TestECSEC2PoolStatusHidesVolumesBeingDeleted(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-live", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	gone := h.ec2.addHomeVolume("vol-gone", "M-2", "af-ws-acme-bob", "ap-northeast-1a")
	gone.State = ec2types.VolumeStateDeleting

	st, err := h.factory().PoolStatus(ctx)
	if err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	for _, home := range st.Homes {
		if home.VolumeID == "vol-gone" {
			t.Fatalf("a volume EC2 is deleting is still listed as a home: %+v", home)
		}
	}
	if len(st.Homes) != 1 || st.Homes[0].VolumeID != "vol-live" {
		t.Errorf("homes = %+v, want just vol-live", st.Homes)
	}
}

// --- how far the bake has got (docs/log/64 §64.30) ---

// The half of a bake that produces no snapshot — the seed's slot, boot-install, the
// slot release — is about 6 of its 11 minutes, and the screen used to call all of it
// "there is no golden". An operator looking at a slow first start in that window is
// told the opposite of what is happening.
func TestECSEC2PoolStatusShowsTheBakeInFlight(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	f := h.factory()
	seedWS := "af-ws-af-golden-af-golden-seed"
	h.ec2.addSlot("i-seed", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addHomeVolume("vol-seed", "M-SEED", seedWS, "ap-northeast-1a").CreateTime =
		aws.Time(time.Now().Add(-4 * time.Minute)) // the anchor the baker's own deadline uses
	h.ec2.attach("vol-seed", "i-seed", time.Now())

	// 1. The seed is up and boot-install is running on its home.
	st, err := f.PoolStatus(ctx)
	if err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	g := st.Goldens[0]
	if g.Phase != ec2BakePhaseBoot || !st.Baking {
		t.Fatalf("phase = %q baking=%v, want %q while boot-install runs", g.Phase, st.Baking, ec2BakePhaseBoot)
	}
	// The seed holds a slot. Without saying which workspace that is, the pool table
	// shows a box occupied by a name nothing on the screen accounts for.
	if g.Seed == nil || g.Seed.Workspace != seedWS || g.Seed.InstanceID != "i-seed" {
		t.Errorf("seed = %+v, want %s on i-seed", g.Seed, seedWS)
	}
	if g.PhaseSince == "" {
		t.Error("no anchor for the elapsed time; the screen cannot say how long this has been going")
	}

	// 2. boot-install finished: the home is being taken off its slot for the capture.
	h.ec2.setTag("vol-seed", ec2TagBakeReady, time.Now().UTC().Format(time.RFC3339))
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if got := st.Goldens[0].Phase; got != ec2BakePhaseCapture {
		t.Errorf("phase = %q, want %q once af-bake-ready is on the home", got, ec2BakePhaseCapture)
	}

	// 3. EBS is copying the candidate. The percentage is EBS's own — a snapshot of a
	//    50 GiB home took ~3 minutes on the live deployment, and "pending" alone does
	//    not tell an operator whether to wait or to go looking.
	h.ec2.addGoldenRole("snap-cand", "clu", h.rt.base.cfg.workspaceImage, ec2RoleGoldenCandidate,
		ec2types.SnapshotStatePending, time.Now())
	h.ec2.snapshots["snap-cand"].Progress = aws.String("63%")
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if g = st.Goldens[0]; g.Phase != ec2BakePhaseSnapshot || g.Candidate != "snap-cand" || g.Progress != 63 {
		t.Errorf("golden = %+v, want the candidate snapshot at 63%%", g)
	}

	// 4. Copied. Nothing is published yet — a probe has to come up from it first, and
	//    that is the step §64.28.3 exists for.
	h.ec2.snapshots["snap-cand"].State = ec2types.SnapshotStateCompleted
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if g = st.Goldens[0]; g.Phase != ec2BakePhaseProbe || g.SnapshotID != "" {
		t.Errorf("golden = %+v, want the probe phase and NOTHING published yet", g)
	}

	// 5. Published.
	h.ec2.addGolden("snap-golden", "clu", h.rt.base.cfg.workspaceImage, ec2types.SnapshotStateCompleted, time.Now())
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if g = st.Goldens[0]; g.Phase != ec2BakePhasePublished || st.Baking {
		t.Errorf("golden = %+v baking=%v, want it reported as in use", g, st.Baking)
	}
}

// "No golden and nothing happening" has three different causes and only one of them
// fixes itself. All three lived in a CP log line that scrolls away — and the pool being
// full is the one that actually stopped a bake on a live deployment (the production deployment).
func TestECSEC2PoolStatusExplainsWhyNothingIsBaking(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	f := h.factory()
	img := h.rt.base.cfg.workspaceImage

	// Nothing in the way: the next tick starts a bake.
	st, err := f.PoolStatus(ctx)
	if err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if got := st.Goldens[0].Phase; got != ec2BakePhaseIdle {
		t.Errorf("phase = %q on an empty pool, want %q", got, ec2BakePhaseIdle)
	}

	// 3 of 4 slots taken. A bake needs two free, so it will not start — and the same
	// arithmetic the baker applies has to be what the screen reports.
	for _, id := range []string{"i-1", "i-2", "i-3"} {
		h.ec2.addSlot(id, "ap-northeast-1a", "m7i.large", true, false)
	}
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if g := st.Goldens[0]; g.Phase != ec2BakePhaseBlocked || g.SlotsInUse != 3 {
		t.Errorf("golden = %+v, want blocked with 3 slots in use", g)
	}
	blocked, _, err := f.bakeBlocked(ctx)
	if err != nil || !blocked {
		t.Errorf("bakeBlocked = (%v, %v), want the baker to agree with the screen", blocked, err)
	}

	// One candidate burned. The baker has one attempt left, so this is not the end.
	h.ec2.addGoldenRole("snap-bad1", "clu", img, ec2RoleGoldenRejected, ec2types.SnapshotStateCompleted, time.Now())
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if g := st.Goldens[0]; g.Phase != ec2BakePhaseRejected || g.Attempts != 1 {
		t.Errorf("golden = %+v, want one rejected attempt", g)
	}

	// Two. The baker stops trying, and an operator who is not told that waits forever
	// for a bake that is never coming.
	h.ec2.addGoldenRole("snap-bad2", "clu", img, ec2RoleGoldenRejected, ec2types.SnapshotStateCompleted, time.Now())
	if st, err = f.PoolStatus(ctx); err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if g := st.Goldens[0]; g.Phase != ec2BakePhaseGaveUp || g.Attempts != 2 {
		t.Errorf("golden = %+v, want the baker reported as having given up", g)
	}
	if n, err := f.rejectedAttempts(ctx, ec2ArchX86); err != nil || n != 2 {
		t.Errorf("rejectedAttempts = (%d, %v), want the baker to agree with the screen", n, err)
	}
}

// AF_ECS_EC2_GOLDEN_AUTOBAKE=0 is the one thing about the golden that is not visible in
// AWS. Left unsaid, "no golden, nothing under way" reads as "wait a few minutes".
func TestPoolStatusSaysWhenAutoBakeIsOff(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	// store は poolStatus が Σ(tenant max_workspaces) を突き合わせるのに要る（決定 25）。
	// テナントが 0 件でも「読めた」ことは要る——読めなければ突き合わせを載せない、が
	// 正しい振る舞いで、それは「読む先が無い」とは別である。
	m := &manager{rtFactory: h.factory(), autoBakeGolden: false, store: p3Store(t)}
	st, ok, err := m.poolStatus(ctx)
	if !ok || err != nil {
		t.Fatalf("poolStatus = (ok=%v, err=%v)", ok, err)
	}
	if st.AutoBake || st.Goldens[0].Phase != ec2BakePhaseOff {
		t.Errorf("auto_bake=%v phase=%q, want the screen to say the baker is switched off",
			st.AutoBake, st.Goldens[0].Phase)
	}
	m.autoBakeGolden = true
	if st, _, err = m.poolStatus(ctx); err != nil {
		t.Fatalf("poolStatus: %v", err)
	}
	if !st.AutoBake || st.Goldens[0].Phase != ec2BakePhaseIdle {
		t.Errorf("auto_bake=%v phase=%q, want a bake to be expected", st.AutoBake, st.Goldens[0].Phase)
	}
}

// What the "starting" dialog reads. The infrastructure half of a start on this runtime
// (a new slot, a new or restored home, an SSM mount) used to be invisible to the
// Console, which could only offer the native runtime's line about installing agent
// CLIs — the wrong wait, and the one an operator judges "stuck" against.
func TestECSEC2PublishesItsProvisioningPhase(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

	// No slot exists: Start grows one and hands the rest to the background half, so the
	// phase has to survive Start returning — the Console polls after that, not during.
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.rt.BootPhase(); got == "" {
		t.Fatal("no phase while the start is still converging; the dialog has nothing to say")
	} else if !strings.HasPrefix(got, "slot:") && !strings.HasPrefix(got, "home:") {
		t.Errorf("phase = %q, want the slot/home work that is actually happening", got)
	}

	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ci.registered["i-new1"] = true
	h.runDeferred(ctx)
	// Cleared on the way out — the dialog stays open for as long as a phase is reported,
	// so a leftover one is a dialog that never closes.
	if got := h.rt.BootPhase(); got != "" {
		t.Errorf("phase %q survived a converged start", got)
	}
}

// …and a start that fails must clear it too, or the workspace looks like it is still
// coming up forever.
func TestECSEC2ClearsThePhaseWhenTheStartFails(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.ssmc.fail["af-mount"] = true

	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start returned nil although the mount failed")
	}
	if got := h.rt.BootPhase(); got != "" {
		t.Errorf("phase %q survived a failed start", got)
	}
}

// The vCPU field is display-only and OPTIONAL, so a ladder written before it existed
// must parse exactly as it did — and a malformed vCPU must cost only the label, never
// the rung. A rung silently dropping out of the ladder would change PLACEMENT.
func TestECSEC2ParseSlotSizesOptionalVCPU(t *testing.T) {
	got := parseSlotSizes("m7i.large:8192:2, m7i.xlarge:16384, m7i.2xlarge:32768:oops")
	if len(got) != 3 {
		t.Fatalf("parseSlotSizes = %+v, want all three rungs", got)
	}
	if got[0].instanceType != "m7i.large" || got[0].vcpu != 2 {
		t.Errorf("declared vCPU lost: %+v", got[0])
	}
	if got[1].vcpu != 0 || got[2].vcpu != 0 {
		t.Errorf("absent or malformed vCPU must be 0, got %+v / %+v", got[1], got[2])
	}
	if got[2].memMiB != 32768 {
		t.Errorf("a bad vCPU dropped the rung: %+v", got[2])
	}
}

// The Console asks the runtime what the three axes mean rather than assuming Fargate
// (ADR 0045 決定 21). On this one, CPU is not used at all, memory picks a box and the
// disk number is the PERSISTENT home — the opposite of what the UI used to say.
func TestECSEC2SizingProfile(t *testing.T) {
	f := &ecsEC2Factory{pool: ec2PoolConfig{
		classes:      parseSlotClasses("m7i.large:8192:2,m7i.xlarge:16384:4"),
		defaultClass: "default",
		homeGiB:      50,
	}}
	p := f.SizingProfile()
	if p.Runtime != "ecs-ec2" || p.CPUEffective {
		t.Fatalf("want ecs-ec2 with no effective CPU axis, got %+v", p)
	}
	if p.MemMeaning != memMeaningSlot || p.DiskMeaning != diskMeaningHome {
		t.Errorf("axis meanings wrong: %+v", p)
	}
	if !p.DiskCreateOnly || p.DiskDefaultGB != 50 {
		t.Errorf("home size is honoured only at creation and defaults to the pool value: %+v", p)
	}
	if len(p.Slots) != 2 || p.Slots[0].InstanceType != "m7i.large" || p.Slots[0].VCPU != 2 {
		t.Errorf("ladder not reported: %+v", p.Slots)
	}
}

// --- golden candidates (ADR 0045 決定 9-1) ---

func (f *fakeEC2) addGoldenRole(id, pool, image, role string, state ec2types.SnapshotState, started time.Time) {
	f.snapshots[id] = &ec2types.Snapshot{
		SnapshotId: aws.String(id), State: state, StartTime: aws.Time(started),
		Tags: []ec2types.Tag{
			{Key: aws.String(ec2TagPool), Value: aws.String(pool)},
			{Key: aws.String(ec2TagRole), Value: aws.String(role)},
			{Key: aws.String(ec2TagImage), Value: aws.String(image)},
		},
	}
}

// The property the whole verification phase rests on: an unpublished candidate is
// reachable ONLY by something that has declared itself a probe. If an ordinary Start
// could pick one up, a golden that cannot boot would reach real users before anything
// had tried to boot it — which is the failure §64.28.3 was.
func TestECSEC2GoldenCandidateIsInvisibleToOrdinaryStarts(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addGoldenRole("snap-cand", "clu", h.rt.base.cfg.workspaceImage,
		ec2RoleGoldenCandidate, ec2types.SnapshotStateCompleted, time.Now())

	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Fatalf("an ordinary workspace found the unpublished candidate %q", got)
	}
	vol, err := h.rt.createHomeVolume(ctx, "ap-northeast-1a")
	if err != nil {
		t.Fatalf("createHomeVolume: %v", err)
	}
	if got := aws.ToString(h.ec2.volumes[aws.ToString(vol.VolumeId)].SnapshotId); got != "" {
		t.Fatalf("a new home was seeded from the unverified candidate %q", got)
	}
}

func TestECSEC2ProbeReadsTheCandidateAndNotTheGolden(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	img := h.rt.base.cfg.workspaceImage
	h.ec2.addGolden("snap-published", "clu", img, ec2types.SnapshotStateCompleted, time.Now())
	h.ec2.addGoldenRole("snap-cand", "clu", img, ec2RoleGoldenCandidate, ec2types.SnapshotStateCompleted, time.Now())

	h.rt.seedFromCandidate()
	if got := h.rt.goldenSnapshot(ctx); got != "snap-cand" {
		t.Fatalf("the probe read %q — it has to test the CANDIDATE or it proves nothing", got)
	}
}

func (f *fakeEC2) addGoldenArch(id, pool, image, role, arch string, started time.Time) {
	f.addGoldenRole(id, pool, image, role, ec2types.SnapshotStateCompleted, started)
	f.snapshots[id].Tags = append(f.snapshots[id].Tags,
		ec2types.Tag{Key: aws.String(ec2TagArch), Value: aws.String(arch)})
}

// Every arch's golden carries the SAME image stamp, so once a second arch is declared
// the image filter stops discriminating and the tie-break is "newest wins" — a coin
// toss decided by which bake happened to finish last.
//
// Measured on the dev deployment (docs/log/70 §70.14.5): baking x86_64 and arm64 together, the x86_64
// probe was seeded from the arm64 candidate. It did not fail — §70.5's self-heal wipes
// the wrong-arch bits and re-runs boot-install — which is what makes it worth a test:
// the golden's entire purpose is thrown away silently, and the probe proves the wrong
// snapshot.
func TestECSEC2GoldenOfAnotherArchIsNotUsed(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	img := h.rt.base.cfg.workspaceImage
	if archOrX86(h.rt.arch) != ec2ArchX86 {
		t.Fatalf("harness arch is %q; this test is written from the x86_64 side", h.rt.arch)
	}
	// The arm64 one is NEWER — so "newest wins" would pick it, and did.
	h.ec2.addGoldenArch("snap-x86", "clu", img, ec2RoleGolden, ec2ArchX86, time.Now().Add(-time.Minute))
	h.ec2.addGoldenArch("snap-arm", "clu", img, ec2RoleGolden, ec2ArchArm, time.Now())

	if got := h.rt.goldenSnapshot(ctx); got != "snap-x86" {
		t.Fatalf("an x86_64 home was seeded from %q — the arm64 golden is not ours", got)
	}

	// The same has to hold for the probe, or a golden is "proven" by booting another
	// architecture's snapshot (§64.28.3 checks nothing in that case).
	h.ec2.addGoldenArch("snap-cand-arm", "clu", img, ec2RoleGoldenCandidate, ec2ArchArm, time.Now())
	h.rt.seedFromCandidate()
	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Fatalf("the x86_64 probe read the arm64 candidate %q", got)
	}
	h.ec2.addGoldenArch("snap-cand-x86", "clu", img, ec2RoleGoldenCandidate, ec2ArchX86, time.Now())
	if got := h.rt.goldenSnapshot(ctx); got != "snap-cand-x86" {
		t.Fatalf("the probe read %q, not its own arch's candidate", got)
	}
}

// An untagged golden is x86_64 (docs/log/70 §70.6): deployments that baked one before
// classes existed must keep working, and reading them as "unknown" would orphan every
// existing golden on upgrade.
func TestECSEC2UntaggedGoldenIsX86(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	img := h.rt.base.cfg.workspaceImage
	h.ec2.addGolden("snap-legacy", "clu", img, ec2types.SnapshotStateCompleted, time.Now())

	if got := h.rt.goldenSnapshot(ctx); got != "snap-legacy" {
		t.Fatalf("the pre-classes golden stopped being found: %q", got)
	}
	h.rt.arch = ec2ArchArm
	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Fatalf("an arm64 home was seeded from the untagged (=x86_64) golden %q", got)
	}
}

// A candidate stamped with another image is the baker's own bookkeeping (the image moved
// mid-bake), not the standing "somebody must re-bake" warning a stale GOLDEN is.
func TestECSEC2StaleCandidateIsNotUsed(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addGoldenRole("snap-old", "clu", "some/other:image",
		ec2RoleGoldenCandidate, ec2types.SnapshotStateCompleted, time.Now())

	h.rt.seedFromCandidate()
	if got := h.rt.goldenSnapshot(ctx); got != "" {
		t.Fatalf("the probe used a candidate baked from another image: %q", got)
	}
}

// --- 配置できない起動が「なぜ」を言う（docs/log/70 §70.14.6） ---

func svcWithEvents(desired, running int32, deployAt time.Time, events ...ecstypes.ServiceEvent) ecstypes.Service {
	return ecstypes.Service{
		DesiredCount: desired,
		RunningCount: running,
		Deployments: []ecstypes.Deployment{
			{Status: aws.String("PRIMARY"), CreatedAt: aws.Time(deployAt)},
		},
		Events: events,
	}
}

func placeEvent(at time.Time, msg string) ecstypes.ServiceEvent {
	return ecstypes.ServiceEvent{CreatedAt: aws.Time(at), Message: aws.String(msg)}
}

const unplaceable = "(service af-ws-x) was unable to place a task because no container " +
	"instance met all of its requirements. The closest matching (container-instance abc) is " +
	"missing an attribute required by your task."

// 配置できない起動には期限が無い。ECS は理由をイベントに書いているのに CP がそれを
// 捨てて素の starting を返していたので、`aws ecs describe-services` を手で読む以外に
// 原因を知る方法が無かった。
func TestECSPlacementBlockedReadsTheCurrentDeployment(t *testing.T) {
	now := time.Now()
	deploy := now.Add(-2 * time.Minute)

	got := ecsPlacementBlocked(svcWithEvents(1, 0, deploy, placeEvent(now.Add(-time.Minute), unplaceable)))
	if !strings.Contains(got, "missing an attribute") {
		t.Fatalf("the placement failure was not surfaced: %q", got)
	}

	// 直った後の再デプロイ。古い苦情はイベント一覧に残り続けるので、デプロイより前の
	// ものを読むと通常のコールドスタートが偽の診断になる。
	if got := ecsPlacementBlocked(svcWithEvents(1, 0, now, placeEvent(now.Add(-time.Minute), unplaceable))); got != "" {
		t.Fatalf("an event older than the current deployment was reported: %q", got)
	}

	// ふつうに上がってくる最中は何も言わない。
	if got := ecsPlacementBlocked(svcWithEvents(1, 0, deploy,
		placeEvent(now, "(service af-ws-x) has started 1 tasks: (task abc)."))); got != "" {
		t.Fatalf("a healthy start was reported as blocked: %q", got)
	}
}

// phase に載って初めて Console に出る。running になったら消えることまでが契約——
// 消えないと bootPhase != "" のせいで起動ダイアログが出たままになる。
func TestECSEC2BlockedPhaseIsSetAndCleared(t *testing.T) {
	h := newEC2Harness(t)
	now := time.Now()
	defer h.rt.setPhase("")

	h.rt.notePlacementBlocked(svcWithEvents(1, 0, now.Add(-time.Minute), placeEvent(now, unplaceable)))
	ph := h.rt.BootPhase()
	if !strings.HasPrefix(ph, blockedPhasePrefix) || !strings.Contains(ph, "missing an attribute") {
		t.Fatalf("phase does not carry the reason: %q", ph)
	}

	h.rt.clearBlockedPhase()
	if got := h.rt.BootPhase(); got != "" {
		t.Fatalf("the blocked phase survived the task starting: %q", got)
	}

	// ⚠️ 進行中の Start が書いた phase は消してはいけない。4 秒ごとのポーリングが
	// これを消すと、起動ダイアログが起動の最中に真っ白になる。
	h.rt.setPhase("home: attaching")
	h.rt.clearBlockedPhase()
	if got := h.rt.BootPhase(); got != "home: attaching" {
		t.Fatalf("a live Start's phase was wiped by a poll: %q", got)
	}
}

// Every service call must carry the single-task deployment configuration AND the
// availabilityZoneRebalancing that ECS demands with it — not just the call that creates
// the service. The defect docs/log/64 §64.39 describes needs maximumPercent 200, that is the
// AWS default, and a deployment that predates this change keeps the default until
// something sends the new one. Measured A/B on one substrate (§64.39.10): 200% produced
// two tasks with the FIRST from the old revision, 2 of 2; 100% produced one task from the
// new revision, 2 of 2.
//
// ⚠️ The pair is the point. A service created before this change also carries
// availabilityZoneRebalancing ENABLED, and ECS rejects maximumPercent <= 100 on such a
// service with a 400 — which is how 0.12.3 stopped every existing workspace from starting
// (§64.39.12). Both halves are asserted on every call for the same reason: the value that
// is wrong is only wrong on services this build did not create.
func TestECSEC2EveryServiceCallPinsOneTaskPerWorkspace(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	if err := h.rt.Start(ctx); err != nil { // CreateService
		t.Fatalf("first Start: %v", err)
	}
	if err := h.rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ec2.addSlot("i-hot-2", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot-2"] = true
	h.ec2.attach("vol-1", "i-hot-2", time.Now())
	if err := h.rt.Start(ctx); err != nil { // the revision + the scale-up
		t.Fatalf("second Start: %v", err)
	}

	check := func(what string, dc *ecstypes.DeploymentConfiguration, azr ecstypes.AvailabilityZoneRebalancing) {
		t.Helper()
		if dc == nil {
			t.Errorf("%s sent no deploymentConfiguration; ECS then keeps the 200%% default that allows a second task", what)
			return
		}
		if aws.ToInt32(dc.MaximumPercent) != 100 {
			t.Errorf("%s maximumPercent = %d, want 100 (200 is what lets the demoted deployment run a task)", what, aws.ToInt32(dc.MaximumPercent))
		}
		if aws.ToInt32(dc.MinimumHealthyPercent) != 0 {
			t.Errorf("%s minimumHealthyPercent = %d, want 0 — at 100%% there is no room to start before stopping", what, aws.ToInt32(dc.MinimumHealthyPercent))
		}
		// ⚠️ The two must travel together. Sending maximumPercent 100 to a service that
		// has Availability Zone rebalancing on is a 400, and that is not hypothetical:
		// it took production down on 0.12.3, for every workspace whose service predated
		// the change (§64.39.12). Omitting the field does not help — on UPDATE it means
		// "keep whatever the service has", which for those services is ENABLED.
		if azr != ecstypes.AvailabilityZoneRebalancingDisabled {
			t.Errorf("%s availabilityZoneRebalancing = %q, want DISABLED; ECS rejects maximumPercent <= 100 "+
				"while rebalancing is on, so a service created by an older build fails its first Start", what, azr)
		}
	}
	for i, c := range h.ecs.createCalls {
		check(fmt.Sprintf("createCalls[%d]", i), c.DeploymentConfiguration, c.AvailabilityZoneRebalancing)
	}
	for i, c := range h.ecs.updateCalls {
		// Only calls that can raise the count or move the revision matter; Stop's
		// scale-to-zero cannot produce a second task whatever the percentages say.
		if c.TaskDefinition == nil && aws.ToInt32(c.DesiredCount) == 0 {
			continue
		}
		check(fmt.Sprintf("updateCalls[%d]", i), c.DeploymentConfiguration, c.AvailabilityZoneRebalancing)
	}
}

// --- the workspace memory cap (ADR 0045 決定 28, docs/log/64 §64.41) ---

// The numbers, and why they are these numbers. A cap that is too generous does not
// contain anything (the incident's whole point was that the daemons had nowhere to
// live); one that is too mean takes RAM off a member for nothing.
func TestECSEC2WorkspaceMemCap(t *testing.T) {
	auto := ec2PoolConfig{hostReserveMiB: 0}
	for _, tc := range []struct {
		name string
		pool ec2PoolConfig
		rung int64
		want int64
	}{
		// ⚠️ The load-bearing case. An "8192 MiB" m7i.large really reports MemTotal 7784
		// (measured), so the reserve has to cover that ~5% AND the ~1.2-1.4 GiB the
		// daemons need. A fifth of the rung leaves 7784-6553 = 1231 MiB of real headroom.
		{"m7i.large lands at 6.4 GiB", auto, 8192, 6554},
		// The ceiling: the daemons cost the same on a big box, so it must not donate
		// proportionally more (a fifth of 32768 would be 6.5 GiB thrown away).
		{"a large rung is clamped to 2 GiB of reserve", auto, 32768, 30720},
		// The floor: a fifth of a small rung would not house the daemons at all.
		{"a small rung gets the 1 GiB floor", auto, 4096, 3072},
		{"explicit reserve is honoured", ec2PoolConfig{hostReserveMiB: 1536}, 8192, 6656},
		{"off means uncapped", ec2PoolConfig{hostReserveMiB: ec2HostReserveOff}, 8192, 0},
		// Capping a rung this small would leave a workspace that cannot run; better to
		// leave the deployment as it was than to break every Start on it.
		{"a rung too small to share is left uncapped", auto, 1024, 0},
		{"an undeclared rung is left uncapped", auto, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pool.workspaceMemCapMiB(tc.rung); got != tc.want {
				t.Errorf("workspaceMemCapMiB(%d) = %d, want %d", tc.rung, got, tc.want)
			}
		})
	}
}

// A typo in the knob that decides how much RAM a workspace gets must not be the reason
// a deployment stops capping — or stops starting.
func TestECSEC2HostReserveParsing(t *testing.T) {
	for in, want := range map[string]int64{
		"": 0, "auto": 0, "AUTO": 0, " auto ": 0,
		"off": ec2HostReserveOff, "none": ec2HostReserveOff,
		"1536": 1536, "0": 0,
		"banana": 0, "-5": 0,
	} {
		if got := parseHostReserveMiB(in); got != want {
			t.Errorf("parseHostReserveMiB(%q) = %d, want %d", in, got, want)
		}
	}
}

// The cap has to reach the task definition, because that is the only place it does
// anything: ECS turns container.Memory into the cgroup limit that keeps a runaway
// workspace from evicting the box's daemons out of memory (docs/log/64 §64.40).
func TestECSEC2TaskDefinitionCarriesTheMemoryCap(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.rt.memCapMiB = h.rt.pool.workspaceMemCapMiB(8192)

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.ecs.regCalls) != 1 {
		t.Fatalf("regCalls = %d, want 1", len(h.ecs.regCalls))
	}
	c := h.ecs.regCalls[0].ContainerDefinitions[0]
	limit := aws.ToInt32(c.Memory)
	if limit != 6554 {
		t.Errorf("container Memory = %d MiB, want 6554", limit)
	}
	// The soft reservation is what ECS schedules on and must stay BELOW the hard cap;
	// a reservation above the limit is a task definition ECS rejects outright.
	if res := aws.ToInt32(c.MemoryReservation); res > limit {
		t.Errorf("MemoryReservation %d exceeds the hard limit %d", res, limit)
	}
}

// A deployment that has switched the cap off must produce exactly the task definition
// it produced before the cap existed — no field, not a zero.
func TestECSEC2UncappedTaskDefinitionOmitsMemory(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.addSlot("i-hot", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-hot"] = true
	h.rt.memCapMiB = 0

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m := h.ecs.regCalls[0].ContainerDefinitions[0].Memory; m != nil {
		t.Errorf("container Memory = %d, want unset", *m)
	}
}
