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

// role is the box's effective state for these tests: ec2RoleQuarantined when it carries an
// af-quarantine tag, else its af-role. The real af-role must stay `slot` throughout (realRole).
func (w *rescueWorld) role() string {
	if _, ok := w.quarantine(); ok {
		return ec2RoleQuarantined
	}
	return w.realRole()
}

func (w *rescueWorld) realRole() string {
	return ec2TagValue(w.h.ec2.instances["i-dark"].Tags, EC2TagRole)
}

// quarantine is the box's af-quarantine tag, if it parses.
func (w *rescueWorld) quarantine() (quarantineTag, bool) {
	return hasQuarantineRecord(w.h.ec2.instances["i-dark"].Tags)
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
		q, ok := w.quarantine()
		reason := q.Reason
		if !ok || w.realRole() != ec2RoleSlot || !strings.HasPrefix(reason, "repeat") || q.Volume != "vol-1" || q.At.IsZero() {
			t.Fatalf("tag %+v ok %v af-role %q: L2 must leave af-role=slot and write a token-fenced tag", q, ok, w.realRole())
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
		q, _ := w.quarantine()
		reason := q.Reason
		if !strings.HasPrefix(reason, "rescue-blocked") || w.realRole() != ec2RoleSlot || len(w.quarantinedEvents()) != 1 {
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

func TestQuarantineFailedWriteStopsBeforeTheDetachAndIsRetried(t *testing.T) {
	w := quarantineWorld(t, false)
	w.seedEpisode(time.Hour, episodeKindDark)
	w.arm()
	w.h.ec2.createTagsFail = func(key, _ string) error {
		if strings.HasPrefix(key, ec2TagQuarantinePrefix) {
			return errBoom
		}
		return nil
	}
	w.pass(w.f)
	if w.role() != ec2RoleSlot || w.detachCalls() != 0 || attachedInstance(w.vol()) != "i-dark" || w.marker() == "" {
		t.Fatalf("role %q detaches %d marker %q: a quarantine that could not be written must hold the detach", w.role(), w.detachCalls(), w.marker())
	}
	w.h.ec2.createTagsFail = nil
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

func TestQuarantineTagRoundTripAndRejectsGarbage(t *testing.T) {
	q := quarantineTag{Volume: "vol-0abc", Gen: 7, Reason: "repeat: dark again within 24h0m0s of 2026-11-12T00:00:00Z",
		At: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC)}
	got, ok := parseQuarantineTag(q.String())
	if !ok || got != q {
		t.Fatalf("round trip %+v %v", got, ok)
	}
	for _, bad := range []string{"", "vol-1:7", "vol-1/x/2026-11-13T00:00:00Z", "vol-1:x/r/2026-11-13T00:00:00Z",
		"vol-1:7/r/yesterday", ":7/r/2026-11-13T00:00:00Z", "vol-1:-1/r/2026-11-13T00:00:00Z"} {
		if _, ok := parseQuarantineTag(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func (w *rescueWorld) setQuarantine(q quarantineTag) {
	w.h.ec2.setInstanceTag("i-dark", quarantineKey(q.Volume, q.Gen), q.String())
}

func (w *rescueWorld) records() []quarantineRec {
	return quarantineRecordsOf(w.h.ec2.instances["i-dark"].Tags)
}

// A late quarantine write that lands after a Start moved the box on is void: it does not hold
// the box, does not move the home's owner, and the sweeper clears it.
func TestQuarantineLateWriteAfterAStartIsIgnoredAndCleared(t *testing.T) {
	w := quarantineWorld(t, false)
	g := w.count()
	q := quarantineTag{Volume: "vol-1", Gen: g, Reason: "repeat: x", At: time.Now()}
	w.setQuarantine(q)
	// The rescue's own token is current: the quarantine holds.
	matches, _, _, err := w.rt().slotTypeMatches(context.Background(), "i-dark")
	if err != nil || matches {
		t.Fatalf("matches=%v err=%v: a valid quarantine must make the box a mismatch", matches, err)
	}
	// A member's Start counts: the token is no longer the box's.
	w.startLands()
	matches, _, _, err = w.rt().slotTypeMatches(context.Background(), "i-dark")
	if err != nil || !matches {
		t.Fatalf("matches=%v err=%v: a void quarantine must not move anybody", matches, err)
	}
	w.pass(w.f)
	if len(w.records()) != 0 {
		t.Fatal("the sweeper left a void quarantine on the box")
	}
	// A late write for another home's token, or of an older count, is void the same way.
	w.setQuarantine(quarantineTag{Volume: "vol-other", Gen: w.count(), Reason: "x", At: time.Now()})
	w.pass(w.f)
	if _, ok := w.quarantine(); ok {
		t.Fatal("a quarantine naming another home was not cleared")
	}
	// A malformed value holds nothing.
	w.h.ec2.setInstanceTag("i-dark", ec2TagQuarantinePrefix+"garbage", "garbage")
	if matches, _, _, _ := w.rt().slotTypeMatches(context.Background(), "i-dark"); !matches {
		t.Fatal("a malformed tag held the box")
	}
}

// A valid quarantine keeps the box out of every free-slot walk.
func TestQuarantineValidBlocksPlacement(t *testing.T) {
	ctx := context.Background()
	w := quarantineWorld(t, false)
	w.h.ec2.volumes["vol-1"].Attachments = nil // free the box: no home on it
	has := func() bool {
		cands, err := w.rt().freeSlots(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cands {
			if c.id == "i-dark" {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("setup: the harness box must be a free candidate")
	}
	w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: w.count(), Reason: "repeat: x", At: time.Now()})
	if has() {
		t.Fatal("a quarantined box was offered to a Start")
	}
}

// A late record for an old token, on a box that has a live home, is void for EVERY reader: it
// neither shrinks the pool count, nor refuses the terminate gate, nor reads as quarantined in the
// pool view, whichever home's runtime asks.
func TestQuarantineVoidRecordOnALiveHomeIsIgnoredByEveryReader(t *testing.T) {
	ctx := context.Background()
	w := quarantineWorld(t, false)
	base, err := w.rt().poolSize(ctx)
	if err != nil || base == 0 {
		t.Fatalf("baseline pool size %d err %v", base, err)
	}
	w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: w.count() + 5, Reason: "repeat: x", At: time.Now()}) // not the home's count
	if n, _ := w.rt().poolSize(ctx); n != base {
		t.Fatalf("pool size %d, want %d: a void record must not shrink it", n, base)
	}
	if blocked, _, _ := w.f.BakeBlocked(ctx); blocked && base < 2 {
		t.Fatal("bake capacity counted a void record")
	}
	if err := w.rt().terminateGate(ctx, "i-dark", terminateMakeRoom); errors.Is(err, ErrTerminateRefused) && strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("terminate gate refused on a void record: %v", err)
	}
	st, err := w.f.PoolStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sl := range st.Slots {
		if sl.InstanceID == "i-dark" && sl.Rung == darkRungQuarantined {
			t.Fatal("the pool view shows a void record as quarantined")
		}
	}
	// The same record with the home's own count is in force, for the same readers.
	w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: w.count(), Reason: "repeat: y", At: time.Now().Add(time.Second)})
	if n, _ := w.rt().poolSize(ctx); n != base-1 {
		t.Fatalf("pool size %d, want %d once the record is in force", n, base-1)
	}
}

// A normal Start or Stop counts on a home that sits on a quarantined box: the quarantine is
// carried to the new count first, so the box stays a mismatch and the home is moved off it.
func TestQuarantineSurvivesTheStartCountOfItsOwnHome(t *testing.T) {
	ctx := context.Background()
	w := quarantineWorld(t, false)
	w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: w.count(), Reason: "rescue-blocked: x", At: time.Now()})
	if _, err := w.rt().beginStart(ctx); err != nil {
		t.Fatal(err)
	}
	matches, _, _, err := w.rt().slotTypeMatches(ctx, "i-dark")
	if err != nil || matches {
		t.Fatalf("matches=%v err=%v: after the Start's count the box must still be held out", matches, err)
	}
	// A second, late write for the OLD token adds a void record and cannot undo the carried one.
	w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: w.count() - 1, Reason: "stale", At: time.Now().Add(time.Minute)})
	w.pass(w.f)
	recs := w.records()
	if len(recs) != 1 || recs[0].Gen != w.count() {
		t.Fatalf("records %+v: the stale one must be cleared and the carried one kept", recs)
	}
	if matches, _, _, _ := w.rt().slotTypeMatches(ctx, "i-dark"); matches {
		t.Fatal("the hold was lost")
	}
}

