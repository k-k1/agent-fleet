package runtime

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Dark-slot detection (ADR 0121, rung L0). A slot is dark when EC2 says `running` while both
// the ECS agent and the SSM agent are silent. This file only observes: it stamps the instance,
// logs and reports one audit event per episode. It never stops, deregisters, detaches or
// quarantines anything; those rungs are not built (ADR 0121 decision 5, rollout).

const (
	// ec2TagDarkSince is `<first dark observation>/<LaunchTime>[/detected]`, written on the
	// first sweep that saw the whole predicate true in a launch epoch. The epoch is what makes
	// a stamp from before a stop/start unusable: LaunchTime moves on every start.
	ec2TagDarkSince = "af-dark-since"

	// Values of AF_ECS_EC2_DARK_SLOT_ACTION. Only these two are implemented.
	darkActionOff    = "off"
	darkActionDetect = "detect"

	darkStampDetected = "detected"
)

// SlotDark is what the dark-slot scan found, for the audit log (the adapter has no database;
// Config.OnSlotDark hands it to the CP).
type SlotDark struct {
	// MembershipID and Workspace (the container name) come from the home's tags: the
	// sweeper has no database, so the CP resolves the tenant from the membership.
	MembershipID, Workspace string
	Slot, Volume            string
	// Since is when the sweeper first saw the slot dark; the episode was reported once it had
	// lasted darkAfter.
	Since time.Time
}

// parseDarkAction maps AF_ECS_EC2_DARK_SLOT_ACTION onto an implemented action. Unset is
// detect, the ADR's first-release default. `rescue` and `quarantine` name rungs that do not
// exist yet; they fall back to detect, which only writes a tag, rather than to off, so that an
// operator who asked for more still gets the evidence the detect-only window exists to gather.
func parseDarkAction(raw string) string {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "", darkActionDetect:
		return darkActionDetect
	case darkActionOff:
		return darkActionOff
	case "rescue", "quarantine":
		log.Printf("ecs-ec2: AF_ECS_EC2_DARK_SLOT_ACTION=%s is not implemented yet; running detect-only", v)
	default:
		log.Printf("ecs-ec2: AF_ECS_EC2_DARK_SLOT_ACTION=%q is not one of off|detect; running detect-only", raw)
	}
	return darkActionDetect
}

// clampDark raises darkAfter to the floors ADR 0121 decision 1 names, so a misconfiguration
// can only make the scan slower, never make it act on a box that is still booting or that a
// waiting Start is about to judge itself. 0 stays 0 (off).
func (p *ec2PoolConfig) clampDark() {
	if p.darkAfter <= 0 {
		return
	}
	floor := p.darkBootGrace
	if lost := p.slotLostAfter + p.sweepEvery; lost > floor {
		floor = lost
	}
	if p.darkAfter < floor {
		log.Printf("ecs-ec2: AF_ECS_EC2_DARK_AFTER_SEC=%.0f is below the floor %.0fs (boot grace, slot-lost wait plus sweep interval); using the floor",
			p.darkAfter.Seconds(), floor.Seconds())
		p.darkAfter = floor
	}
}

// darkStamp is the parsed af-dark-since value.
type darkStamp struct {
	since, launch time.Time
	detected      bool
}

func (s darkStamp) String() string {
	v := s.since.UTC().Format(time.RFC3339) + "/" + s.launch.UTC().Format(time.RFC3339)
	if s.detected {
		v += "/" + darkStampDetected
	}
	return v
}

// parseDarkStamp reads a stamp; a value that does not parse is "no stamp", so the next pass
// rewrites it rather than trusting a half-written one.
func parseDarkStamp(v string) (darkStamp, bool) {
	parts := strings.Split(v, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return darkStamp{}, false
	}
	since, err1 := time.Parse(time.RFC3339, parts[0])
	launch, err2 := time.Parse(time.RFC3339, parts[1])
	if err1 != nil || err2 != nil {
		return darkStamp{}, false
	}
	s := darkStamp{since: since, launch: launch}
	if len(parts) == 3 {
		if parts[2] != darkStampDetected {
			return darkStamp{}, false
		}
		s.detected = true
	}
	return s, true
}

