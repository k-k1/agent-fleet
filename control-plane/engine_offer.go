package main

// engine_offer.go — which box this role buys, out of a list of OFFERS (ADR 0075 P0).
//
// ADR 0074 gave the role a ladder of rungs and let an administrator pick one. ADR 0075 keeps the
// ladder and changes what the CP does with it: the rungs become offers, each with a purchase
// option, and a start walks them from the top until a box actually arrives. Three rules the
// operator asked for, and everything in this file is one of them:
//
//  1. buy what fits the VRAM, cheapest first, without caring whether it is Spot or on-demand —
//     "cheapest first" is the DECLARATION ORDER, because the CP knows no prices (decision 1);
//  2. if Spot cannot be had, take on-demand. ECS will not do this by itself: a
//     capacityProviderStrategy is a weighting, not an ordered fallback, and a request it cannot
//     place simply stays unplaced with a reason in the service events (measured: 17 minutes, four
//     attempts, `UnfulfillableCapacity` every time);
//  3. a Spot box may be taken away; rebuild from the top of the list (P1, not here).
//
// 🔴 The two invariants that keep this from being expensive:
//
//   - nothing in this file runs for a deployment that declares no offer. The hooks are attached
//     in newEngineRegistry only when the list is non-empty, so a deployment that never configures
//     this makes not one extra ECS call (ADR 0074 decision 3, inherited);
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

// The failure codes ECS writes into the service events. All four have been seen on this
// deployment (ADR 0074 P1, ADR 0071, ADR 0075 live run 4), which is the only reason it is
// defensible to branch on strings at all — and why the default below is "wait", never "give up".
//
// 🔴 They are matched as SUBSTRINGS because ECS wraps them. The Spot quota arrives inside another
// error entirely (measured): "…was unable to place a task. Reason: ResourceInitializationError:
// Unable to launch instance(s) for capacity provider af-…-image-spot.
// MaxSpotInstanceCountExceeded: Max spot instance count exceeded." Anchoring on the start of the
// message would have read that as "no known code" and waited out the whole budget, which is what
// the deployment did.
const (
	engineEventUnfulfillable = "UnfulfillableCapacity"
	engineEventInsufficient  = "InsufficientInstanceCapacity"
	engineEventVcpuLimit     = "VcpuLimitExceeded"
	// engineEventSpotQuota is the Spot side of the same wall as engineEventVcpuLimit. Managed
	// Instances reports it with this word and not with `VcpuLimitExceeded` — on this deployment
	// the latter never fired for Spot at all — so a table holding only the vCPU one leaves the
	// quota case as the one branch of decision 5 that does not work.
	engineEventSpotQuota = "MaxSpotInstanceCountExceeded"
)

// What one offer came to, as the panel reads it (contract B). `active` is the one that is not an
// outcome yet: it is the offer the service's strategy is pointing at right now.
const (
	engineOfferActive        = "active"
	engineOfferUnfulfillable = "unfulfillable"
	engineOfferInsufficient  = "insufficient"
	engineOfferQuota         = "quota"
	engineOfferBudget        = "budget"
	// engineOfferUnusable is the offer that could not even be ASKED for: writing its rung to the
	// capacity provider was refused, so the provider still holds the previous offer's
	// requirements. Measured (ADR 0075 live run, the positive control that could not be built):
	// `UpdateCapacityProvider` answers 400 "No instance types satisfy the instance requirements",
	// and the CP used to log that and start anyway — buying a box against a declaration nobody
	// had managed to apply.
	engineOfferUnusable = "unusable"
)

// engineOfferAttempt is one row of the trail: which offer was tried, how it was bought, and what
// came of it.
type engineOfferAttempt struct {
	ID     string
	Buy    string
	Result string
}

