package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func rescueFixture() rescueMarker {
	return rescueMarker{
		Volume: "vol-bob",
		Epoch:  time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC),
		Phase:  rescuePhaseBegun,
		Gen:    7,
		Began:  time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC),
	}
}

func TestRescueMarkerRoundTrip(t *testing.T) {
	for _, phase := range []rescuePhase{rescuePhaseBegun, rescuePhaseDeregistering, rescuePhaseDeregistered,
		rescuePhaseStopping, rescuePhaseStopped, rescuePhaseDetaching, rescuePhaseDetached} {
		for _, blocked := range []time.Time{{}, time.Date(2026, 11, 12, 2, 5, 0, 0, time.UTC)} {
			m := rescueFixture()
			m.Phase, m.BlockedSince = phase, blocked
			got, ok := parseRescueMarker(m.String())
			if !ok || got != m {
				t.Fatalf("%s blocked=%v: round trip gave %+v ok=%v from %q", phase, blocked, got, ok, m.String())
			}
		}
	}
	if got := rescueFixture().String(); got != "vol-bob/2026-11-12T01:02:03Z/begun/7/2026-11-12T02:00:00Z/" {
		t.Fatalf("wire format changed: %q", got)
	}
}

func TestRescueMarkerMalformedIsRejected(t *testing.T) {
	good := rescueFixture().String()
	for name, v := range map[string]string{
		"empty":           "",
		"five segments":   "vol-bob/2026-11-12T01:02:03Z/begun/7/2026-11-12T02:00:00Z",
		"seven segments":  good + "/x",
		"no volume":       "/2026-11-12T01:02:03Z/begun/7/2026-11-12T02:00:00Z/",
		"bad epoch":       "vol-bob/yesterday/begun/7/2026-11-12T02:00:00Z/",
		"unknown phase":   "vol-bob/2026-11-12T01:02:03Z/rescued/7/2026-11-12T02:00:00Z/",
		"bad gen":         "vol-bob/2026-11-12T01:02:03Z/begun/seven/2026-11-12T02:00:00Z/",
		"negative gen":    "vol-bob/2026-11-12T01:02:03Z/begun/-1/2026-11-12T02:00:00Z/",
		"bad began":       "vol-bob/2026-11-12T01:02:03Z/begun/7/soon/",
		"bad blocked":     "vol-bob/2026-11-12T01:02:03Z/begun/7/2026-11-12T02:00:00Z/later",
		"phase upper-cas": "vol-bob/2026-11-12T01:02:03Z/Begun/7/2026-11-12T02:00:00Z/",
	} {
		if m, ok := parseRescueMarker(v); ok {
			t.Errorf("%s: %q parsed as %+v", name, v, m)
		}
	}
}

// markedDormant is bob's dormant home on i-a (idle 2h, service at desired 0) with a rescue
// marker on i-a, and a pool with room for nobody else.
func markedDormant(t *testing.T) *ec2Harness {
	t.Helper()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", false, false)
	h.ci.registered["i-a"] = true
	h.ec2.addHomeVolume("vol-bob", "M-BOB", "af-ws-bob", "ap-northeast-1a")
	h.ec2.attach("vol-bob", "i-a", time.Now().Add(-3*time.Hour))
	h.ec2.setTag("vol-bob", EC2TagIdleSince, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))
	h.ecs.services["af-ws-bob"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	h.ec2.setInstanceTag("i-a", ec2TagRescue, rescueFixture().String())
	h.rt.base.name, h.rt.base.membershipID = "af-ws-bob", "M-BOB"
	return h
}

func stillOnTheBox(t *testing.T, h *ec2Harness) {
	t.Helper()
	if got := attachedInstance(h.ec2.volumes["vol-bob"]); got != "i-a" {
		t.Fatalf("bob's home is on %q; a rescue marker must keep every automatic releaser off it", got)
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "DetachVolume") || strings.HasPrefix(c, "TerminateInstances") || strings.HasPrefix(c, "SSM af-umount") {
			t.Fatalf("destructive call under a marker: %v", h.ec2.calls)
		}
	}
}

func TestRescueMarkerRefusesEviction(t *testing.T) {
	h := markedDormant(t)
	h.rt.base.name, h.rt.base.membershipID = "af-ws-dave", "M-DAVE"
	if _, _, err := h.rt.evictLongestIdle(context.Background(), "ap-northeast-1a"); !errors.Is(err, ErrReleaseFenced) {
		t.Fatalf("evictLongestIdle err = %v, want ErrReleaseFenced", err)
	}
	stillOnTheBox(t, h)
}

