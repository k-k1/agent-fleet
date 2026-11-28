package main

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// An Agent stamps a turn end either to the second (older ones, kiro) or with fractions
// (status.go's RFC3339Nano). Both must pin the answer read to the same boundary — the next whole
// second — or a person's resume in the second after a fractional end is read as the run's own turn.
func TestMemoAnswerPinIsTheNextSecondForWholeAndFractionalStamps(t *testing.T) {
	want := time.Date(2026, 11, 28, 12, 0, 1, 0, time.UTC)
	for _, stamp := range []string{
		"2026-11-28T12:00:00Z",
		"2026-11-28T12:00:00.000000001Z",
		"2026-11-28T12:00:00.800Z",
		"2026-11-28T12:00:00.999999999Z",
		"2026-11-28T21:00:00.800+09:00",
	} {
		d := store.MemoDelivery{CloseTurnGen: 3, HeldGen: 3, Outcome: store.DeliveryClean, CloseTurnEndAt: stamp}
		got, ok := memoAnswerPin(d)
		if !ok || !got.Equal(want) {
			t.Errorf("stamp %q: pin = %v (ok=%v), want %v", stamp, got, ok, want)
		}
	}
	// A turn starting 12:00:01.100 (a person's quick resume) is at or after the pin, never the run's.
	pin, _ := memoAnswerPin(store.MemoDelivery{CloseTurnGen: 1, HeldGen: 1, Outcome: store.DeliveryClean, CloseTurnEndAt: "2026-11-28T12:00:00.800Z"})
	resume := time.Date(2026, 11, 28, 12, 0, 1, 100_000_000, time.UTC)
	if resume.Before(pin) {
		t.Errorf("a resume at %v reads as inside the run (pin %v)", resume, pin)
	}
}
