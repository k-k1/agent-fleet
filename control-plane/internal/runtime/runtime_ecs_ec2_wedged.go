package runtime

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Wedged-slot detection (ADR 0121, amendment of 2026-11-12). A slot is wedged when its ECS
// agent still answers but the workload on it cannot make progress: a task is stuck STOPPING,
// or the Agent drops connections while ECS reports the task HEALTHY. The dark predicate never
// fires for it (the agents are up), and nothing else looks while the member is away. This
// file observes: it stamps the instance, logs and reports one audit event per episode. With
// the action on `release` a slot whose stamp is `detected` is handed to the release
// (runtime_ecs_ec2_wedged_release.go); it never deregisters or stops the box.

const (
	// ec2TagWedgedSince has the shape of af-dark-since, `<first observation>/<LaunchTime>[/detected]`,
	// and the same epoch rule: a stamp from before a stop/start is unusable.
	ec2TagWedgedSince = "af-wedged-since"

	// Values of AF_ECS_EC2_WEDGED_SLOT_ACTION.
	wedgedActionOff     = "off"
	wedgedActionDetect  = "detect"
	wedgedActionRelease = "release"

	// wedgedStopGrace is added to the task definition's stopTimeout before a STOPPING task
	// counts. The stop of a healthy slot ends within stopTimeout plus the agent's report; the
	// incident's task stayed STOPPING for more than 30 minutes.
	wedgedStopGrace = 5 * time.Minute

	// wedgedStopLimit is how long a task may be STOPPING before it counts. stopGraceSec, which
	// sets every task definition this repo registers (docker stop -t and the ECS stopTimeout),
	// is clamped to 1-120 s, so 120 s is an upper bound on any such task's stopTimeout whatever
	// the CP's setting was when the task was registered; the grace comes on top.
	wedgedStopLimit = 120*time.Second + wedgedStopGrace

	wedgedSignalStopping    = "task_stopping"
	wedgedSignalProbeFailed = "agent_probe_failed"
)

// SlotWedged is what the wedged-slot scan found, for the audit log (the adapter has no
// database; Config.OnSlotWedged hands it to the CP).
type SlotWedged struct {
	MembershipID, Workspace string
	Slot, Volume            string
	// Since is when the sweeper first saw the slot wedged; the episode was reported once it
	// had lasted wedgedAfter.
	Since time.Time
	// Signal is what the reporting pass saw: wedgedSignalStopping or wedgedSignalProbeFailed.
	Signal string
	// Action is "" for the detection of an episode, "released" when the release took the home
	// off the slot, or "blocked" when it could not (Reason says why where it is known).
	// Since is then when the release began, not when the slot was first seen wedged.
	Action, Reason string
}

// parseWedgedAction maps AF_ECS_EC2_WEDGED_SLOT_ACTION onto an action. Unset is detect, and so
// is a value that is not one of off|detect|release: the fallback only writes a tag.
func parseWedgedAction(raw string) string {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "", wedgedActionDetect:
		return wedgedActionDetect
	case wedgedActionOff:
		return wedgedActionOff
	case wedgedActionRelease:
		return wedgedActionRelease
	default:
		log.Printf("ecs-ec2: AF_ECS_EC2_WEDGED_SLOT_ACTION=%q is not one of off|detect|release; running detect-only", raw)
	}
	return wedgedActionDetect
}

// clampWedged raises wedgedAfter to the floor darkAfter has (boot grace; slot-lost wait plus
// sweep interval), so a misconfiguration can only make the scan slower: a Start that is
// waiting on the slot judges it itself for slotLostAfter, and a stamp younger than that
// proves nothing. 0 stays 0 (off).
func (p *ec2PoolConfig) clampWedged() {
	if p.wedgedAfter <= 0 {
		return
	}
	floor := p.darkBootGrace
	if lost := p.slotLostAfter + p.sweepEvery; lost > floor {
		floor = lost
	}
	if p.wedgedAfter < floor {
		log.Printf("ecs-ec2: AF_ECS_EC2_WEDGED_AFTER_SEC=%.0f is below the floor %.0fs (boot grace, slot-lost wait plus sweep interval); using the floor",
			p.wedgedAfter.Seconds(), floor.Seconds())
		p.wedgedAfter = floor
	}
}

