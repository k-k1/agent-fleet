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
			if got := ec2TagValue(h.ec2.instances["i-new1"].Tags, ec2TagReplacesHome); got != "vol-1" {
				t.Fatalf("new slot's %s = %q, want vol-1 — written at launch so a crash cannot lose the link", ec2TagReplacesHome, got)
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
			if got := ec2TagValue(h.ec2.instances["i-new1"].Tags, ec2TagReplacesHome); got != "" {
				t.Fatalf("%s = %q after the home moved in, want it cleared", ec2TagReplacesHome, got)
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
		// Review #1526-4: half a stamp, or a version that is not a positive integer, is unknown.
		{"version without template id", tags("", "1"), lt, false, false},
		{"another template with a garbage version", tags("lt-0", "garbage"), lt, false, false},
		{"version zero", tags("lt-1", "0"), lt, false, false},
		{"negative version", tags("lt-1", "-3"), lt, false, false},
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

// liveSlots counts the pool's slots that still exist, i.e. what the cap is checked against.
func liveSlots(h *ec2Harness) int {
	n := 0
	for _, i := range h.ec2.instances {
		if ec2TagValue(i.Tags, EC2TagPool) == "clu" && i.State.Name != ec2types.InstanceStateNameTerminated {
			n++
		}
	}
	return n
}

// Review #1526-1: a release that fails after the new slot was launched must not leave that
// slot behind. In a full pool it would hold the one place the retry needs, and with both
// timers off nothing would ever collect it — every later Start would fail at the cap.
func TestECSEC2FailedReleaseRetiresTheNewSlotAndTheRetrySucceeds(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, true)
	h.rt.pool.maxSlots = 1
	h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
	h.ssmc.fail["af-umount"] = true

	if err := h.rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "reservation stays") {
		t.Fatalf("Start with a failing umount = %v, want the release failure", err)
	}
	if h.ec2.instances["i-new1"].State.Name != ec2types.InstanceStateNameTerminated {
		t.Fatalf("the unused replacement i-new1 is %s, want terminated", h.ec2.instances["i-new1"].State.Name)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "" {
		t.Fatalf("claim %q left behind — the workspace would read `starting` until it expires", got)
	}
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-old" {
		t.Fatalf("home on %q, want it still on i-old", got)
	}
	if ec2TagValue(h.ec2.instances["i-old"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("the reservation was dropped by a failed release")
	}
	if n := liveSlots(h); n != 1 {
		t.Fatalf("%d live slots after the failure, want the pool back at its cap of 1", n)
	}

	// The cause goes away; the retry must not find the pool full.
	delete(h.ssmc.fail, "af-umount")
	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("retry after the cause was fixed: %v", err)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new2" {
		t.Fatalf("claim = %q, want the retry's new slot i-new2", got)
	}
	if h.ec2.instances["i-old"].State.Name != ec2types.InstanceStateNameTerminated {
		t.Fatal("the reserved slot was not retired by the successful retry")
	}
}

// Review #1526-2: the new slot is claimed from the moment it exists, so another member's Start
// running while this one waits for the old home's umount cannot attach to it.
func TestECSEC2ReplacementSlotIsProtectedWhileTheOldHomeIsReleased(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, true)
	bobVol := h.ec2.addHomeVolume("vol-bob", "M-2", "af-ws-acme-bob", "ap-northeast-1a")
	bob := h.rt.siblingFor(bobVol)
	var bobOn string
	h.ssmc.onSend = func(cmd string) {
		if !strings.Contains(cmd, "af-umount") || bobOn != "" {
			return
		}
		// The replacement has booted by now and is a registered, running box.
		h.ec2.mu.Lock()
		h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
		h.ec2.mu.Unlock()
		h.ci.registered["i-new1"] = true
		p, err := bob.placeHome(ctx)
		if err != nil {
			t.Errorf("Bob's placeHome: %v", err)
			return
		}
		bobOn = p.instanceID
	}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if bobOn == "" || bobOn == "i-new1" {
		t.Fatalf("Bob was placed on %q, want anything but Alice's replacement i-new1", bobOn)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
		t.Fatalf("Alice's claim = %q, want i-new1", got)
	}
}

