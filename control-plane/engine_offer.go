package main

// engine_offer.go — which box this role buys, out of a list of OFFERS (ADR 0075 P0, rebuilt on
// ADR 0077 P1).
//
// ADR 0074 gave the role a ladder of rungs and let an administrator pick one. ADR 0075 kept the
// ladder and changed what the CP does with it: the rungs became offers, each with a purchase
// option, and a start walks them from the top until a box actually arrives. Three rules the
// operator asked for, and everything in this file is one of them:
//
//  1. buy what fits the VRAM, cheapest first, without caring whether it is Spot or on-demand —
//     "cheapest first" is the DECLARATION ORDER, because the CP knows no prices (decision 1);
//  2. if Spot cannot be had, take on-demand;
//  3. a Spot box may be taken away; rebuild from the top of the list (P2, not here).
//
// 🔴 ADR 0077 changed WHO BUYS, and that is what this file now reads like. Under 0075 the CP
// moved the service's capacity provider strategy and ECS went shopping; the outcome had to be
// inferred from service events minutes later, which bought two boxes, read one offer's echo as
// the next offer's answer, and could not declare an offer unbuyable (three hardware rounds,
// about $2.2). Now one offer row is one synchronous `CreateFleet` (engine_fleet.go) and the
// ORDER IS REVERSED: buy the box, wait for it to register with the cluster, and only then move
// the desired count. There is no interval in which desired is 1 and no box exists.
//
// 🔴 The two invariants that keep this from being expensive:
//
//   - nothing in this file runs for a deployment that declares no offer. The hooks are attached
//     in newEngineRegistry only when the list is non-empty AND a launch template is declared, so
//     such a deployment makes not one extra ECS or EC2 call (ADR 0074 decision 3, inherited);
//   - going round the whole list once is ONE failure, not one per offer. The controller's
//     cooldown doubles per consecutive failure up to 16x, and counting per offer would make a
//     three-row list reach the four-hour cooldown three times as fast.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// What one offer came to, as the panel reads it (contract B, unchanged — the Console is not
// touched by ADR 0077). `active` is the one that is not an outcome yet: it is the offer whose box
// this engine is on, or is waiting for.
const (
	engineOfferActive        = "active"
	engineOfferUnfulfillable = "unfulfillable"
	engineOfferInsufficient  = "insufficient"
	engineOfferQuota         = "quota"
	// engineOfferBudget is the offer whose box was bought and never joined the cluster inside
	// `<role>OfferBudgetSec`. 🔴 That is ALL the budget means now (ADR 0077 decision 1): under
	// ADR 0075 it timed "did this provider produce a box", which is the question `CreateFleet`
	// answers in one call, so what is left to time is the box's own boot → ECS registration.
	engineOfferBudget = "budget"
	// engineOfferInterrupted is the offer whose box was TAKEN AWAY (ADR 0077 decision 4): a Spot
	// reclaim, or anything else that ends the instance while the service still wants a task. It
	// is not a failure — rule 3 of what the operator asked for is "a Spot box dying suddenly is
	// acceptable, and the rebuild handles it" — and it is in the trail because "why is it on this
	// box" has to be answerable after one.
	engineOfferInterrupted = "interrupted"
	// engineOfferUnusable is the offer that could not even be ASKED for. Under ADR 0075 that was
	// `UpdateCapacityProvider` refusing a rung (400, "No instance types satisfy the instance
	// requirements") while the provider still held the PREVIOUS offer's declaration; here a
	// misspelt type is refused by `CreateFleet` on the spot, which is the same verdict arriving
	// in the response to its own request.
	engineOfferUnusable = "unusable"
)

// errEngineBoxRegistering says the box for this offer is bought and has not joined the cluster
// yet. NOT a failed start: the expensive half has succeeded, and the next attempt is one
// ListContainerInstances away from writing the desired count. Counting it would double a cooldown
// over a call that did exactly what it was asked to.
//
// ⚠️ The wait it stands for is tens of seconds to minutes (ADR 0045 decision 22 measured boot →
// ECS registration at 21 s for a CPU slot), so it is answered by coming back on the next tick
// rather than by sleeping inside this one — the admin toggle shares this path and it answers an
// HTTP request.
var errEngineBoxRegistering = errors.New("the box is bought; waiting for it to register with the cluster")

// errEngineOffersSpent says every candidate offer was tried and none produced a box. ONE failed
// start, which is what the controller's cooldown counts.
var errEngineOffersSpent = errors.New("every offer was tried and none produced a box")

// errEngineFleetRefused says EC2 refused the request for a reason that is about the DEPLOYMENT
// and not about the offer: a grant the CP does not hold. Every other row would be refused the
// same way, so the walk stops where it is — and the cooldown, not a busy loop, decides when the
// next demand may try again.
var errEngineFleetRefused = errors.New("EC2 refused to launch for this deployment")

