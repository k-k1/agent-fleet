package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// The rescue marker and the gates that read it (ADR 0121 decisions 3 and 4). Nothing in
// production writes the marker yet: the fences have to exist before the rescue that arms
// them, so that the first marker ever written meets a releaser that already refuses.

const (
	// ec2TagRescue is `<home volume id>/<launch epoch>/<phase>/<g_r>/<began>/<blocked_since>[/wedged]`,
	// an instance tag written before a rescue's first side effect. `blocked_since` is empty
	// until a step first fails its bound.
	ec2TagRescue = "af-rescue"

	// rescuePhasePreparing is the marker as first written, before the Start count was bumped
	// and read back. Its Gen is a prediction: a member's Start after a crash can reach the
	// same number, so a count equal to Gen proves nothing until the rescue has confirmed its
	// own bump by advancing the marker to begun. Never resumable, only finalized.
	rescuePhasePreparing     rescuePhase = "preparing"
	rescuePhaseBegun         rescuePhase = "begun"
	rescuePhaseDeregistering rescuePhase = "deregistering"
	rescuePhaseDeregistered  rescuePhase = "deregistered"
	rescuePhaseStopping      rescuePhase = "stopping"
	rescuePhaseStopped       rescuePhase = "stopped"
	rescuePhaseDetaching     rescuePhase = "detaching"
	rescuePhaseDetached      rescuePhase = "detached"
)

type rescuePhase string

// valid reports whether p is one of the phases of decision 4 (plus preparing).
func (p rescuePhase) valid() bool {
	switch p {
	case rescuePhasePreparing, rescuePhaseBegun, rescuePhaseDeregistering, rescuePhaseDeregistered,
		rescuePhaseStopping, rescuePhaseStopped, rescuePhaseDetaching, rescuePhaseDetached:
		return true
	}
	return false
}

// rank orders the phases by progress; an unknown phase ranks below all of them.
func (p rescuePhase) rank() int {
	for i, q := range []rescuePhase{rescuePhasePreparing, rescuePhaseBegun, rescuePhaseDeregistering, rescuePhaseDeregistered,
		rescuePhaseStopping, rescuePhaseStopped, rescuePhaseDetaching, rescuePhaseDetached} {
		if p == q {
			return i
		}
	}
	return -1
}

// rescueMarker is the parsed af-rescue value.
type rescueMarker struct {
	Volume string
	// Epoch is the box's LaunchTime when the rescue began: an operator's restart from the EC2
	// console moves it and not the Start count.
	Epoch time.Time
	Phase rescuePhase
	// Gen is g_r, the Start count the rescue's own bump produced.
	Gen          int64
	Began        time.Time
	BlockedSince time.Time // zero until a step first fails its bound
	// Wedged is set by the wedged-slot release (ADR 0121, amendment of 2026-11-12): the same
	// token, a different ladder (umount and detach, never deregister or stop). Written as a
	// seventh field only when set, so an older CP that meets one cannot parse it and fences
	// the home instead of running the dark ladder on it.
	Wedged bool
	// Operator is set by the operator force-stop: the same token, carried by the handler
	// that wrote it, never by the sweeper's resume scan (which leaves a young one alone and
	// only finalizes one that has aged). Written as the seventh field like Wedged.
	Operator bool
}

const (
	rescueKindWedged   = "wedged"
	rescueKindOperator = "operator"
)

func (m rescueMarker) String() string {
	blocked := ""
	if !m.BlockedSince.IsZero() {
		blocked = m.BlockedSince.UTC().Format(time.RFC3339)
	}
	fields := []string{
		m.Volume,
		m.Epoch.UTC().Format(time.RFC3339),
		string(m.Phase),
		strconv.FormatInt(m.Gen, 10),
		m.Began.UTC().Format(time.RFC3339),
		blocked,
	}
	switch {
	case m.Wedged:
		fields = append(fields, rescueKindWedged)
	case m.Operator:
		fields = append(fields, rescueKindOperator)
	}
	return strings.Join(fields, "/")
}

// parseRescueMarker reads a marker. A value that does not parse is not a marker the caller
// may act on; releaseGate still treats it as a fence for the volume it names.
func parseRescueMarker(v string) (rescueMarker, bool) {
	parts := strings.Split(v, "/")
	if (len(parts) != 6 && len(parts) != 7) || parts[0] == "" || (len(parts) == 7 && parts[6] != rescueKindWedged && parts[6] != rescueKindOperator) {
		return rescueMarker{}, false
	}
	epoch, err1 := time.Parse(time.RFC3339, parts[1])
	began, err2 := time.Parse(time.RFC3339, parts[4])
	gen, err3 := strconv.ParseInt(parts[3], 10, 64)
	m := rescueMarker{Volume: parts[0], Epoch: epoch, Phase: rescuePhase(parts[2]), Gen: gen, Began: began, Wedged: len(parts) == 7 && parts[6] == rescueKindWedged, Operator: len(parts) == 7 && parts[6] == rescueKindOperator}
	if err1 != nil || err2 != nil || err3 != nil || gen < 0 || !m.Phase.valid() {
		return rescueMarker{}, false
	}
	if parts[5] != "" {
		blocked, err := time.Parse(time.RFC3339, parts[5])
		if err != nil {
			return rescueMarker{}, false
		}
		m.BlockedSince = blocked
	}
	return m, true
}