func TestTerminateSlotCheckedStopsWhenTheLastCheckFails(t *testing.T) {
	ctx := context.Background()
	w := quarantineWorld(t, false)
	err := w.rt().terminateSlotChecked(ctx, "i-dark", "test", terminateOperator, func(context.Context) error { return ErrSlotInUse })
	if !errors.Is(err, ErrSlotInUse) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range w.h.ec2.calls {
		if strings.HasPrefix(c, "TerminateInstances") {
			t.Fatal("terminated despite the failed last check")
		}
	}
}

func TestQuarantineIsNeverCollectedByTheSweeperAndTerminateOnlyByAnOperator(t *testing.T) {
	ctx := context.Background()
	w := quarantineWorld(t, false)
	w.h.ec2.volumes["vol-1"].Attachments = nil
	w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: w.count(), Reason: "repeat: x", At: time.Now()})
	if err := w.rt().terminateGate(ctx, "i-dark", terminateMakeRoom); !errors.Is(err, ErrTerminateRefused) {
		t.Fatalf("automatic terminate gate = %v", err)
	}
	reason, err := w.f.TerminateQuarantinedSlot(ctx, "i-dark")
	if err != nil || reason != "repeat: x" {
		t.Fatalf("operator terminate: %q %v", reason, err)
	}
	// Without a quarantine it is still refused as before.
	w2 := quarantineWorld(t, false)
	w2.h.ec2.volumes["vol-1"].Attachments = nil
	if _, err := w2.f.TerminateQuarantinedSlot(ctx, "i-dark"); !errors.Is(err, ErrSlotNotQuarantined) {
		t.Fatalf("err = %v", err)
	}
}
