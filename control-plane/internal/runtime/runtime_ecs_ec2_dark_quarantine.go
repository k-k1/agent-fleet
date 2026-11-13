package runtime

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Rung L2 of ADR 0121 (decision 2): a slot that goes dark again within darkRepeatWindow of
// its last episode, or whose rescue has been blocked for rescueBlockedAfter, is held out of the
// pool with a token-fenced af-quarantine tag (af-role stays `slot`). It runs only under
// AF_ECS_EC2_DARK_SLOT_ACTION=quarantine.
//
// Quarantine is a tag and nothing else. It never detaches a volume, forced or not (the forced
// detach of a blocked rescue stays disabled until Open question 5 is measured), and it never
// stops or terminates the box: the rescue ladder carries on exactly as under `rescue`, and
// the operator terminates the box when it holds no home. A slot whose service is up is never
// rescued (rescuePrepare), so it never reaches this rung either.

const (
	// ec2TagWedgeAt is the single, overwritten episode tag of earlier builds. It is read as one
	// more episode record, so a deployed box keeps its history; nothing writes it any more.
	ec2TagWedgeAt = "af-wedge-at"
	// ec2TagWedgeAtPrefix starts the key of one episode record: the prefix plus the episode's
	// `began` as episodeKeyLayout. The value is `<began RFC 3339>/<kind>`.
	ec2TagWedgeAtPrefix = "af-wedge-at-"
	episodeKeyLayout    = "20060102T150405Z"

	// Kinds of an episode record. A legacy record carries no kind.
	episodeKindDark     = "dark"
	episodeKindWedged   = "wedged"
	episodeKindOperator = "operator"
	episodeKindLegacy   = "legacy"

	// ec2TagQuarantinePrefix starts the key of one of rung L2's quarantine records, which is
	// `af-quarantine-<home volume>-<g_r>` with the value `<home volume>:<g_r>/<reason>/<RFC 3339>`.
	// One record per token, never overwritten: a single tag would let a delayed write for an
	// old token replace the current one, and the cleanup of the old one would then delete the
	// quarantine itself. The box keeps af-role=slot, so SSM and every operator action still
	// reach it. The reason has no '/' and only the characters EC2 allows in a tag value.
	ec2TagQuarantinePrefix = "af-quarantine-"

	defaultDarkRepeatWindow   = 24 * time.Hour
	defaultRescueBlockedAfter = 30 * time.Minute
)

func (p *ec2PoolConfig) repeatWindow() time.Duration {
	if p.darkRepeatWindow > 0 {
		return p.darkRepeatWindow
	}
	return defaultDarkRepeatWindow
}

func (p *ec2PoolConfig) blockedAfter() time.Duration {
	if p.rescueBlockedAfter > 0 {
		return p.rescueBlockedAfter
	}
	return defaultRescueBlockedAfter
}

// quarantineTag is the parsed af-quarantine value. Its token is the ownership token of the
// rescue that decided it: the home it was holding and that home's Start count (g_r).
type quarantineTag struct {
	Volume string
	Gen    int64
	Reason string
	At     time.Time
}

func (q quarantineTag) String() string {
	return fmt.Sprintf("%s:%d/%s/%s", q.Volume, q.Gen, q.Reason, q.At.UTC().Format(time.RFC3339))
}

// parseQuarantineTag reads a value; one that does not parse is no quarantine at all.
func parseQuarantineTag(v string) (quarantineTag, bool) {
	first := strings.Index(v, "/")
	last := strings.LastIndex(v, "/")
	if first <= 0 || last <= first {
		return quarantineTag{}, false
	}
	token := v[:first]
	colon := strings.LastIndex(token, ":")
	if colon <= 0 {
		return quarantineTag{}, false
	}
	gen, err1 := strconv.ParseInt(token[colon+1:], 10, 64)
	at, err2 := time.Parse(time.RFC3339, v[last+1:])
	if err1 != nil || err2 != nil || gen < 0 {
		return quarantineTag{}, false
	}
	return quarantineTag{Volume: token[:colon], Gen: gen, Reason: v[first+1 : last], At: at}, true
}

// quarantineRec is one af-quarantine-* record on a box.
type quarantineRec struct {
	key string
	quarantineTag
}

