package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// --- slots ---

type ec2SlotCandidate struct {
	id         string
	az         string
	running    bool
	registered bool
}

// slotsOfMyType lists the pool's slots THIS workspace could actually run on: the right
// instance type, and (when az is set) the AZ its home is pinned to. Both placement paths
// start here — freeSlots for an empty box, evictLongestIdle for one it has to take back.
//
// ⚠️ It exists because those two did NOT agree. freeSlots filtered on instance-type from
// the start; evictLongestIdle filtered only by AZ and by "not me", so at the cap a member
// on a bigger rung could take a SMALLER box and be attached to it. ECS then refuses to
// place the task — "no container instance met all of its requirements" — and the service
// stays at desiredCount 1 / runningCount 0 forever, which is a stuck workspace rather than
// a slow one. Across architectures placeHome's own header records the same symptom
// measured on a live deployment (docs/log/70 §70.14.5). Two copies of one rule is how that
// happened, so there is now one.
//
// az == "" means the whole pool: a home that does not exist yet is not pinned anywhere,
// and where a slot can be had is what decides its AZ.
func (e *ecsEC2Runtime) slotsOfMyType(ctx context.Context, az string) (*ec2.DescribeInstancesOutput, error) {
	filters := []ec2types.Filter{
		tagFilter(EC2TagPool, e.pool.pool),
		tagFilter(EC2TagRole, ec2RoleSlot),
		{Name: aws.String("instance-state-name"), Values: []string{"running", "stopped"}},
		{Name: aws.String("instance-type"), Values: []string{e.instanceType}},
	}
	if az != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("availability-zone"), Values: []string{az}})
	}
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filters})
	if err != nil {
		return nil, err
	}
	// A slot reserved for replacement takes nobody new: its next tenant would run on the
	// very box the operator wants gone. EC2 cannot filter on a tag being absent, so it is
	// dropped here, the one place both placement paths read. So is a replacement launched
	// for somebody else's home that has not moved onto it yet (pendingReplacementsForOthers).
	out = withoutReservedSlots(out)
	return withoutSlots(out, e.pendingReplacementsForOthers(ctx, out)), nil
}

// freeSlots lists the pool's slots that nobody's home is on, hot ones first (22–27s to
// swap) and stopped ones after (~50s).
//
// Occupancy is read from the VOLUMES, not from the instances' BlockDeviceMappings. Both
// are eventually consistent, but measured on real AWS the instance view lags: seconds
// after a detach had completed (volume `available`), the instance still listed /dev/sdf,
// so the freed slot looked busy and the next Start grew the pool instead of swapping
// onto it — the exact behaviour this design exists to avoid. The volume side is also the
// same source State() and releaseSlot() already trust, so there is one story about who
// is on which slot instead of two.
//
// Claims cover the rest: a slot some other workspace is launching onto has no attachment
// yet, and only its claim says so.
func (e *ecsEC2Runtime) freeSlots(ctx context.Context, az string) ([]ec2SlotCandidate, error) {
	out, err := e.slotsOfMyType(ctx, az)
	if err != nil {
		return nil, err
	}
	busy, err := e.occupiedInstances(ctx)
	if err != nil {
		return nil, err
	}
	registered, err := e.registeredSlots(ctx)
	if err != nil {
		return nil, err
	}
	var cands []ec2SlotCandidate
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			if busy[id] {
				continue
			}
			cands = append(cands, ec2SlotCandidate{
				id:         id,
				az:         aws.ToString(inst.Placement.AvailabilityZone),
				running:    inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning,
				registered: registered[id],
			})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].running != cands[j].running {
			return cands[i].running
		}
		if cands[i].registered != cands[j].registered {
			return cands[i].registered
		}
		return cands[i].id < cands[j].id
	})
	return cands, nil
}

// occupiedInstances is the set of slots that are taken: someone's home is attached to
// them, or someone is launching onto them (claim). One DescribeVolumes answers both.
func (e *ecsEC2Runtime) occupiedInstances(ctx context.Context) (map[string]bool, error) {
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return nil, err
	}
	busy := map[string]bool{}
	for i := range out.Volumes {
		if inst := attachedInstance(&out.Volumes[i]); inst != "" {
			busy[inst] = true
		}
		if inst := ec2TagValue(out.Volumes[i].Tags, EC2TagClaim); inst != "" && e.claimLive(&out.Volumes[i]) {
			busy[inst] = true
		}
	}
	return busy, nil
}

