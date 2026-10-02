package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Reserving a slot for replacement (#1473). The acceptance list in the issue is the spine of
// these tests: a reserved slot of a stopped workspace is replaced on the next Start with the
// home intact; a running one is untouched; a replacement that cannot launch fails the Start
// and keeps the mark; the bulk path selects exactly the slots below $Latest.

// reservedSlotHarness: alice's home is on i-old (launch template version 3 of 5), which an
// operator has reserved, and her workspace is stopped.
func reservedSlotHarness(t *testing.T, running bool) *ec2Harness {
	t.Helper()
	slotReplaceSeen.reset()
	h := newEC2Harness(t)
	h.ec2.ltLatest = 5
	h.ec2.addSlot("i-old", "ap-northeast-1a", "m7i.large", running, false)
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateVersion, "3")
	h.ec2.setInstanceTag("i-old", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	h.ci.registered["i-old"] = true
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-old", time.Now().Add(-time.Hour))
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 0}
	return h
}

func TestECSEC2ReservedSlotIsReplacedOnTheNextStart(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "stopped slot"
		if running {
			name = "running slot"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := reservedSlotHarness(t, running)

			if err := h.rt.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			run, detach, term := callIndex(h, "RunInstances"), callIndex(h, "DetachVolume vol-1"), callIndex(h, "TerminateInstances i-old")
			if run < 0 || detach < 0 || term < 0 {
				t.Fatalf("calls = %v, want a new slot launched, the home detached and i-old terminated", h.ec2.calls)
			}
			// Launch first: a launch that fails must find everything as it was.
			if !(run < detach && detach < term) {
				t.Fatalf("order RunInstances=%d DetachVolume=%d Terminate=%d, want launch < detach < terminate: %v",
					run, detach, term, h.ec2.calls)
			}
			if callIndex(h, "DeleteVolume") >= 0 {
				t.Fatalf("a home was deleted: %v", h.ec2.calls)
			}
			if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
				t.Fatalf("claim = %q, want the new slot i-new1", got)
			}
			if got := ec2TagValue(h.ec2.instances["i-new1"].Tags, ec2TagLaunchTemplateVersion); got != "5" {
				t.Fatalf("new slot's template version = %q, want $Latest (5)", got)
			}
			if got := string(h.ec2.instances["i-new1"].InstanceType); got != "m7i.large" {
				t.Fatalf("new slot is %s, want the workspace's own m7i.large", got)
			}

			// The background half attaches the same home to the new slot.
			h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
			h.ci.registered["i-new1"] = true
			h.runDeferred(ctx)
			if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-new1" {
				t.Fatalf("home attached to %q, want i-new1", got)
			}
		})
	}
}

func TestECSEC2ReservedSlotWhoseReplacementCannotLaunchKeepsHomeAndMark(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, false)
	h.ec2.runErr["sub-1a"] = errors.New("InsufficientInstanceCapacity: none left")

	err := h.rt.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "reserved for replacement") || !strings.Contains(err.Error(), "InsufficientInstanceCapacity") {
		t.Fatalf("Start error = %v, want the reservation and the launch failure named", err)
	}
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-old" {
		t.Fatalf("home attached to %q, want it left on i-old", got)
	}
	if ec2TagValue(h.ec2.instances["i-old"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("the reservation was dropped by a failed replacement")
	}
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "DetachVolume") || strings.HasPrefix(c, "TerminateInstances") || strings.HasPrefix(c, "StartInstances i-old") {
			t.Fatalf("%s after the replacement failed to launch — the reserved slot must not be touched or reused", c)
		}
	}
	if got := h.rt.State(ctx); got != "stopped" {
		t.Fatalf("State = %q, want stopped (no claim left behind)", got)
	}
}

// The slot being retired does not count against the cap it is about to give back.
func TestECSEC2ReservedSlotIsReplacedInAFullPool(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, false)
	h.rt.pool.maxSlots = 1

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start in a full pool: %v", err)
	}
	if callIndex(h, "RunInstances") < 0 {
		t.Fatalf("no replacement launched: %v", h.ec2.calls)
	}
}

func TestECSEC2ReservedSlotOfARunningWorkspaceIsUntouched(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, true)
	h.ecs.services["af-ws-acme-alice"] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.factory().sweepFreeSlots(ctx, []ec2types.Volume{*h.ec2.volumes["vol-1"]})
	for _, c := range h.ec2.calls {
		if strings.HasPrefix(c, "RunInstances") || strings.HasPrefix(c, "DetachVolume") ||
			strings.HasPrefix(c, "TerminateInstances") || strings.HasPrefix(c, "StopInstances") {
			t.Fatalf("%s while the workspace runs on the reserved slot", c)
		}
	}
	if !h.rt.SlotReplacePending(ctx) {
		t.Fatal("SlotReplacePending = false; the member is not told the next start moves")
	}
}