// wedgedVerdict is the answer of one look at a slot.
type wedgedVerdict struct {
	state  wedgedState
	signal string
}

type wedgedState int

// The zero value is unknown on purpose: a verdict nobody filled in must neither stamp nor clear.
const (
	wedgedUnknown wedgedState = iota
	wedgedNo
	wedgedYes
)

// sweepWedgedSlots walks the population the dark scan walks: slots that hold a home and have
// no live claim. A read that fails is not an answer: it leaves the slot untouched (no stamp,
// no clear).
func (f *ecsEC2Factory) sweepWedgedSlots(ctx context.Context, homes []ec2types.Volume) {
	if (f.pool.wedgedAction != wedgedActionDetect && f.pool.wedgedAction != wedgedActionRelease) || f.pool.wedgedAfter <= 0 {
		return
	}
	probe := f.probeRuntime()
	byInstance := map[string]*ec2types.Volume{}
	var ids []string
	for i := range homes {
		vol := &homes[i]
		inst := attachedInstance(vol)
		if inst == "" || probe.claimLive(vol) || isBakeHome(vol) {
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
		log.Printf("ecs-ec2 sweep: wedged-slot scan skipped, cannot read the cluster: %v", err)
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
			log.Printf("ecs-ec2 sweep: wedged-slot scan skipped, cannot read the slots: %v", err)
			return
		}
		for _, r := range out.Reservations {
			for i := range r.Instances {
				inst := &r.Instances[i]
				id := aws.ToString(inst.InstanceId)
				if vol := byInstance[id]; vol != nil {
					f.scanWedgedSlot(ctx, pass, inst, vol, registered[id], now)
				}
			}
		}
	}
}