// isPoolContainerInstance reports whether a container instance belongs to the workspace
// slot pool rather than to something else sharing the cluster.
//
// Every container-instance walk here is written for pool members — "ACTIVE and agentConnected
// means a slot is ready", "no ECS task means the box is idle", "the EC2 instance is gone so
// deregister" — and an engine box satisfies none of those premises: it is not a slot, and its
// idleness and its lifetime are the engine controller's business. So the whole cluster walk is
// filtered here rather than at each caller (ADR 0071 review R7(a)).
//
// 🔴 BOTH halves are needed, and each excludes a different kind of engine box (ADR 0077
// decision 3):
//
//   - an ADR 0071 Managed Instances engine box carries its capacity provider's NAME and no
//     attribute. The pool's own boxes are launched by the CP from a launch template and
//     register themselves through user-data, so ECS reports no provider for them;
//   - a box the CP bought with EC2 Fleet (ADR 0077) also has an EMPTY provider name — which is
//     exactly why the provider test alone collapses — and is told apart by the `af-role`
//     attribute its user-data writes: `engine-llm`, `engine-image`. A slot writes none, and an
//     old slot that predates the attribute writes none either, so "absent" has to mean "slot".
//
// Dropping either half readmits one of the two. A three-kind test pins it.
func isPoolContainerInstance(ci ecstypes.ContainerInstance) bool {
	if strings.TrimSpace(aws.ToString(ci.CapacityProviderName)) != "" {
		return false
	}
	switch containerInstanceAttr(ci, EC2TagRole) {
	case "", ec2RoleSlot:
		return true
	default:
		return false
	}
}

// containerInstanceAttr reads one ECS container-instance attribute, "" when it is absent.
//
// The attribute is written by the box itself, through `ECS_INSTANCE_ATTRIBUTES` in
// /etc/ecs/ecs.config — the same file a slot's user-data writes `ECS_CLUSTER` into — and it
// carries the SAME WORD as the EC2 tag (af-role) on purpose: one vocabulary, so that "whose box
// is this" has one answer whether it is asked of EC2 or of ECS.
func containerInstanceAttr(ci ecstypes.ContainerInstance, name string) string {
	for _, at := range ci.Attributes {
		if aws.ToString(at.Name) == name {
			return strings.TrimSpace(aws.ToString(at.Value))
		}
	}
	return ""
}

// registeredSlots is the set of EC2 instance ids the cluster currently accepts tasks
// on (ACTIVE + agentConnected). An unregistered slot can hold a volume but cannot run
// the task yet, so it sorts behind the hot ones rather than being skipped.
func (e *ecsEC2Runtime) registeredSlots(ctx context.Context) (map[string]bool, error) {
	arns, err := e.listContainerInstanceARNs(ctx)
	if err != nil || len(arns) == 0 {
		return map[string]bool{}, err
	}
	ready := map[string]bool{}
	for _, chunk := range chunkStrings(arns, 100) {
		out, err := e.ci.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
			Cluster:            aws.String(e.base.cfg.cluster),
			ContainerInstances: chunk,
		})
		if err != nil {
			return nil, err
		}
		for _, ci := range out.ContainerInstances {
			if !isPoolContainerInstance(ci) {
				continue
			}
			if aws.ToString(ci.Status) == "ACTIVE" && ci.AgentConnected {
				ready[aws.ToString(ci.Ec2InstanceId)] = true
			}
		}
	}
	return ready, nil
}

func (e *ecsEC2Runtime) listContainerInstanceARNs(ctx context.Context) ([]string, error) {
	var arns []string
	var next *string
	for {
		out, err := e.ci.ListContainerInstances(ctx, &ecs.ListContainerInstancesInput{
			Cluster:   aws.String(e.base.cfg.cluster),
			NextToken: next,
		})
		if err != nil {
			return nil, err
		}
		arns = append(arns, out.ContainerInstanceArns...)
		if next = out.NextToken; next == nil {
			return arns, nil
		}
	}
}

// runSlot grows the pool by one instance in the given AZ. The launch template owns
// everything about what a slot IS (AMI, user-data, instance profile, security group);
// the CP only chooses size and placement.
// growPool adds a slot and reports which AZ it landed in.
//
// The az argument is a CONSTRAINT, not a preference. An existing home cannot move, so a
// slot anywhere else is useless to it and a capacity failure there is a real failure. A
// home that does not exist yet passes "" and may go wherever there is room — which is the
// only reason to try more than one AZ.
//
// Without this, one AZ running out of the slot type stops every NEW user in the deployment
// (everybody already placed keeps working, so it does not look like an outage) — and the
// AZ that ran out is the one AZ the adapter ever picks, because a fixed first-AZ pick is deterministic.
// docs/log/64 §64.20.4.
func (e *ecsEC2Runtime) growPool(ctx context.Context, az string) (string, string, error) {
	if az != "" {
		id, err := e.runSlot(ctx, az, e.pool.maxSlots, "")
		return id, az, err
	}
	azs, err := e.spreadAZs(ctx)
	if err != nil {
		return "", "", err
	}
	if len(azs) == 0 {
		return "", "", fmt.Errorf("no usable subnet/AZ configured (AF_ECS_SUBNETS)")
	}
	var lastErr error
	for _, candidate := range azs {
		id, err := e.runSlot(ctx, candidate, e.pool.maxSlots, "")
		if err == nil {
			return id, candidate, nil
		}
		// Only a "there is no room here" answer is worth asking somewhere else. A bad
		// launch template or a hit quota fails identically in every AZ, and retrying it
		// three times just buries the real message under the last one.
		if !isEC2CapacityError(err) {
			return "", "", err
		}
		log.Printf("ecs-ec2: %s cannot take a %s right now (%v); trying the next AZ", candidate, e.instanceType, err)
		lastErr = err
	}
	return "", "", fmt.Errorf("no configured AZ (%s) could take a new %s slot: %w",
		strings.Join(azs, ", "), e.instanceType, lastErr)
}