func quarantineKey(volume string, gen int64) string {
	return ec2TagQuarantinePrefix + volume + "-" + strconv.FormatInt(gen, 10)
}

// quarantineRecordsOf reads every record that parses, oldest decision first.
func quarantineRecordsOf(tags []ec2types.Tag) []quarantineRec {
	var out []quarantineRec
	for _, t := range tags {
		key := aws.ToString(t.Key)
		if !strings.HasPrefix(key, ec2TagQuarantinePrefix) {
			continue
		}
		if q, ok := parseQuarantineTag(aws.ToString(t.Value)); ok {
			out = append(out, quarantineRec{key: key, quarantineTag: q})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// hasQuarantineRecord is the parse-only view: whether any record is on the box. It is for the
// operator's Terminate and the status page; a reader that decides something about a slot asks
// quarantineInForce.
func hasQuarantineRecord(tags []ec2types.Tag) (quarantineTag, bool) {
	recs := quarantineRecordsOf(tags)
	if len(recs) == 0 {
		return quarantineTag{}, false
	}
	return recs[len(recs)-1].quarantineTag, true
}

// quarantineInForce is whether a record on the box is in force, for any reader, with whatever
// runtime it has. A record is in force while its token is the box's own: with no home on the
// box, any record is (it holds the box out of the pool); with a home on it, only a record that
// names that home and carries that workspace's current Start count. Every other record is void:
// a Start moved on, another home is there, or a late write for an old token. A read that
// fails keeps the box out: nothing is placed on a doubt. The count is read from the OWNER's
// runtime, whoever asks.
func (e *ecsEC2Runtime) quarantineInForce(ctx context.Context, tags []ec2types.Tag, instanceID string) (quarantineTag, bool) {
	recs := quarantineRecordsOf(tags)
	if len(recs) == 0 {
		return quarantineTag{}, false
	}
	newest := recs[len(recs)-1].quarantineTag
	holder, err := e.slotHolder(ctx, instanceID, "")
	if err != nil || holder == "" {
		return newest, true
	}
	var gen int64
	var genErr error
	haveGen := false
	for _, r := range recs {
		if r.Volume != holder {
			continue
		}
		if !haveGen {
			gen, genErr = e.holderGen(ctx, holder)
			haveGen = true
		}
		if genErr != nil || r.Gen == gen {
			return r.quarantineTag, true
		}
	}
	return quarantineTag{}, false
}

// holderGen is the Start count of the workspace that owns volume.
func (e *ecsEC2Runtime) holderGen(ctx context.Context, volumeID string) (int64, error) {
	out, err := e.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	if err != nil || len(out.Volumes) == 0 {
		return 0, fmt.Errorf("read %s: %v", volumeID, err)
	}
	rt := e.siblingFor(&out.Volumes[0])
	if rt == nil {
		return 0, fmt.Errorf("no runtime for %s", volumeID)
	}
	return rt.startGenNow(ctx)
}

// heldOut is quarantineInForce for a caller that has only the instance.
func (e *ecsEC2Runtime) heldOut(ctx context.Context, inst ec2types.Instance) bool {
	_, held := e.quarantineInForce(ctx, inst.Tags, aws.ToString(inst.InstanceId))
	return held
}

// withoutQuarantined drops the boxes held out of the pool from a DescribeInstances answer.
func (e *ecsEC2Runtime) withoutQuarantined(ctx context.Context, out *ec2.DescribeInstancesOutput) *ec2.DescribeInstancesOutput {
	kept := &ec2.DescribeInstancesOutput{}
	for _, r := range out.Reservations {
		var insts []ec2types.Instance
		for _, inst := range r.Instances {
			if _, held := e.quarantineInForce(ctx, inst.Tags, aws.ToString(inst.InstanceId)); !held {
				insts = append(insts, inst)
			}
		}
		if len(insts) > 0 {
			r.Instances = insts
			kept.Reservations = append(kept.Reservations, r)
		}
	}
	return kept
}

func withoutQuarantineRecords(tags []ec2types.Tag) []ec2types.Tag {
	var out []ec2types.Tag
	for _, t := range tags {
		if !strings.HasPrefix(aws.ToString(t.Key), ec2TagQuarantinePrefix) {
			out = append(out, t)
		}
	}
	return out
}

// carryQuarantine is the hand-over: a Start, a Stop or a rescue is about to count, and the home it
// belongs to sits on a box that a valid record holds out. The record is written again for the
// count that is about to exist, BEFORE the bump (a record for a count not yet reached is void
// until the bump), so the hold survives the bump and the Start still finds the box a mismatch
// and moves the home off it. Called under the claim lock, from the one place every count goes
// through (bumpStartCount, beginStart).
//
// It is a required step: a read or a write that fails, or a lease that is gone, is an error and
// the count is not moved, because a bump without the carry would void the hold and let the next
// Start reuse the box. Only under quarantine mode; a pool that has switched it off lets a Start
// overtake a held box, which is the setting's cost.
func (e *ecsEC2Runtime) carryQuarantine(ctx context.Context, newGen int64) error {
	if !e.pool.quarantines() {
		return nil
	}
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return fmt.Errorf("carry the quarantine: home: %w", err)
	}
	if vol == nil {
		return nil
	}
	holder, slot := aws.ToString(vol.VolumeId), attachedInstance(vol)
	if slot == "" {
		return nil
	}
	inst, err := e.slotInstance(ctx, slot)
	if err != nil {
		return fmt.Errorf("carry the quarantine: slot %s: %w", slot, err)
	}
	if inst == nil {
		return nil
	}
	for _, r := range quarantineRecordsOf(inst.Tags) {
		if r.Volume != holder || r.Gen != newGen-1 {
			continue
		}
		if err := homeGuardOf(ctx).check(); err != nil {
			return fmt.Errorf("carry the quarantine: %w", err)
		}
		next := r.quarantineTag
		next.Gen = newGen
		if _, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
			Resources: []string{slot},
			Tags:      []ec2types.Tag{{Key: aws.String(quarantineKey(holder, newGen)), Value: aws.String(next.String())}},
		}); err != nil {
			return fmt.Errorf("carry the quarantine of %s to start %d: %w", slot, newGen, err)
		}
		return nil
	}
	return nil
}