// A replacement an earlier attempt launched and never used (a CP that died mid-way) is
// reused rather than launching yet another box over the cap.
func TestECSEC2ReplacementAdoptsTheSlotAnEarlierAttemptLaunched(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, false)
	h.ec2.addSlot("i-spare", "ap-northeast-1a", "m7i.large", false, false)
	// Launched from the current $Latest, so it is what a launch now would give.
	h.ec2.setInstanceTag("i-spare", ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag("i-spare", ec2TagLaunchTemplateVersion, "5")
	h.ec2.setTag("vol-1", EC2TagClaim, "i-spare")
	h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)) // expired

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if callIndex(h, "RunInstances") >= 0 {
		t.Fatalf("launched another slot instead of reusing i-spare: %v", h.ec2.calls)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-spare" {
		t.Fatalf("claim = %q, want i-spare", got)
	}
}

// Review #1526-3: a reservation that lands after the candidate list was read still keeps the
// home off that slot — whether it lands before the attach or between the attach and the claim.
func TestECSEC2ReservationDuringPlacementIsHonoured(t *testing.T) {
	for _, when := range []string{"before the attach", "after the attach"} {
		t.Run(when, func(t *testing.T) {
			ctx := context.Background()
			h := newEC2Harness(t)
			h.ec2.addSlot("i-a", "ap-northeast-1a", "m7i.large", true, false)
			h.ec2.addSlot("i-b", "ap-northeast-1a", "m7i.large", true, false)
			h.ci.registered["i-a"], h.ci.registered["i-b"] = true, true
			h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")
			reserve := func() {
				h.ec2.mu.Lock()
				h.ec2.setInstanceTag("i-a", ec2TagSlotReplace, "2026-10-03T00:00:00Z")
				h.ec2.mu.Unlock()
			}
			if when == "before the attach" {
				// occupiedInstances' read comes after the candidate list was taken.
				h.ec2.afterDescribeVolumes = func() { h.ec2.afterDescribeVolumes = nil; reserve() }
			} else {
				h.ec2.onAttach = func(id string) {
					if id == "i-a" {
						reserve()
					}
				}
			}

			p, err := h.rt.placeHome(ctx)
			if err != nil {
				t.Fatalf("placeHome: %v", err)
			}
			if p.instanceID != "i-b" {
				t.Fatalf("placed on %q, want i-b — i-a was reserved during the placement", p.instanceID)
			}
			if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-b" {
				t.Fatalf("home attached to %q, want i-b", got)
			}
		})
	}
}

// expireClaim backdates the home's claim past claimTTL, standing in for the wait before a
// member can start again after a failure that kept it.
func expireClaim(h *ec2Harness, vol string) {
	h.ec2.setTag(vol, ec2TagClaimAt, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
}

// Review #1526 round 2, item 1: when the cleanup cannot confirm the unused replacement is
// gone (or at least reserved), the home's claim on it is KEPT — it is what keeps other Starts
// off the box and what points the next Start at it. Dropping it left a box nothing collects
// and a pool stuck over its cap.
func TestECSEC2CleanupThatCannotRetireTheReplacementKeepsTheLinkAndTheRetryAdoptsIt(t *testing.T) {
	for _, fault := range []string{"occupancy unreadable", "terminate and reserve both fail"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			h := reservedSlotHarness(t, true)
			h.rt.pool.maxSlots = 1
			h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
			h.ssmc.fail["af-umount"] = true
			switch fault {
			case "occupancy unreadable":
				h.ssmc.onSend = func(cmd string) {
					if strings.Contains(cmd, "af-umount") {
						h.ec2.mu.Lock()
						h.ec2.describeVolumesErr = errors.New("RequestLimitExceeded")
						h.ec2.mu.Unlock()
					}
				}
			case "terminate and reserve both fail":
				h.ec2.terminateErr = errors.New("UnauthorizedOperation")
				h.ec2.createTagsErr = map[string]error{ec2TagSlotReplace: errors.New("RequestLimitExceeded")}
			}

			if err := h.rt.Start(ctx); err == nil {
				t.Fatal("Start with a failing umount succeeded")
			}
			if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
				t.Fatalf("claim = %q after an unconfirmed cleanup, want it kept on i-new1", got)
			}

			// The causes go away and the claim runs out; the next Start must reuse i-new1
			// rather than find the pool full.
			h.ssmc.onSend = nil
			delete(h.ssmc.fail, "af-umount")
			h.ec2.describeVolumesErr, h.ec2.terminateErr, h.ec2.createTagsErr = nil, nil, nil
			expireClaim(h, "vol-1")
			if err := h.rt.Start(ctx); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, ok := h.ec2.instances["i-new2"]; ok {
				t.Fatal("the retry launched another slot instead of adopting i-new1")
			}
			if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
				t.Fatalf("claim after the retry = %q, want i-new1", got)
			}
			if n := liveSlots(h); n != 1 {
				t.Fatalf("%d live slots after the retry, want 1 (i-old retired)", n)
			}
		})
	}
}

