package usagehistory

import (
	"math"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/usagehist"
)

func f(v float64) *float64 { return &v }
func u(v uint64) *uint64   { return &v }

// The Agent runs next to member code: a figure that cannot be a measurement becomes "not
// measured" and the rest of the minute survives; a window with no usable identity or time is out.
func TestSanitizeDropsFiguresThatCannotBeMeasurements(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := []Window{
		{T: now.Unix() - 60, LID: "A", CPUPct: f(-1), WorkingSet: u(1 << 30), Some10Max: f(math.Inf(1)), Full60: f(0.5), RefaultRate: f(2)},
		{T: now.Unix() - 60, LID: "", CPUPct: f(1)},                       // no launch id
		{T: now.Unix() + 3600, LID: "A", CPUPct: f(1)},                    // from the future
		{T: 0, LID: "A", CPUPct: f(1)},                                    // no time
		{T: now.Unix() - 30, LID: "A", WorkingSet: u(1 << 60)},            // absurd
		{T: now.Unix() - 30, LID: string(make([]byte, 65)), CPUPct: f(1)}, // not an id we issued
	}
	got := Sanitize(in, now)
	if len(got) != 2 {
		t.Fatalf("kept %d windows: %+v", len(got), got)
	}
	if got[0].CPUPct != nil || got[0].Some10Max != nil || got[0].WorkingSet == nil || got[0].Full60 == nil || got[0].RefaultRate == nil {
		t.Fatalf("figure-level drop wrong: %+v", got[0])
	}
	if got[1].WorkingSet != nil {
		t.Fatalf("absurd working set kept: %+v", got[1])
	}
}

// vCPU-normalised CPU exists only where the rung declared its vCPU, quantiles never exceed the
// exact max, and a signal with no measured minute is null however the row's other columns look.
func TestDayOfNormalisesOnlyWithADeclaredVCPU(t *testing.T) {
	var h usagehist.Hist
	for i := 0; i < 20; i++ {
		h.Add(usagehist.CPU, 100)
	}
	max := 101.0
	row := store.UsageDayLabelled{UsageDay: store.UsageDay{
		Day: "2026-11-10", InstanceType: "m7i.large", VCPU: 2, RunningSecs: 1200,
		CPUMeasuredMin: 20, CPUHist: h, CPUMax: &max,
	}}
	w := DayOf(row, false)
	if w.CPUP95 == nil || *w.CPUP95 > max || w.CPUBoxP95 == nil || math.Abs(*w.CPUBoxP95-*w.CPUP95/2) > 1e-9 {
		t.Fatalf("p95 %v box %v", w.CPUP95, w.CPUBoxP95)
	}
	if w.RunningMinutes != 20 {
		t.Fatalf("running minutes = %v", w.RunningMinutes)
	}
	row.VCPU = 0
	w = DayOf(row, false)
	if w.CPUP95 == nil || w.CPUBoxP95 != nil || w.VCPU != nil {
		t.Fatalf("undeclared vCPU: p95 %v box %v vcpu %v", w.CPUP95, w.CPUBoxP95, w.VCPU)
	}
	// A burst count only means something against a declared baseline.
	n := 3
	row.BurstMin, row.BaselinePct = &n, 40
	if got := DayOf(row, false).AboveBaselineMinutes; got != nil {
		t.Fatalf("burst minutes %d reported without a vCPU", *got)
	}
	row.VCPU = 2
	if got := DayOf(row, false).AboveBaselineMinutes; got == nil || *got != 3 {
		t.Fatalf("burst minutes = %v", got)
	}
	// Max stored but the signal was never measured this day: still null.
	row.WSMax = u(5 << 30)
	if w := DayOf(row, false); w.WorkingSetMax != nil || w.WorkingSetP95 != nil || w.PSIWarnMinutes != nil {
		t.Fatalf("unmeasured signal reported: %+v", w)
	}
}

// A pull must not move its cursor past windows dropped for their timestamp.
func TestHoldBeforeFuture(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ok := now.Unix() - 120
	if _, _, skewed := HoldBeforeFuture([]Window{{T: ok - 60}, {T: ok}}, now); skewed {
		t.Fatal("clean windows reported as skewed")
	}
	to, held, skewed := HoldBeforeFuture([]Window{{T: ok - 60}, {T: ok}, {T: now.Unix() + 3600}, {T: now.Unix() + 3660}}, now)
	if !skewed || !held || to != ok {
		t.Fatalf("to %d held %v skewed %v, want the last clean window %d", to, held, skewed, ok)
	}
	if _, held, skewed := HoldBeforeFuture([]Window{{T: now.Unix() + 3600}}, now); !skewed || held {
		t.Fatalf("nothing before the skew: held %v skewed %v", held, skewed)
	}
}