// engineOfferRun is rule 2's state for ONE demand: the candidate list this start was judged on,
// where in it we are, when we got there, and what every earlier offer answered.
//
// In memory and nowhere else. A CP replaced mid-start adopts nothing — it reports no trail and
// does not move the strategy — because the alternative is inferring somebody else's attempt from
// a service it did not write, and the cost of doing nothing is bounded by the start deadline
// that was already there.
type engineOfferRun struct {
	mu    sync.Mutex
	list  []engineClass
	idx   int
	trail []engineOfferAttempt
	// since is when the strategy was written for the current offer, i.e. when its budget started.
	since time.Time
	// skipBuy holds the purchase options a quota error has taken off the table for this demand
	// (decision 5). The quotas are SEPARATE (`L-DB2E81BA` on-demand, `L-3819A6DF` Spot), so
	// "on-demand is full" says nothing about Spot and vice versa — which is exactly why the
	// answer to a quota error is to change purchase option rather than to wait.
	skipBuy map[string]bool
	// noted is whether the "no code matched" line has been written for the current offer. Once
	// per offer, because the whole value of that line is that somebody reads it after AWS has
	// changed a message and this table has silently stopped matching.
	noted bool
	// noOffer is whether the refusal of decision 2 has already been recorded. Without it, an
	// engine whose models outgrew every offer would audit a line every tick.
	noOffer bool
	// arrived is whether a box has appeared on the current offer's capacity provider. 🔴 It ENDS
	// the budget: what the budget buys is "was this offer able to produce a box", and everything
	// after the box is the cold start, which `StartDeadlineSec` governs (decision 5, and the
	// reason it is not a second deadline). Measured, ADR 0075 live run 3: the box arrived in 26
	// seconds and the engine still moved on at 180, because the clock was reading the TASK.
	arrived bool
	// settling is true between the two halves of a start: the strategy is written and the desired
	// count is waiting for the deployment it replaced to go (measured 2 m 35 s). Every start path
	// reads it — the controller, the admin toggle's follow-up tick and the gateway's wait loop —
	// so that the offer is chosen and its rung written ONCE per start rather than once per poll.
	settling bool
	// budgetSec is how long one offer is given, live. Carried here rather than read off the row
	// this process started with, because the engine table is re-read while the CP runs and an
	// operator raising the budget must not need a Control Plane replacement to be heard (measured
	// gap: "changed in the table in a way this process cannot take live (offer budget)").
	budgetSec int
	now       func() time.Time // test seam
}

func newEngineOfferRun(budget time.Duration) *engineOfferRun {
	r := &engineOfferRun{now: time.Now}
	r.setBudget(budget)
	return r
}

// budget is the per-offer budget, live.
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
// start — the offer being waited on simply gets the new figure.
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
// place that knows the start is actually about to happen.
func (r *engineOfferRun) begin(list []engineClass) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]engineClass(nil), list...)
	r.idx, r.trail, r.noted, r.noOffer, r.arrived = 0, nil, false, false, false
	r.skipBuy = map[string]bool{}
	r.since = time.Time{}
}

// current is the offer the service's strategy was last written to by this process.
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

// took records that the strategy now points at the current offer, and starts its budget. It
// reports whether that was new.
//
// 🔴 Taking the SAME offer twice is not a second attempt. Two start paths can fire within a second
// of each other — the admin toggle starts the box itself and the controller's next tick agrees —
// and the deployment showed what that costs: `offer_trail` said `[spot3 active, spot3 active]`,
// which is a lie about the walk, and the budget clock was drawn again from the second one, giving
// the offer a longer run than it had.
func (r *engineOfferRun) took(c engineClass) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.trail); n > 0 && r.trail[n-1].ID == c.ID && r.trail[n-1].Result == engineOfferActive {
		// The clock is restored rather than left alone: the other start path called begin() a
		// moment ago, which cleared it, and an offer whose budget never starts is an offer rule 2
		// waits on for ever.
		if r.since.IsZero() {
			r.since = r.clock()
		}
		return false
	}
	r.since = r.clock()
	r.noted, r.arrived = false, false
	r.trail = append(r.trail, engineOfferAttempt{ID: c.ID, Buy: c.buy(), Result: engineOfferActive})
	return true
}

