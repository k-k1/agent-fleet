package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// quarantineWorld is a rescue world whose pool runs `quarantine`.
func quarantineWorld(t *testing.T, store bool) *rescueWorld {
	w := newRescueWorld(t, store)
	w.f.pool.darkAction = darkActionQuarantine
	return w
}

func bothQuarantineModes(t *testing.T, fn func(t *testing.T, w *rescueWorld)) {
	for name, store := range map[string]bool{"local": false, "store": true} {
		t.Run(name, func(t *testing.T) { fn(t, quarantineWorld(t, store)) })
	}
}

func (w *rescueWorld) role() string {
	return ec2TagValue(w.h.ec2.instances["i-dark"].Tags, EC2TagRole)
}

// seedEpisode puts one per-episode record on the box, `ago` before now.
func (w *rescueWorld) seedEpisode(ago time.Duration, kind string) time.Time {
	at := time.Now().Add(-ago).UTC().Truncate(time.Second)
	v := at.Format(time.RFC3339)
	if kind != "" {
		v += "/" + kind
	}
	w.h.ec2.setInstanceTag("i-dark", ec2TagWedgeAtPrefix+at.Format(episodeKeyLayout), v)
	return at
}

func (w *rescueWorld) quarantinedEvents() []SlotDark {
	var out []SlotDark
	for _, ev := range *w.events {
		if ev.Action == "quarantined" {
			out = append(out, ev)
		}
	}
	return out
}

func TestQuarantineRepeatEpisodeQuarantinesBeforeTheDetach(t *testing.T) {
	bothQuarantineModes(t, func(t *testing.T, w *rescueWorld) {
		w.seedEpisode(3*time.Hour, episodeKindDark)
		w.arm()
		var roleAtDetach string
		rescueStepHook = func(_ context.Context, step string) error {
			if step == "detaching" {
				roleAtDetach = w.role()
			}
			return nil
		}
		w.pass(w.f)
		if roleAtDetach != ec2RoleQuarantined {
			t.Fatalf("role at the detach = %q: the slot must leave the free list while it still holds the home", roleAtDetach)
		}
		w.rescued() // the home is still moved off the box and the box stopped
		inst := w.h.ec2.instances["i-dark"]
		reason := ec2TagValue(inst.Tags, ec2TagQuarantineReason)
		if w.role() != ec2RoleQuarantined || !strings.HasPrefix(reason, quarantineReasonPrefix+"repeat") ||
			ec2TagValue(inst.Tags, ec2TagQuarantineAt) == "" {
			t.Fatalf("role %q reason %q", w.role(), reason)
		}
		if evs := w.quarantinedEvents(); len(evs) != 1 || evs[0].Reason != reason {
			t.Fatalf("quarantined events = %+v, want exactly one carrying the reason", evs)
		}
		if _, rung := darkView(*inst); rung != darkRungQuarantined {
			t.Fatalf("rung %q", rung)
		}
		// Quarantine is a tag: nothing is terminated, and the one detach is the rescue's ordinary one.
		if w.detachCalls() != 1 {
			t.Fatalf("detach calls = %d", w.detachCalls())
		}
	})
}