// isEC2CapacityError reports whether RunInstances failed for a reason that another AZ
// might not have. Matched on the message like isAWSNotFound, because that is how this
// package reads AWS error codes.
func isEC2CapacityError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"InsufficientInstanceCapacity",
		"InsufficientHostCapacity",
		"InsufficientCapacity",
		"Unsupported", // the instance type is not offered in that AZ at all
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// validate refuses a pool configuration that cannot work, at BOOT, and fills in the
// default class. Every failure here would otherwise surface as an error on somebody's
// first Start — long after the operator stopped watching the logs — so this follows
// the line AF_ECS_EC2_LAUNCH_TEMPLATE already drew: a CP that cannot run workspaces
// says so instead of starting and running none.
func (p *ec2PoolConfig) validate() error {
	if p.launchTemplate == "" {
		return fmt.Errorf("AF_ECS_EC2_LAUNCH_TEMPLATE is required for AF_RUNTIME=ecs-ec2")
	}
	if len(p.classes) == 0 {
		return fmt.Errorf("AF_ECS_EC2_SLOT_TYPES has no usable entry (want type:memMiB,... or id|label|arch|type:memMiB,...)")
	}
	if p.amiArm64 == "" {
		for _, c := range p.classes {
			if c.arch == EC2ArchArm {
				return fmt.Errorf("slot class %q is %s but AF_ECS_EC2_AMI_ARM64 is empty "+
					"(redeploy cfn/40-ec2-pool.yaml and pass its SlotAmiIdArm64 output)", c.id, c.arch)
			}
		}
	}
	if p.defaultClass == "" {
		p.defaultClass = p.classes[0].id
		return nil
	}
	if p.classFor(p.defaultClass).id != p.defaultClass {
		return fmt.Errorf("AF_ECS_EC2_DEFAULT_SLOT_CLASS=%q is not one of the declared classes", p.defaultClass)
	}
	return nil
}

// amiFor is the AMI to override the launch template's with, or "" to use the
// template's own. An empty arch (a deployment that never declared classes) is x86_64,
// which is what the template has always pinned.
func (p ec2PoolConfig) amiFor(arch string) string {
	if arch == EC2ArchArm {
		return p.amiArm64
	}
	return ""
}

// launchTemplateSpec accepts either an id (lt-…) or a name, the way every
// AF_ECS_EC2_LAUNCH_TEMPLATE* value may be written.
func launchTemplateSpec(ref string) *ec2types.LaunchTemplateSpecification {
	lt := &ec2types.LaunchTemplateSpecification{Version: aws.String("$Latest")}
	if strings.HasPrefix(ref, "lt-") {
		lt.LaunchTemplateId = aws.String(ref)
	} else {
		lt.LaunchTemplateName = aws.String(ref)
	}
	return lt
}

// describeSlotClasses renders the parsed ladders for the one boot log line — the
// operator's only chance to see that the spec they wrote parsed the way they meant.
func describeSlotClasses(cs []ec2SlotClass) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		types := make([]string, 0, len(c.slots))
		for _, s := range c.slots {
			types = append(types, s.instanceType)
		}
		parts = append(parts, fmt.Sprintf("%s(%s:%s)", c.id, c.arch, strings.Join(types, "/")))
	}
	return strings.Join(parts, " ")
}

