package engine

// offer_run.go — rule 2's in-memory state for one demand: the candidate list, where in it the
// walk is, which box the current offer bought and what every earlier offer answered (ADR 0075,
// ADR 0077). Nothing here calls ECS or EC2; the engine runtime state in package main (engine_offer.go)
// drives it.

import (
	"errors"
	"sync"
	"time"
)

// What one offer came to, as the panel reads it (contract B, unchanged — the Console is not
// touched by ADR 0077). `active` is the one that is not an outcome yet: it is the offer whose box
// this engine is on, or is waiting for.
const (
	OfferActive        = "active"
	OfferUnfulfillable = "unfulfillable"
	OfferInsufficient  = "insufficient"
	OfferQuota         = "quota"
	// OfferBudget is the offer whose box was bought and never joined the cluster inside
	// `<role>OfferBudgetSec`. 🔴 That is ALL the budget means now (ADR 0077 decision 1): under
	// ADR 0075 it timed "did this provider produce a box", which is the question `CreateFleet`
	// answers in one call, so what is left to time is the box's own boot → ECS registration.
	OfferBudget = "budget"
	// OfferInterrupted is the offer whose box was TAKEN AWAY (ADR 0077 decision 4): a Spot
	// reclaim, or anything else that ends the instance while the service still wants a task. It
	// is not a failure — rule 3 of what the operator asked for is "a Spot box dying suddenly is
	// acceptable, and the rebuild handles it" — and it is in the trail because "why is it on this
	// box" has to be answerable after one.
	OfferInterrupted = "interrupted"
	// OfferUnusable is the offer that could not even be ASKED for. Under ADR 0075 that was
	// `UpdateCapacityProvider` refusing a rung (400, "No instance types satisfy the instance
	// requirements") while the provider still held the PREVIOUS offer's declaration; here a
	// misspelt type is refused by `CreateFleet` on the spot, which is the same verdict arriving
	// in the response to its own request.
	OfferUnusable = "unusable"
)

// ErrBoxRegistering says the box for this offer is bought and has not joined the cluster
// yet. NOT a failed start: the expensive half has succeeded, and the next attempt is one
// ListContainerInstances away from writing the desired count. Counting it would double a cooldown
// over a call that did exactly what it was asked to.
//
// ⚠️ The wait it stands for is tens of seconds to minutes (ADR 0045 decision 22 measured boot →
// ECS registration at 21 s for a CPU slot), so it is answered by coming back on the next tick
// rather than by sleeping inside this one — the admin toggle shares this path and it answers an
// HTTP request.
var ErrBoxRegistering = errors.New("the box is bought; waiting for it to register with the cluster")

// ErrOffersSpent says every candidate offer was tried and none produced a box. ONE failed
// start, which is what the controller's cooldown counts.
var ErrOffersSpent = errors.New("every offer was tried and none produced a box")

// ErrFleetRefused says EC2 refused the request for a reason that is about the DEPLOYMENT
// and not about the offer: a grant the CP does not hold. Every other row would be refused the
// same way, so the walk stops where it is — and the cooldown, not a busy loop, decides when the
// next demand may try again.
var ErrFleetRefused = errors.New("EC2 refused to launch for this deployment")

// OfferAttempt is one row of the trail: which offer was tried, how it was bought, and what
// came of it.
type OfferAttempt struct {
	ID     string
	Buy    string
	Result string
}