// clearVoidQuarantines deletes the records whose token is no longer the box's, for a box with a
// home on it, while holding the claim lock of that home, each value matched exactly so a newer
// record is never deleted. Readers already ignore a void record; this only keeps it from
// lingering.
func (f *ecsEC2Factory) clearVoidQuarantines(ctx context.Context, homes []ec2types.Volume) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			{Name: aws.String("tag-key"), Values: []string{ec2TagQuarantinePrefix + "*"}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		return
	}
	for _, r := range out.Reservations {
		for _, inst := range r.Instances {
			id := aws.ToString(inst.InstanceId)
			var home *ec2types.Volume
			for i := range homes {
				if attachedInstance(&homes[i]) == id {
					home = &homes[i]
				}
			}
			if home == nil {
				continue // no home on it: every record holds the box, and there is no token to compare
			}
			if rt := f.rescueRuntime(home); rt != nil {
				rt.clearVoidQuarantine(ctx, id, aws.ToString(home.VolumeId))
			}
		}
	}
}

func (e *ecsEC2Runtime) clearVoidQuarantine(ctx context.Context, id, holder string) {
	ctx, unlock, err := e.lockBounded(ctx, homeLockClaim, e.base.name, rescueClaimBudget)
	if err != nil {
		return
	}
	defer unlock()
	inst, err := e.slotInstance(ctx, id)
	if err != nil || inst == nil {
		return
	}
	g, err := e.startGenNow(ctx)
	if err != nil {
		return
	}
	// The home on the box is read again under the lock: a Start that moved the box on is why this runs.
	if v, err := e.homeVolume(ctx); err != nil || v == nil || attachedInstance(v) != id || aws.ToString(v.VolumeId) != holder {
		return
	}
	var void []ec2types.Tag
	for _, t := range inst.Tags {
		key := aws.ToString(t.Key)
		if !strings.HasPrefix(key, ec2TagQuarantinePrefix) {
			continue
		}
		if q, ok := parseQuarantineTag(aws.ToString(t.Value)); ok && q.Volume == holder && q.Gen == g {
			continue // in force
		}
		void = append(void, ec2types.Tag{Key: t.Key, Value: t.Value})
	}
	if len(void) == 0 {
		return
	}
	if err := homeGuardOf(ctx).check(); err != nil {
		return
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{Resources: []string{id}, Tags: void}); err != nil {
		log.Printf("ecs-ec2 sweep: clearing the void quarantine records on %s: %v", id, err)
		return
	}
	log.Printf("ecs-ec2 sweep: cleared %d void quarantine record(s) on %s", len(void), id)
}