// sweepDarkSlots is the dark-slot scan. It walks the population ADR 0121 decision 1 names:
// slots that hold a home and have no live claim. A live claim means a Start is converging on
// the slot and owns the verdict through slotLostAfter.
//
// A read that fails is not an answer: a failed ECS or SSM call leaves the slot untouched (no
// stamp, no clear), because "could not ask" and "asked and it was silent" must not look alike.
func (f *ecsEC2Factory) sweepDarkSlots(ctx context.Context, homes []ec2types.Volume) {
	if f.pool.darkAction != darkActionDetect || f.pool.darkAfter <= 0 {
		return
	}
	cmds, ok := f.ssmc.(ssmSlotCommands)
	if !ok {
		return // a client that cannot read SSM liveness cannot prove the SSM leg
	}
	probe := f.probeRuntime()
	byInstance := map[string]*ec2types.Volume{}
	var ids []string
	for i := range homes {
		vol := &homes[i]
		inst := attachedInstance(vol)
		if inst == "" || probe.claimLive(vol) {
			continue
		}
		if _, seen := byInstance[inst]; !seen {
			ids = append(ids, inst)
		}
		byInstance[inst] = vol
	}
	if len(ids) == 0 {
		return
	}
	registered, err := probe.registeredSlots(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: dark-slot scan skipped, cannot read the cluster: %v", err)
		return
	}
	now := time.Now()
	pass := f.newDarkProbePass()
	for _, chunk := range chunkStrings(ids, 100) {
		out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{
				tagFilter(EC2TagPool, f.pool.pool),
				{Name: aws.String("instance-id"), Values: chunk},
			},
		})
		if err != nil {
			log.Printf("ecs-ec2 sweep: dark-slot scan skipped, cannot read the slots: %v", err)
			return
		}
		for _, r := range out.Reservations {
			for i := range r.Instances {
				inst := &r.Instances[i]
				id := aws.ToString(inst.InstanceId)
				if vol := byInstance[id]; vol != nil {
					f.scanDarkSlot(ctx, cmds, pass, inst, vol, registered[id], now)
				}
			}
		}
	}
}