func TestQuarantineNeedsTheModeAndARepeatInsideTheWindow(t *testing.T) {
	cases := map[string]struct {
		mode     string
		ago      time.Duration
		kind     string
		legacy   bool
		wantRole string
	}{
		"a repeat under rescue mode is not quarantined": {darkActionRescue, 3 * time.Hour, episodeKindDark, false, ec2RoleSlot},
		"the first episode is not a repeat":             {darkActionQuarantine, 0, "", false, ec2RoleSlot},
		"an episode outside the window is not a repeat": {darkActionQuarantine, 25 * time.Hour, episodeKindDark, false, ec2RoleSlot},
		"a wedged episode does not make a dark repeat":  {darkActionQuarantine, 3 * time.Hour, episodeKindWedged, false, ec2RoleSlot},
		"an operator force-stop does not make a repeat": {darkActionQuarantine, 3 * time.Hour, episodeKindOperator, false, ec2RoleSlot},
		"the legacy single tag is read as an episode":   {darkActionQuarantine, 3 * time.Hour, "", true, ec2RoleQuarantined},
		"a record of the window's edge still counts":    {darkActionQuarantine, 23 * time.Hour, episodeKindDark, false, ec2RoleQuarantined},
		"detect mode neither rescues nor quarantines":   {darkActionDetect, 3 * time.Hour, episodeKindDark, false, ec2RoleSlot},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newRescueWorld(t, false)
			w.f.pool.darkAction = tc.mode
			if tc.legacy {
				w.h.ec2.setInstanceTag("i-dark", ec2TagWedgeAt, time.Now().Add(-tc.ago).UTC().Format(time.RFC3339))
			} else if tc.ago > 0 {
				w.seedEpisode(tc.ago, tc.kind)
			}
			w.arm()
			w.pass(w.f)
			if w.role() != tc.wantRole {
				t.Fatalf("role = %q, want %q", w.role(), tc.wantRole)
			}
			if tc.mode == darkActionDetect {
				return
			}
			if got := len(w.quarantinedEvents()); (tc.wantRole == ec2RoleQuarantined) != (got == 1) || got > 1 {
				t.Fatalf("quarantined events = %d", got)
			}
		})
	}
}

func TestQuarantineBlockedRescueWaitsForRescueBlockedAfterAndDetachesNothing(t *testing.T) {
	bothQuarantineModes(t, func(t *testing.T, w *rescueWorld) {
		shortBounds(t)
		withENI(w)
		w.h.ci.stickyENI = true
		w.arm()
		w.pass(w.f)
		m, ok := parseRescueMarker(w.marker())
		if !ok || m.BlockedSince.IsZero() {
			t.Fatalf("marker %q: setup wants a blocked rescue", w.marker())
		}
		w.pass(w.f)
		if w.role() != ec2RoleSlot || len(w.quarantinedEvents()) != 0 {
			t.Fatal("quarantined before rescueBlockedAfter")
		}
		// Time passes: the same marker, blocked since long ago.
		m.BlockedSince = time.Now().Add(-45 * time.Minute).UTC().Truncate(time.Second)
		w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
		w.pass(w.f)
		if w.role() != ec2RoleQuarantined {
			t.Fatalf("role %q after rescueBlockedAfter", w.role())
		}
		reason := ec2TagValue(w.h.ec2.instances["i-dark"].Tags, ec2TagQuarantineReason)
		if !strings.HasPrefix(reason, quarantineReasonPrefix+"rescue-blocked") || len(w.quarantinedEvents()) != 1 {
			t.Fatalf("reason %q events %d", reason, len(w.quarantinedEvents()))
		}
		// Never a forced detach, never a stop, never a terminate; the home stays where it is.
		if w.stopCalls() != 0 || w.detachCalls() != 0 || attachedInstance(w.vol()) != "i-dark" {
			t.Fatalf("stops %d detaches %d home on %q", w.stopCalls(), w.detachCalls(), attachedInstance(w.vol()))
		}
		if _, rung := darkView(*w.h.ec2.instances["i-dark"]); rung != darkRungQuarantined {
			t.Fatalf("rung %q", rung)
		}
		// Another pass does not report it again, and the rescue is still retried: when the ENI
		// goes the home comes off, and the box stays quarantined.
		w.pass(w.f)
		if len(w.quarantinedEvents()) != 1 {
			t.Fatalf("quarantined events = %d after a retry", len(w.quarantinedEvents()))
		}
		w.h.ci.stickyENI = false
		w.h.ec2.dropTaskENIs("i-dark")
		w.pass(w.f)
		w.rescued()
		if w.role() != ec2RoleQuarantined || len(w.quarantinedEvents()) != 1 {
			t.Fatalf("role %q events %d", w.role(), len(w.quarantinedEvents()))
		}
	})
}