// OfferRun is rule 2's state for ONE demand: the candidate list this start was judged on,
// where in it we are, which box the current offer bought, when, and what every earlier offer
// answered.
//
// In memory and nowhere else. A CP replaced mid-start adopts nothing — it reports no trail and
// waits for no box it did not buy — because the alternative is inferring somebody else's attempt
// from a cluster it did not write. What makes that safe is decision 3: a box it forgot is still
// findable by TAG, and the sweep (decision 5) is what collects it.
type OfferRun struct {
	mu    sync.Mutex
	list  []Class
	idx   int
	trail []OfferAttempt
	// box is the EC2 instance the current offer bought, "" before the purchase. It is also the
	// flag for "a start is in flight": the gate has chosen the offer, the money is being spent,
	// and no second path may choose again.
	box string
	// since is when that box was bought, i.e. when the registration ceiling started.
	since time.Time
	// skipBuy holds the purchase options a quota error has taken off the table for this demand
	// (decision 8). The quotas are SEPARATE (`L-DB2E81BA` on-demand, `L-3819A6DF` Spot), so
	// "on-demand is full" says nothing about Spot and vice versa — which is exactly why the
	// answer to a quota error is to change purchase option rather than to wait.
	skipBuy map[string]bool
	// noOffer is whether the refusal of decision 2 has already been recorded. Without it, an
	// engine whose models outgrew every offer would audit a line every tick.
	noOffer bool
	// rebuild is true while this walk is decision 4's REBUILD rather than a start: the desired
	// count is already 1 and the task is PENDING, so the walk buys a box and writes nothing.
	// Told apart from a start because the two end differently, and because the departure sweep
	// has to stand down for both.
	rebuild bool
	// skipOffer holds the offers an interruption has taken off the table for this demand: an
	// offer interrupted TWICE IN A ROW is skipped for the rest of it (decision 4, inherited from
	// ADR 0075 decision 6). lastInterrupted is what "in a row" is measured against.
	skipOffer       map[string]bool
	lastInterrupted string
	// onBox is the box this engine's task is believed to be on: the one the last walk left the
	// cluster with. 🔴 It is the LEDGER decision 4's detection needs when there is no state
	// transition to read — a box taken away before its task ever reached RUNNING produces none
	// (measured, ADR 0077 P2 hardware run) — and it is the difference between "no box is
	// running, so one was taken away" and "no box is running because none was ever bought".
	// In memory, like everything else here: a CP replaced mid-demand detects nothing and says
	// nothing, which is the same limit the transition already had (prevState is memory too).
	onBox string
	// budgetSec is how long one offer's box is given to register, live. Carried here rather than
	// read off the row this process started with, because the engine table is re-read while the
	// CP runs and an operator raising the budget must not need a Control Plane replacement to be
	// heard.
	budgetSec int
	now       func() time.Time // test seam
}

func NewOfferRun(budget time.Duration) *OfferRun {
	r := &OfferRun{now: time.Now}
	r.SetBudget(budget)
	return r
}

// Budget is the per-offer registration ceiling, live.
func (r *OfferRun) Budget() time.Duration {
	if r == nil {
		return OfferBudgetDefault
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.budgetSec <= 0 {
		return OfferBudgetDefault
	}
	return time.Duration(r.budgetSec) * time.Second
}

// SetBudget takes a new budget from the table, reporting whether it changed. It keys nothing and
// is read once per tick, so unlike the controller's own intervals it can move under a running
// start — the box being waited on simply gets the new figure.
func (r *OfferRun) SetBudget(d time.Duration) bool {
	if r == nil {
		return false
	}
	secs := int(d.Seconds())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.budgetSec == secs {
		return false
	}
	r.budgetSec = secs
	return true
}

func (r *OfferRun) clock() time.Time {
	if r == nil {
		return time.Time{}
	}
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// Begin starts a fresh walk down the candidate list. Called by the start gate, which is the one
// place that knows the start is actually about to happen — and which refuses to run at all while
// a box is already bought (see startGate), so this can never throw one away.
func (r *OfferRun) Begin(list []Class) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]Class(nil), list...)
	r.idx, r.trail, r.noOffer = 0, nil, false
	r.skipBuy, r.skipOffer = map[string]bool{}, map[string]bool{}
	r.box, r.since, r.rebuild, r.lastInterrupted, r.onBox = "", time.Time{}, false, "", ""
}

// Restart puts the walk back at the top of the list after an interruption (decision 4).
//
// 🔴 It is NOT Begin(): the demand has not changed, so the trail and both skip lists are KEPT. A
// trail that forgot the interruption would answer "why is this engine on this box" with the box
// that was taken away, and a skip list that forgot would buy the same reclaimed offer for ever.
// It reports the offer to try, and false when every candidate has been skipped.
func (r *OfferRun) Restart(list []Class) (Class, bool) {
	if r == nil {
		return Class{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]Class(nil), list...)
	r.box, r.since, r.rebuild = "", time.Time{}, true
	for i := range r.list {
		if r.skipBuy[r.list[i].BuyOption()] || r.skipOffer[r.list[i].ID] {
			continue
		}
		r.idx = i
		return r.list[i], true
	}
	r.idx = len(r.list)
	return Class{}, false
}

// Settled ends the walk on the box that registered, and remembers it: the walk is over, and from
// here that box is what the engine is believed to be running on.
func (r *OfferRun) Settled(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.box, r.since, r.rebuild, r.onBox = "", time.Time{}, false, id
	r.mu.Unlock()
}

// HadBox reports whether the ledger holds a box, i.e. whether "nothing of this role is running"
// is a LOSS rather than the ordinary state of an engine that has not bought anything.
func (r *OfferRun) HadBox() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onBox != ""
}

// ForgetBox clears the ledger, for a box whose loss has been acted on (or which the CP itself
// ended). Without it one interruption would be detected on every tick until the replacement
// registered.
func (r *OfferRun) ForgetBox() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.onBox = ""
	r.mu.Unlock()
}