// noteArrived records that a box has appeared on this offer's provider, and reports whether that
// is news. From here the budget is over and the start deadline owns the clock.
func (r *engineOfferRun) noteArrived() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.arrived {
		return false
	}
	r.arrived = true
	return true
}

// isSettling reports whether a start is between its two halves.
func (r *engineOfferRun) isSettling() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settling
}

func (r *engineOfferRun) noteSettling(v bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.settling = v
	r.mu.Unlock()
}

// takenAt is when the strategy was written for the current offer. It is the line an event has to
// be on the far side of to be evidence about this attempt (see engineEventIsAbout).
func (r *engineOfferRun) takenAt() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.since
}

// boxArrived reports whether this offer has already produced a box.
func (r *engineOfferRun) boxArrived() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.arrived
}

// waited is how long the current offer has had, and whether it has had anything at all (a zero
// `since` is a CP that did not write this strategy).
//
// 🔴 The clock runs from the NEW PRIMARY DEPLOYMENT, not from the moment the strategy was
// written. Every accepted strategy write forces a deployment (see setStrategy), and a deployment
// takes about 89 seconds to complete even with no task to replace (measured, ADR 0075 live test
// 0). Measuring from our own write would spend half of a 180-second budget on ECS's own
// bookkeeping and move to the next offer before this one had been asked for capacity.
//
// The LATER of the two is used: `deploymentAt` before our write belongs to the previous offer,
// and trusting it would shorten this offer's budget by however long the last one ran.
func (r *engineOfferRun) waited(deploymentAt time.Time) (time.Duration, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.since.IsZero() {
		return 0, false
	}
	from := r.since
	if deploymentAt.After(from) {
		from = deploymentAt
	}
	return r.clock().Sub(from), true
}

// settle closes the current offer with its outcome.
func (r *engineOfferRun) settle(result string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.trail); n > 0 {
		r.trail[n-1].Result = result
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
		if r.skipBuy[r.list[i].buy()] {
			continue
		}
		r.idx = i
		return r.list[i], true
	}
	r.idx = len(r.list)
	return engineClass{}, false
}

// useIndex puts the walk on one offer of the candidate list, for a start that had to skip past
// offers whose rung the capacity provider refused.
func (r *engineOfferRun) useIndex(i int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.idx = i
	r.mu.Unlock()
}

// noteUnusable writes an offer into the trail that was never asked for, because its rung could not
// be written to the capacity provider. It is in the trail rather than only in the log because the
// panel's question is "why is it on this box", and "the one above could not be applied" is the
// answer.
func (r *engineOfferRun) noteUnusable(c engineClass) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.trail = append(r.trail, engineOfferAttempt{ID: c.ID, Buy: c.buy(), Result: engineOfferUnusable})
	r.mu.Unlock()
}

// noteOnce reports whether the "none of the known codes matched" line still has to be written for
// the current offer.
func (r *engineOfferRun) noteOnce() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noted {
		return false
	}
	r.noted = true
	return true
}

// noteNoOfferOnce is the same guard for decision 2's refusal.
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

