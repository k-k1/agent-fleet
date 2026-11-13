package runtime

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Rung L2 of ADR 0121 (decision 2): a slot that goes dark again within darkRepeatWindow of
// its last episode, or whose rescue has been blocked for rescueBlockedAfter, is tagged
// af-role=quarantined. It runs only under AF_ECS_EC2_DARK_SLOT_ACTION=quarantine.
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

	// quarantineReasonPrefix marks the reason of an L2 quarantine, which is how the pool view
	// tells it from a mount-failure quarantine without a tag key the IAM fence does not list.
	// The reason uses only the characters EC2 allows in a tag value (letters, digits, spaces
	// and _ . : / = + - @).
	quarantineReasonPrefix = "dark-slot "

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

// isDarkQuarantine is whether the box is quarantined by rung L2 (not by a failed mount).
func isDarkQuarantine(tags []ec2types.Tag) bool {
	return ec2TagValue(tags, EC2TagRole) == ec2RoleQuarantined &&
		strings.HasPrefix(ec2TagValue(tags, ec2TagQuarantineReason), quarantineReasonPrefix)
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
		return fmt.Sprintf("%srepeat: dark again within %s of %s", quarantineReasonPrefix,
			e.pool.repeatWindow(), last.UTC().Format(time.RFC3339)), true
	}
}

// blockedDecision is the second trigger: the rescue has been blocked for rescueBlockedAfter.
func (e *ecsEC2Runtime) blockedDecision(m rescueMarker) quarantineDecision {
	return func(*ec2types.Instance) (string, bool) {
		if m.BlockedSince.IsZero() || e.now().Sub(m.BlockedSince) < e.pool.blockedAfter() {
			return "", false
		}
		return fmt.Sprintf("%srescue-blocked: phase %s blocked since %s for over %s", quarantineReasonPrefix,
			m.Phase, m.BlockedSince.UTC().Format(time.RFC3339), e.pool.blockedAfter()), true
	}
}

// quarantineApplies is whether this marker's episode may reach L2 at all: the pool runs
// quarantine mode and the marker is a dark rescue's. The wedged release and the operator's
// force-stop never quarantine (the first is the open question named at countsTowardDarkRepeat).
func (e *ecsEC2Runtime) quarantineApplies(m rescueMarker) bool {
	return e.pool.quarantines() && !m.Wedged && !m.Operator
}

// Bounds of the quarantine write (variables only so a test can shorten them).
var (
	// quarantineWriteBudget caps the one CreateTags (and so how long it can be in flight).
	quarantineWriteBudget = 10 * time.Second
	// quarantineSettle is how long a write whose outcome is unknown is waited out.
	quarantineSettle = 5 * time.Second
)

// rescueQuarantine writes the quarantine tags if decide says so, inside the rescue's guarded
// section: the mount lock outer, the token re-verified under the claim lock inside it, both
// leases asked right before the write, and the box re-read there.
//
// This is NOT a fence, and nothing here can make it one. An EC2 tag write cannot be
// conditional, and a request that was already sent cannot be taken back or bounded by a
// client-side deadline: a late one can land after the leases are gone, after another replica
// finalized the marker, after an operator returned the slot, and onto a slot that has since
// been given a live home. The deadline, the missing SDK retry and the uncancellable settle make
// that unlikely; they do not make it impossible.
//
// What a late write leaves is a running, healthy box that reads quarantined with a live home
// and no rescue marker. Nothing in the product recovers that by itself: a Start cannot re-place
// the home (releasing it needs SSM, which the role is not granted on a quarantined box), the
// operator force-stop needs a marker, and Terminate refuses a box with a home. Recovery is
// manual, with the operator's own credentials (guide, "Return it to service"). Whether that
// residual is acceptable before `quarantine` is enabled on a deployment is the owner's decision.
//
// A box that is already quarantined is left alone and reported nothing: the decision is
// once per box, not once per pass.
func (e *ecsEC2Runtime) rescueQuarantine(ctx context.Context, slot string, m *rescueMarker, decide quarantineDecision) error {
	reason := ""
	err := e.rescueGuarded(ctx, m, slot, func(c context.Context) error {
		inst, err := e.readRescueBox(c, slot, m)
		if err != nil {
			return err
		}
		if ec2TagValue(inst.Tags, EC2TagRole) != ec2RoleSlot {
			return nil // quarantined already, or not a slot: nothing to move
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
		deadline := e.now().Add(quarantineWriteBudget)
		for _, g := range []*homeLockGuard{rescueMountGuard(c), homeGuardOf(c)} {
			if d := g.deadline(); !d.IsZero() && d.Before(deadline) {
				deadline = d
			}
		}
		wctx, cancel := context.WithDeadline(c, deadline)
		err = e.writeDarkQuarantine(wctx, slot, why)
		cancel()
		if err != nil {
			// Outcome unknown. The wait must not die with the pass: that is exactly when it is needed.
			sctx, scancel := context.WithTimeout(context.WithoutCancel(c), 2*quarantineSettle)
			_ = e.sleep(sctx, quarantineSettle)
			scancel()
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

// writeDarkQuarantine is markQuarantined's write with the failure handed back, and none of
// its owner-tag clearing: the home is still on the box when this runs, and the detach's own
// bookkeeping (afterDetach) removes the owner.
func (e *ecsEC2Runtime) writeDarkQuarantine(ctx context.Context, instanceID, reason string) error {
	if len(reason) > 200 {
		reason = reason[:200]
	}
	_, err := e.ec2.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags: []ec2types.Tag{
			{Key: aws.String(EC2TagRole), Value: aws.String(ec2RoleQuarantined)},
			{Key: aws.String(ec2TagQuarantineReason), Value: aws.String(reason)},
			{Key: aws.String(ec2TagQuarantineAt), Value: aws.String(e.now().UTC().Format(time.RFC3339))},
		},
	}, func(o *ec2.Options) { o.RetryMaxAttempts = 1 })
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