// RebuildAbandoned reports whether a rebuild is in flight for a service that no longer wants a
// task. Its box has to be ended by the caller: nothing would drive the walk again.
func (r *OfferRun) RebuildAbandoned(desired int32) bool {
	if r == nil || desired >= 1 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuild && r.box != ""
}

// Rebuilding reports whether the walk in flight is a rebuild.
func (r *OfferRun) Rebuilding() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuild && r.box != ""
}

// NoteInterrupted records that the current offer's box was taken away, and reports whether that
// was the SECOND in a row for this offer — which takes it off the table for the rest of this
// demand (decision 4). "In a row" is per offer and not per demand: an offer that was reclaimed,
// then replaced by another that was also reclaimed, gets its second chance.
func (r *OfferRun) NoteInterrupted(id string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.skipOffer == nil {
		r.skipOffer = map[string]bool{}
	}
	twice := r.lastInterrupted == id && id != ""
	r.lastInterrupted = id
	if twice {
		r.skipOffer[id] = true
	}
	return twice
}

// Current is the offer this walk is on.
func (r *OfferRun) Current() (Class, bool) {
	if r == nil {
		return Class{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idx < 0 || r.idx >= len(r.list) {
		return Class{}, false
	}
	return r.list[r.idx], true
}

// Took records the box this offer bought and starts its registration ceiling.
func (r *OfferRun) Took(c Class, instanceID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.box, r.since = instanceID, r.clock()
	r.trail = append(r.trail, OfferAttempt{ID: c.ID, Buy: c.BuyOption(), Result: OfferActive})
}

// BoxID is the instance the current offer bought, "" when nothing has been bought yet.
func (r *OfferRun) BoxID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.box
}

// StartInFlight reports whether money has already been spent on this demand: a box is bought and
// the desired count has not been written yet.
//
// 🔴 It is what keeps the two start paths — the admin toggle, which starts the box itself because
// somebody is watching, and the controller's next tick a second later — from buying two boxes.
// On the ADR 0075 deployment those two raced and `offer_trail` read `[spot3 active, spot3
// active]`, which is a lie about the walk; here the second one would be a second GPU.
func (r *OfferRun) StartInFlight() bool { return r.BoxID() != "" }

// DropBox forgets the current offer's box, for a walk that has just terminated it.
func (r *OfferRun) DropBox() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.box, r.since, r.rebuild = "", time.Time{}, false
	r.mu.Unlock()
}

// WaitedForBox is how long the current offer's box has had to register, and whether it has been
// bought at all.
func (r *OfferRun) WaitedForBox() (time.Duration, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.since.IsZero() {
		return 0, false
	}
	return r.clock().Sub(r.since), true
}

// Settle closes the current offer with its outcome.
//
// 🔴 It APPENDS when the offer has no row yet, because most outcomes now happen before anything
// is bought: `CreateFleet` answers in the same breath as the request, so an offer that was tried
// and refused never reaches Took(). A trail that only held the purchases would answer the panel's
// question — "why is it on this box" — by hiding every row that was asked for and could not be
// had.
func (r *OfferRun) Settle(result string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := Class{}, r.idx >= 0 && r.idx < len(r.list)
	if ok {
		cur = r.list[r.idx]
	}
	switch n := len(r.trail); {
	case n > 0 && r.trail[n-1].ID == cur.ID && r.trail[n-1].Result == OfferActive:
		r.trail[n-1].Result = result
	case ok:
		r.trail = append(r.trail, OfferAttempt{ID: cur.ID, Buy: cur.BuyOption(), Result: result})
	}
	if result == OfferQuota {
		if cur := r.idx; cur >= 0 && cur < len(r.list) {
			r.skipBuy[r.list[cur].BuyOption()] = true
		}
	}
}

// Advance moves to the next offer that is still worth trying, skipping the purchase options a
// quota error ruled out. false means the list has been gone round once.
func (r *OfferRun) Advance() (Class, bool) {
	if r == nil {
		return Class{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := r.idx + 1; i < len(r.list); i++ {
		if r.skipBuy[r.list[i].BuyOption()] || r.skipOffer[r.list[i].ID] {
			continue
		}
		r.idx = i
		return r.list[i], true
	}
	r.idx = len(r.list)
	return Class{}, false
}

// NoteNoOfferOnce is the guard on decision 2's refusal: once per demand, not once per tick.
func (r *OfferRun) NoteNoOfferOnce() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noOffer {
		return false
	}
	r.noOffer = true
	return true
}

// Attempts is the trail, copied.
func (r *OfferRun) Attempts() []OfferAttempt {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]OfferAttempt(nil), r.trail...)
}

// SetClock replaces the clock the registration timer reads. Tests only: the default is time.Now.
func (r *OfferRun) SetClock(now func() time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}
