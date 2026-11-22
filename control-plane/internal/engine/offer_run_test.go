package engine

import (
	"testing"
	"time"
)

func offerList() []Class {
	return []Class{
		{ID: "a-spot", Buy: BuySpot},
		{ID: "b-od", Buy: BuyOnDemand},
		{ID: "c-spot", Buy: BuySpot},
		{ID: "d-od"},
	}
}

func TestOfferRunWalksTheListInDeclarationOrder(t *testing.T) {
	r := NewOfferRun(0)
	r.Begin(offerList())
	var got []string
	for c, ok := r.Current(); ok; c, ok = r.Advance() {
		got = append(got, c.ID)
	}
	if want := "a-spot,b-od,c-spot,d-od"; join(got) != want {
		t.Fatalf("walk = %s, want %s", join(got), want)
	}
	if _, ok := r.Current(); ok {
		t.Fatal("Current must be empty once the list is spent")
	}
}

func TestOfferRunQuotaTakesThePurchaseOptionOffTheTable(t *testing.T) {
	r := NewOfferRun(0)
	r.Begin(offerList())
	r.Took(Class{ID: "a-spot", Buy: BuySpot}, "i-1")
	r.Settle(OfferQuota)
	next, ok := r.Advance()
	if !ok || next.ID != "b-od" {
		t.Fatalf("after a Spot quota error the next offer is on-demand, got %+v ok=%v", next, ok)
	}
	// c-spot is skipped, d-od (empty Buy = on-demand) is next.
	next, ok = r.Advance()
	if !ok || next.ID != "d-od" {
		t.Fatalf("Spot offers stay skipped for this demand, got %+v ok=%v", next, ok)
	}
	trail := r.Attempts()
	if len(trail) != 1 || trail[0].Result != OfferQuota || trail[0].Buy != BuySpot {
		t.Fatalf("trail = %+v", trail)
	}
}

func TestOfferRunSettleOnAnUnboughtOfferStillWritesTheTrail(t *testing.T) {
	r := NewOfferRun(0)
	r.Begin(offerList())
	r.Settle(OfferUnusable)
	trail := r.Attempts()
	if len(trail) != 1 || trail[0].ID != "a-spot" || trail[0].Result != OfferUnusable {
		t.Fatalf("trail = %+v", trail)
	}
}

func TestOfferRunBoxIsTheInFlightFlagAndTheRegistrationTimerReadsTheClock(t *testing.T) {
	r := NewOfferRun(0)
	clk := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return clk })
	r.Begin(offerList())
	if r.StartInFlight() {
		t.Fatal("no box, no start in flight")
	}
	if _, ok := r.WaitedForBox(); ok {
		t.Fatal("nothing to wait for before a purchase")
	}
	c, _ := r.Current()
	r.Took(c, "i-1")
	clk = clk.Add(40 * time.Second)
	if waited, ok := r.WaitedForBox(); !ok || waited != 40*time.Second {
		t.Fatalf("waited = %v ok=%v", waited, ok)
	}
	if !r.StartInFlight() || r.BoxID() != "i-1" {
		t.Fatal("the bought box marks the start in flight")
	}
	r.Settled("i-1")
	if r.StartInFlight() || !r.HadBox() {
		t.Fatal("a registered box ends the start and becomes the ledger entry")
	}
	r.ForgetBox()
	if r.HadBox() {
		t.Fatal("ForgetBox clears the ledger")
	}
}

func TestOfferRunInterruptedTwiceInARowIsSkippedAndRestartSkipsIt(t *testing.T) {
	r := NewOfferRun(0)
	r.Begin(offerList())
	if r.NoteInterrupted("a-spot") {
		t.Fatal("once is not twice")
	}
	if !r.NoteInterrupted("a-spot") {
		t.Fatal("twice in a row is skipped")
	}
	c, ok := r.Restart(offerList())
	if !ok || c.ID != "b-od" {
		t.Fatalf("restart must begin at the first non-skipped offer, got %+v ok=%v", c, ok)
	}
	c, _ = r.Current()
	r.Took(c, "i-2")
	if !r.Rebuilding() {
		t.Fatal("a restart with a bought box is a rebuild in flight")
	}
	if !r.RebuildAbandoned(0) || r.RebuildAbandoned(1) {
		t.Fatal("a rebuild is abandoned only once the desired count is back at zero")
	}
	r.DropBox()
	if r.Rebuilding() {
		t.Fatal("DropBox ends the rebuild")
	}
}

func TestOfferRunNoOfferIsRecordedOnce(t *testing.T) {
	r := NewOfferRun(0)
	r.Begin(nil)
	if !r.NoteNoOfferOnce() || r.NoteNoOfferOnce() {
		t.Fatal("the refusal is audited once per demand")
	}
	r.Begin(nil)
	if !r.NoteNoOfferOnce() {
		t.Fatal("a new demand audits again")
	}
}

func TestOfferRunBudgetIsLiveAndNilSafe(t *testing.T) {
	r := NewOfferRun(0)
	if r.Budget() != OfferBudgetDefault {
		t.Fatalf("unset budget = %v", r.Budget())
	}
	if !r.SetBudget(900*time.Second) || r.SetBudget(900*time.Second) {
		t.Fatal("SetBudget reports a change only when the figure moved")
	}
	if r.Budget() != 900*time.Second {
		t.Fatalf("budget = %v", r.Budget())
	}
	var nilRun *OfferRun
	if nilRun.Budget() != OfferBudgetDefault || nilRun.StartInFlight() || nilRun.Attempts() != nil {
		t.Fatal("a nil run answers as an idle one")
	}
	nilRun.Begin(offerList())
	nilRun.Settle(OfferQuota)
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
