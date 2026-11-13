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

	// ec2TagQuarantine is rung L2's quarantine: `<home volume>:<g_r>/<reason>/<RFC 3339>`. The
	// box keeps af-role=slot, so SSM and every operator action still reach it. The reason has
	// no '/' and only the characters EC2 allows in a tag value.
	ec2TagQuarantine = "af-quarantine"

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

func quarantineOf(tags []ec2types.Tag) (quarantineTag, bool) {
	v := ec2TagValue(tags, ec2TagQuarantine)
	if v == "" {
		return quarantineTag{}, false
	}
	return parseQuarantineTag(v)
}

// slotHeldOut is what a free-slot walk asks: does the box carry a quarantine that parses. A
// walk looks at slots with no home on them, where a quarantine cannot have been overtaken, so
// parsing is the whole test. (A slot with a home goes through quarantineHolds.) The role stays
// `slot`, so this has to be asked wherever a walk would otherwise pick, stop, replace or
// terminate the box.
func slotHeldOut(inst ec2types.Instance) bool {
	_, ok := quarantineOf(inst.Tags)
	return ok
}

// withoutQuarantinedSlots drops quarantined boxes from a DescribeInstances answer.
func withoutQuarantinedSlots(out *ec2.DescribeInstancesOutput) *ec2.DescribeInstancesOutput {
	kept := &ec2.DescribeInstancesOutput{}
	for _, r := range out.Reservations {
		var insts []ec2types.Instance
		for _, inst := range r.Instances {
			if !slotHeldOut(inst) {
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

// quarantineHolds is whether q is a quarantine in force for the box, asked by a runtime that
// knows its own home. A quarantine is valid while its token is the box's current ownership
// token: with no home on the box it is valid (it holds the box out of the pool); with a home
// on it, only if that is the home the token names and that home's Start count has not moved.
// Anything else is void: a later Start, a return, a re-placement or a late write onto a box that
// has moved on. A read that fails keeps the box out (nothing is placed on a doubt).
func (e *ecsEC2Runtime) quarantineHolds(ctx context.Context, q quarantineTag, instanceID string) bool {
	holder, err := e.slotHolder(ctx, instanceID, "")
	if err != nil || holder == "" {
		return true
	}
	if holder != q.Volume {
		return false
	}
	vol, err := e.homeVolume(ctx)
	if err != nil {
		return true
	}
	if vol == nil || aws.ToString(vol.VolumeId) != holder {
		return true // another workspace's home: its count is not ours to read
	}
	g, err := e.startGenNow(ctx)
	return err != nil || g == q.Gen
}

// clearVoidQuarantines deletes a quarantine whose token is no longer the box's, while holding
// the claim lock of the home on it, with the value matched exactly so a newer one is never
// deleted. Readers already ignore a void tag; this only keeps it from lingering.
func (f *ecsEC2Factory) clearVoidQuarantines(ctx context.Context, homes []ec2types.Volume) {
	out, err := f.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			tagFilter(EC2TagPool, f.pool.pool),
			{Name: aws.String("tag-key"), Values: []string{ec2TagQuarantine}},
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
				continue // no home on it: a quarantine holds the box, and there is no token to compare
			}
			rt := f.rescueRuntime(home)
			if rt == nil {
				continue
			}
			rt.clearVoidQuarantine(ctx, id, aws.ToString(home.VolumeId))
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
	raw := ec2TagValue(inst.Tags, ec2TagQuarantine)
	if raw == "" {
		return
	}
	if q, ok := parseQuarantineTag(raw); ok {
		g, err := e.startGenNow(ctx)
		if err != nil || (q.Volume == holder && g == q.Gen) {
			return // still in force (or unreadable)
		}
	}
	// The home on the box is read again under the lock: a Start that moved the box on is why this runs.
	if v, err := e.homeVolume(ctx); err != nil || v == nil || attachedInstance(v) != id {
		return
	}
	if err := homeGuardOf(ctx).check(); err != nil {
		return
	}
	if _, err := e.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{id},
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagQuarantine), Value: aws.String(raw)}},
	}); err != nil {
		log.Printf("ecs-ec2 sweep: clearing the void quarantine on %s: %v", id, err)
		return
	}
	log.Printf("ecs-ec2 sweep: cleared the void quarantine %q on %s", raw, id)
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
// whose token is no longer the box's is void and is ignored (quarantineHolds), then deleted
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
		if cur, ok := quarantineOf(inst.Tags); ok && cur.Volume == m.Volume && cur.Gen == m.Gen {
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
		Tags:      []ec2types.Tag{{Key: aws.String(ec2TagQuarantine), Value: aws.String(q.String())}},
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