// A free reserved slot takes nobody new: the next tenant would run on the box the operator
// wants gone.
func TestECSEC2FreeReservedSlotIsNeverPlacedOn(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-res", "ap-northeast-1a", "m7i.large", true, false)
	h.ci.registered["i-res"] = true
	h.ec2.setInstanceTag("i-res", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

	p, err := h.rt.placeHome(ctx)
	if err != nil {
		t.Fatalf("placeHome: %v", err)
	}
	if p.instanceID == "i-res" {
		t.Fatal("placed a home on a slot reserved for replacement")
	}
}

// At the cap with only a free reserved box of the right size, makeRoom retires it rather
// than failing the Start: placement may not use it, so it blocks the cap like a wrong size.
func TestECSEC2MakeRoomRetiresAFreeReservedSlotOfTheRightSize(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.maxSlots = 1
	h.ec2.addSlot("i-res", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.setInstanceTag("i-res", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

	freed, err := h.rt.makeRoom(ctx)
	if err != nil || !freed {
		t.Fatalf("makeRoom = %v, %v; want the reserved box retired", freed, err)
	}
	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-res" {
		t.Fatalf("TerminateInstances = %v, want [i-res]", got)
	}
}

func TestECSEC2SweeperRetiresAFreeReservedSlotEvenWithTheTimersOff(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, time.Minute)
	h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
	h.ec2.setInstanceTag("i-free", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	// An occupied reserved slot is its workspace's to move at its next Start.
	h.ec2.addSlot("i-held", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.setInstanceTag("i-held", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-held", time.Now())

	h.factory().sweepFreeSlots(ctx, []ec2types.Volume{*h.ec2.volumes["vol-1"]})

	if got := terminatedInstances(h); len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("TerminateInstances = %v, want only the free reserved slot", got)
	}
}

func TestECSEC2SweeperLeavesAFreeReservedSlotWithATaskAlone(t *testing.T) {
	ctx := context.Background()
	h := freeSlotHarness(t, time.Minute)
	h.ec2.setInstanceTag("i-free", ec2TagSlotReplace, time.Now().UTC().Format(time.RFC3339))
	h.ci.tasks["i-free"] = 1

	h.factory().sweepFreeSlots(ctx, nil)

	if got := terminatedInstances(h); len(got) != 0 {
		t.Fatalf("TerminateInstances = %v on a slot still running a task", got)
	}
}

func TestSlotTemplateOutdated(t *testing.T) {
	lt := ec2LaunchTemplate{id: "lt-1", latest: 5}
	tags := func(id, ver string) []ec2types.Tag {
		var out []ec2types.Tag
		if id != "" {
			out = append(out, ec2types.Tag{Key: aws.String(ec2TagLaunchTemplateID), Value: aws.String(id)})
		}
		if ver != "" {
			out = append(out, ec2types.Tag{Key: aws.String(ec2TagLaunchTemplateVersion), Value: aws.String(ver)})
		}
		return out
	}
	for _, tc := range []struct {
		name            string
		tags            []ec2types.Tag
		lt              ec2LaunchTemplate
		outdated, known bool
	}{
		{"below $Latest", tags("lt-1", "4"), lt, true, true},
		{"at $Latest", tags("lt-1", "5"), lt, false, true},
		// Numeric, not lexical: "10" > "5" although "10" < "5" as strings.
		{"two-digit at $Latest", tags("lt-1", "10"), ec2LaunchTemplate{id: "lt-1", latest: 10}, false, true},
		{"single digit below two-digit $Latest", tags("lt-1", "9"), ec2LaunchTemplate{id: "lt-1", latest: 10}, true, true},
		{"another template", tags("lt-0", "7"), lt, true, true},
		{"no stamp", tags("", ""), lt, false, false},
		{"unreadable stamp", tags("lt-1", "$Latest"), lt, false, false},
		{"template unknown", tags("lt-1", "1"), ec2LaunchTemplate{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, outdated, known := slotTemplateOutdated(tc.tags, tc.lt)
			if outdated != tc.outdated || known != tc.known {
				t.Fatalf("outdated=%v known=%v, want %v %v", outdated, known, tc.outdated, tc.known)
			}
		})
	}
}

func TestECSEC2ReserveSlotReplacement(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.ltLatest = 5
	h.ec2.addSlot("i-old", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateVersion, "4")
	h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-old", time.Now())
	h.ec2.addSlot("i-cur", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-cur", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-cur", ec2TagLaunchTemplateVersion, "5")
	h.ec2.addSlot("i-bad", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.setInstanceTag("i-bad", EC2TagRole, ec2RoleQuarantined)
	h.ec2.addSlot("i-theirs", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.setInstanceTag("i-theirs", EC2TagPool, "another-deployment")
	f := h.factory()

	res, err := f.ReserveSlotReplacement(ctx, "i-old", true, true)
	if err != nil {
		t.Fatalf("reserve i-old: %v", err)
	}
	if !res.Reserved || res.Workspace != "af-ws-acme-alice" || res.TemplateVersion != "4" || res.TemplateLatest != "5" {
		t.Fatalf("reservation = %+v", res)
	}
	if ec2TagValue(h.ec2.instances["i-old"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("no reservation tag on i-old")
	}
	if _, err := f.ReserveSlotReplacement(ctx, "i-cur", true, true); !errors.Is(err, ErrSlotNotOutdated) {
		t.Fatalf("bulk reserve of a $Latest slot = %v, want ErrSlotNotOutdated", err)
	}
	if ec2TagValue(h.ec2.instances["i-cur"].Tags, ec2TagSlotReplace) != "" {
		t.Fatal("the bulk path reserved a slot already on $Latest")
	}
	// One at a time, an operator may reserve a current slot too (a box they distrust).
	if _, err := f.ReserveSlotReplacement(ctx, "i-cur", true, false); err != nil {
		t.Fatalf("single reserve of a $Latest slot: %v", err)
	}
	if _, err := f.ReserveSlotReplacement(ctx, "i-bad", true, false); !errors.Is(err, ErrSlotQuarantined) {
		t.Fatalf("reserve a quarantined slot = %v, want ErrSlotQuarantined", err)
	}
	for _, id := range []string{"i-theirs", "i-none"} {
		if _, err := f.ReserveSlotReplacement(ctx, id, true, false); !errors.Is(err, ErrSlotNotFound) {
			t.Fatalf("reserve %s = %v, want ErrSlotNotFound", id, err)
		}
	}
	if _, err := f.ReserveSlotReplacement(ctx, "i-old", false, false); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if ec2TagValue(h.ec2.instances["i-old"].Tags, ec2TagSlotReplace) != "" {
		t.Fatal("cancel left the reservation tag")
	}
}

// Without the launch template, nothing can be called outdated — the bulk path must refuse
// rather than guess.
func TestECSEC2BulkReserveRefusesWhenTheTemplateIsUnreadable(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t) // ltLatest 0: DescribeLaunchTemplates fails
	h.ec2.addSlot("i-old", "ap-northeast-1a", "m7i.large", false, false)
	h.ec2.setInstanceTag("i-old", ec2TagLaunchTemplateVersion, "1")
	if _, err := h.factory().ReserveSlotReplacement(ctx, "i-old", true, true); !errors.Is(err, ErrSlotNotOutdated) {
		t.Fatalf("bulk reserve with an unreadable template = %v, want ErrSlotNotOutdated", err)
	}
}

func TestECSEC2PoolStatusReportsTemplateVersionsAndReservations(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.ltLatest = 5
	h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-a", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-a", ec2TagLaunchTemplateVersion, "4")
	h.ec2.setInstanceTag("i-a", ec2TagSlotReplace, "2026-10-02T00:00:00Z")
	h.ec2.addSlot("i-b", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.setInstanceTag("i-b", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-b", ec2TagLaunchTemplateVersion, "5")

	st, err := h.factory().PoolStatus(ctx)
	if err != nil {
		t.Fatalf("PoolStatus: %v", err)
	}
	if st.TemplateLatest != "5" {
		t.Fatalf("template_latest = %q, want 5", st.TemplateLatest)
	}
	got := map[string]ec2SlotView{}
	for _, s := range st.Slots {
		got[s.InstanceID] = s
	}
	if a := got["i-a"]; a.TemplateVersion != "4" || !a.TemplateOutdated || !a.ReplaceReserved || a.ReplaceReservedAt == "" {
		t.Fatalf("i-a = %+v, want version 4, outdated, reserved", a)
	}
	if b := got["i-b"]; b.TemplateVersion != "5" || b.TemplateOutdated || b.ReplaceReserved {
		t.Fatalf("i-b = %+v, want version 5, current, not reserved", b)
	}
}