// scanDarkSlot applies the predicate of ADR 0121 decision 1 to one slot. The cheap legs run
// first so the SSM call is made only for a box the cluster cannot reach.
func (f *ecsEC2Factory) scanDarkSlot(ctx context.Context, cmds ssmSlotCommands, pass *darkProbePass, inst *ec2types.Instance, vol *ec2types.Volume, connected bool, now time.Time) {
	id := aws.ToString(inst.InstanceId)
	// A quarantined box is the operator's; an engine box carries af-pool too.
	if ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot {
		return
	}
	raw := ec2TagValue(inst.Tags, ec2TagDarkSince)
	if inst.State == nil || inst.State.Name != ec2types.InstanceStateNameRunning {
		// Booting or being stopped is not dark, and a stamp left from before is of no use.
		// A terminated box cannot be tagged (the tags go with it); trying would log a
		// failure nobody can act on every sweep.
		if inst.State != nil && (inst.State.Name == ec2types.InstanceStateNameTerminated ||
			inst.State.Name == ec2types.InstanceStateNameShuttingDown) {
			return
		}
		f.clearDarkStamp(ctx, id, raw)
		return
	}
	if inst.LaunchTime == nil {
		return // cannot place the box in a launch epoch: unknown
	}
	launch := inst.LaunchTime.UTC().Truncate(time.Second)
	if connected {
		f.clearDarkStamp(ctx, id, raw)
		return
	}
	if now.Sub(launch) < f.pool.darkBootGrace {
		return // still inside the boot/wake grace: neither dark nor shown healthy
	}
	online, err := ssmOnline(ctx, cmds, id)
	if err != nil {
		log.Printf("ecs-ec2 sweep: dark-slot scan: SSM state of %s unknown: %v", id, err)
		return
	}
	if online {
		f.clearDarkStamp(ctx, id, raw)
		return
	}
	// Both cluster legs are silent. A slot whose service is up may still be serving its
	// member, so it is dark only if the Agent does not answer either.
	switch f.darkAgentLeg(ctx, pass, id, vol) {
	case AgentProbeHealthy:
		f.clearDarkStamp(ctx, id, raw)
		return
	case AgentProbeUnknown:
		return // could not ask: neither stamp nor clear
	}

	stamp, ok := parseDarkStamp(raw)
	if !ok || !stamp.launch.Equal(launch) {
		stamp = darkStamp{since: now.UTC().Truncate(time.Second), launch: launch}
		if f.writeDarkStamp(ctx, id, stamp) {
			log.Printf("ecs-ec2 sweep: slot %s looks dark (ecs agent disconnected, ssm not online); watching it", id)
		}
		return
	}
	if stamp.detected || now.Sub(stamp.since) < f.pool.darkAfter {
		return
	}
	// Mark first, report second: a failed mark is retried next pass with the event still
	// unsent, and a failed report costs one audit row rather than a repeat every sweep.
	stamp.detected = true
	if !f.writeDarkStamp(ctx, id, stamp) {
		return
	}
	ev := SlotDark{Slot: id, Volume: aws.ToString(vol.VolumeId), Since: stamp.since,
		MembershipID: ec2TagValue(vol.Tags, EC2TagMembership), Workspace: ec2TagValue(vol.Tags, EC2TagWorkspace)}
	log.Printf("ecs-ec2 sweep: slot %s holding %s has been dark since %s (%.0fm); detect-only, no action taken",
		id, ev.Workspace, stamp.since.Format(time.RFC3339), now.Sub(stamp.since).Minutes())
	if f.onSlotDark != nil {
		f.onSlotDark(ctx, ev)
	}
}

// darkAgentLeg is the Agent leg of the predicate. A slot whose service is down has no Agent
// to ask and is not probed: it counts as failed (the leg is satisfied). A service that is up
// is probed; a failed read of the service is unknown.
func (f *ecsEC2Factory) darkAgentLeg(ctx context.Context, pass *darkProbePass, slot string, vol *ec2types.Volume) AgentProbe {
	rt := f.runtimeForVolume(vol)
	if rt == nil {
		return AgentProbeUnknown
	}
	s, ok, err := rt.base.describeService(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: dark-slot scan: service of %s unknown: %v", slot, err)
		return AgentProbeUnknown
	}
	if !ok || s.DesiredCount <= 0 {
		f.probes.forget(slot)
		return AgentProbeFailed
	}
	res := f.probeAgent(ctx, pass, rt.Endpoint())
	f.probes.set(slot, probeObservation{Result: res, At: f.darkNow()})
	if res != AgentProbeFailed {
		log.Printf("ecs-ec2 sweep: dark-slot scan: Agent of %s on %s probe %s", rt.base.name, slot, res)
	}
	return res
}

func (f *ecsEC2Factory) writeDarkStamp(ctx context.Context, instanceID string, s darkStamp) bool {
	if _, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagDarkSince), Value: aws.String(s.String())}},
	}); err != nil {
		log.Printf("ecs-ec2 sweep: stamping dark slot %s failed: %v", instanceID, err)
		return false
	}
	return true
}

func (f *ecsEC2Factory) clearDarkStamp(ctx context.Context, instanceID, current string) {
	if current == "" {
		return
	}
	if _, err := f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagDarkSince)}},
	}); err != nil {
		log.Printf("ecs-ec2 sweep: clearing the dark stamp on %s failed: %v", instanceID, err)
	}
}