// quarantines is whether rung L2 is on for this pool.
func (p *ec2PoolConfig) quarantines() bool { return p.darkAction == darkActionQuarantine }

// episodeRecord is one af-wedge-at record.
type episodeRecord struct {
	key  string
	at   time.Time
	kind string
}

func episodeKind(m rescueMarker) string {
	switch {
	case m.Wedged:
		return episodeKindWedged
	case m.Operator:
		return episodeKindOperator
	}
	return episodeKindDark
}

// episodeRecordsOf reads every record on a box: the per-episode tags and the legacy single
// tag. A value that does not parse falls back to the time in its key; one that still does not
// is not a record.
func episodeRecordsOf(tags []ec2types.Tag) []episodeRecord {
	var out []episodeRecord
	for _, t := range tags {
		key := aws.ToString(t.Key)
		switch {
		case key == ec2TagWedgeAt:
			if at, err := time.Parse(time.RFC3339, aws.ToString(t.Value)); err == nil {
				out = append(out, episodeRecord{key: key, at: at, kind: episodeKindLegacy})
			}
		case strings.HasPrefix(key, ec2TagWedgeAtPrefix):
			rec := episodeRecord{key: key, kind: episodeKindLegacy}
			stamp, kind, _ := strings.Cut(aws.ToString(t.Value), "/")
			if at, err := time.Parse(time.RFC3339, stamp); err == nil {
				rec.at = at
			} else if at, err := time.Parse(episodeKeyLayout, strings.TrimPrefix(key, ec2TagWedgeAtPrefix)); err == nil {
				rec.at = at
			} else {
				continue
			}
			if kind != "" {
				rec.kind = kind
			}
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// countsTowardDarkRepeat is whether a record of this kind makes the next DARK episode a
// repeat. Dark episodes and legacy records (written before kinds existed, by any ladder) do.
// Whether a wedged release or an operator force-stop also counts, and whether a repeat of
// the wedged predicate itself quarantines, is ADR 0121's open question of 2026-11-13 and is
// the owner's to answer: widening it is this one function.
func countsTowardDarkRepeat(kind string) bool {
	return kind == episodeKindDark || kind == episodeKindLegacy
}

// lastDarkEpisodeBefore is the maximum over the records that count and precede `before`
// (the episode being judged writes its own record only when it ends). The maximum is what
// makes a record that lands late harmless: a record can only add a time, never move one back.
func lastDarkEpisodeBefore(tags []ec2types.Tag, before time.Time) (time.Time, bool) {
	var last time.Time
	found := false
	for _, r := range episodeRecordsOf(tags) {
		if !countsTowardDarkRepeat(r.kind) || !r.at.Before(before) {
			continue
		}
		if !found || r.at.After(last) {
			last, found = r.at, true
		}
	}
	return last, found
}

// ec2TagLimit is EC2's limit of user tags on one resource.
const ec2TagLimit = 50

// recordEpisode writes the marker's episode as its own tag, `af-wedge-at-<began>`, and prunes
// the records the repeat test can no longer see. Called under the per-instance lease by the
// finalizer; tags is the box as the finalizer just read it.
//
// One record per episode, never overwritten (ADR 0121 Open question 8, option b): EC2 has no
// conditional write, and a single overwritten tag lets a CreateTags that was delayed past its
// lease land last and move the time backwards. Here a late write adds only its own key, and
// the repeat test takes the maximum.
//
// Pruning is safe for the same reason: a record older than darkRepeatWindow can never be the
// maximum of an episode that is within the window, so deleting one changes no decision.
//
// The box has EC2's 50 tags to share with every other af-* tag, so the deletes go out BEFORE
// the write: a box that is full of expired records must still be able to record. When the
// expired ones do not make room (records inside the window, or tags that are not ours, fill
// it), the oldest records are dropped anyway, which at worst loses a repeat the window would
// have seen: a missed escalation, never a wrong stop. When even that leaves no room the
// episode is logged and not recorded, so the marker still goes: a finalizer that fails the
// same way every sweep would hold the member's home for good.
func (e *ecsEC2Runtime) recordEpisode(ctx context.Context, instanceID string, tags []ec2types.Tag, m rescueMarker) error {
	recordKey := ec2TagWedgeAtPrefix + m.Began.UTC().Format(episodeKeyLayout)
	exists := ec2TagValue(tags, recordKey) != ""
	// The window is measured from now, not from this episode: a record is dead once no
	// episode that is still to begin can be within the window of it.
	cutoff := e.now().Add(-e.pool.repeatWindow())
	total := 0
	for _, t := range tags {
		if !strings.HasPrefix(aws.ToString(t.Key), "aws:") {
			total++
		}
	}
	if !exists {
		total++
	}
	needRoom := total > ec2TagLimit
	var drop []ec2types.Tag
	dropped := map[string]bool{}
	recs := episodeRecordsOf(tags) // oldest first
	for _, r := range recs {
		if r.at.Before(cutoff) && r.key != recordKey {
			drop = append(drop, ec2types.Tag{Key: aws.String(r.key)})
			dropped[r.key] = true
			total--
		}
	}
	for _, r := range recs {
		if total <= ec2TagLimit {
			break
		}
		if !dropped[r.key] && r.key != recordKey {
			drop = append(drop, ec2types.Tag{Key: aws.String(r.key)})
			dropped[r.key] = true
			total--
		}
	}
	if len(drop) > 0 {
		if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{Resources: []string{instanceID}, Tags: drop}); err != nil {
			if needRoom {
				return fmt.Errorf("making room for the episode record: %w", err)
			}
			// Best effort otherwise: a record that stays is one more row for the maximum to ignore.
			log.Printf("ecs-ec2: pruning the old episode records on %s: %v", instanceID, err)
		}
	}
	if exists {
		return nil
	}
	if total > ec2TagLimit {
		log.Printf("ecs-ec2: %s carries %d tags and no episode record can be dropped; the episode of %s is not recorded",
			instanceID, total, m.Began.UTC().Format(time.RFC3339))
		return nil
	}
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{{Key: aws.String(recordKey),
			Value: aws.String(m.Began.UTC().Format(time.RFC3339) + "/" + episodeKind(m))}},
	})
	return err
}