// engineOfferVerdict is decision 5's table, as a pure function over the service events: what the
// current offer is to be recorded as, and whether to move on now.
//
// 🔴 ONLY THIS OFFER'S EVENTS COUNT, and that is not a refinement — it is what keeps one
// misreading from throwing the whole list away. The event list is the newest three of the SERVICE,
// so right after a move it still holds the previous offer's failure; measured (ADR 0075 re-run 4),
// `l4` was judged five seconds after the move on a Spot quota event from twenty seconds before it,
// and because a quota takes a whole purchase option off the table the engine gave up on a list
// that had a working offer left in it. An event belongs to this offer when it NAMES this offer's
// capacity provider (ECS writes "…for capacity provider af-<stack>-image-spot" into every one of
// these, measured for all three codes) and is NEWER than the moment the offer was taken.
//
// ⚠️ The DEFAULT is to wait out the budget and to say that nothing matched — and an event that
// names no provider at all (an ordinary placement failure) falls into it. AWS may reword an event
// at any time, and a table that fell through to "give up" would turn a reworded message into an
// engine that walks its whole offer list in seconds and cools down for four hours.
//
// The events are newest first (ECS's own order), so the FIRST recognised code wins: a service
// that hit the quota and then, once a box left, hit plain capacity shortage is on the second
// problem now.
func engineOfferVerdict(events []engineServiceEvent, provider string, since time.Time, waited, budget time.Duration) (result string, move, matched bool) {
	for _, ev := range events {
		if !engineEventIsAbout(ev, provider, since) {
			continue
		}
		e := ev.message
		switch {
		case strings.Contains(e, engineEventUnfulfillable):
			// "Your request's configuration cannot be fulfilled" — no AZ hint, no "try later".
			// Measured: four attempts over 17 minutes, the same answer every time. Waiting out a
			// budget here buys nothing at all.
			return engineOfferUnfulfillable, true, true
		case strings.Contains(e, engineEventVcpuLimit), strings.Contains(e, engineEventSpotQuota):
			// A quota, and the quotas are per purchase option. Moving to another offer of the
			// SAME option would hit the same wall, so settle() takes that option off the table.
			//
			// TWO words for one wall: on-demand says `VcpuLimitExceeded`, Spot says
			// `MaxSpotInstanceCountExceeded` wrapped in a `ResourceInitializationError` (measured
			// — the vCPU word never appeared for Spot on this deployment at all).
			return engineOfferQuota, true, true
		case strings.Contains(e, engineEventInsufficient):
			// The one code that is a function of the clock: that type, that AZ, right now. It is
			// worth the rest of the budget and not a second more.
			return engineOfferInsufficient, waited >= budget, true
		}
	}
	return engineOfferBudget, waited >= budget, false
}

// engineEventIsAbout reports whether one service event is evidence about THIS offer: it names the
// capacity provider the offer buys from, and ECS wrote it after the offer was taken.
//
// Both halves are needed. The name alone would let the previous attempt on the SAME provider —
// two offers can share one — answer for this one; the time alone would let a stray event from the
// other provider through in the seconds after a move, which is the failure this exists for. An
// event with no timestamp is not evidence either: it cannot be placed on either side of the move.
func engineEventIsAbout(ev engineServiceEvent, provider string, since time.Time) bool {
	if provider == "" || since.IsZero() || ev.at.IsZero() {
		return false
	}
	if !strings.Contains(ev.message, provider) {
		return false
	}
	return ev.at.After(since)
}

// --- the engine's side ---------------------------------------------------------------

// wireOffers attaches everything that can reach ECS about a box, and attaches it ONLY when this
// role declares at least one offer.
//
// 🔴 This is ADR 0074 decision 3, inherited: a deployment that configures none of this has no
// path from its start to DescribeCapacityProviders or to a strategy write, so it cannot log an
// AccessDenied for a feature it does not use and cannot pay for a call it did not ask for. It is
// one function because the four wirings are one claim — a test can hold this whole rule by
// calling it with an empty list and counting zero.
//
// The three hooks also stand or fall together. startGate CHOOSES the offer, startWith writes it
// to the service together with the desired count (decision 4 (a)), and offerStep is rule 2. A
// gate with no start path would pick an offer nobody addresses; a start path with no step would
// sit on the first offer until the start deadline, which is the failure this ADR removes.
func (e *engineRuntimeState) wireOffers(capacity engineCapacityAPI) {
	if e == nil || len(e.classList()) == 0 {
		return
	}
	e.capacity = capacity
	if e.ctrl == nil {
		return
	}
	e.ctrl.startGate = e.startGate
	e.ctrl.startWith = e.startOnOffer
	e.ctrl.offerStep = e.stepOffers
	e.ctrl.startSettling = e.offers.isSettling
}