// scanWedgedSlot applies the wedged predicate to one slot.
func (f *ecsEC2Factory) scanWedgedSlot(ctx context.Context, pass *darkProbePass, inst *ec2types.Instance, vol *ec2types.Volume, connected bool, now time.Time) {
	id := aws.ToString(inst.InstanceId)
	if ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot {
		return // a quarantined box is the operator's; an engine box carries af-pool too
	}
	raw := ec2TagValue(inst.Tags, ec2TagWedgedSince)
	if inst.State == nil || inst.State.Name != ec2types.InstanceStateNameRunning {
		if inst.State != nil && (inst.State.Name == ec2types.InstanceStateNameTerminated ||
			inst.State.Name == ec2types.InstanceStateNameShuttingDown) {
			return // its tags go with it; trying would log a failure nobody can act on
		}
		f.clearWedgedStamp(ctx, id, raw)
		return
	}
	if inst.LaunchTime == nil {
		return // cannot place the box in a launch epoch: unknown
	}
	launch := inst.LaunchTime.UTC().Truncate(time.Second)
	if !connected {
		// "The agents still answer" is part of the definition. A slot whose ECS agent is
		// silent belongs to the dark scan (and to #2447's blocked-phase line).
		f.clearWedgedStamp(ctx, id, raw)
		return
	}
	if now.Sub(launch) < f.pool.darkBootGrace {
		return // still inside the boot/wake grace: neither wedged nor shown healthy
	}
	// g_d, the Start count the release's locked step compares against, is read before the
	// verdict's legs so that a Start landing while they are read is seen as a moved count.
	// Only a slot already past its wait needs it, and one that carries a marker is somebody's.
	var gd int64
	canRelease := false
	if f.pool.wedgedAction == wedgedActionRelease && ec2TagValue(inst.Tags, ec2TagRescue) == "" {
		if st, ok := parseDarkStamp(raw); ok && st.launch.Equal(launch) && (st.detected || now.Sub(st.since) >= f.pool.wedgedAfter) {
			gd, canRelease = f.rescueStartCount(ctx, vol)
		}
	}
	v := f.wedgedVerdictFor(ctx, pass, id, vol, now)
	switch v.state {
	case wedgedNo:
		f.clearWedgedStamp(ctx, id, raw)
		return
	case wedgedUnknown:
		return // could not ask: neither stamp nor clear
	}

	stamp, ok := parseDarkStamp(raw)
	if !ok || !stamp.launch.Equal(launch) {
		stamp = darkStamp{since: now.UTC().Truncate(time.Second), launch: launch}
		if f.writeWedgedStamp(ctx, id, stamp) {
			log.Printf("ecs-ec2 sweep: slot %s looks wedged (%s); watching it", id, v.signal)
		}
		return
	}
	if now.Sub(stamp.since) < f.pool.wedgedAfter {
		return
	}
	d := wedgedDecision{slot: id, vol: vol, launch: launch, gd: gd, signal: v.signal, pass: pass}
	if stamp.detected {
		if canRelease {
			f.releaseWedgedSlot(ctx, d)
		}
		return
	}
	// Mark first, report second: a failed mark is retried next pass with the event still
	// unsent, and a failed report costs one audit row rather than a repeat every sweep.
	stamp.detected = true
	if !f.writeWedgedStamp(ctx, id, stamp) {
		return
	}
	ev := SlotWedged{Slot: id, Volume: aws.ToString(vol.VolumeId), Since: stamp.since, Signal: v.signal,
		MembershipID: ec2TagValue(vol.Tags, EC2TagMembership), Workspace: ec2TagValue(vol.Tags, EC2TagWorkspace)}
	log.Printf("ecs-ec2 sweep: slot %s holding %s has been wedged (%s) since %s (%.0fm); action %s",
		id, ev.Workspace, v.signal, stamp.since.Format(time.RFC3339), now.Sub(stamp.since).Minutes(), f.pool.wedgedAction)
	f.reportWedged(ctx, ev, "")
	if canRelease {
		f.releaseWedgedSlot(ctx, d)
	}
}

// reportWedged hands a wedged-slot event to the audit callback. Nothing here can fail the scan.
func (f *ecsEC2Factory) reportWedged(ctx context.Context, ev SlotWedged, action string) {
	ev.Action = action
	if f.onSlotWedged != nil {
		f.onSlotWedged(ctx, ev)
	}
}

