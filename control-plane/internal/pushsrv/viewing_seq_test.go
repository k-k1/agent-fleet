package pushsrv

import (
	"testing"
	"time"
)

func TestViewingDropsAHeartbeatThatOvertookItsClear(t *testing.T) {
	var v viewing
	now := time.Now()
	v.set("m1", "d", "s1", 1, now)
	v.set("m1", "d", "", 3, now)   // the clear, seq 3
	v.set("m1", "d", "s1", 2, now) // the heartbeat sent before it, arriving late
	if v.on("m1", "s1", now) {
		t.Fatal("a late heartbeat suppressed pushes after the clear")
	}
	v.set("m1", "d", "s1", 4, now) // a genuinely newer report still counts
	if !v.on("m1", "s1", now) {
		t.Fatal("a newer report was dropped")
	}
}

func TestViewingWithoutSeqKeepsArrivalOrder(t *testing.T) {
	var v viewing
	now := time.Now()
	v.set("m1", "d", "s1", 0, now)
	v.set("m1", "d", "", 0, now)
	if v.on("m1", "s1", now) || len(v.byMember) != 0 {
		t.Fatal("an unsequenced clear must delete the entry")
	}
	v.set("m1", "d", "s1", 5, now)
	v.set("m1", "d", "s2", 0, now) // an older Console without seq is applied as before
	if !v.on("m1", "s2", now) {
		t.Fatal("an unsequenced report was dropped")
	}
}

func TestViewingTombstoneExpires(t *testing.T) {
	var v viewing
	now := time.Now()
	v.set("m1", "d", "", 9, now)
	v.set("other", "d", "s", 0, now.Add(tombstoneTTL+sweepEvery+time.Second))
	if _, ok := v.byMember["m1"]; ok {
		t.Fatal("a tombstone outlived its TTL")
	}
}