// quarantineDecision says why a box is to be quarantined, or that it is not.
type quarantineDecision func(inst *ec2types.Instance) (reason string, ok bool)

// repeatDecision is the first trigger of L2: this dark episode began within the window of the
// last dark episode on the box.
func (e *ecsEC2Runtime) repeatDecision(m rescueMarker) quarantineDecision {
	return func(inst *ec2types.Instance) (string, bool) {
		last, ok := lastDarkEpisodeBefore(inst.Tags, m.Began)
		if !ok || m.Began.Sub(last) > e.pool.repeatWindow() {
			return "", false
		}
		return fmt.Sprintf("repeat: dark again within %s of %s",
			e.pool.repeatWindow(), last.UTC().Format(time.RFC3339)), true
	}
}

// blockedDecision is the second trigger: the rescue has been blocked for rescueBlockedAfter.
func (e *ecsEC2Runtime) blockedDecision(m rescueMarker) quarantineDecision {
	return func(*ec2types.Instance) (string, bool) {
		if m.BlockedSince.IsZero() || e.now().Sub(m.BlockedSince) < e.pool.blockedAfter() {
			return "", false
		}
		return fmt.Sprintf("rescue-blocked: phase %s blocked since %s for over %s",
			m.Phase, m.BlockedSince.UTC().Format(time.RFC3339), e.pool.blockedAfter()), true
	}
}

// quarantineApplies is whether this marker's episode may reach L2 at all: the pool runs
// quarantine mode and the marker is a dark rescue's. The wedged release and the operator's
// force-stop never quarantine (the first is the open question named at countsTowardDarkRepeat).
func (e *ecsEC2Runtime) quarantineApplies(m rescueMarker) bool {
	return e.pool.quarantines() && !m.Wedged && !m.Operator
}