// runSlot launches one slot under the cap limit. Every caller but one passes maxSlots; the
// replacement of a reserved slot passes one more, because it launches the new box before the
// old one is gone and the old one must not count against the place it is about to give back.
//
// replacesHome, when set, is the home volume a replacement slot is launched for. It is stamped
// at launch (ec2TagReplacesHome), so the link exists the moment the instance does.
func (e *ecsEC2Runtime) runSlot(ctx context.Context, az string, limit int, replacesHome string) (string, error) {
	total, err := e.poolSize(ctx)
	if err != nil {
		return "", err
	}
	if total >= limit {
		return "", fmt.Errorf("slot pool is full (%d/%d); raise AF_ECS_EC2_MAX_SLOTS", total, e.pool.maxSlots)
	}
	subnet, err := e.subnetIn(ctx, az)
	if err != nil {
		return "", err
	}
	lt := launchTemplateSpec(e.pool.launchTemplate)
	tags := []ec2types.Tag{
		{Key: aws.String(EC2TagPool), Value: aws.String(e.pool.pool)},
		{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleSlot)},
		{Key: aws.String(EC2TagSlotSize), Value: aws.String(e.instanceType)},
		{Key: aws.String("Name"), Value: aws.String("af-slot-" + e.instanceType)},
	}
	if replacesHome != "" {
		tags = append(tags, ec2types.Tag{Key: aws.String(ec2TagReplacesHome), Value: aws.String(replacesHome)})
	}
	out, err := e.ec2.RunInstances(ctx, &ec2.RunInstancesInput{
		LaunchTemplate: lt,
		// Overrides the template's ImageId on arm64 and is nil everywhere else, so an
		// x86_64 launch is byte-for-byte the call it has always been.
		ImageId:      strOrNil(e.pool.amiFor(e.arch)),
		InstanceType: ec2types.InstanceType(e.instanceType),
		SubnetId:     aws.String(subnet),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         tags,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("run slot: %w", err)
	}
	if len(out.Instances) == 0 {
		return "", fmt.Errorf("run slot: no instance returned")
	}
	id := aws.ToString(out.Instances[0].InstanceId)
	log.Printf("ecs-ec2: grew the pool with slot %s (%s, %s) for %s", id, e.instanceType, az, e.base.name)
	return id, nil
}

// instanceRunning reports whether a slot is up. A dormant slot is stopped, not gone, so
// "which of the two" decides between the 13s path and the ~90s one.
func (e *ecsEC2Runtime) instanceRunning(ctx context.Context, instanceID string) (bool, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, err
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			if inst.State == nil {
				continue
			}
			switch inst.State.Name {
			case ec2types.InstanceStateNameRunning:
				return true, nil
			case ec2types.InstanceStateNameTerminated, ec2types.InstanceStateNameShuttingDown:
				return false, fmt.Errorf("slot %s is %s", instanceID, inst.State.Name)
			}
			return false, nil
		}
	}
	return false, fmt.Errorf("slot %s no longer exists", instanceID)
}

func (e *ecsEC2Runtime) poolFull(ctx context.Context) (bool, error) {
	n, err := e.poolSize(ctx)
	if err != nil {
		return false, err
	}
	return n >= e.pool.maxSlots, nil
}

// evictLongestIdle takes a slot back from the workspace that has been dormant longest and
// returns the freed instance id AND its AZ — the caller may not have an AZ yet (a home
// that has not been created), and the reclaimed slot is what decides it.
//
// az is a filter, not a preference: pass the AZ an existing home is pinned to, or "" to
// consider the whole pool.
//
// ⚠️ A victim has to be a box this workspace can actually RUN on, which means the same
// instance type — see slotsOfMyType for what taking the wrong one does. The
// longest-dormant box overall is therefore not always the victim: a member on the xlarge
// rung skips past a large that has been asleep for days.
//
// Only dormant homes are candidates (the af-idle-since tag, written by Stop), and
// releaseSlot refuses any victim whose service is not actually at desiredCount 0 — so a
// workspace that woke up between the pick and the release keeps its slot and we simply
// report that there was nothing to take.
func (e *ecsEC2Runtime) evictLongestIdle(ctx context.Context, az string) (string, string, error) {
	usable, err := e.slotsOfMyType(ctx, az)
	if err != nil {
		return "", "", err
	}
	canRunHere := map[string]bool{}
	for _, r := range usable.Reservations {
		for _, inst := range r.Instances {
			canRunHere[aws.ToString(inst.InstanceId)] = true
		}
	}
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return "", "", err
	}
	var best *ec2types.Volume
	var bestIdle time.Duration
	now := e.now()
	for i := range out.Volumes {
		v := &out.Volumes[i]
		if inst := attachedInstance(v); inst == "" || !canRunHere[inst] {
			continue
		}
		if az != "" && aws.ToString(v.AvailabilityZone) != az {
			continue
		}
		if aws.ToString(v.VolumeId) == "" || ec2TagValue(v.Tags, EC2TagMembership) == e.base.membershipID {
			continue
		}
		idle, ok := idleSince(v, now)
		if !ok || (best != nil && idle <= bestIdle) {
			continue
		}
		best, bestIdle = v, idle
	}
	if best == nil {
		// Say which size, because "full" and "full of the wrong size" call for different
		// actions: the first is raise the cap, the second is wait for a box of this size
		// to fall dormant (or for Ec2SlotTerminateAfterSec to collect one and free room to
		// build the right one).
		return "", "", fmt.Errorf("slot pool is full (%d) and no dormant %s slot can be reclaimed; raise AF_ECS_EC2_MAX_SLOTS",
			e.pool.maxSlots, e.instanceType)
	}
	victimSlot := attachedInstance(best)
	victimAZ := aws.ToString(best.AvailabilityZone)
	victim := e.siblingFor(best)
	if victim == nil {
		return "", "", fmt.Errorf("slot %s holds an unidentifiable home %s", victimSlot, aws.ToString(best.VolumeId))
	}
	log.Printf("ecs-ec2: reclaiming slot %s in %s from %s (dormant %.0fm) for %s",
		victimSlot, victimAZ, victim.base.name, bestIdle.Minutes(), e.base.name)
	if err := victim.releaseSlot(ctx); err != nil {
		return "", "", fmt.Errorf("reclaim slot %s: %w", victimSlot, err)
	}
	return victimSlot, victimAZ, nil
}

