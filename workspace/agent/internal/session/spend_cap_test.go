package session

import (
	"testing"
	"time"
)

func TestSpendCrossingBound(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 10, 0, time.UTC)
	created := now.Add(-time.Hour).Format(time.RFC3339)
	t1, t2 := now.Add(-30*time.Second), now.Add(-8*time.Second)
	sp := Spend{Marks: []SpendMark{{End: t1, USD: 4.9}, {End: t2, USD: 5.2}}}
	if got := SpendCrossingBound(sp, 5, created, now); !got.Equal(t1) {
		t.Fatalf("bound = %v, want the end of the last turn under the cap %v", got, t1)
	}
	if got := SpendCrossingBound(sp, 4, created, now); got.Format(time.RFC3339) != created {
		t.Fatalf("first turn crossed: bound = %v, want the session's creation", got)
	}
	// An untimed turn under the cap does not move the bound past the last known one.
	sp.Marks = []SpendMark{{End: t1, USD: 1}, {USD: 2}, {End: t2, USD: 6}}
	if got := SpendCrossingBound(sp, 5, created, now); !got.Equal(t1) {
		t.Fatalf("bound = %v, want %v", got, t1)
	}
	// Never older than a live arm can be.
	old := now.Add(-StopArmMaxAge * 2).Format(time.RFC3339)
	got := SpendCrossingBound(Spend{}, 5, old, now)
	if _, live := StopArmedAt(Meta{StopAfterTurnAt: got.Format(time.RFC3339)}, now); !live {
		t.Fatalf("bound %v would be an expired arm", got)
	}
}