// rescueQuarantine writes the quarantine tag if decide says so, inside the rescue's guarded
// section: the mount lock outer, the token re-verified under the claim lock inside it, both
// leases asked right before the write, and the box re-read there.
//
// The write itself cannot be fenced: an EC2 tag write is unconditional, and a request already
// sent can land after the leases are gone, after the marker is finalized, after a Start moved
// the box on, or after an operator cleared the tag. So the tag carries the ownership token of
// this rescue (the home and its Start count g_r) and every reader fences on it: a quarantine
// whose token is no longer the box's is void and is ignored (quarantineInForce), then deleted
// (clearVoidQuarantine). A late write therefore cannot hold a box that a Start has moved on to;
// at worst it holds an idle box with no home, which an operator clears by deleting the tag.
// That is why L2 leaves af-role alone: a role write that outlives its owner would also cut
// the box off from SSM.
//
// A box that already carries this rescue's quarantine is left alone and reported nothing: the
// decision is once per box, not once per pass.
func (e *ecsEC2Runtime) rescueQuarantine(ctx context.Context, slot string, m *rescueMarker, decide quarantineDecision) error {
	reason := ""
	err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
		inst, err := e.readRescueBox(c, slot, m)
		if err != nil {
			return err
		}
		if ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot {
			return nil // another kind of quarantine, or not a slot: nothing to add
		}
		if ec2TagValue(inst.Tags, quarantineKey(m.Volume, m.Gen)) != "" {
			return nil
		}
		why, ok := decide(inst)
		if !ok {
			return nil
		}
		if err := rescueStep(c, "quarantining"); err != nil {
			return err
		}
		if err := rescueLeasesHeld(c); err != nil {
			return fmt.Errorf("%w: %w", errRescueAborted, err)
		}
		q := quarantineTag{Volume: m.Volume, Gen: m.Gen, Reason: why, At: e.now()}
		if err := e.writeDarkQuarantine(c, slot, q); err != nil {
			return fmt.Errorf("%w: quarantine %s: %w", errRescueAborted, slot, err)
		}
		reason = why
		return nil
	})
	if err != nil {
		return err
	}
	if reason != "" {
		log.Printf("ecs-ec2 sweep: QUARANTINED %s holding %s: %s", slot, e.base.name, reason)
		e.reportRescue(ctx, SlotDark{Slot: slot, Volume: m.Volume, Since: m.Began, Workspace: e.base.name,
			MembershipID: e.base.membershipID, Reason: reason}, "quarantined")
	}
	return nil
}

// writeDarkQuarantine writes the af-quarantine tag and nothing else: the role stays `slot`.
func (e *ecsEC2Runtime) writeDarkQuarantine(ctx context.Context, instanceID string, q quarantineTag) error {
	if len(q.Reason) > 150 {
		q.Reason = q.Reason[:150]
	}
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      []ec2types.Tag{{Key: aws.String(quarantineKey(q.Volume, q.Gen)), Value: aws.String(q.String())}},
	})
	return err
}

// rescueRepeatStage is trigger one, run between `stopped` and the detach: the slot leaves
// the free list while it still holds the home, so no other Start can be handed it in the
// moment after the detach.
func (e *ecsEC2Runtime) rescueRepeatStage(ctx context.Context, slot string, m *rescueMarker) error {
	if !e.quarantineApplies(*m) {
		return nil
	}
	return e.rescueQuarantine(ctx, slot, m, e.repeatDecision(*m))
}

// rescueBlockedQuarantine is trigger two, run after a pass ended blocked.
func (e *ecsEC2Runtime) rescueBlockedQuarantine(ctx context.Context, slot string, m *rescueMarker) {
	if !e.quarantineApplies(*m) || m.BlockedSince.IsZero() {
		return
	}
	if err := e.rescueQuarantine(ctx, slot, m, e.blockedDecision(*m)); err != nil {
		log.Printf("ecs-ec2 sweep: quarantine of %s on %s not written this pass: %v", e.base.name, slot, err)
	}
}