// makeRoom frees ONE place under the cap by TERMINATING a dormant box of a size this
// workspace cannot use, so that growPool can build one it can. It is the last thing tried
// before a Start fails at the cap, and it exists because the alternative is a member who
// simply cannot work — the one outcome this deployment's operator ruled out.
//
// ⚠️ Terminate rather than take the box over, because an instance type is not something a
// running box can change. evictLongestIdle has already had first refusal on every box of
// the RIGHT size, so whatever is left is a box no amount of reuse can turn into what this
// workspace needs — while it still holds one of the maxSlots places. Before this, that
// state failed the Start and went on failing until an operator noticed and terminated
// something by hand (docs/log/64 §64.33).
//
// ⚠️ NOT filtered by AZ, unlike evictLongestIdle, and the difference is the whole point:
// there the box is REUSED, so it has to be where the volume is pinned; here it is
// destroyed to buy a place under a cap that counts the entire pool. A box freed in another
// AZ is worth exactly as much, and refusing to look there would leave the requester stuck
// for nothing.
//
// ⚠️ Deliberately NOT gated on slotTerminateAfter. That knob is about idle COST — how long
// to keep paying for a box nobody is using — and its 0 means "keep them". This is a
// capacity deadlock, it only ever runs when the alternative is a failed Start, and what it
// costs the victim is the image cache (their return goes from ~110s to ~135s), never their
// home: releaseSlot detaches it first, and the volume is DeleteOnTermination=false either
// way. Reading "never terminate" as "leave the deadlock in place" would answer a question
// the parameter was never asked.
//
// Empty boxes are taken before occupied ones — those spend nobody's affinity at all.
func (e *ecsEC2Runtime) makeRoom(ctx context.Context) (bool, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			// af-role=slot leaves quarantined boxes alone, as everywhere else: those are
			// evidence an operator has not looked at yet (decision 20).
			tagFilter(EC2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"running", "stopped"}},
		},
	})
	if err != nil {
		return false, err
	}
	vols, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		return false, err
	}
	// The ECS side as well as the EC2 side, for the reason sweepFreeSlots gives: a task can
	// be running on a box no home is attached to at all (the golden baker's probe).
	tasks, err := e.slotTaskCounts(ctx)
	if err != nil {
		return false, err
	}
	pending := e.pendingReplacementsForOthers(ctx, out)
	now := e.now()
	type occupant struct {
		vol     *ec2types.Volume
		idle    time.Duration
		dormant bool
	}
	occupied, claimed := map[string]occupant{}, map[string]bool{}
	for i := range vols.Volumes {
		v := &vols.Volumes[i]
		// A box a Start is landing on holds no attachment yet; only the claim says so.
		if inst := ec2TagValue(v.Tags, EC2TagClaim); inst != "" && e.claimLive(v) {
			claimed[inst] = true
		}
		if inst := attachedInstance(v); inst != "" {
			idle, ok := idleSince(v, now)
			occupied[inst] = occupant{vol: v, idle: idle, dormant: ok}
		}
	}
	type candidate struct {
		id, itype string
		free      bool
		idle      time.Duration
		vol       *ec2types.Volume
	}
	var best *candidate
	take := func(c candidate) {
		switch {
		case best == nil:
		case best.free != c.free:
			if !c.free {
				return // an empty box always wins over somebody's dormant one
			}
		case c.idle <= best.idle:
			return
		}
		best = &c
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			// evictLongestIdle already had first refusal on this size — except a box reserved
			// for replacement, which no placement may reuse and so blocks the cap exactly as
			// a box of the wrong size does.
			if string(inst.InstanceType) == e.instanceType && !slotExcluded(inst) {
				continue
			}
			// Another home's pending replacement is spoken for, whatever its size.
			if pending[id] {
				continue
			}
			if claimed[id] || tasks[id] > 0 {
				continue
			}
			occ, taken := occupied[id]
			switch {
			case !taken:
				// Free, so the only clock is how long it has been free. An unreadable or
				// missing mark reads as 0 and simply loses every tie-break — this walk is
				// about capacity, not about timing, so a fresh box is still eligible when
				// it is the only one.
				var freeFor time.Duration
				if at, err := time.Parse(time.RFC3339, ec2TagValue(inst.Tags, ec2TagSlotIdleSince)); err == nil {
					freeFor = now.Sub(at)
				}
				take(candidate{id: id, itype: string(inst.InstanceType), free: true, idle: freeFor})
			case occ.dormant && ec2TagValue(occ.vol.Tags, EC2TagMembership) != e.base.membershipID:
				take(candidate{id: id, itype: string(inst.InstanceType), idle: occ.idle, vol: occ.vol})
			}
		}
	}
	if best == nil {
		return false, nil
	}
	if best.vol != nil {
		victim := e.siblingFor(best.vol)
		if victim == nil {
			return false, fmt.Errorf("slot %s holds an unidentifiable home %s", best.id, aws.ToString(best.vol.VolumeId))
		}
		log.Printf("ecs-ec2: the pool is full of slots %s cannot use; taking %s (%s, %s's, dormant %.0fm) out of it",
			e.instanceType, best.id, best.itype, victim.base.name, best.idle.Minutes())
		// Same order and the same reason as the sweeper's terminate stage: releaseSlot is
		// anchored to the victim's Start generation, so a Start racing this aborts the
		// release instead of losing its home to a box that is going away.
		if err := victim.releaseSlot(ctx); err != nil {
			return false, fmt.Errorf("release %s before terminating slot %s: %w",
				aws.ToString(best.vol.VolumeId), best.id, err)
		}
	} else {
		log.Printf("ecs-ec2: the pool is full of slots %s cannot use; taking the empty %s (%s) out of it",
			e.instanceType, best.id, best.itype)
	}
	e.terminateSlot(ctx, best.id, "made room for a "+e.instanceType)
	return true, e.waitSlotGone(ctx, best.id)
}

