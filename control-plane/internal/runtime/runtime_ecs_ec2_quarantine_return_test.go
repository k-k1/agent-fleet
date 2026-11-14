package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// held puts a rung-L2 record on the box for the home's current count and returns its token.
func (w *rescueWorld) held(gen int64) string {
	w.t.Helper()
	q := quarantineTag{Volume: "vol-1", Gen: gen, Reason: "repeat: x", At: time.Now().UTC().Truncate(time.Second)}
	w.setQuarantine(q)
	return q.String()
}

func (w *rescueWorld) heldOut() bool {
	w.t.Helper()
	matches, _, _, err := w.rt().slotTypeMatches(context.Background(), "i-dark")
	if err != nil {
		w.t.Fatal(err)
	}
	return !matches
}

// The record in force is deleted by value, and the home's count moves on after it: a write of
// the same token that was already on its way is void instead of holding the slot again.
func TestReturnQuarantinedSlotDeletesTheRecordAndBumpsTheCount(t *testing.T) {
	bothQuarantineModes(t, func(t *testing.T, w *rescueWorld) {
		g := w.count()
		token := w.held(g)
		if !w.heldOut() {
			t.Fatal("setup: the record must hold the box")
		}
		res, err := w.f.ReturnQuarantinedSlot(context.Background(), "i-dark", token)
		if err != nil {
			t.Fatal(err)
		}
		if len(w.records()) != 0 || w.heldOut() {
			t.Fatalf("records %+v: the slot was not returned", w.records())
		}
		if !res.CountBumped || w.count() != g+1 || res.Workspace == "" || res.Reason != "repeat: x" {
			t.Fatalf("result %+v, count %d (was %d)", res, w.count(), g)
		}
		if w.realRole() != ec2RoleSlot {
			t.Fatal("the role moved")
		}
		// The late write of the old token lands after the return: void, so it holds nothing.
		w.setQuarantine(quarantineTag{Volume: "vol-1", Gen: g, Reason: "repeat: x", At: time.Now()})
		if w.heldOut() {
			t.Fatal("a late write of the returned token held the slot again")
		}
	})
}

// A re-quarantine under a new token, or a token that was never on the box, is not undone: the
// delete is by value.
func TestReturnQuarantinedSlotRefusesAStaleOrForgedToken(t *testing.T) {
	bothQuarantineModes(t, func(t *testing.T, w *rescueWorld) {
		ctx := context.Background()
		g := w.count()
		old := quarantineTag{Volume: "vol-1", Gen: g, Reason: "repeat: old", At: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
		current := w.held(g) // same key as old, a different value: the box was re-quarantined
		before := w.count()
		if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", old.String()); !errors.Is(err, ErrSlotQuarantineChanged) {
			t.Fatalf("stale token: %v", err)
		}
		forged := quarantineTag{Volume: "vol-zzz", Gen: 9, Reason: "x", At: time.Now()}.String()
		if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", forged); !errors.Is(err, ErrSlotQuarantineChanged) {
			t.Fatalf("forged token: %v", err)
		}
		if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", "garbage"); !errors.Is(err, ErrSlotQuarantineToken) {
			t.Fatalf("unparsable token: %v", err)
		}
		if recs := w.records(); len(recs) != 1 || recs[0].String() != current || w.count() != before || !w.heldOut() {
			t.Fatalf("a refused return changed something: %+v count %d", recs, w.count())
		}
		if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-nope", current); !errors.Is(err, ErrSlotNotFound) {
			t.Fatalf("unknown id: %v", err)
		}
	})
}

// af-role=quarantined is Terminate's kind: it keeps its evidence and must not be returned, even
// when it also carries an af-quarantine record.
func TestReturnQuarantinedSlotRefusesARoleQuarantinedBox(t *testing.T) {
	w := quarantineWorld(t, false)
	token := w.held(w.count())
	w.h.ec2.setInstanceTag("i-dark", EC2TagRole, ec2RoleQuarantined)
	if _, err := w.f.ReturnQuarantinedSlot(context.Background(), "i-dark", token); !errors.Is(err, ErrSlotRoleQuarantined) {
		t.Fatalf("err = %v", err)
	}
	if len(w.records()) != 1 {
		t.Fatal("the record was deleted")
	}
}

// A slot that was never quarantined (or was returned already) answers "not quarantined".
func TestReturnQuarantinedSlotOnAnUnheldSlot(t *testing.T) {
	w := quarantineWorld(t, false)
	token := quarantineTag{Volume: "vol-1", Gen: 1, Reason: "x", At: time.Now()}.String()
	if _, err := w.f.ReturnQuarantinedSlot(context.Background(), "i-dark", token); !errors.Is(err, ErrSlotNotQuarantined) {
		t.Fatalf("err = %v", err)
	}
}