// A request context cancelled mid-release (the member closed the tab) must not stop the
// cleanup: the unused replacement is still terminated.
func TestECSEC2CleanupOutlivesACancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := reservedSlotHarness(t, true)
	h.rt.pool.maxSlots = 1
	h.ssmc.fail["af-umount"] = true
	h.ssmc.onSend = func(cmd string) {
		if strings.Contains(cmd, "af-umount") {
			cancel()
		}
	}

	_ = h.rt.Start(ctx)
	if h.ec2.instances["i-new1"].State.Name != ec2types.InstanceStateNameTerminated {
		t.Fatalf("the unused replacement is %s after the request was cancelled, want terminated", h.ec2.instances["i-new1"].State.Name)
	}
	if n := liveSlots(h); n != 1 {
		t.Fatalf("%d live slots, want the pool back at 1", n)
	}
}

// Review #1526 round 2, item 2: a replacement whose launch answered (or was accepted with the
// answer lost) but whose claim was never written — the CP died in between — is found through
// the af-replaces-home tag RunInstances itself wrote, and reused.
func TestECSEC2ReplacementLaunchedBeforeACrashIsFoundByItsTag(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, false)
	h.rt.pool.maxSlots = 1
	h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
	// The previous attempt got exactly this far: the launch, nothing else.
	if _, err := h.rt.runSlot(ctx, "ap-northeast-1a", 2, "vol-1"); err != nil {
		t.Fatalf("seed launch: %v", err)
	}
	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "" {
		t.Fatalf("setup: claim %q, want none (the crash came before it)", got)
	}

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start after the crash: %v", err)
	}
	if _, ok := h.ec2.instances["i-new2"]; ok {
		t.Fatal("launched a second replacement; the first one should have been found by its tag")
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
		t.Fatalf("claim = %q, want the earlier replacement i-new1", got)
	}
	if n := liveSlots(h); n != 1 {
		t.Fatalf("%d live slots, want 1", n)
	}
}

// seedCrashedReplacement leaves exactly what an attempt that died right after RunInstances
// leaves: a running slot tagged for vol-1, launched from version `ver`, and no claim.
func seedCrashedReplacement(t *testing.T, h *ec2Harness, ver int64) {
	t.Helper()
	h.ec2.ltLatest = ver
	if _, err := h.rt.runSlot(context.Background(), "ap-northeast-1a", h.rt.pool.maxSlots+1, "vol-1"); err != nil {
		t.Fatalf("seed launch: %v", err)
	}
	h.ec2.instances["i-new1"].State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	h.ci.registered["i-new1"] = true
}

// Review #1526 round 3, item 1: a replacement launched for Alice's home is hers while it is
// pending — with no claim written, or with the claim expired — so Bob's Start cannot take it
// and leave Alice's retry facing a full pool for good.
func TestECSEC2PendingReplacementIsNotPlacedOnForAnotherMember(t *testing.T) {
	for _, claim := range []string{"no claim", "expired claim"} {
		t.Run(claim, func(t *testing.T) {
			ctx := context.Background()
			h := reservedSlotHarness(t, false)
			h.rt.pool.maxSlots = 1
			h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
			seedCrashedReplacement(t, h, 5)
			if claim == "expired claim" {
				h.ec2.setTag("vol-1", EC2TagClaim, "i-new1")
				expireClaim(h, "vol-1")
			}
			bob := h.rt.siblingFor(h.ec2.addHomeVolume("vol-bob", "M-2", "af-ws-acme-bob", "ap-northeast-1a"))

			if p, err := bob.placeHome(ctx); err == nil && p.instanceID == "i-new1" {
				t.Fatal("Bob was placed on Alice's pending replacement i-new1")
			}
			if got := attachedInstance(h.ec2.volumes["vol-bob"]); got == "i-new1" {
				t.Fatal("Bob's home is attached to i-new1")
			}
			if err := h.rt.Start(ctx); err != nil {
				t.Fatalf("Alice's retry: %v", err)
			}
			if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new1" {
				t.Fatalf("Alice's claim = %q, want her replacement i-new1", got)
			}
		})
	}
}