// waitSlotGone waits until a terminated box stops counting toward the cap.
// TerminateInstances returns as soon as the request is accepted and DescribeInstances can
// still answer `stopped` for a moment afterwards — runSlot re-reads poolSize, so without
// this the Start we just freed a place for would fail on the box we freed it with.
func (e *ecsEC2Runtime) waitSlotGone(ctx context.Context, instanceID string) error {
	for i := 0; i < 30; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err != nil {
			if isAWSNotFound(err) {
				return nil
			}
			return err
		}
		counted := false
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				if inst.State == nil {
					continue
				}
				switch inst.State.Name {
				case ec2types.InstanceStateNameShuttingDown, ec2types.InstanceStateNameTerminated:
				default:
					counted = true
				}
			}
		}
		if !counted {
			return nil
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("slot %s still counts toward the pool after being terminated", instanceID)
}

// siblingFor builds the runtime of ANOTHER workspace from its home volume's tags — the
// same trick the sweeper uses. Only the identity and the clients matter here; the
// runtime is used exclusively to release that workspace's slot.
func (e *ecsEC2Runtime) siblingFor(vol *ec2types.Volume) *ecsEC2Runtime {
	membership := ec2TagValue(vol.Tags, EC2TagMembership)
	name := ec2TagValue(vol.Tags, EC2TagWorkspace)
	if membership == "" || name == "" {
		return nil
	}
	base := *e.base
	base.name = name
	base.membershipID = membership
	sib := *e
	sib.base = &base
	return &sib
}

// poolSize counts every slot that is not terminated, across sizes and AZs — the cap is
// about the deployment's blast radius and bill, not about one size.
func (e *ecsEC2Runtime) poolSize(ctx context.Context) (int, error) {
	out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, ec2RoleSlot),
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range out.Reservations {
		n += len(r.Instances)
	}
	return n, nil
}

func (e *ecsEC2Runtime) subnetIn(ctx context.Context, az string) (string, error) {
	azs, err := e.azOfSubnet(ctx)
	if err != nil {
		return "", err
	}
	subnets := append([]string(nil), e.base.cfg.subnets...)
	sort.Strings(subnets)
	for _, s := range subnets {
		if az == "" || azs[s] == az {
			return s, nil
		}
	}
	return "", fmt.Errorf("no configured subnet in %s (AF_ECS_SUBNETS)", az)
}