func TestRescueMarkerRefusesMakeRoom(t *testing.T) {
	h := markedDormant(t)
	h.rt.base.name, h.rt.base.membershipID = "af-ws-dave", "M-DAVE"
	h.rt.instanceType = "m7i.xlarge"
	if _, err := h.rt.makeRoom(context.Background()); !errors.Is(err, ErrReleaseFenced) {
		t.Fatalf("makeRoom err = %v, want ErrReleaseFenced", err)
	}
	stillOnTheBox(t, h)
}

func TestRescueMarkerRefusesHibernation(t *testing.T) {
	h := markedDormant(t)
	h.rt.pool.hibernateAfter = 24 * time.Hour
	// hibernate swallows the refusal and retries on the next pass; what matters is the home.
	_ = h.rt.hibernate(context.Background())
	if len(h.ec2.snapshots) != 0 {
		t.Fatal("a snapshot was started for a home a rescue holds")
	}
	stillOnTheBox(t, h)
}

func TestRescueMarkerRefusesTheSweepersDriftAndTerminateStages(t *testing.T) {
	ctx := context.Background()
	// Terminate stage: dormant past slotTerminateAfter.
	h := markedDormant(t)
	h.rt.pool.slotTerminateAfter = time.Hour
	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-bob"])
	stillOnTheBox(t, h)

	// Drift stage: attached, but the workspace has no service at all.
	h = markedDormant(t)
	delete(h.ecs.services, "af-ws-bob")
	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-bob"])
	stillOnTheBox(t, h)
}

// The control: the same two sweeps release and terminate when there is no marker, so the
// refusals above are the marker's doing.
func TestRescueMarkerAbsentLetsTheSweeperRelease(t *testing.T) {
	ctx := context.Background()
	h := markedDormant(t)
	h.ec2.setInstanceTag("i-a", ec2TagRescue, "")
	if _, err := h.ec2.DeleteTags(ctx, &ec2.DeleteTagsInput{Resources: []string{"i-a"}, Tags: []ec2types.Tag{{Key: aws.String(ec2TagRescue)}}}); err != nil {
		t.Fatal(err)
	}
	h.rt.pool.slotTerminateAfter = time.Hour
	h.factory().sweepVolume(ctx, h.ec2.volumes["vol-bob"])
	if got := terminatedInstances(h); len(got) != 1 {
		t.Fatalf("TerminateInstances = %v, want [i-a]", got)
	}
}

func TestRescueMarkerForAnotherHomeDoesNotFence(t *testing.T) {
	h := markedDormant(t)
	other := rescueFixture()
	other.Volume = "vol-carol"
	h.ec2.setInstanceTag("i-a", ec2TagRescue, other.String())
	if err := h.rt.releaseSlot(context.Background()); err != nil {
		t.Fatalf("a marker naming another volume refused bob's release: %v", err)
	}
	if attachedInstance(h.ec2.volumes["vol-bob"]) != "" {
		t.Fatal("bob's home is still attached")
	}
}

func TestRescueMarkerDoesNotRefuseTheOwnersOwnActions(t *testing.T) {
	ctx := context.Background()
	h := markedDormant(t)
	if err := h.rt.releaseSlot(asHomeOwner(ctx)); err != nil {
		t.Fatalf("the owner's own release was refused: %v", err)
	}
	if attachedInstance(h.ec2.volumes["vol-bob"]) != "" {
		t.Fatal("the owner's release did not detach")
	}
}

// The occupant's own Start, size mismatch: placeHome → releaseAndReplace. Not refused.
func TestRescueMarkerDoesNotRefuseTheOccupantsStart(t *testing.T) {
	ctx := context.Background()
	h := markedDormant(t)
	h.rt.pool.maxSlots = 4
	h.rt.instanceType = "m7i.xlarge" // bob's slot is a large: mismatch
	h.ec2.addSlot("i-new", "ap-northeast-1a", "m7i.xlarge", true, false)

	p, err := h.rt.placeHome(ctx)
	if err != nil {
		t.Fatalf("placeHome: %v", err)
	}
	if !p.deferred || p.releaseFrom != "i-a" {
		t.Fatalf("placeHome = %+v, want a deferred placement that releases i-a", p)
	}
	if p.gen, err = h.rt.beginStart(ctx); err != nil {
		t.Fatal(err)
	}
	next, ok := h.rt.releaseAndReplace(ctx, p)
	if !ok {
		t.Fatalf("the occupant's own Start was refused by the marker: %q", h.rt.BootPhase())
	}
	if next.instanceID != "i-new" {
		t.Fatalf("placed on %q, want i-new", next.instanceID)
	}
}