// With no home on the slot there is no count to move and no claim lock to take: the record goes
// under the per-instance lease and the slot is a free candidate again.
func TestReturnQuarantinedSlotWithNoHomeOnIt(t *testing.T) {
	bothQuarantineModes(t, func(t *testing.T, w *rescueWorld) {
		ctx := context.Background()
		w.h.ec2.volumes["vol-1"].Attachments = nil
		g := w.count()
		token := w.held(g)
		res, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", token)
		if err != nil {
			t.Fatal(err)
		}
		if res.CountBumped || w.count() != g || len(w.records()) != 0 || res.Workspace != "" {
			t.Fatalf("result %+v count %d records %+v", res, w.count(), w.records())
		}
		cands, err := w.rt().freeSlots(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range cands {
			found = found || c.id == "i-dark"
		}
		if !found {
			t.Fatal("the returned slot is not offered to a Start")
		}
		// The lease is the instance's: a holder of it makes the call back off rather than race.
		token = w.held(g)
		_, unlock, err := w.rt().lockBounded(ctx, homeLockWedge, "i-dark", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", token); !errors.Is(err, ErrSlotBusy) || len(w.records()) != 1 {
			t.Fatalf("err = %v, records %+v", err, w.records())
		}
	})
}

// A Start that holds a live claim on the home is placing it right now: the bump would overtake
// it, so the return waits. The claim lock held by anyone else is a retry, not a race.
func TestReturnQuarantinedSlotYieldsToAStartAndToTheClaimLock(t *testing.T) {
	w := quarantineWorld(t, false)
	ctx := context.Background()
	token := w.held(w.count())
	w.h.ec2.setTag("vol-1", EC2TagClaim, "i-dark")
	w.h.ec2.setTag("vol-1", ec2TagClaimAt, time.Now().UTC().Format(time.RFC3339))
	if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", token); !errors.Is(err, ErrSlotPlacing) || len(w.records()) != 1 {
		t.Fatalf("live claim: err = %v, records %+v", err, w.records())
	}
	w.h.ec2.setTag("vol-1", EC2TagClaim, "")
	_, unlock, err := w.rt().lockBounded(ctx, homeLockClaim, rescueWorkspace, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := w.f.ReturnQuarantinedSlot(ctx, "i-dark", token); !errors.Is(err, ErrSlotBusy) || len(w.records()) != 1 {
		t.Fatalf("held claim lock: err = %v, records %+v", err, w.records())
	}
}

// A record that is already void (a Start moved the count) is deleted but moves nothing: it held
// nobody, and bumping would overtake a Start that has nothing to do with it.
func TestReturnQuarantinedSlotOfAVoidRecordLeavesTheCountAlone(t *testing.T) {
	w := quarantineWorld(t, false)
	g := w.count()
	token := w.held(g)
	w.startLands()
	if _, err := w.f.ReturnQuarantinedSlot(context.Background(), "i-dark", token); err != nil {
		t.Fatal(err)
	}
	if len(w.records()) != 0 || w.count() != g+1 {
		t.Fatalf("records %+v count %d (the Start's own bump is the only one)", w.records(), w.count())
	}
}

// The pool view hands the Console the token of the record in force, on an L2 row only.
func TestPoolStatusCarriesTheQuarantineTokenOfAnL2Row(t *testing.T) {
	w := quarantineWorld(t, false)
	ctx := context.Background()
	slot := func() ec2SlotView {
		st, err := w.f.PoolStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range st.Slots {
			if s.InstanceID == "i-dark" {
				return s
			}
		}
		t.Fatal("no row")
		return ec2SlotView{}
	}
	if s := slot(); s.QuarantineToken != "" {
		t.Fatalf("token %q on an unheld row", s.QuarantineToken)
	}
	token := w.held(w.count())
	if s := slot(); s.QuarantineToken != token || !s.Quarantined || s.Terminable {
		t.Fatalf("row %+v, want token %q", s, token)
	}
}

// afterDeleteTags runs a hook once a DeleteTags has gone through, with that call's context.
type afterDeleteTags struct {
	ec2API
	after func(context.Context)
}

func (h *afterDeleteTags) DeleteTags(ctx context.Context, in *ec2.DeleteTagsInput, opts ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error) {
	out, err := h.ec2API.DeleteTags(ctx, in, opts...)
	if err == nil {
		h.after(ctx)
	}
	return out, err
}

// A write of the returned token that lands between the delete and the count's bump must not be
// carried to the new count: the bump here does not carry, so that write reads as void.
func TestReturnQuarantinedSlotDoesNotCarryALateWriteOfItsOwnToken(t *testing.T) {
	bothQuarantineModes(t, func(t *testing.T, w *rescueWorld) {
		token := w.held(w.count())
		q, _ := parseQuarantineTag(token)
		w.f.ec2 = &afterDeleteTags{ec2API: w.h.ec2, after: func(context.Context) { w.setQuarantine(q) }}
		if _, err := w.f.ReturnQuarantinedSlot(context.Background(), "i-dark", token); err != nil {
			t.Fatal(err)
		}
		if w.heldOut() {
			t.Fatalf("records %+v: the late write was carried to the new count and holds the slot", w.records())
		}
	})
}

// When the claim lease lapsed while the delete was in flight and a newer Start counted, the
// return must not count again: that would void the new Start's own token.
func TestReturnQuarantinedSlotDoesNotOvertakeANewerStartAfterALapsedLease(t *testing.T) {
	w := quarantineWorld(t, true)
	g := w.count()
	token := w.held(g)
	w.f.ec2 = &afterDeleteTags{ec2API: w.h.ec2, after: func(ctx context.Context) {
		homeGuardOf(ctx).markLost()
		w.startLands()
	}}
	res, err := w.f.ReturnQuarantinedSlot(context.Background(), "i-dark", token)
	if err != nil {
		t.Fatal(err)
	}
	if w.count() != g+1 || res.CountBumped {
		t.Fatalf("count %d (want %d), result %+v: an expired return moved the count", w.count(), g+1, res)
	}
	if len(w.records()) != 0 {
		t.Fatalf("the delete did not stand: %+v", w.records())
	}
}