// The ownership expires by itself: a slot whose home has moved away (or no longer exists) is
// an ordinary slot again, so a stale tag can never hold a box out of the pool.
func TestECSEC2StaleReplacementLinkDoesNotHoldASlot(t *testing.T) {
	for _, home := range []string{"home detached", "home gone"} {
		t.Run(home, func(t *testing.T) {
			ctx := context.Background()
			h := newEC2Harness(t)
			h.ec2.addSlot("i-was-new", "ap-northeast-1a", "m7i.large", true, false)
			h.ci.registered["i-was-new"] = true
			h.ec2.setInstanceTag("i-was-new", ec2TagReplacesHome, "vol-carol")
			if home == "home detached" {
				h.ec2.addHomeVolume("vol-carol", "M-3", "af-ws-acme-carol", "ap-northeast-1a")
			}
			h.ec2.addHomeVolume("vol-1", "M-1", "af-ws-acme-alice", "ap-northeast-1a")

			p, err := h.rt.placeHome(ctx)
			if err != nil {
				t.Fatalf("placeHome: %v", err)
			}
			if p.instanceID != "i-was-new" {
				t.Fatalf("placed on %q, want the free slot whose replacement link has expired", p.instanceID)
			}
		})
	}
}

// Review #1526 round 3, item 2: an earlier replacement is reused only if it is from the
// template's current $Latest. One from before a template change is retired, giving its place
// back, and a new slot is launched from $Latest; one that cannot be judged is left alone.
func TestECSEC2EarlierReplacementFromAnOlderTemplateIsRetiredNotAdopted(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, false)
	h.rt.pool.maxSlots = 1
	h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
	seedCrashedReplacement(t, h, 4)
	h.ec2.ltLatest = 5 // the template moved on before the retry

	if err := h.rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.ec2.instances["i-new1"].State.Name != ec2types.InstanceStateNameTerminated {
		t.Fatalf("the version-4 replacement is %s, want terminated", h.ec2.instances["i-new1"].State.Name)
	}
	if got := ec2TagValue(h.ec2.volumes["vol-1"].Tags, EC2TagClaim); got != "i-new2" {
		t.Fatalf("claim = %q, want the new $Latest slot i-new2", got)
	}
	if got := ec2TagValue(h.ec2.instances["i-new2"].Tags, ec2TagLaunchTemplateVersion); got != "5" {
		t.Fatalf("i-new2 version = %q, want 5", got)
	}
}

func TestECSEC2EarlierReplacementIsLeftAloneWhenTheTemplateCannotBeRead(t *testing.T) {
	ctx := context.Background()
	h := reservedSlotHarness(t, false)
	h.rt.pool.maxSlots = 1
	seedCrashedReplacement(t, h, 5)
	h.ec2.ltLatest = 0 // DescribeLaunchTemplates now fails

	if err := h.rt.Start(ctx); err == nil {
		t.Fatal("Start adopted or launched with the template unreadable and the pool full")
	}
	if got := h.ec2.instances["i-new1"].State.Name; got == ec2types.InstanceStateNameTerminated {
		t.Fatal("an earlier replacement was destroyed on a guess")
	}
	if got := attachedInstance(h.ec2.volumes["vol-1"]); got != "i-old" {
		t.Fatalf("home on %q, want it still on i-old", got)
	}
	if ec2TagValue(h.ec2.instances["i-old"].Tags, ec2TagSlotReplace) == "" {
		t.Fatal("the reservation was dropped")
	}
}