func TestQuarantineNeverCoversAServiceThatIsUp(t *testing.T) {
	w := quarantineWorld(t, false)
	w.h.ecs.services[rescueWorkspace] = ecstypes.Service{Status: aws.String("ACTIVE"), DesiredCount: 1, RunningCount: 1}
	w.h.ci.running = append(w.h.ci.running, runningTask(rescueWorkspace, "i-dark"))
	w.seedEpisode(time.Hour, episodeKindDark)
	w.arm()
	w.pass(w.f)
	if w.marker() != "" || w.role() != ec2RoleSlot || len(w.quarantinedEvents()) != 0 || w.stopCalls() != 0 {
		t.Fatalf("marker %q role %q: a slot whose service is up is not touched (#3998)", w.marker(), w.role())
	}
}

func TestQuarantineWriteIsFencedByTheToken(t *testing.T) {
	w := quarantineWorld(t, false)
	w.seedEpisode(time.Hour, episodeKindDark)
	m := rescueMarker{Volume: "vol-1", Epoch: aws.ToTime(w.h.ec2.instances["i-dark"].LaunchTime).UTC().Truncate(time.Second),
		Phase: rescuePhaseStopped, Gen: w.count(), Began: time.Now().UTC().Truncate(time.Second)}
	// The operator returned the slot: no marker is on the box. A quarantine that was delayed or
	// retried past that point must write nothing.
	err := w.rt().rescueQuarantine(context.Background(), "i-dark", &m, w.rt().repeatDecision(m))
	if !errors.Is(err, errRescueOvertaken) {
		t.Fatalf("err = %v, want the rescue overtaken", err)
	}
	if w.role() != ec2RoleSlot || w.h.ec2.countCalls("CreateTags") != 0 {
		t.Fatalf("role %q: a write went out without the marker", w.role())
	}
	// With the marker on the box the same call writes, once; a repeat finds it quarantined.
	w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
	for i := 0; i < 2; i++ {
		if err := w.rt().rescueQuarantine(context.Background(), "i-dark", &m, w.rt().repeatDecision(m)); err != nil {
			t.Fatal(err)
		}
	}
	if w.role() != ec2RoleQuarantined || len(w.quarantinedEvents()) != 1 {
		t.Fatalf("role %q events %d, want one report for the one write", w.role(), len(w.quarantinedEvents()))
	}
	if n := w.h.ec2.countCalls("CreateTags"); n != 1 {
		t.Fatalf("CreateTags calls = %d, want one", n)
	}
}

func TestQuarantineFailedWriteStopsBeforeTheDetachAndIsRetried(t *testing.T) {
	w := quarantineWorld(t, false)
	w.seedEpisode(time.Hour, episodeKindDark)
	w.arm()
	w.h.ec2.createTagsErr = map[string]error{ec2TagQuarantineReason: errBoom}
	w.pass(w.f)
	if w.role() != ec2RoleSlot || w.detachCalls() != 0 || attachedInstance(w.vol()) != "i-dark" || w.marker() == "" {
		t.Fatalf("role %q detaches %d marker %q: a quarantine that could not be written must hold the detach", w.role(), w.detachCalls(), w.marker())
	}
	w.h.ec2.createTagsErr = nil
	w.pass(w.f)
	w.rescued()
	if w.role() != ec2RoleQuarantined {
		t.Fatalf("role %q after the retry", w.role())
	}
}