// engineOfferAttempt is one row of the trail: which offer was tried, how it was bought, and what
// came of it.
type engineOfferAttempt struct {
	ID     string
	Buy    string
	Result string
}

// engineOfferRun is rule 2's state for ONE demand: the candidate list this start was judged on,
// where in it we are, which box the current offer bought, when, and what every earlier offer
// answered.
//
// In memory and nowhere else. A CP replaced mid-start adopts nothing — it reports no trail and
// waits for no box it did not buy — because the alternative is inferring somebody else's attempt
// from a cluster it did not write. What makes that safe is decision 3: a box it forgot is still
// findable by TAG, and the sweep (decision 5) is what collects it.
type engineOfferRun struct {
	mu    sync.Mutex
	list  []engineClass
	idx   int
	trail []engineOfferAttempt
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
	// budgetSec is how long one offer's box is given to register, live. Carried here rather than
	// read off the row this process started with, because the engine table is re-read while the
	// CP runs and an operator raising the budget must not need a Control Plane replacement to be
	// heard.
	budgetSec int
	now       func() time.Time // test seam
}

func newEngineOfferRun(budget time.Duration) *engineOfferRun {
	r := &engineOfferRun{now: time.Now}
	r.setBudget(budget)
	return r
}

// budget is the per-offer registration ceiling, live.
func (r *engineOfferRun) budget() time.Duration {
	if r == nil {
		return engineOfferBudgetDefault
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.budgetSec <= 0 {
		return engineOfferBudgetDefault
	}
	return time.Duration(r.budgetSec) * time.Second
}

// setBudget takes a new budget from the table, reporting whether it changed. It keys nothing and
// is read once per tick, so unlike the controller's own intervals it can move under a running
// start — the box being waited on simply gets the new figure.
func (r *engineOfferRun) setBudget(d time.Duration) bool {
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

func (r *engineOfferRun) clock() time.Time {
	if r == nil {
		return time.Time{}
	}
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// begin starts a fresh walk down the candidate list. Called by the start gate, which is the one
// place that knows the start is actually about to happen — and which refuses to run at all while
// a box is already bought (see startGate), so this can never throw one away.
func (r *engineOfferRun) begin(list []engineClass) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]engineClass(nil), list...)
	r.idx, r.trail, r.noOffer = 0, nil, false
	r.skipBuy, r.skipOffer = map[string]bool{}, map[string]bool{}
	r.box, r.since, r.rebuild, r.lastInterrupted = "", time.Time{}, false, ""
}

// restart puts the walk back at the top of the list after an interruption (decision 4).
//
// 🔴 It is NOT begin(): the demand has not changed, so the trail and both skip lists are KEPT. A
// trail that forgot the interruption would answer "why is this engine on this box" with the box
// that was taken away, and a skip list that forgot would buy the same reclaimed offer for ever.
// It reports the offer to try, and false when every candidate has been skipped.
func (r *engineOfferRun) restart(list []engineClass) (engineClass, bool) {
	if r == nil {
		return engineClass{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]engineClass(nil), list...)
	r.box, r.since, r.rebuild = "", time.Time{}, true
	for i := range r.list {
		if r.skipBuy[r.list[i].buy()] || r.skipOffer[r.list[i].ID] {
			continue
		}
		r.idx = i
		return r.list[i], true
	}
	r.idx = len(r.list)
	return engineClass{}, false
}

// rebuildAbandoned reports whether a rebuild is in flight for a service that no longer wants a
// task. Its box has to be ended by the caller: nothing would drive the walk again.
func (r *engineOfferRun) rebuildAbandoned(desired int32) bool {
	if r == nil || desired >= 1 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuild && r.box != ""
}

// rebuilding reports whether the walk in flight is a rebuild.
func (r *engineOfferRun) rebuilding() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuild && r.box != ""
}