// taskSubnetIn is the subnet of az a workspace task's ENI goes to. The slot instance stays
// in AF_ECS_SUBNETS (subnetIn): the ECS agent pulls the image through the instance's ENI,
// so the pull keeps the private route table's S3 endpoint while the task's own traffic
// takes the workspace route table (#2716). An ENI may sit in any subnet of the instance's
// AZ. Without AF_ECS_WORKSPACE_SUBNETS this is subnetIn.
//
// An AZ with slots but no workspace subnet is an error, never a fallback to the private
// subnet: that would put the workspace behind the endpoint's policy and off the egress
// route without anyone noticing. poolAZs keeps new homes out of such an AZ.
func (e *ecsEC2Runtime) taskSubnetIn(ctx context.Context, az string) (string, error) {
	if len(e.base.cfg.wsSubnets) == 0 {
		return e.subnetIn(ctx, az)
	}
	azs, err := e.azOfSubnet(ctx)
	if err != nil {
		return "", err
	}
	// Sorted for the same reason as subnetIn: a stable answer whatever order the operator
	// wrote, and independent of the order of AF_ECS_SUBNETS; only the AZ pairs them.
	subnets := append([]string(nil), e.base.cfg.wsSubnets...)
	sort.Strings(subnets)
	for _, s := range subnets {
		if az == "" || azs[s] == az {
			return s, nil
		}
	}
	return "", fmt.Errorf("no workspace subnet in %s (AF_ECS_WORKSPACE_SUBNETS); add one for every AZ of AF_ECS_SUBNETS", az)
}

// poolAZs is every AZ the pool may use, in the order a new home tries them: the configured
// subnets sorted by SUBNET ID, deduplicated.
//
// ⚠️ Sorted by id, which is NOT the order they were written in AF_ECS_SUBNETS — an
// operator listing 1a first still gets 1c when its subnet id happens to be smaller
// (measured, docs/log/64 §64.20.4). The order is arbitrary but it must be STABLE: every new
// home going to the same AZ is what keeps a pool from scattering one workspace's slots
// away from where the free ones are.
func (e *ecsEC2Runtime) poolAZs(ctx context.Context) ([]string, error) {
	bySubnet, err := e.azOfSubnet(ctx)
	if err != nil {
		return nil, err
	}
	subnets := append([]string(nil), e.base.cfg.subnets...)
	sort.Strings(subnets)
	// With workspace subnets configured, an AZ that has none could hold a home that can
	// never start (taskSubnetIn refuses it), so it is not offered.
	var wsAZ map[string]bool
	if len(e.base.cfg.wsSubnets) > 0 {
		wsAZ = map[string]bool{}
		for _, s := range e.base.cfg.wsSubnets {
			if az := bySubnet[s]; az != "" {
				wsAZ[az] = true
			}
		}
	}
	seen := map[string]bool{}
	var azs []string
	for _, s := range subnets {
		az := bySubnet[s]
		if az == "" || seen[az] || (wsAZ != nil && !wsAZ[az]) {
			continue
		}
		seen[az] = true
		azs = append(azs, az)
	}
	return azs, nil
}

// spreadAZs is poolAZs ordered by how many homes are already in each — fewest first, ties
// broken by the stable poolAZs order. Only growPool uses it, and only for a home that has
// no AZ yet.
//
// Why spread at all, when the pool otherwise works hardest to keep homes and free slots in
// the same place: because "the same place" turned out to mean ONE AZ for everybody. New
// homes followed a deterministic first choice, so the blast radius of a single AZ going
// down was not half the deployment, it was all of it (docs/log/64 §64.21). An EBS home cannot
// be evacuated — the only lever is not putting everyone in the same AZ to begin with.
//
// ⚠️ It is not free. A home in 1a can only ever use a slot in 1a, so free slots in the
// other AZ are useless to it and the pool grows instead of reusing — more instances for
// the same number of people. That is the price of the blast radius, and it is why the
// FREE-SLOT-FIRST preference in placeHome is untouched: spreading only decides where a
// NEW slot goes, never whether an existing one gets reused.
func (e *ecsEC2Runtime) spreadAZs(ctx context.Context) ([]string, error) {
	azs, err := e.poolAZs(ctx)
	if err != nil || len(azs) < 2 {
		return azs, err
	}
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, e.pool.pool),
			tagFilter(EC2TagRole, ec2RoleHome),
		},
	})
	if err != nil {
		// Balancing is an optimisation; the fixed order still works. Never fail a Start
		// over it.
		log.Printf("ecs-ec2: could not count homes per AZ (%v); falling back to the fixed AZ order", err)
		return azs, nil
	}
	homes := map[string]int{}
	for i := range out.Volumes {
		homes[aws.ToString(out.Volumes[i].AvailabilityZone)]++
	}
	ordered := append([]string(nil), azs...)
	rank := map[string]int{}
	for i, az := range azs {
		rank[az] = i
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if homes[ordered[i]] != homes[ordered[j]] {
			return homes[ordered[i]] < homes[ordered[j]]
		}
		return rank[ordered[i]] < rank[ordered[j]]
	})
	return ordered, nil
}