// wedgedVerdictFor reads the slot's workspace service and tasks once and decides. Signals:
//
//   - a task of the service on this slot that has been STOPPING for longer than stopTimeout
//     plus wedgedStopGrace;
//   - the Agent probe failed while ECS reports the task on this slot HEALTHY.
//
// Everything else that is read successfully is "no" (a task stopping within its stopTimeout,
// a rolling deployment, a task not yet HEALTHY, a probe that answered). A read that fails,
// a stopping task without a time, and a probe that is unknown are "unknown".
func (f *ecsEC2Factory) wedgedVerdictFor(ctx context.Context, pass *darkProbePass, slot string, vol *ec2types.Volume, now time.Time) wedgedVerdict {
	rt := f.runtimeForVolume(vol)
	if rt == nil {
		return wedgedVerdict{}
	}
	_, ok, err := rt.base.describeService(ctx)
	if err != nil {
		log.Printf("ecs-ec2 sweep: wedged-slot scan: service of %s unknown: %v", slot, err)
		return wedgedVerdict{}
	}
	if !ok {
		f.probes.forget(slot)
		return wedgedVerdict{state: wedgedNo} // no service, nothing to be stuck; sweepVolume owns the drift
	}
	tasks, ok := f.serviceTasks(ctx, rt.base.name)
	if !ok {
		log.Printf("ecs-ec2 sweep: wedged-slot scan: tasks of %s on %s unknown", rt.base.name, slot)
		return wedgedVerdict{}
	}

	// Signal 1: an old STOPPING task on this slot. The limit does not depend on this CP's
	// current AF_STOP_GRACE_SEC: a task outlives a changed setting.
	var stuck []ecstypes.Task
	undated := false
	for _, t := range tasks {
		switch aws.ToString(t.LastStatus) {
		case "STOPPING", "DEACTIVATING", "DEPROVISIONING":
		default:
			continue
		}
		if t.StoppingAt == nil {
			undated = true // cannot tell a stuck stop from a young one
			continue
		}
		if now.Sub(*t.StoppingAt) >= wedgedStopLimit {
			stuck = append(stuck, t)
		}
	}
	if len(stuck) > 0 {
		var arns []string
		for _, t := range stuck {
			arns = append(arns, aws.ToString(t.ContainerInstanceArn))
		}
		slots, ok := f.containerInstanceSlots(ctx, arns)
		if !ok {
			return wedgedVerdict{}
		}
		for _, t := range stuck {
			if slots[aws.ToString(t.ContainerInstanceArn)] == slot {
				f.probes.forget(slot)
				return wedgedVerdict{state: wedgedYes, signal: wedgedSignalStopping}
			}
		}
	}
	if undated {
		return wedgedVerdict{}
	}

	// Signal 2: the Agent drops connections while ECS says the task is healthy. Only a task
	// that ECS reports HEALTHY is asked about; one that is starting or already unhealthy is
	// ECS's own to replace.
	var running []ecstypes.Task
	for _, t := range tasks {
		if aws.ToString(t.LastStatus) == "RUNNING" {
			running = append(running, t)
		}
	}
	if len(running) == 0 {
		f.probes.forget(slot)
		return wedgedVerdict{state: wedgedNo}
	}
	for _, t := range running {
		if t.HealthStatus != ecstypes.HealthStatusHealthy {
			f.probes.forget(slot)
			return wedgedVerdict{state: wedgedNo}
		}
	}
	// The alias cannot prove which task answered: a probe speaks for this slot only when the
	// service's tasks are all on it, and while a task is being replaced it is unknown.
	if !f.tasksRunOnlyOn(ctx, tasks, slot) {
		f.probes.forget(slot)
		return wedgedVerdict{}
	}
	res, proven := f.probeAgentTask(ctx, pass, rt.Endpoint(), runningTaskIDs(tasks))
	f.probes.set(slot, probeObservation{Result: res, At: f.darkNow(), TaskProven: proven})
	switch res {
	case AgentProbeFailed:
		return wedgedVerdict{state: wedgedYes, signal: wedgedSignalProbeFailed}
	case AgentProbeHealthy:
		return wedgedVerdict{state: wedgedNo}
	}
	log.Printf("ecs-ec2 sweep: wedged-slot scan: Agent of %s on %s probe %s", rt.base.name, slot, res)
	return wedgedVerdict{}
}

func (f *ecsEC2Factory) writeWedgedStamp(ctx context.Context, instanceID string, s darkStamp) bool {
	if _, err := f.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagWedgedSince), Value: aws.String(s.String())}},
	}); err != nil {
		log.Printf("ecs-ec2 sweep: stamping wedged slot %s failed: %v", instanceID, err)
		return false
	}
	return true
}

func (f *ecsEC2Factory) clearWedgedStamp(ctx context.Context, instanceID, current string) {
	f.wedgedBlocked.Delete(instanceID)
	if current == "" {
		return
	}
	if _, err := f.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagWedgedSince)}},
	}); err != nil {
		log.Printf("ecs-ec2 sweep: clearing the wedged stamp on %s failed: %v", instanceID, err)
	}
}

// clearWedgedStampOn is clearWedgedStamp for the finalizer, which holds a runtime and not the
// factory. It reads nothing and deletes the key whatever it holds.
func (e *ecsEC2Runtime) clearWedgedStampOn(ctx context.Context, instanceID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rescueTagBudget)
	defer cancel()
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagWedgedSince)}},
	}); err != nil {
		log.Printf("ecs-ec2: clearing the wedged stamp on %s failed: %v", instanceID, err)
	}
}
