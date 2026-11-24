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

func TestViewingSeqMarkExpiresWithoutASweep(t *testing.T) {
	var v viewing
	now := time.Now()
	v.set("m1", "d", "", 9, now)
	// Inside the TTL a lower seq is late; past it, the age check alone lets it through even
	// when no sweep has run (the sweep is rate limited, so it may not have).
	v.set("m1", "d", "s1", 2, now.Add(seqTTL-time.Second))
	if v.on("m1", "s1", now.Add(seqTTL-time.Second)) {
		t.Fatal("a late report inside the TTL was applied")
	}
	v.set("m1", "d", "s1", 2, now.Add(seqTTL+time.Second))
	if !v.on("m1", "s1", now.Add(seqTTL+time.Second)) {
		t.Fatal("an expired seq mark still dropped a report")
	}
}

func TestViewingSeqMarksDoNotTakeOnScreenSlots(t *testing.T) {
	var v viewing
	now := time.Now()
	v.set("m1", "watcher", "s1", 1, now)
	// Many other devices open and leave: their clears must not evict the one still on screen.
	for i := 0; i < 3*maxViewDevices; i++ {
		d := "tab" + string(rune('a'+i%26)) + string(rune('A'+i/26))
		v.set("m1", d, "s9", int64(1), now)
		v.set("m1", d, "", int64(2), now)
	}
	if !v.on("m1", "s1", now) {
		t.Fatal("a device on screen was evicted by other devices' clears")
	}
}

func TestViewingKeepsAClearUnderChurn(t *testing.T) {
	var v viewing
	now := time.Now()
	v.set("m1", "d", "s1", 1, now)
	v.set("m1", "d", "", 3, now)
	for i := 0; i < maxViewDevices+4; i++ { // churn of on-screen devices
		v.set("m1", "x"+string(rune('a'+i)), "s2", 1, now)
	}
	v.set("m1", "d", "s1", 2, now) // the heartbeat that overtook the clear
	if v.on("m1", "s1", now) {
		t.Fatal("a late heartbeat revived the view after its clear was crowded out")
	}
}