// replaceReservedSlot's release (moveHomeOff) is the occupant's own Start too.
func TestRescueMarkerDoesNotRefuseMoveHomeOff(t *testing.T) {
	h := markedDormant(t)
	if err := h.rt.moveHomeOff(context.Background(), "i-a"); err != nil {
		t.Fatalf("moveHomeOff was refused by the marker: %v", err)
	}
}

func TestRescueMarkerPassesOnlyItsOwnRescue(t *testing.T) {
	ctx := context.Background()
	h := markedDormant(t)
	stranger := rescueFixture()
	stranger.Gen++
	if err := h.rt.releaseSlot(asRescue(ctx, stranger)); !errors.Is(err, ErrReleaseFenced) {
		t.Fatalf("another rescue's identity opened the gate: %v", err)
	}
	stillOnTheBox(t, h)

	own := rescueFixture()
	own.Phase = rescuePhaseDetaching // phase moves as the rescue advances; identity does not
	if err := h.rt.releaseSlot(asRescue(ctx, own)); err != nil {
		t.Fatalf("the rescue's own release was refused: %v", err)
	}
}

// An unreadable marker list is refused, not allowed.
func TestRescueMarkerReadFailureRefuses(t *testing.T) {
	h := markedDormant(t)
	h.ec2.setInstanceTag("i-a", ec2TagRescue, "")
	h.rt.ec2 = failingMarkerRead{ec2API: h.ec2}
	err := h.rt.releaseSlot(context.Background())
	if !errors.Is(err, ErrReleaseFenced) {
		t.Fatalf("err = %v, want ErrReleaseFenced", err)
	}
	if attachedInstance(h.ec2.volumes["vol-bob"]) != "i-a" {
		t.Fatal("detached although the marker read failed")
	}
}

// failingMarkerRead fails only the marker listing.
type failingMarkerRead struct{ ec2API }

func (f failingMarkerRead) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, o ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	for _, fl := range in.Filters {
		if aws.ToString(fl.Name) == "tag-key" {
			return nil, errors.New("throttled")
		}
	}
	return f.ec2API.DescribeInstances(ctx, in, o...)
}

// Rule 1, second half.
func TestReleaseWaitsOutAStoppingBox(t *testing.T) {
	ctx := context.Background()
	h := markedDormant(t)
	h.ec2.setInstanceTag("i-a", ec2TagRescue, "")
	h.ec2.instances["i-a"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopping}
	waits := 0
	h.rt.sleep = func(context.Context, time.Duration) error {
		waits++
		if waits == 3 {
			h.ec2.mu.Lock()
			h.ec2.instances["i-a"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}
			h.ec2.mu.Unlock()
		}
		return nil
	}
	if err := h.rt.releaseSlot(ctx); err != nil {
		t.Fatalf("releaseSlot: %v", err)
	}
	if waits < 3 {
		t.Fatalf("waited %d times; the box was detached from while stopping", waits)
	}
	if attachedInstance(h.ec2.volumes["vol-bob"]) != "" {
		t.Fatal("not detached after the box stopped")
	}
}

func TestReleaseNeverDetachesFromABoxThatStaysStopping(t *testing.T) {
	h := markedDormant(t)
	h.ec2.setInstanceTag("i-a", ec2TagRescue, "")
	h.ec2.instances["i-a"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopping}
	if err := h.rt.releaseSlot(context.Background()); err == nil {
		t.Fatal("releaseSlot succeeded on a box that never left stopping")
	}
	stillOnTheBox(t, h)
}