// setOfferBudget takes a new per-offer budget from the table, reporting whether it changed.
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
// and 8).
//
//   - an administrator's stored choice is a PIN: that offer and nothing else, and no falling
//     through to the next one. Somebody who said "try it on the 48 GB card" must not be quietly
//     put on a 24 GB one — ADR 0074 decision 4 called that the most expensive kind of lie;
//   - no stored choice is AUTOMATIC: every offer whose declared VRAM covers the largest enabled
//     model, in declaration order. An `unknown` demand filters nothing (decision 2: refusing to
//     start because nobody measured a model would take the engine away from a deployment that
//     merely has not been measured);
//   - an offer whose capacity provider this deployment does not declare is dropped. It cannot be
//     addressed, so offering to try it would be a wait with no request behind it.
func (e *engineRuntimeState) candidateOffers(ctx context.Context) []engineClass {
	list := e.offerList()
	if len(list) == 0 {
		return nil
	}
	if id := e.selectedClassID(ctx); id != "" {
		if c, ok := engineClassByID(list, id); ok && id == c.ID {
			if e.providerForOffer(c) == "" {
				log.Printf("engines: %s: the pinned offer %s buys %s and this deployment declares no %s capacity provider",
					e.def.Key, c.ID, c.buy(), c.buy())
				return nil
			}
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
		if e.providerForOffer(c) == "" {
			log.Printf("engines: %s: skipping the offer %s: no %s capacity provider is declared",
				e.def.Key, c.ID, c.buy())
			continue
		}
		out = append(out, c)
	}
	return out
}

// applyFirstUsableOffer writes rungs down the candidate list until one lands, and answers with the
// offer the start is to be made on.
//
// 🔴 An offer whose rung the capacity provider REFUSES is not an offer this engine can be started
// on. Measured (ADR 0075, building the live positive control): `UpdateCapacityProvider` answers
// 400 `ClientException: No instance types satisfy the instance requirements specified in the
// Managed Instances capacity provider` for a misspelt type or requirements nothing matches — and
// the CP used to log that and start anyway, so the provider still held the PREVIOUS offer's
// requirements and bought a box against a declaration that had never been applied. Skipping to the
// next offer is the only reading under which the panel and the bill agree.
//
// The ADR 0074 exception stays: a failure on the rung THIS PROCESS already applied is a transient
// API error in front of a provider that already says the right thing, and taking the engine away
// over it would be an outage caused by a check.
func (e *engineRuntimeState) applyFirstUsableOffer(ctx context.Context, cands []engineClass) (engineClass, bool) {
	for i, c := range cands {
		err := e.applyClass(ctx, c)
		if err == nil || e.lastAppliedClass() == c.ID {
			if err != nil {
				log.Printf("engines: %s: re-applying the instance class %s failed (already applied by this process): %v",
					e.def.Key, c.ID, err)
			}
			e.offers.useIndex(i)
			return c, true
		}
		log.Printf("engines: %s: the offer %s could not be applied to its capacity provider, skipping it: %v",
			e.def.Key, c.ID, err)
		e.offers.noteUnusable(c)
	}
	log.Printf("engines: %s: not starting — no offer's instance class could be applied", e.def.Key)
	return engineClass{}, false
}

// startOnOffer is the start itself (decision 4 (a)): the chosen offer's capacity provider, then
// the desired count — two calls inside setStrategy, because one call buys two boxes.
//
// The offer was chosen by startGate a moment ago, in the same tick, and is read back from the run
// rather than re-derived: re-deriving it here would let a catalogue edit between the two calls
// start the engine on an offer the gate never applied a rung to.
func (e *engineRuntimeState) startOnOffer(ctx context.Context) error {
	c, ok := e.offers.current()
	if !ok {
		// No walk in progress. The only way here is a start that did not come through the gate,
		// and starting with the strategy the service already has is the same behaviour as every
		// deployment without offers.
		return e.ecs.setEnabled(ctx, true)
	}
	provider := e.providerForOffer(c)
	if err := e.ecs.setStrategy(ctx, provider, true); err != nil {
		// The strategy landed and the desired count is waiting for the old deployment: remember
		// that, so the next attempt is the one short call rather than the whole gate again.
		e.offers.noteSettling(errors.Is(err, errEngineStrategySettling))
		return err
	}
	e.offers.noteSettling(false)
	// Only the FIRST start of this demand writes a trail row and an audit line. The admin toggle
	// and the controller's next tick both come through here, one second apart (measured), and the
	// second one is the same offer being started again — not a second attempt at it.
	if e.offers.took(c) {
		e.noteOffer(ctx, c, provider, engineOfferActive)
	}
	return nil
}

// startEngine is the start, for a caller that is not the controller — the admin toggle, which
// starts the box itself because somebody is watching. It routes through exactly the same place
// the controller's does: a second start path that moved the desired count without the strategy
// would be a way round decision 4 (a), and the box it bought would be whatever provider the
// service was last pointed at.
func (e *engineRuntimeState) startEngine(ctx context.Context) error {
	if len(e.offerList()) > 0 {
		return e.startOnOffer(ctx)
	}
	return e.ecs.setEnabled(ctx, true)
}

// stepOffers is rule 2 (decision 5), asked on every tick while a start is in flight. It reports
// whether the whole list has now been tried — the controller turns that into ONE failed start,
// which is what the existing cooldown counts.
func (e *engineRuntimeState) stepOffers(ctx context.Context, view engineServiceView) bool {
	cur, ok := e.offers.current()
	if !ok {
		return false
	}
	// 🔴 THE BUDGET ENDS WHEN THE BOX ARRIVES, not when the task is running. What one offer is
	// given time for is "can this provider produce a box"; everything after that is the cold start
	// (measured 165-197 s for image, plus the model sync), and `StartDeadlineSec` is what governs
	// it. Judging on the task instead made the deployment walk its whole list with a perfectly
	// good Spot box already registered — 26 seconds to arrive, moved on at 180, two boxes bought
	// and nothing started (ADR 0075 live run 3).
	if e.offers.boxArrived() {
		return false
	}
	if e.offerBoxIsUp(ctx, cur) {
		if e.offers.noteArrived() {
			log.Printf("engines: %s: offer %s produced a box; the start deadline owns the clock from here",
				e.def.Key, cur.ID)
		}
		return false
	}
	waited, started := e.offers.waited(view.lastStart)
	if !started {
		return false
	}
	// The events are filtered to THIS offer: its provider's name, and after the moment it was
	// taken. `takenAt` is that moment — the offer's own clock, not the deployment's, because what
	// is being asked is "did ECS say this about the attempt we are waiting on".
	result, move, matched := engineOfferVerdict(view.events, e.providerForOffer(cur), e.offers.takenAt(), waited, e.offers.budget())
	if !matched && e.offers.noteOnce() {
		// The one line that makes a reworded AWS message findable. Without it, the table in ADR
		// 0075 stops matching and the only symptom is that every offer waits its full budget.
		log.Printf("engines: %s: offer %s has no event matching a known capacity failure code yet: %s",
			e.def.Key, cur.ID, strings.Join(view.eventMessages(), " | "))
	}
	if !move {
		return false
	}
	log.Printf("engines: %s: offer %s answered %s after %s", e.def.Key, cur.ID, result, waited.Round(time.Second))
	return e.moveToNextOffer(ctx, result)
}

// offerBoxIsUp reports whether a container instance is registered on this offer's own capacity
// provider. The read is the cached one every Managed Instances engine already makes (20 s), so a
// start that is going well costs three of them.
func (e *engineRuntimeState) offerBoxIsUp(ctx context.Context, c engineClass) bool {
	provider := e.providerForOffer(c)
	if provider == "" {
		return false
	}
	// Narrowed to THIS offer's provider: a start that changed provider can have two boxes at once
	// — the previous offer's leaving and this one's arriving — and the unnarrowed read answers
	// with whichever ECS listed first.
	b, ok := e.ecs.boxOn(ctx, provider)
	// ACTIVE only. A DRAINING box is the PREVIOUS offer's going away, and reading that as an
	// arrival would hand the start deadline a clock for a box that is leaving.
	return ok && b.status == "ACTIVE"
}

// moveToNextOffer closes the current offer with `result` and points the service at the next
// candidate. It reports whether the list has been gone round once — ONE failed start, which is
// what the controller's cooldown counts.
//
// It loops rather than moving once, because an offer can be unusable before it is ever asked for:
// `UpdateCapacityProvider` refuses requirements that no instance type satisfies (measured, 400
// "No instance types satisfy the instance requirements"), and pointing the service at a provider
// that still holds the PREVIOUS offer's requirements would buy a box nobody declared.
func (e *engineRuntimeState) moveToNextOffer(ctx context.Context, result string) bool {
	for {
		e.offers.settle(result)
		next, ok := e.offers.advance()
		if !ok {
			log.Printf("engines: %s: every offer was tried (%s); giving up on this start", e.def.Key, e.offerTrailLine())
			return true
		}
		// The rung goes to the NEXT offer's provider before the service is pointed at it: the
		// provider the box will come from has to hold the right instance requirements first,
		// exactly as it does for a start (ADR 0074 decision 5).
		if err := e.applyClass(ctx, next); err != nil && e.lastAppliedClass() != next.ID {
			log.Printf("engines: %s: the offer %s cannot be applied to its capacity provider, skipping it: %v",
				e.def.Key, next.ID, err)
			e.offers.took(next)
			result = engineOfferUnusable
			continue
		}
		provider := e.providerForOffer(next)
		if err := e.ecs.setStrategy(ctx, provider, false); err != nil {
			// Not a reason to keep walking: either the service came up between the two reads (the
			// running guard, which is the correct refusal) or ECS is refusing, and both are
			// answered by leaving the start where it is and letting the start deadline judge it.
			log.Printf("engines: %s: moving to the offer %s failed: %v", e.def.Key, next.ID, err)
			return false
		}
		e.offers.took(next)
		log.Printf("engines: %s: trying the offer %s (%s)", e.def.Key, next.ID, next.buy())
		e.noteOffer(ctx, next, provider, result)
		return false
	}
}

// noteOffer writes the one audit line per move (decision 5). "Why is this engine running on the
// expensive box" is a question somebody asks a day later, with only the ledger to answer it.
func (e *engineRuntimeState) noteOffer(ctx context.Context, c engineClass, provider, after string) {
	if e == nil || e.audit == nil {
		return
	}
	detail := fmt.Sprintf("buy=%s provider=%s", c.buy(), provider)
	if after != "" && after != engineOfferActive {
		detail += " after=" + after
	}
	_ = e.audit.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "engine-" + e.def.Key + "-controller",
		Action: "engine." + e.def.Key + ".offer", Target: c.ID, Detail: detail, At: store.NowTS(),
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

// offerFromStrategy is decision 11: which offer the SERVICE says it is on, read back from
// DescribeServices rather than from what this process remembers choosing.
//
// A provider name answers the purchase option exactly; it answers WHICH ROW only when one row of
// that option exists. With several, the offer this process is on wins if it agrees with the
// service, and otherwise the first declared row of that purchase option is named — the honest
// approximation, and the reason the purchase option is reported alongside rather than folded in.
func (e *engineRuntimeState) offerFromStrategy(provider string) (engineClass, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return engineClass{}, false
	}
	od, spot := e.ecs.providers()
	buy := ""
	switch {
	case spot != "" && provider == spot:
		buy = engineBuySpot
	case od != "" && provider == od:
		buy = engineBuyOnDemand
	default:
		// CloudFormation, or somebody, pointed the service at a provider this role does not
		// declare. Saying nothing is the only truthful answer.
		return engineClass{}, false
	}
	if cur, ok := e.offers.current(); ok && cur.buy() == buy {
		return cur, true
	}
	for _, c := range e.offerList() {
		if c.buy() == buy {
			return c, true
		}
	}
	return engineClass{}, false
}