// noteInterrupted records that the current offer's box was taken away, and reports whether that
// was the SECOND in a row for this offer — which takes it off the table for the rest of this
// demand (decision 4). "In a row" is per offer and not per demand: an offer that was reclaimed,
// then replaced by another that was also reclaimed, gets its second chance.
func (r *engineOfferRun) noteInterrupted(id string) bool {
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

// current is the offer this walk is on.
func (r *engineOfferRun) current() (engineClass, bool) {
	if r == nil {
		return engineClass{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idx < 0 || r.idx >= len(r.list) {
		return engineClass{}, false
	}
	return r.list[r.idx], true
}

// took records the box this offer bought and starts its registration ceiling.
func (r *engineOfferRun) took(c engineClass, instanceID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.box, r.since = instanceID, r.clock()
	r.trail = append(r.trail, engineOfferAttempt{ID: c.ID, Buy: c.buy(), Result: engineOfferActive})
}

// boxID is the instance the current offer bought, "" when nothing has been bought yet.
func (r *engineOfferRun) boxID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.box
}

// startInFlight reports whether money has already been spent on this demand: a box is bought and
// the desired count has not been written yet.
//
// 🔴 It is what keeps the two start paths — the admin toggle, which starts the box itself because
// somebody is watching, and the controller's next tick a second later — from buying two boxes.
// On the ADR 0075 deployment those two raced and `offer_trail` read `[spot3 active, spot3
// active]`, which is a lie about the walk; here the second one would be a second GPU.
func (r *engineOfferRun) startInFlight() bool { return r.boxID() != "" }

// dropBox forgets the current offer's box, for a walk that has just terminated it.
func (r *engineOfferRun) dropBox() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.box, r.since, r.rebuild = "", time.Time{}, false
	r.mu.Unlock()
}

// waitedForBox is how long the current offer's box has had to register, and whether it has been
// bought at all.
func (r *engineOfferRun) waitedForBox() (time.Duration, bool) {
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

// settle closes the current offer with its outcome.
//
// 🔴 It APPENDS when the offer has no row yet, because most outcomes now happen before anything
// is bought: `CreateFleet` answers in the same breath as the request, so an offer that was tried
// and refused never reaches took(). A trail that only held the purchases would answer the panel's
// question — "why is it on this box" — by hiding every row that was asked for and could not be
// had.
func (r *engineOfferRun) settle(result string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := engineClass{}, r.idx >= 0 && r.idx < len(r.list)
	if ok {
		cur = r.list[r.idx]
	}
	switch n := len(r.trail); {
	case n > 0 && r.trail[n-1].ID == cur.ID && r.trail[n-1].Result == engineOfferActive:
		r.trail[n-1].Result = result
	case ok:
		r.trail = append(r.trail, engineOfferAttempt{ID: cur.ID, Buy: cur.buy(), Result: result})
	}
	if result == engineOfferQuota {
		if cur := r.idx; cur >= 0 && cur < len(r.list) {
			r.skipBuy[r.list[cur].buy()] = true
		}
	}
}

// advance moves to the next offer that is still worth trying, skipping the purchase options a
// quota error ruled out. false means the list has been gone round once.
func (r *engineOfferRun) advance() (engineClass, bool) {
	if r == nil {
		return engineClass{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := r.idx + 1; i < len(r.list); i++ {
		if r.skipBuy[r.list[i].buy()] || r.skipOffer[r.list[i].ID] {
			continue
		}
		r.idx = i
		return r.list[i], true
	}
	r.idx = len(r.list)
	return engineClass{}, false
}

// noteNoOfferOnce is the guard on decision 2's refusal: once per demand, not once per tick.
func (r *engineOfferRun) noteNoOfferOnce() bool {
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

// attempts is the trail, copied.
func (r *engineOfferRun) attempts() []engineOfferAttempt {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engineOfferAttempt(nil), r.trail...)
}

// --- the engine's side ---------------------------------------------------------------

// wireOffers attaches everything that can buy, wait for or end a box, and attaches it ONLY when
// this role declares at least one offer AND a launch template to buy from.
//
// 🔴 This is ADR 0074 decision 3, inherited twice: a deployment that configures none of this has
// no path from its start to `CreateFleet`, so it cannot log an AccessDenied for a feature it does
// not use and cannot pay for a call it did not ask for. It is one function because the wirings
// are one claim — a test can hold the whole rule by calling it with an empty list and counting
// zero.
//
// The hooks also stand or fall together. startGate CHOOSES the offer, startWith is decision 1
// and 2 (buy, wait, then desired 1), boxStep is decision 5 (the box goes when the service does)
// and rebuildStep is decision 4 (the box was taken away). A gate with no start path would pick an
// offer nobody buys; a start path with no departure would leave a GPU running after the idle
// window closed; a departure with no rebuild would leave a reclaimed Spot box as an engine that
// is `starting` for ever.
func (e *engineRuntimeState) wireOffers(fleet *engineFleet) {
	if e == nil || len(e.classList()) == 0 || fleet == nil {
		return
	}
	e.fleet = fleet
	// `draining` is an EC2 fact now (ADR 0077 decision 5): the container instance is
	// deregistered before the terminate, so between those two steps only EC2 knows the box is
	// still there.
	if e.ecs != nil {
		e.ecs.mu.Lock()
		e.ecs.boxLive = fleet.live
		e.ecs.mu.Unlock()
	}
	if e.ctrl == nil {
		return
	}
	e.ctrl.startGate = e.startGate
	e.ctrl.startWith = e.startOnOffer
	e.ctrl.startSettling = e.offers.startInFlight
	e.ctrl.boxStep = e.sweepBoxes
	e.ctrl.rebuildStep = e.stepRebuild
}

// setOfferBudget takes a new registration ceiling from the table, reporting whether it changed.
func (e *engineRuntimeState) setOfferBudget(d time.Duration) bool {
	if e == nil {
		return false
	}
	return e.offers.setBudget(d)
}

// offerList is the declared offers. Same storage as ADR 0074's ladder: an offer IS a rung with a
// purchase option, and keeping one list is what stops the picker, the start gate and the panel
// from ever disagreeing about what exists.
func (e *engineRuntimeState) offerList() []engineClass { return e.classList() }

// candidateOffers is what this start may buy from, in the order it will try them (decisions 2
// and 8 of ADR 0075, inherited by 0077 decision 8).
//
//   - an administrator's stored choice is a PIN: that offer and nothing else, and no falling
//     through to the next one. Somebody who said "try it on the 48 GB card" must not be quietly
//     put on a 24 GB one — ADR 0074 decision 4 called that the most expensive kind of lie;
//   - no stored choice is AUTOMATIC: every offer whose declared VRAM covers the largest enabled
//     model, in declaration order. An `unknown` demand filters nothing (refusing to start because
//     nobody measured a model would take the engine away from a deployment that merely has not
//     been measured);
//   - with no launch template there is nothing to buy from, so there are no candidates at all.
//     That is the deployment whose CP was upgraded before its stack: it keeps serving, and it
//     keeps starting the engine the plain way, because the offer hooks were never attached.
func (e *engineRuntimeState) candidateOffers(ctx context.Context) []engineClass {
	list := e.offerList()
	if len(list) == 0 || e.fleet == nil {
		return nil
	}
	if id := e.selectedClassID(ctx); id != "" {
		if c, ok := engineClassByID(list, id); ok && id == c.ID {
			return []engineClass{c}
		}
		// A pin nobody declares any more. Falling back to AUTOMATIC rather than to the first
		// offer: the stored id is the only evidence of intent and it no longer names anything, so
		// the honest reading is "no choice", which is the one the operator can see and change.
		log.Printf("engines: %s: the pinned offer %q is not in the list; choosing automatically", e.def.Key, id)
	}
	need, _, _ := engineVramDemand(e.catalog.list(ctx))
	out := make([]engineClass, 0, len(list))
	for _, c := range list {
		if !engineClassFits(c, need) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// startOnOffer is the start itself (ADR 0077 decisions 1 and 2), and the order in it is the whole
// point of the ADR:
//
//	buy the box → wait for it to REGISTER with the cluster → desired 1.
//
// 🔴 THE ONLY `desiredCount: 1` IN THIS FILE IS BEHIND `registered()`. Under ADR 0075 the desired
// count is what sent ECS shopping, so a start was "ask, then find out what happened from the
// service's events"; every defect three hardware rounds found lives in that gap. Here nothing is
// asked for until the hardware is in the cluster, and a test states it as a negative (ADR 0077
// done item 3).
//
// Each iteration either returns or moves the walk on by one, so the loop is bounded by the
// candidate list.
func (e *engineRuntimeState) startOnOffer(ctx context.Context) error {
	if _, ok := e.offers.current(); !ok {
		// No walk in progress. The only way here is a start that did not come through the gate,
		// and starting the plain way is the same behaviour as every deployment without offers.
		return e.ecs.setEnabled(ctx, true)
	}
	// One buyer at a time. The admin toggle and the controller's tick can reach this within a
	// second of each other (measured on the ADR 0075 deployment), and two `CreateFleet` calls a
	// second apart are two GPUs.
	e.startMu.Lock()
	defer e.startMu.Unlock()
	// 🔴 Already asked for. This is the OTHER half of "one start buys one box", and it is what
	// makes it safe to forget the box once the count is written (below): the admin toggle calls
	// this unconditionally on `mode=on`, so without it a press while the engine is already
	// running would walk the list again and buy a second GPU.
	if v, err := e.ecs.view(ctx); err == nil && v.desired >= 1 {
		return nil
	}
	return e.walkOffers(ctx, true)
}

// walkOffers is the purchase walk itself, and both a start and a REBUILD (decision 4) are it.
//
// `ask` is the one difference: a start writes the desired count once its box has registered, and
// a rebuild does not — the count is already 1 and the task is PENDING, so ECS places it on the
// new box by itself the moment it registers. Writing it again would be a call that buys nothing;
// NOT writing it is what makes the rebuild synchronous and free of ADR 0075 decision 6's
// restriction ("write only when the first candidate differs from the current provider", which
// existed to avoid racing ECS's own re-placement — ECS buys nothing here).
//
// The caller holds startMu.
func (e *engineRuntimeState) walkOffers(ctx context.Context, ask bool) error {
	c, ok := e.offers.current()
	if !ok {
		return nil
	}
	after := ""
	for {
		if id := e.offers.boxID(); id != "" {
			b, known := e.ecs.boxFor(ctx, id)
			if known && b.registered() {
				// The box is in the cluster and ECS will place on it. This is the only line that
				// asks for a task, and it carries the desired count alone — no strategy, no
				// forced deployment, nothing that could replace a running one.
				log.Printf("engines: %s: the box %s registered (%s)", e.def.Key, id,
					map[bool]string{true: "asking for the task", false: "the pending task goes to it"}[ask])
				if ask {
					if err := e.ecs.setEnabled(ctx, true); err != nil {
						return err
					}
				}
				// 🔴 THE WALK IS OVER, AND FORGETTING THE BOX IS WHAT ENDS IT. `startInFlight`
				// means "bought and the desired count not written yet", and the departure sweep
				// stands down while it is true; until this line the only things that cleared it
				// were the registration ceiling and the next start, so a start that SUCCEEDED
				// left it true for ever and decision 5's departure never ran once. Measured on
				// hardware (ADR 0077 P1 run): `mode=off`, the task gone, and the box still
				// running four minutes later — terminated by hand. A rebuild ends here the same
				// way, which is what lets the sweep collect the box after the NEXT idle window.
				e.offers.dropBox()
				return nil
			}
			waited, started := e.offers.waitedForBox()
			if !started || waited < e.offers.budget() {
				return errEngineBoxRegistering
			}
			// Past the ceiling. The box is ours and it is billing, so it goes before the next
			// offer is tried — this is the one place ADR 0075's "two boxes" could still happen.
			log.Printf("engines: %s: the box %s for offer %s did not register within %s; ending it",
				e.def.Key, id, c.ID, e.offers.budget().Round(time.Second))
			e.endBox(ctx, id, "it never registered with the cluster")
			e.offers.dropBox()
			next, ok := e.nextOffer(ctx, engineOfferBudget)
			if !ok {
				return errEngineOffersSpent
			}
			c, after = next, engineOfferBudget
			continue
		}
		id, verdict := e.fleet.buy(ctx, c)
		if id != "" {
			e.offers.took(c, id)
			e.noteOffer(ctx, c, id, after)
			// Deliberately not "wait for it here": the caller may be an HTTP request, and the
			// next tick is five seconds away.
			return errEngineBoxRegistering
		}
		if verdict.action == engineFleetRefused {
			// 🔴 Not "try the next offer". The response said the deployment cannot launch
			// anything — a missing `iam:PassRole` arrives as `UnauthorizedOperation` INSIDE a
			// 200, in the same shape as "there was no stock" (measured, ADR 0077 P0) — so
			// walking the list would ask the same impossible question once per row, every time
			// demand appears. One failed start, and the cooldown holds it off.
			e.offers.settle(verdict.result)
			return fmt.Errorf("%w: %s", errEngineFleetRefused, c.ID)
		}
		next, ok := e.nextOffer(ctx, verdict.result)
		if !ok {
			return errEngineOffersSpent
		}
		c, after = next, verdict.result
	}
}

// nextOffer closes the current offer with `result` and answers with the next candidate worth
// trying. false means the list has been gone round once — ONE failed start, which is what the
// controller's cooldown counts.
func (e *engineRuntimeState) nextOffer(ctx context.Context, result string) (engineClass, bool) {
	e.offers.settle(result)
	next, ok := e.offers.advance()
	if !ok {
		log.Printf("engines: %s: every offer was tried (%s); giving up on this start",
			e.def.Key, e.offerTrailLine())
		e.noteNoBox(ctx)
		return engineClass{}, false
	}
	log.Printf("engines: %s: trying the offer %s (%s) after %s", e.def.Key, next.ID, next.buy(), result)
	return next, true
}

// startEngine is the start, for a caller that is not the controller — the admin toggle, which
// starts the box itself because somebody is watching. It routes through exactly the same place
// the controller's does: a second start path that moved the desired count on its own would be a
// way round decision 2, and it would ask for a task on a cluster with no box in it.
func (e *engineRuntimeState) startEngine(ctx context.Context) error {
	if len(e.offerList()) > 0 && e.fleet != nil {
		return e.startOnOffer(ctx)
	}
	return e.ecs.setEnabled(ctx, true)
}

// --- decision 4: the box was taken away -------------------------------------------------

// stepRebuild is decision 4, asked on every tick while the service still wants a task.
//
// 🔴 An interruption is read as an EC2 FACT, and that is the whole of what ADR 0077 could change
// about ADR 0075 decision 6's design. Under Managed Instances a box could not be enumerated at
// all, so "was it taken away" had to be guessed from the service's own states; a box the CP
// bought answers `describe-instances` by tag, so the two conditions the ADR names can both be
// read: the service went `running` → `starting` with the desired count still 1, and NO box
// tagged for this role is `pending` or `running` any more.
//
// ⚠️ THE SECOND CONDITION IS WHAT KEEPS THIS FROM FIRING ON AN ORDINARY REPLACEMENT. A task that
// was OOM-killed or failed its health check (measured in ADR 0071 P0) produces exactly the same
// service transition with the box still there — and rebuilding then would buy a second GPU for a
// task ECS is already placing on the first one.
//
// It returns the walk's error, which the controller turns into a failure or not: an interruption
// is NOT a failure (rule 3: a Spot box dying suddenly is acceptable), a rebuild that cannot get a
// box by the registration ceiling is.
func (e *engineRuntimeState) stepRebuild(ctx context.Context, view engineServiceView, replaced bool) error {
	if e == nil || e.fleet == nil {
		return nil
	}
	e.startMu.Lock()
	defer e.startMu.Unlock()
	// 🔴 Abandoned BEFORE walked, because the two conditions overlap and this one wins: the
	// engine was switched off while its replacement box was still booting. A rebuild exists to
	// put a PENDING task back on hardware; with the task withdrawn there is nothing to put
	// anywhere, and the box is ours and billing. Nothing would drive this walk again either —
	// the rebuild's premise is a task the service wants — so the box goes here rather than
	// waiting for the sweep's ceiling backstop.
	if e.offers.rebuildAbandoned(view.desired) {
		if id := e.offers.boxID(); id != "" {
			e.endBox(ctx, id, "the engine stopped while its replacement box was still registering")
		}
		e.offers.dropBox()
		return nil
	}
	if e.offers.rebuilding() {
		// A rebuild is already under way: keep walking it. This is the tick that notices the
		// registration ceiling and moves to the next offer — nothing else drives the walk once
		// the desired count is 1, because the controller's own decision is "do nothing" then.
		return e.walkOffers(ctx, false)
	}
	if view.desired < 1 || !replaced || e.offers.startInFlight() {
		return nil
	}
	if b, ok := e.fleet.current(ctx); ok {
		log.Printf("engines: %s: the task was replaced but the box %s is still here; not a rebuild",
			e.def.Key, b.instanceID)
		return nil
	}
	cur, had := e.offers.current()
	if twice := e.offers.noteInterrupted(cur.ID); twice {
		log.Printf("engines: %s: the offer %s has been interrupted twice in a row; skipping it for this demand",
			e.def.Key, cur.ID)
	}
	if had {
		e.offers.settle(engineOfferInterrupted)
	}
	e.noteInterrupted(ctx, cur)
	// From the TOP of the list, as decision 4 says: the cheapest offer that fits is the one the
	// operator asked for, and an interruption says nothing about the offers above the one that
	// was reclaimed. The candidates are re-derived because the catalogue may have moved since
	// this demand began; the skip lists are what the restart keeps.
	next, ok := e.offers.restart(e.candidateOffers(ctx))
	if !ok {
		log.Printf("engines: %s: the box was taken away and every offer has been skipped (%s)",
			e.def.Key, e.offerTrailLine())
		return errEngineOffersSpent
	}
	log.Printf("engines: %s: the box was taken away; rebuilding from %s (%s)", e.def.Key, next.ID, next.buy())
	return e.walkOffers(ctx, false)
}

// noteInterrupted is the one audit line per interruption (decision 4). It is not a charge anybody
// decided to make, and it is the only place the operator can see that a box they are paying for
// was taken away and replaced.
func (e *engineRuntimeState) noteInterrupted(ctx context.Context, c engineClass) {
	if e == nil || e.audit == nil {
		return
	}
	target := c.ID
	if target == "" {
		// A CP replaced mid-demand remembers no offer. The interruption is still worth a line:
		// the box is gone either way, and the tags on the next one say what replaced it.
		target = "none"
	}
	_ = e.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "engine-" + e.def.Key + "-controller",
		Action: "engine." + e.def.Key + ".interrupted", Target: target,
		Detail: "the box was taken away while the service still wanted a task", At: store.NowTS(),
	})
}

// --- decision 5: the box leaves when the service does -----------------------------------

// engineBoxDepartGrace is how old a box has to be before a sweep at desired 0 may end it. It
// guards the seconds between `CreateFleet` returning and this process recording the purchase —
// and, on a deployment running two Control Planes, the box the OTHER one has just bought.
const engineBoxDepartGrace = 2 * time.Minute

// engineBoxStrayAfter is the same guard for a box found while the engine is up: one role runs one
// box, so a second one with no task on it is a leak (ADR 0075 bought two, three times out of
// three), but it is only called a leak after long enough that it cannot be a start in progress.
const engineBoxStrayAfter = 15 * time.Minute

// sweepBoxes is decision 5, in both directions, on every controller tick.
//
// 🔴 Neither direction consults the CP's memory, and that is ADR 0045 decision 29 applied to a
// GPU: "when the thing is in AWS and its name is in the DB, a cleanup that can only be reached
// from the DB turns 'the row is gone' into a permanent leak". A CP that restarted mid-start still
// finds the box, because the box carries the tags.
//
//	(a) an instance tagged for this role, with no task on its container instance, while the
//	    service wants nothing — deregistered and terminated. That IS the ordinary departure: the
//	    idle window closes, the controller writes desired 0, the task goes, and the next tick
//	    ends the box. It is also the repair for a terminate this process forgot;
//	(b) a container instance carrying this role's attribute whose EC2 instance is gone —
//	    deregistered. The slot pool's own ghost sweep no longer sees these (decision 3), and a
//	    ghost that looks ACTIVE still satisfies placement constraints, so ECS aims the next task
//	    at a box that is not there (ADR 0045 decision 3-2).
func (e *engineRuntimeState) sweepBoxes(ctx context.Context, view engineServiceView) {
	if e == nil || e.fleet == nil {
		return
	}
	// A box bought seconds ago has no task on it BY CONSTRUCTION — the desired count is what
	// comes after it registers — so the sweep stands down for a walk in flight. But only INSIDE
	// THE REGISTRATION CEILING, with the walk's own slack on top: past that the walk itself would
	// have ended the box, and a box that is neither registered nor claimed by a walk anybody is
	// driving is exactly a stray. That is what covers the walk nobody drives any more — a start
	// or a rebuild whose engine was switched off while its box was still booting, which has no
	// path back into walkOffers at all.
	if waited, inFlight := e.offers.waitedForBox(); inFlight && waited < e.offers.budget()+engineBoxDepartGrace {
		return
	}
	registered := map[string]engineBox{}
	busy := 0
	for _, b := range e.ecs.boxes(ctx) {
		registered[b.instanceID] = b
		if b.tasks > 0 {
			busy++
		}
	}
	live := map[string]bool{}
	for _, fb := range e.fleet.boxes(ctx) {
		if !fb.alive() {
			continue
		}
		live[fb.instanceID] = true
		ci, inCluster := registered[fb.instanceID]
		if inCluster && ci.tasks > 0 {
			continue
		}
		grace, why := engineBoxDepartGrace, "the engine is stopped and nothing is running on it"
		if view.desired >= 1 {
			// The engine wants a box. A second one with nothing on it is the two-box failure —
			// but only when ANOTHER box is actually carrying the task, and only after long
			// enough that it cannot be one still being placed.
			//
			// 🔴 Both halves are needed. Without the "another box is busy" half, an ordinary task
			// REPLACEMENT — the OOM kill ADR 0071 P0 measured, a failed health check — shows up
			// here as the engine's only box with zero tasks on it, and terminating that turns a
			// restart ECS was already handling into a box purchase (decision 4's rebuild would
			// then buy one, so the cost is real).
			if busy == 0 {
				continue
			}
			grace, why = engineBoxStrayAfter, "it is a second box with no task on it"
		}
		if !fb.launchedAt.IsZero() && time.Since(fb.launchedAt) < grace {
			continue
		}
		e.endBox(ctx, fb.instanceID, why)
	}
	for id, ci := range registered {
		if live[id] {
			continue
		}
		log.Printf("engines: %s: deregistering the ghost container instance %s (ec2 %s is gone)",
			e.def.Key, ci.arn, id)
		if err := e.ecs.deregisterBox(ctx, ci.arn); err != nil {
			log.Printf("engines: %s: deregistering %s failed: %v", e.def.Key, id, err)
			continue
		}
		e.noteBoxAction(ctx, id, "deregistered a ghost container instance")
	}
}

// endBox takes one box out of the cluster and then out of EC2, in that order (decision 5, the
// slot pool's `terminateSlot`).
//
// ⚠️ The ECS half is not optional and not automatic. A terminated instance stays registered as
// ACTIVE with agentConnected false, and a ghost that looks ACTIVE still satisfies placement
// constraints (ADR 0045 decision 3-2). Deregistering first also means a failed terminate degrades
// into direction (b) of the sweep rather than into a live box ECS will not place on.
func (e *engineRuntimeState) endBox(ctx context.Context, instanceID, why string) {
	if b, ok := e.ecs.boxFor(ctx, instanceID); ok {
		if err := e.ecs.deregisterBox(ctx, b.arn); err != nil {
			log.Printf("engines: %s: deregistering %s before terminating it failed: %v", e.def.Key, instanceID, err)
		}
	}
	if err := e.fleet.terminate(ctx, instanceID); err != nil {
		log.Printf("engines: %s: terminating the box %s (%s) failed: %v", e.def.Key, instanceID, why, err)
		return
	}
	log.Printf("engines: %s: terminated the box %s (%s)", e.def.Key, instanceID, why)
	e.noteBoxAction(ctx, instanceID, "terminated: "+why)
}

// --- the ledger -------------------------------------------------------------------------

// noteOffer writes the one audit line per purchase (decision 8, inherited from ADR 0075 decision
// 5). "Why is this engine running on the expensive box" is a question somebody asks a day later,
// with only the ledger to answer it.
func (e *engineRuntimeState) noteOffer(ctx context.Context, c engineClass, instanceID, after string) {
	if e == nil || e.audit == nil {
		return
	}
	detail := fmt.Sprintf("buy=%s instance=%s", c.buy(), instanceID)
	if after != "" && after != engineOfferActive {
		detail += " after=" + after
	}
	_ = e.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "engine-" + e.def.Key + "-controller",
		Action: "engine." + e.def.Key + ".offer", Target: c.ID, Detail: detail, At: store.NowTS(),
	})
}

// noteBoxAction records what the CP did to a box it owns. The sweep is the one thing here that
// spends — or stops spending — money without anybody asking, so it is audited rather than logged
// (ADR 0077 decision 5).
func (e *engineRuntimeState) noteBoxAction(ctx context.Context, instanceID, what string) {
	if e == nil || e.audit == nil {
		return
	}
	_ = e.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "engine-" + e.def.Key + "-controller",
		Action: "engine." + e.def.Key + ".box", Target: instanceID, Detail: what, At: store.NowTS(),
	})
}