// Rule 1, first half: the role comes before the reservation and the template.
func TestSlotTypeMatchesTreatsAQuarantinedSlotAsAMismatch(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-q", "ap-northeast-1a", "m7i.large", false, false)
	h.rt.instanceType = "m7i.large"
	matches, _, _, err := h.rt.slotTypeMatches(ctx, "i-q")
	if err != nil || !matches {
		t.Fatalf("control: matches=%v err=%v, want a plain match", matches, err)
	}
	h.ec2.setInstanceTag("i-q", EC2TagRole, ec2RoleQuarantined)
	h.ec2.setInstanceTag("i-q", ec2TagSlotReplace, "2026-11-12T00:00:00Z")
	matches, reserved, outdated, err := h.rt.slotTypeMatches(ctx, "i-q")
	if err != nil || matches || reserved || outdated {
		t.Fatalf("quarantined + reserved: matches=%v reserved=%v outdated=%v err=%v, want all false",
			matches, reserved, outdated, err)
	}
}

// Rule 4.
func TestTerminateSlotIntent(t *testing.T) {
	ctx := context.Background()
	quarantinedBox := func() *ec2Harness {
		h := newEC2Harness(t)
		h.ec2.addSlot("i-q", "ap-northeast-1a", "m7i.large", false, false)
		h.ec2.setInstanceTag("i-q", EC2TagRole, ec2RoleQuarantined)
		return h
	}
	for _, intent := range []terminateIntent{terminateAutomatic, terminateMakeRoom} {
		h := quarantinedBox()
		if err := h.rt.terminateSlot(ctx, "i-q", "test", intent); !errors.Is(err, ErrTerminateRefused) {
			t.Fatalf("intent %d: err = %v, want ErrTerminateRefused", intent, err)
		}
		if got := terminatedInstances(h); len(got) != 0 {
			t.Fatalf("intent %d terminated a quarantined box: %v", intent, got)
		}
	}
	h := quarantinedBox()
	if err := h.rt.terminateSlot(ctx, "i-q", "operator", terminateOperator); err != nil {
		t.Fatalf("the operator's terminate was refused: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 1 {
		t.Fatalf("operator terminate: %v", got)
	}
	// And through the public entry the operator's button uses.
	h = quarantinedBox()
	if _, err := h.factory().TerminateQuarantinedSlot(ctx, "i-q"); err != nil {
		t.Fatalf("TerminateQuarantinedSlot: %v", err)
	}
	if got := terminatedInstances(h); len(got) != 1 {
		t.Fatalf("TerminateQuarantinedSlot: %v", got)
	}
}

func TestTerminateSlotAutomaticStillTerminatesAnOrdinaryBox(t *testing.T) {
	h := newEC2Harness(t)
	h.ec2.addSlot("i-s", "ap-northeast-1a", "m7i.large", false, false)
	if err := h.rt.terminateSlot(context.Background(), "i-s", "test", terminateAutomatic); err != nil {
		t.Fatal(err)
	}
	if got := terminatedInstances(h); len(got) != 1 {
		t.Fatalf("terminated = %v", got)
	}
}

func TestMakeRoomTerminateRefusesAMarkerOrADarkStamp(t *testing.T) {
	ctx := context.Background()
	for _, tag := range []struct{ key, val string }{
		{ec2TagRescue, rescueFixture().String()},
		{ec2TagDarkSince, "2026-11-12T00:00:00Z/2026-11-11T00:00:00Z"},
	} {
		h := newEC2Harness(t)
		h.ec2.addSlot("i-s", "ap-northeast-1a", "m7i.large", false, false)
		h.ec2.setInstanceTag("i-s", tag.key, tag.val)
		if err := h.rt.terminateSlot(ctx, "i-s", "made room", terminateMakeRoom); !errors.Is(err, ErrTerminateRefused) {
			t.Fatalf("%s: err = %v, want ErrTerminateRefused", tag.key, err)
		}
		// The other automatic intents only look at the role.
		if err := h.rt.terminateSlot(ctx, "i-s", "sweeper", terminateAutomatic); err != nil {
			t.Fatalf("%s: automatic: %v", tag.key, err)
		}
	}
}

func TestTerminateGateReadFailureRefuses(t *testing.T) {
	h := newEC2Harness(t)
	h.ec2.addSlot("i-s", "ap-northeast-1a", "m7i.large", false, false)
	h.rt.ec2 = failingDescribeByID{ec2API: h.ec2}
	if err := h.rt.terminateSlot(context.Background(), "i-s", "test", terminateAutomatic); !errors.Is(err, ErrTerminateRefused) {
		t.Fatalf("err = %v, want ErrTerminateRefused", err)
	}
	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("terminated although the role could not be read: %v", got)
	}
}

type failingDescribeByID struct{ ec2API }

func (failingDescribeByID) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return nil, errors.New("throttled")
}