// wakeSlot starts a dormant slot, waiting first for it to be startable.
//
// The wait is the whole point: the sweeper stops a slot the moment its workspace goes
// dormant, so a user who comes back seconds later finds it in `stopping`, and EC2
// answers StartInstances with "IncorrectInstanceState" (measured — it failed the live
// test). Retrying from `stopping` is not an error case, it is the normal race between a
// person and a 15-minute timer.
func (e *ecsEC2Runtime) wakeSlot(ctx context.Context, instanceID string) error {
	for i := 0; ; i++ {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err != nil {
			return err
		}
		state := ec2types.InstanceStateName("")
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				if inst.State != nil {
					state = inst.State.Name
				}
			}
		}
		switch state {
		case ec2types.InstanceStateNameRunning, ec2types.InstanceStateNamePending:
			return nil
		case ec2types.InstanceStateNameStopped:
			log.Printf("ecs-ec2: waking slot %s for %s (home still attached)", instanceID, e.base.name)
			_, err := e.ec2.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{instanceID}})
			return err
		case ec2types.InstanceStateNameTerminated, ec2types.InstanceStateNameShuttingDown:
			return fmt.Errorf("slot %s is %s", instanceID, state)
		}
		if i >= 60 {
			return fmt.Errorf("slot %s stuck in %s", instanceID, state)
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

func (e *ecsEC2Runtime) waitInstanceRunning(ctx context.Context, instanceID string) error {
	for {
		out, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		if err == nil {
			for _, r := range out.Reservations {
				for _, inst := range r.Instances {
					if inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning {
						return nil
					}
				}
			}
		}
		if err := e.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// errSlotLost marks the one failure a Start can recover from by itself: the slot is
// EC2-`running` but the cluster cannot reach its agent, and has not been able to for
// slotLostAfter. Everything above it treats this as "use a different box", not as "this
// Start failed" — see abandonLostSlot and converge.
var errSlotLost = errors.New("slot lost")

// slotRegisterPoll is the interval every registration wait uses. Named because the grace
// below is expressed in TIME and has to be converted into polls.
const slotRegisterPoll = 3 * time.Second

// waitSlotRegistered waits until the slot is an ACTIVE, agent-connected container
// instance. Measured at ~20s after an instance start (docs/log/64 §64.12.1); a hot slot
// returns on the first poll.
//
// Two different failures come out of here and the caller must be able to tell them apart:
//
//   - errSlotLost — the box is running and simply not answering the cluster any more.
//     Waiting longer cannot help (docs/log/64 §64.40); the workspace has to be moved.
//   - anything else — still converging when the budget ran out, or the poll itself
//     failed. The slot may well be fine, so nothing is torn down.
//
// The grace is counted in POLLS rather than off the clock on purpose: `sleep` is injected,
// and a wall-clock deadline would make every test that stubs it out wait for real.
func (e *ecsEC2Runtime) waitSlotRegistered(ctx context.Context, instanceID string) error {
	// Bounded as well as context-scoped. The caller's budget is the real limit in
	// production, but an unbounded "poll until" hangs anything that drives this with a
	// context that never expires.
	limit := 400
	if e.pool.slotLostAfter > 0 {
		// At least one poll: a grace shorter than the interval still means "look once,
		// then decide", not "decide without looking".
		if n := int(e.pool.slotLostAfter / slotRegisterPoll); n < limit {
			limit = max(n, 1)
		}
	}
	for i := 0; i < limit; i++ {
		ready, err := e.registeredSlots(ctx)
		if err == nil && ready[instanceID] {
			return nil
		}
		if err := e.sleep(ctx, slotRegisterPoll); err != nil {
			return err
		}
	}
	// Out of patience. Whether this box is merely slow or actually gone is decided by
	// EC2, not by the cluster: a slot that is still `pending` (or one whose state we
	// could not read) gets the benefit of the doubt, because tearing down a box that is
	// mid-boot would turn a slow Start into a destroyed one.
	if e.pool.slotLostAfter > 0 {
		if running, err := e.instanceRunning(ctx, instanceID); err == nil && running {
			return fmt.Errorf("slot %s has been running without reaching the cluster for %s: %w",
				instanceID, e.pool.slotLostAfter, errSlotLost)
		}
	}
	return fmt.Errorf("slot %s never registered with the cluster", instanceID)
}