// noteNoBox records a walk that ended with nothing bought, with the trail that says what each
// offer answered.
func (e *engineRuntimeState) noteNoBox(ctx context.Context) {
	if e == nil || e.audit == nil {
		return
	}
	_ = e.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "engine-" + e.def.Key + "-controller",
		Action: "engine." + e.def.Key + ".offer", Target: "none",
		Detail: "no offer produced a box: " + e.offerTrailLine(), At: store.NowTS(),
	})
}

// noteNoOffer records decision 2's refusal — once, not once per tick. It is a contradiction
// between two things the operator declared (the models and the offers), so it belongs in the
// ledger next to the charges that did not happen because of it.
func (e *engineRuntimeState) noteNoOffer(ctx context.Context) {
	if !e.offers.noteNoOfferOnce() {
		return
	}
	need, source, id := engineVramDemand(e.catalog.list(ctx))
	log.Printf("engines: %s: not starting — no declared offer holds %d MiB (%s, %s); the largest is %s",
		e.def.Key, need, source, id, engineLargestOfferLabel(e.offerList()))
	if e.audit == nil {
		return
	}
	_ = e.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "engine-" + e.def.Key + "-controller",
		Action: "engine." + e.def.Key + ".offer", Target: "none",
		Detail: fmt.Sprintf("no offer holds %d MiB (%s, %s)", need, source, id), At: store.NowTS(),
	})
}