// sameRescue is "this is the rescue that wrote that marker": the phase and the blocked time
// move as the rescue advances, the volume, the epoch and the token do not.
func (m rescueMarker) sameRescue(o rescueMarker) bool {
	return m.Volume == o.Volume && m.Epoch.Equal(o.Epoch) && m.Gen == o.Gen
}

// ErrReleaseFenced is why a releaseSlot did not run: a rescue owns the home's slot, or
// whether one does could not be read.
var ErrReleaseFenced = errors.New("a dark-slot rescue holds this home; not releasing its slot")

// Who may release a slot under a rescue marker. The default is "nobody": the many automatic
// releasers (eviction, makeRoom, hibernation, the sweeper, the baker) stay unmodified and
// are refused, and the few callers the ADR exempts opt in on the context. Failing closed
// this way means a releaser added later is fenced until somebody argues it should not be.
type (
	homeOwnerKey  struct{}
	rescueCallKey struct{}
)

// asHomeOwner marks ctx as an action of the workspace's own owner: its Start
// (releaseAndReplace, replaceReservedSlot) or its Destroy / EraseHome. Those move the Start
// count, which is how they overtake a rescue (decision 4), so a marker does not refuse them.
func asHomeOwner(ctx context.Context) context.Context {
	return context.WithValue(ctx, homeOwnerKey{}, true)
}

// asRescue marks ctx as the rescue that wrote m; only that rescue's own releaseSlot passes
// its own marker.
func asRescue(ctx context.Context, m rescueMarker) context.Context {
	return context.WithValue(ctx, rescueCallKey{}, m)
}

// rescueMarkersOf lists the markers on this pool's live boxes. A value that does not parse
// is returned raw so the caller can still fence on the volume it names.
func (e *ecsEC2Runtime) rescueMarkersOf(ctx context.Context) ([]string, error) {
	var out []string
	var next *string
	for {
		res, err := e.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{
				tagFilter(EC2TagPool, e.pool.pool),
				{Name: aws.String("tag-key"), Values: []string{ec2TagRescue}},
				{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
			},
			NextToken: next,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range res.Reservations {
			for _, inst := range r.Instances {
				if v := ec2TagValue(inst.Tags, ec2TagRescue); v != "" {
					out = append(out, v)
				}
			}
		}
		if next = res.NextToken; next == nil || *next == "" {
			return out, nil
		}
	}
}

// releaseGate is rule 2 of ADR 0121 decision 3: refuse while a rescue marker exists for the
// home, unless the caller is the home's owner or that rescue. It runs at the destructive
// entry, under the home's mount lock, so a releaser that picked its victim before the rescue
// began meets the marker here and a rescue that writes its marker later finds the home gone.
//
// A failed read refuses. "Could not ask" is not "no marker": allowing on an error would let
// an AWS throttle open the fence exactly when the box is wedged and the API is slow. The
// refusal costs a retry, because every automatic releaser runs again on its next pass and the
// owner's own actions are not gated at all.
func (e *ecsEC2Runtime) releaseGate(ctx context.Context, volumeID string) error {
	if ctx.Value(homeOwnerKey{}) != nil {
		return nil
	}
	values, err := e.rescueMarkersOf(ctx)
	if err != nil {
		return fmt.Errorf("%w: could not read the rescue markers of %s: %w", ErrReleaseFenced, volumeID, err)
	}
	self, isRescue := ctx.Value(rescueCallKey{}).(rescueMarker)
	for _, v := range values {
		m, ok := parseRescueMarker(v)
		if !ok {
			if strings.HasPrefix(v, volumeID+"/") {
				return fmt.Errorf("%w: unreadable marker %q names %s", ErrReleaseFenced, v, volumeID)
			}
			continue
		}
		if m.Volume != volumeID {
			continue
		}
		if isRescue && self.sameRescue(m) {
			continue
		}
		return fmt.Errorf("%w: %s is in phase %s since %s", ErrReleaseFenced, volumeID, m.Phase, m.Began.UTC().Format(time.RFC3339))
	}
	return nil
}