func TestEpisodeRecordsAreAMaximumNotAnOverwrite(t *testing.T) {
	t1 := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	t2 := t1.Add(3 * time.Hour)
	tag := func(at time.Time, kind string) ec2types.Tag {
		return ec2tagKV(ec2TagWedgeAtPrefix+at.Format(episodeKeyLayout), at.Format(time.RFC3339)+"/"+kind)
	}
	tags := []ec2types.Tag{tag(t2, episodeKindDark)}
	before := t2.Add(time.Hour)
	if last, _ := lastDarkEpisodeBefore(tags, before); !last.Equal(t2) {
		t.Fatalf("last = %v", last)
	}
	// A write for the older episode lands late: it adds a record, and the maximum does not move.
	tags = append(tags, tag(t1, episodeKindDark))
	if last, _ := lastDarkEpisodeBefore(tags, before); !last.Equal(t2) {
		t.Fatalf("last = %v after a late record, want %v", last, t2)
	}
	// The episode being judged is not its own predecessor.
	if last, _ := lastDarkEpisodeBefore(tags, t2); !last.Equal(t1) {
		t.Fatalf("last before t2 = %v, want %v", last, t1)
	}
	// The legacy single tag is one more record.
	tags = append(tags, ec2tagKV(ec2TagWedgeAt, t2.Add(30*time.Minute).Format(time.RFC3339)))
	if last, _ := lastDarkEpisodeBefore(tags, before); !last.Equal(t2.Add(30 * time.Minute)) {
		t.Fatalf("last = %v with the legacy tag", last)
	}
	// A value that does not parse falls back to its key; garbage is not a record.
	if recs := episodeRecordsOf([]ec2types.Tag{ec2tagKV(ec2TagWedgeAtPrefix+"junk", "x"),
		ec2tagKV(ec2TagWedgeAtPrefix+t1.Format(episodeKeyLayout), "x")}); len(recs) != 1 || !recs[0].at.Equal(t1) {
		t.Fatalf("records = %+v", recs)
	}
}