// engineLargestOfferLabel names the biggest offer there is, for the line that says none of them
// is big enough: the number the operator has to change is that one.
func engineLargestOfferLabel(list []engineClass) string {
	best, ok := engineClass{}, false
	for _, c := range list {
		if !ok || c.VramMiB > best.VramMiB {
			best, ok = c, true
		}
	}
	if !ok {
		return "(none)"
	}
	return fmt.Sprintf("%s (%d MiB)", best.ID, best.VramMiB)
}

// offerTrailLine is the walk in one line, for the log that closes it.
func (e *engineRuntimeState) offerTrailLine() string {
	parts := make([]string, 0, 4)
	for _, a := range e.offers.attempts() {
		parts = append(parts, a.ID+"="+a.Result)
	}
	if len(parts) == 0 {
		return "(nothing tried)"
	}
	return strings.Join(parts, " ")
}

// offerOnBox is decision 8's replacement for ADR 0075 decision 11: which offer this engine is
// running on, read off THE BOX'S OWN TAGS rather than off the service.
//
// The service can no longer answer it — it declares a launch type and has no strategy to read
// (decision 2) — and the box is the better source anyway: `af-engine-offer` and `af-engine-buy`
// were written by the call that bought it, so they name the row that was actually paid for even
// after the table has been edited underneath.
func (e *engineRuntimeState) offerOnBox(ctx context.Context) (string, string, bool) {
	if e == nil || e.fleet == nil {
		return "", "", false
	}
	b, ok := e.fleet.current(ctx)
	if !ok || b.offerID == "" {
		return "", "", false
	}
	buy := b.buy
	if buy != engineBuySpot {
		buy = engineBuyOnDemand
	}
	return b.offerID, buy, true
}
