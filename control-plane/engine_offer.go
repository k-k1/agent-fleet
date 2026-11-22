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
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

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
	e.ctrl.startSettling = e.offers.StartInFlight
	e.ctrl.boxStep = e.sweepBoxes
	e.ctrl.rebuildStep = e.stepRebuild
}

// setOfferBudget takes a new registration ceiling from the table, reporting whether it changed.
func (e *engineRuntimeState) setOfferBudget(d time.Duration) bool {
	if e == nil {
		return false
	}
	return e.offers.SetBudget(d)
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
//
// 🔴 And a `spot` offer is a candidate only where an administrator has ACCEPTED INTERRUPTION
// (engineSpotSettingKey). The filter is here, in the one place that decides what may be bought,
// and not in the panel that draws the list: the pin is the only half a picker could gate, and
// every unpinned start — which is the normal one — walks this list instead. A gate on the select
// alone would leave an engine nobody pinned quietly buying interruptible boxes with the box
// unticked, which is the opposite of what ticking it means.
func (e *engineRuntimeState) candidateOffers(ctx context.Context) []engineClass {
	list := e.offerList()
	if len(list) == 0 || e.fleet == nil {
		return nil
	}
	spotOK := e.spotAllowed(ctx)
	if id := e.selectedClassID(ctx); id != "" {
		c, ok := engineClassByID(list, id)
		switch {
		case ok && id == c.ID && (spotOK || c.BuyOption() != engineBuySpot):
			return []engineClass{c}
		case ok && id == c.ID:
			// Pinned to an offer this role is not allowed to buy. Automatic, for the same reason
			// as the undeclared pin below: the pin cannot be honoured, so the honest behaviour is
			// the one the panel can explain — and it says so beside the picker, where the tick
			// box that would honour it is.
			log.Printf("engines: %s: the pinned offer %q is Spot and interruption is not accepted; choosing automatically", e.def.Key, id)
		default:
			// A pin nobody declares any more. Falling back to AUTOMATIC rather than to the first
			// offer: the stored id is the only evidence of intent and it no longer names anything,
			// so the honest reading is "no choice", which is the one the operator can see and change.
			log.Printf("engines: %s: the pinned offer %q is not in the list; choosing automatically", e.def.Key, id)
		}
	}
	need, _, _ := engineVramDemand(e.catalog.list(ctx))
	out := make([]engineClass, 0, len(list))
	for _, c := range list {
		if !engineClassFits(c, need) {
			continue
		}
		if !spotOK && c.BuyOption() == engineBuySpot {
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
	if _, ok := e.offers.Current(); !ok {
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
	c, ok := e.offers.Current()
	if !ok {
		return nil
	}
	after := ""
	for {
		if id := e.offers.BoxID(); id != "" {
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
				//
				// The box goes into the LEDGER as it leaves the walk: decision 4's detection has
				// to be able to say "the box we were running on is gone" without a state
				// transition to read (see stepRebuild).
				e.offers.Settled(id)
				return nil
			}
			waited, started := e.offers.WaitedForBox()
			if !started || waited < e.offers.Budget() {
				return errEngineBoxRegistering
			}
			// Past the ceiling. The box is ours and it is billing, so it goes before the next
			// offer is tried — this is the one place ADR 0075's "two boxes" could still happen.
			log.Printf("engines: %s: the box %s for offer %s did not register within %s; ending it",
				e.def.Key, id, c.ID, e.offers.Budget().Round(time.Second))
			e.endBox(ctx, id, "it never registered with the cluster")
			e.offers.DropBox()
			next, ok := e.nextOffer(ctx, engineOfferBudget)
			if !ok {
				return errEngineOffersSpent
			}
			c, after = next, engineOfferBudget
			continue
		}
		id, instanceType, verdict := e.fleet.buy(ctx, c)
		if id != "" {
			e.offers.Took(c, id)
			e.noteOffer(ctx, c, id, instanceType, after)
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
			e.offers.Settle(verdict.result)
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
	e.offers.Settle(result)
	next, ok := e.offers.Advance()
	if !ok {
		log.Printf("engines: %s: every offer was tried (%s); giving up on this start",
			e.def.Key, e.offerTrailLine())
		e.noteNoBox(ctx)
		return engineClass{}, false
	}
	log.Printf("engines: %s: trying the offer %s (%s) after %s", e.def.Key, next.ID, next.BuyOption(), result)
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
// bought answers `describe-instances` by tag.
//
// Two things can say a box was lost, and it is an OR:
//
//   - the service went `running` → `starting` with the desired count still 1 (the transition
//     ADR 0075 decision 6 named), AND no box of this role is alive;
//   - 🔴 or no box of this role is alive while the LEDGER holds one — whatever the service did.
//     The transition alone is not enough, and the P2 hardware run measured why: a box taken away
//     BEFORE its task ever reached RUNNING produces no transition at all (the service was
//     `starting` and stays `starting`), `startInFlight` is already down because the box had
//     registered, and nothing else has a clock. The engine then sat at desired 1 with no box and
//     no log line, and its only automatic way out was throwing the demand away after the
//     900-second idle window — against a measured cold start of 4 minutes 45 seconds, i.e. a
//     window that is anything but rare.
//
// ⚠️ "NO BOX IS ALIVE" IS WHAT KEEPS THIS FROM FIRING ON AN ORDINARY REPLACEMENT. A task that was
// OOM-killed or failed its health check (measured in ADR 0071 P0) produces the same transition
// with the box still there — and rebuilding then would buy a second GPU for a task ECS is already
// placing on the first one.
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
	if e.offers.RebuildAbandoned(view.desired) {
		if id := e.offers.BoxID(); id != "" {
			e.endBox(ctx, id, "the engine stopped while its replacement box was still registering")
		}
		e.offers.DropBox()
		e.offers.ForgetBox()
		return nil
	}
	if e.offers.Rebuilding() {
		// A rebuild is already under way: keep walking it. This is the tick that notices the
		// registration ceiling and moves to the next offer — nothing else drives the walk once
		// the desired count is 1, because the controller's own decision is "do nothing" then.
		return e.walkOffers(ctx, false)
	}
	if view.desired < 1 || e.offers.StartInFlight() {
		// Nothing is asked for, or a walk is already spending money on this demand. Note that
		// desired 0 with the box gone is not an interruption but a DEPARTURE — the sweep's
		// business (decision 5), and it has already run this tick.
		return nil
	}
	if b, ok := e.fleet.current(ctx); ok {
		if replaced {
			log.Printf("engines: %s: the task was replaced but the box %s is still here; not a rebuild",
				e.def.Key, b.instanceID)
		}
		return nil
	}
	if !replaced && !e.offers.HadBox() {
		// No box is running and none was ever bought for this demand: that is a start that has
		// not happened yet, not a loss. The start path owns it.
		return nil
	}
	// The ledger is consumed here: without this one interruption would be detected again on
	// every tick until the replacement registered, and each detection would begin a walk.
	e.offers.ForgetBox()
	cur, had := e.offers.Current()
	if twice := e.offers.NoteInterrupted(cur.ID); twice {
		log.Printf("engines: %s: the offer %s has been interrupted twice in a row; skipping it for this demand",
			e.def.Key, cur.ID)
	}
	if had {
		e.offers.Settle(engineOfferInterrupted)
	}
	e.noteInterrupted(ctx, cur)
	// From the TOP of the list, as decision 4 says: the cheapest offer that fits is the one the
	// operator asked for, and an interruption says nothing about the offers above the one that
	// was reclaimed. The candidates are re-derived because the catalogue may have moved since
	// this demand began; the skip lists are what the restart keeps.
	next, ok := e.offers.Restart(e.candidateOffers(ctx))
	if !ok {
		log.Printf("engines: %s: the box was taken away and every offer has been skipped (%s)",
			e.def.Key, e.offerTrailLine())
		return errEngineOffersSpent
	}
	log.Printf("engines: %s: the box was taken away; rebuilding from %s (%s)", e.def.Key, next.ID, next.BuyOption())
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
	if waited, inFlight := e.offers.WaitedForBox(); inFlight && waited < e.offers.Budget()+engineBoxDepartGrace {
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
		e.offers.ForgetBox()
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
// ⚠️ The caller clears the LEDGER (forgetBox) when the box it is ending is the one the engine was
// believed to be on: a box the CP itself removed is not a box that was taken away, and leaving it
// in the ledger would have decision 4 read the departure as an interruption on the next demand.
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
// with only the ledger to answer it — which a row widened to several types (decision 1) could
// not answer before: the offer id names the RANGE, not what was actually bought. instanceType is
// EC2 Fleet's own answer (engineFleetInstanceType), logged even when it duplicates a single-type
// row's only choice, so every line has the same shape to read or to query for later. Uptime is
// not this line's job: it is the gap between this timestamp and the matching
// "engine.<key>.box"/"engine.<key>.interrupted" line for the same instance id, both already
// written (noteBoxAction, noteInterrupted) — recomputing it here would be a second, driftable
// copy of what those lines already say once.
func (e *engineRuntimeState) noteOffer(ctx context.Context, c engineClass, instanceID, instanceType, after string) {
	if e == nil || e.audit == nil {
		return
	}
	detail := fmt.Sprintf("buy=%s instance=%s type=%s price=%.2f", c.BuyOption(), instanceID, instanceType, c.UsdPerHour)
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
	if !e.offers.NoteNoOfferOnce() {
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
	for _, a := range e.offers.Attempts() {
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