func TestRecordEpisodeWritesItsOwnKeyAndPrunesOnlyWhatTheWindowCannotSee(t *testing.T) {
	w := newRescueWorld(t, false)
	old := w.seedEpisode(30*time.Hour, episodeKindDark)
	young := w.seedEpisode(2*time.Hour, episodeKindDark)
	w.h.ec2.setInstanceTag("i-dark", ec2TagWedgeAt, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	began := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	m := rescueMarker{Volume: "vol-1", Epoch: began.Add(-time.Hour), Phase: rescuePhaseDetached, Gen: 9, Began: began}
	w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
	w.f.resumeRescues(context.Background())
	tags := w.h.ec2.instances["i-dark"].Tags
	have := map[string]bool{}
	for _, r := range episodeRecordsOf(tags) {
		have[r.key] = true
	}
	for key, want := range map[string]bool{
		ec2TagWedgeAtPrefix + old.Format(episodeKeyLayout):   false,
		ec2TagWedgeAtPrefix + young.Format(episodeKeyLayout): true,
		ec2TagWedgeAtPrefix + began.Format(episodeKeyLayout): true,
		ec2TagWedgeAt: false,
	} {
		if have[key] != want {
			t.Errorf("record %s present = %v, want %v", key, have[key], want)
		}
	}
	if got := ec2TagValue(tags, ec2TagWedgeAtPrefix+began.Format(episodeKeyLayout)); got != began.Format(time.RFC3339)+"/dark" {
		t.Errorf("record value %q", got)
	}
	// Recording the same episode twice is one tag.
	if err := w.rt().recordEpisode(context.Background(), "i-dark", tags, m); err != nil {
		t.Fatal(err)
	}
	if n := len(episodeRecordsOf(w.h.ec2.instances["i-dark"].Tags)); n != 2 {
		t.Errorf("records = %d, want 2", n)
	}
}

func TestPoolViewRungQuarantinedOnlyForTheDarkLadder(t *testing.T) {
	mk := func(reason string) ec2types.Instance {
		return ec2types.Instance{Tags: []ec2types.Tag{ec2tagKV(EC2TagRole, ec2RoleQuarantined),
			ec2tagKV(ec2TagQuarantineReason, reason), ec2tagKV(ec2TagQuarantineAt, "2026-11-13T00:00:00Z")}}
	}
	if _, rung := darkView(mk(quarantineReasonPrefix + "repeat: x")); rung != darkRungQuarantined {
		t.Fatalf("rung %q", rung)
	}
	if since, rung := darkView(mk("mount failed")); rung != "" || since != "" {
		t.Fatalf("a mount-failure quarantine is not rung L2: %q %q", since, rung)
	}
}

func TestDarkActsCoversRescueAndQuarantineOnly(t *testing.T) {
	for mode, want := range map[string]bool{darkActionOff: false, darkActionDetect: false, darkActionRescue: true, darkActionQuarantine: true} {
		if got := (&ec2PoolConfig{darkAction: mode}).darkActs(); got != want {
			t.Errorf("darkActs(%s) = %v", mode, got)
		}
	}
	p := &ec2PoolConfig{}
	if p.repeatWindow() != 24*time.Hour || p.blockedAfter() != 30*time.Minute {
		t.Fatal(fmt.Sprint("defaults ", p.repeatWindow(), p.blockedAfter()))
	}
}

// quarantinedRescue puts a live dark-rescue marker (phase stopped, the rescue's own count) and
// the L2 quarantine tags on the box, the moment between the quarantine and the detach.
func quarantinedRescue(w *rescueWorld, reason string) rescueMarker {
	g := w.count()
	m := rescueMarker{Volume: "vol-1", Epoch: aws.ToTime(w.h.ec2.instances["i-dark"].LaunchTime).UTC().Truncate(time.Second),
		Phase: rescuePhaseStopped, Gen: g, Began: time.Now().UTC().Truncate(time.Second)}
	w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
	w.h.ec2.setInstanceTag("i-dark", EC2TagRole, ec2RoleQuarantined)
	w.h.ec2.setInstanceTag("i-dark", ec2TagQuarantineReason, reason)
	return m
}

func TestStartYieldsToARescueOnAnL2QuarantinedBox(t *testing.T) {
	w := quarantineWorld(t, false)
	m := quarantinedRescue(w, quarantineReasonPrefix+"repeat: x")
	adm, err := w.rt().admitStart(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if adm.kind != admitYield || adm.marker.Gen != m.Gen || w.count() != m.Gen {
		t.Fatalf("admission %+v count %d: a Start must yield to the rescue, not overtake it", adm, w.count())
	}
	// A quarantine of another kind is not a rescue the Start waits for.
	w2 := quarantineWorld(t, false)
	quarantinedRescue(w2, "mount failed")
	adm, err = w2.rt().admitStart(context.Background(), true)
	if err != nil || adm.kind != admitStart {
		t.Fatalf("admission %+v err %v: a mount-failure quarantine keeps the old behaviour", adm, err)
	}
}

func TestQuarantinedBlockedRescueIsStillResumedAfterAStartYields(t *testing.T) {
	w := quarantineWorld(t, false)
	shortBounds(t)
	withENI(w)
	w.h.ci.stickyENI = true
	w.arm()
	w.pass(w.f)
	m, _ := parseRescueMarker(w.marker())
	m.BlockedSince = time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
	w.pass(w.f)
	if w.role() != ec2RoleQuarantined {
		t.Fatal("setup: not quarantined")
	}
	before := w.count()
	adm, err := w.rt().admitStart(context.Background(), true)
	if err != nil || adm.kind != admitYield || w.count() != before {
		t.Fatalf("admission %+v err %v count %d->%d", adm, err, before, w.count())
	}
	w.h.ci.stickyENI = false
	w.h.ec2.dropTaskENIs("i-dark")
	w.pass(w.f)
	w.rescued()
}

func TestForceStopTakesOverAnL2QuarantinedBlockedRescueButNotOtherQuarantines(t *testing.T) {
	ctx := context.Background()
	h := wedgedSlot(t)
	h.ec2.giveTaskENI("i-wedge")
	g, _ := h.rt.startGenNow(ctx)
	m := rescueMarker{Volume: "vol-1", Epoch: slotEpoch(h), Phase: rescuePhaseDeregistering, Gen: g,
		Began: time.Now().Add(-time.Minute).UTC().Truncate(time.Second), BlockedSince: time.Now().UTC().Truncate(time.Second)}
	h.ec2.setInstanceTag("i-wedge", ec2TagRescue, m.String())
	h.ec2.setInstanceTag("i-wedge", EC2TagRole, ec2RoleQuarantined)
	h.ec2.setInstanceTag("i-wedge", ec2TagQuarantineReason, "mount failed")
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); !errors.Is(err, ErrSlotQuarantined) {
		t.Fatalf("err = %v, want a mount-failure quarantine refused", err)
	}
	h.ec2.setInstanceTag("i-wedge", ec2TagQuarantineReason, quarantineReasonPrefix+"rescue-blocked: x")
	if _, err := h.factory().ForceStopSlot(ctx, "i-wedge", true); err != nil {
		t.Fatalf("ForceStopSlot on an L2 quarantine with a blocked rescue: %v", err)
	}
	// The stop is sent, and the marker is the rescue's own again so that the sweep finishes it.
	if back, ok := parseRescueMarker(slotMarker(h)); stopsOnBox(h) != 1 || !ok || back.Operator || back.Phase != rescuePhaseStopping {
		t.Fatalf("stops %v marker %q", h.ec2.stopForce, slotMarker(h))
	}
	// Without a marker there is nothing to finish: the box is for Terminate.
	h2 := wedgedSlot(t)
	h2.ec2.setInstanceTag("i-wedge", EC2TagRole, ec2RoleQuarantined)
	h2.ec2.setInstanceTag("i-wedge", ec2TagQuarantineReason, quarantineReasonPrefix+"repeat: x")
	if _, err := h2.factory().ForceStopSlot(ctx, "i-wedge", true); !errors.Is(err, ErrSlotQuarantined) {
		t.Fatalf("err = %v, want refused without a rescue marker", err)
	}
}

func TestQuarantineWriteIsBoundedAndSettlesWhenItsOutcomeIsUnknown(t *testing.T) {
	w := quarantineWorld(t, true)
	oldB, oldS := quarantineWriteBudget, quarantineSettle
	quarantineWriteBudget, quarantineSettle = 2*time.Second, 40*time.Millisecond
	t.Cleanup(func() { quarantineWriteBudget, quarantineSettle = oldB, oldS })
	w.seedEpisode(time.Hour, episodeKindDark)
	w.arm()
	var slept []time.Duration
	w.rt() // the factory's sleeper is shared by every runtime it builds
	w.f.sleep = func(sctx context.Context, d time.Duration) error {
		if sctx.Err() != nil {
			t.Errorf("the settle wait was handed a cancelled context: %v", sctx.Err())
		}
		slept = append(slept, d)
		return nil
	}
	w.h.ec2.createTagsErr = map[string]error{ec2TagQuarantineReason: errBoom}
	w.pass(w.f)
	dl, ok := w.h.ec2.createTagsDeadline[ec2TagQuarantineReason]
	if !ok || time.Until(dl) > 3*time.Second {
		t.Fatalf("the write carried no short deadline (%v, %v): it must not be able to outlive its leases", dl, ok)
	}
	settled := false
	for _, d := range slept {
		settled = settled || d == quarantineSettle
	}
	if !settled {
		t.Fatalf("sleeps %v: a write of unknown outcome must be waited out with the locks held", slept)
	}
	if w.detachCalls() != 0 {
		t.Fatal("the detach ran after a quarantine write that failed")
	}
}

func TestRecordEpisodeMakesRoomBeforeWritingOnAFullBox(t *testing.T) {
	fill := func(w *rescueWorld, expired, recent int) {
		n := len(w.h.ec2.instances["i-dark"].Tags)
		for i := 0; i < expired; i++ {
			w.seedEpisode(time.Duration(48+i)*time.Hour, episodeKindDark)
		}
		for i := 0; i < recent; i++ {
			w.seedEpisode(time.Duration(i+1)*time.Minute+2*time.Hour, episodeKindWedged)
		}
		for i := 0; len(w.h.ec2.instances["i-dark"].Tags) < 50-n+n && i < 100; i++ {
			w.h.ec2.setInstanceTag("i-dark", fmt.Sprintf("user-%d", i), "x")
		}
	}
	began := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	m := rescueMarker{Volume: "vol-1", Epoch: began.Add(-time.Hour), Phase: rescuePhaseDetached, Gen: 9, Began: began}
	key := ec2TagWedgeAtPrefix + began.Format(episodeKeyLayout)
	t.Run("expired records make room", func(t *testing.T) {
		w := newRescueWorld(t, false)
		fill(w, 3, 0)
		w.h.ec2.maxTags = 50
		w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
		w.f.resumeRescues(context.Background())
		if w.marker() != "" || ec2TagValue(w.h.ec2.instances["i-dark"].Tags, key) == "" {
			t.Fatalf("marker %q: the record must be written after the expired ones went", w.marker())
		}
	})
	t.Run("recent records fill the box: the oldest goes, progress is kept", func(t *testing.T) {
		w := newRescueWorld(t, false)
		fill(w, 0, 6)
		w.h.ec2.maxTags = 50
		w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
		w.f.resumeRescues(context.Background())
		if w.marker() != "" || ec2TagValue(w.h.ec2.instances["i-dark"].Tags, key) == "" {
			t.Fatalf("marker %q", w.marker())
		}
		if n := len(w.h.ec2.instances["i-dark"].Tags); n > 50 {
			t.Fatalf("%d tags", n)
		}
	})
	t.Run("nothing can be dropped: the episode is not recorded but the marker goes", func(t *testing.T) {
		w := newRescueWorld(t, false)
		fill(w, 0, 0)
		for i := 0; len(w.h.ec2.instances["i-dark"].Tags) < 50; i++ {
			w.h.ec2.setInstanceTag("i-dark", fmt.Sprintf("other-%d", i), "x")
		}
		w.h.ec2.maxTags = 50
		w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
		w.f.resumeRescues(context.Background())
		if w.marker() != "" {
			t.Fatalf("marker %q survived: a finalizer that cannot record must not hold the home", w.marker())
		}
	})
}

// The operator's force-stop on an L2-quarantined blocked rescue must not strand the home: the
// marker goes back to the rescue's own kind, and the next sweep carries on to the detach.
func TestForceStopOnAnL2QuarantinedRescueHandsItBackAndTheNextSweepDetaches(t *testing.T) {
	for name, fail := range map[string]bool{"stop sent": false, "stop fails": true} {
		t.Run(name, func(t *testing.T) {
			w := quarantineWorld(t, false)
			shortBounds(t)
			withENI(w)
			w.h.ci.stickyENI = true
			w.arm()
			w.pass(w.f)
			m, _ := parseRescueMarker(w.marker())
			m.BlockedSince = time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
			w.h.ec2.setInstanceTag("i-dark", ec2TagRescue, m.String())
			w.pass(w.f)
			if w.role() != ec2RoleQuarantined {
				t.Fatal("setup: not quarantined")
			}
			w.h.ci.stickyENI = false
			w.h.ec2.dropTaskENIs("i-dark")
			if fail {
				w.h.ec2.stopErr = errBoom
			}
			_, err := w.f.ForceStopSlot(context.Background(), "i-dark", true)
			if fail != (err != nil) {
				t.Fatalf("ForceStopSlot err = %v", err)
			}
			w.h.ec2.stopErr = nil
			back, ok := parseRescueMarker(w.marker())
			if !ok || back.Operator || back.Wedged || !back.BlockedSince.IsZero() {
				t.Fatalf("marker %q: want the dark rescue's own marker back", w.marker())
			}
			w.pass(w.f)
			if attachedInstance(w.vol()) != "" || w.marker() != "" || w.role() != ec2RoleQuarantined {
				t.Fatalf("home on %q marker %q role %q: the next sweep must finish the rescue", attachedInstance(w.vol()), w.marker(), w.role())
			}
		})
	}
}
