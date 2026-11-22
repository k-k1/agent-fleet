package main

// engine_class.go — which GPU an engine buys, chosen at runtime (ADR 0074).
//
// ADR 0071 put the box in CloudFormation: a role's capacity provider carries
// `AllowedInstanceTypes` and a VRAM floor, so the card is fixed at deployment. ADR 0072 then
// made the MODEL editable from the Console, which left the two halves out of step — a 23.8 GB
// checkpoint can be taken in with three clicks and lands on a 24 GB card that cannot hold it.
//
// So the box becomes a choice from a LADDER the operator declares (decision 1), which ADR 0075
// turned into a list of OFFERS and ADR 0077 made the CP buy directly. Three properties are worth
// keeping in mind when editing this file:
//
//   - the CP asks nobody what a rung means. Neither EC2 nor the Pricing API is called; the
//     numbers are the operator's, exactly as the workspace slot ladder's are (ADR 0045
//     decision 21). A rung nobody declared does not exist;
//   - a change reaches the NEXT box only, which is why a switch is stop → wait for the box to
//     leave → start rather than an update. Under ADR 0071 that was the capacity provider's own
//     rule ("These changes only apply to new Amazon ECS Managed Instances"); under ADR 0077 it
//     is simply what an instance is;
//   - with no ladder declared, nothing here calls AWS at all. A deployment that never
//     configures this must not start logging AccessDenied for a feature it does not use.

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineModelVramNeed is what one model wants in VRAM, and how well that is known.
func engineModelVramNeed(m store.EngineModel) (int, string) {
	if m.VramMiB > 0 {
		return m.VramMiB, engineVramDeclared
	}
	var total int64
	for _, f := range m.Files {
		total += f.Bytes
	}
	if total == 0 {
		return 0, engineVramUnknown
	}
	weights := int(total / (1024 * 1024))
	// The KV cache, when the row knows enough to say. Both halves are required and neither is
	// guessed: the geometry comes from the GGUF header (engine_gguf.go) and the window is the
	// operator's declared `context_tokens`, because llama.cpp allocates for the context it is
	// GIVEN, not the one the model was trained at — measured, a 1.5B whose header says 32768
	// allocated 448 MiB for the 16384 it was started with.
	if kv := engineKVCacheMiB(engineRowGeometry(m), m.ContextTokens); kv > 0 {
		return weights + kv, engineVramWeightsKV
	}
	return weights, engineVramFloor
}

// engineVramDemand is the largest demand among the models an engine would load, the id it
// belongs to, and how it was answered.
//
// The MAXIMUM, not the sum, and that is the whole reason this is a function rather than a loop
// at the call site: the llm router runs `--models-max 1` and sd-server holds one checkpoint, so
// what has to fit is one model at a time. Summing would report a deployment with five 8 GB
// models as needing 40 GB and warn about every start.
//
// An `unknown` model does not lower the demand and does not raise it either: the number
// returned is the largest KNOWN one, and the source is `unknown` when anything in the set could
// not be answered — so the panel can say "at least X, and one model did not say".
func engineVramDemand(rows []store.EngineModel) (int, string, string) {
	worst, id := 0, ""
	anyUnknown := false
	weakest := engineVramDeclared
	for _, m := range rows {
		if !m.Enabled || engineModelIsLora(m) {
			continue
		}
		need, src := engineModelVramNeed(m)
		if src == engineVramUnknown {
			anyUnknown = true
			continue
		}
		if engineVramStrength(src) < engineVramStrength(weakest) {
			weakest = src
		}
		if need > worst {
			worst, id = need, m.ID
		}
	}
	switch {
	case worst == 0:
		return 0, engineVramUnknown, ""
	case anyUnknown:
		// Something in the set could not be answered, so the largest KNOWN demand is only ever
		// a floor — whatever the winning row happened to carry.
		return worst, engineVramFloor, id
	default:
		// Mixed evidence reads as the WEAKEST present. A set holding one measured row and one
		// that only knows its bytes is not "measured": the engine loads whichever is asked for.
		return worst, weakest, id
	}
}

// engineClassSwapWaitMax bounds how long a start is held back waiting for a box of the previous
// rung to leave.
//
// The wait itself is decision 4: a draining box and a new one exceed the G-family vCPU quota on
// a deployment that has 8 of them, and the failure is a placement that silently never happens.
// The BOUND is here because the previous box's end was AWS's to decide — `scaleInAfter: -1` meant
// "never tidy up", and an unbounded gate turned that into an engine that could never start again.
// ADR 0077 decision 5 gives the terminate to the CP, so the trap is gone and the bound is now
// belt and braces. Measured drain under Managed Instances: 427-477 s, about 8 minutes from
// desired 0 to the instance being gone, so 20 minutes waits out a slow one and still gives up.
const engineClassSwapWaitMax = 20 * time.Minute

// classes reports the ladder. Nil is the normal case.
func (e *engineRuntimeState) classList() []engineClass {
	if e == nil {
		return nil
	}
	// Read under the lock because the ladder is re-read from the engine table while this
	// process runs (engine_table_reload.go): a CloudFormation update that changes a rung used
	// to reach a running CP only through a blue/green deployment of the CP itself.
	//
	// The slice is REPLACED, never appended to or written through, so handing the caller the
	// live one costs nothing and hands out nothing that can change under it.
	e.classesMu.RLock()
	defer e.classesMu.RUnlock()
	return e.classes
}

// setClasses swaps the ladder for the one the table now declares, and says whether that was a
// change. Nothing else about a row is taken live — see engine_table_reload.go for what is and
// why the rest needs a restart.
func (e *engineRuntimeState) setClasses(next []engineClass) bool {
	if e == nil {
		return false
	}
	e.classesMu.Lock()
	defer e.classesMu.Unlock()
	if engineClassesEqual(e.classes, next) {
		return false
	}
	e.classes = next
	return true
}

// setLaunchTemplate takes a replaced launch template live, reporting whether it changed.
//
// It travels the way the capacity provider's name used to (ADR 0074; #536 measured what happens
// otherwise — the CP kept addressing the provider a replacement had renamed, matched no box and
// showed a card the engine was not on). It is a destination string and nothing else: it keys
// nothing this process holds, no object was built around it, and the next purchase simply goes to
// the new one.
func (e *engineRuntimeState) setLaunchTemplate(ref string) bool {
	if e == nil || e.fleet == nil {
		return false
	}
	return e.fleet.setTemplate(ref)
}

// selectedClassID is the rung an administrator chose, or "" for the stack's default. The stored
// setting wins over everything (ADR 0074 decision 2) — that is what keeps a CloudFormation
// update from silently taking a deployment back to a smaller card.
func (e *engineRuntimeState) selectedClassID(ctx context.Context) string {
	if e == nil || e.settings == nil {
		return ""
	}
	v, _ := e.settings.GetSetting(ctx, engineClassSettingKey(e.def.Key))
	return strings.TrimSpace(v)
}

// spotAllowed reports whether an administrator has accepted interruption for this role, which is
// what lets a `spot` offer be bought at all (see engineSpotSettingKey). Anything but the stored
// "true" is no: a store that cannot be read answers the safe way round, since the cost of a
// wrong `false` is a dearer box and the cost of a wrong `true` is a conversation dropped mid-answer.
func (e *engineRuntimeState) spotAllowed(ctx context.Context) bool {
	if e == nil || e.settings == nil {
		return false
	}
	v, _ := e.settings.GetSetting(ctx, engineSpotSettingKey(e.def.Key))
	return strings.TrimSpace(v) == "true"
}

// selectedClass resolves that id against the ladder. The second result is false when this
// deployment has no ladder at all, which every caller reads as "this feature is not configured"
// rather than as an error.
//
// ⚠️ A stored id that is no longer in the ladder resolves to the DEFAULT rung and says so in
// the log. The alternative — refusing — would let an edit to the stack's ladder strand an
// engine on a setting nobody can see, and the panel shows the resolved rung either way.
func (e *engineRuntimeState) selectedClass(ctx context.Context) (engineClass, bool) {
	list := e.classList()
	if len(list) == 0 {
		return engineClass{}, false
	}
	id := e.selectedClassID(ctx)
	c, ok := engineClassByID(list, id)
	if !ok {
		log.Printf("engines: %s: the stored instance class %q is not in the ladder; using %s",
			e.def.Key, id, list[0].ID)
		return list[0], true
	}
	return c, true
}

// defaultClass is what the stack declares, i.e. the first rung. It is what the panel compares
// the selection against to decide whether to warn that this deployment is running on something
// other than its default (decision 7).
func (e *engineRuntimeState) defaultClass() (engineClass, bool) {
	list := e.classList()
	if len(list) == 0 {
		return engineClass{}, false
	}
	return list[0], true
}

// The reasons a start is held back by the class machinery. They are logged and audited beside
// the controller's own reasons, so "why did nothing start" has one place to be answered.
const (
	engineReasonClassSwapWait = "class_swap_wait" // a box of the previous rung has not gone yet
	// engineReasonNoOffer is ADR 0075 decision 2's refusal: every declared offer is smaller than
	// the model needs, so there is no box to buy. NOT the same as "it might not fit" (decision 6
	// of ADR 0074, which warns and starts anyway) — this is an arithmetic contradiction between
	// two declarations the operator made.
	engineReasonNoOffer = "no_offer"
)

// startGate is what the controller asks before it buys a box (ADR 0074 decisions 4 and 5, as ADR
// 0077 decision 8 left them).
//
// 🔴 ADR 0074 decision 5's third step — write the rung to the capacity provider, idempotently,
// because CloudFormation reverts it — IS GONE, and with it the whole `UpdateCapacityProvider`
// read-modify-write. A rung is now the set of instance types in the `CreateFleet` overrides: the
// declaration IS the request, so "declared and actual drift apart" has no structure to happen in
// and a misspelt type is refused on the spot rather than three minutes later by a box that never
// came. What is left is the gates that are about hardware, not about ECS:
//
//  1. a start already in flight is not re-judged. The offer is chosen, the box is bought, and
//     re-running the gate every five seconds would begin the walk again and buy a second one;
//  2. NEITHER IS A START ON AN ENGINE THAT IS ALREADY UP. The admin toggle presses this gate on
//     every `mode: on`, including one pressed while the engine is running, and the walk it would
//     begin is a walk for a demand that is already being served;
//  3. a box of a DIFFERENT rung still registered means the previous one has not gone. Starting
//     now either exceeds the vCPU quota or places the task straight back onto the old card
//     (ADR 0071 decision 7's drain wait, inherited by ADR 0077 decision 5);
//  4. and then the one line of evidence: what is about to be loaded against what the card holds,
//     written to the log BEFORE the start. CUDA does not fail in a diagnosable shape, so the one
//     place this can be recorded is in front of it.
//
// 🔴 (1) and (2) are BEFORE begin(), and that ordering is the whole of ADR 0077 P1 hardware run
// 2's one red point. `begin()` empties the trail, so a gate that reached it and only then found
// out the start was redundant left the panel with no `offer_trail` for the demand it is serving:
// measured on hardware — `mode: on` pressed on a running engine bought nothing (the start's own
// guard held) and blanked the trail anyway.
func (e *engineRuntimeState) startGate(ctx context.Context) (bool, string) {
	if e == nil || len(e.classList()) == 0 {
		return true, ""
	}
	if e.offers.startInFlight() {
		return true, ""
	}
	// Already up, or on its way. Read from the service rather than from what this process
	// remembers, for the same reason everything else here is: a CP replaced mid-demand knows
	// nothing, and the desired count is the one fact that says whether a start is needed.
	if v, err := e.ecs.view(ctx); err == nil && v.desired >= 1 {
		return true, ""
	}
	// Which offers this start may buy from, in the order they will be tried (ADR 0075 decisions
	// 2 and 8). The FIRST of them is what everything below is about; the rest are what the walk
	// falls through to, and they are handed to the run here so that the fallback list is the one
	// this start was judged on rather than one re-derived a minute later from a changed
	// catalogue.
	cands := e.candidateOffers(ctx)
	if len(cands) == 0 {
		e.noteNoOffer(ctx)
		return false, engineReasonNoOffer
	}
	e.offers.begin(cands)
	if b, on := e.ecs.box(ctx); on && b.instanceType != "" && !engineClassHasType(cands[0], b.instanceType) {
		if e.swapWaitExpired() {
			log.Printf("engines: %s: a %s box is still registered after %s; starting on the old class anyway",
				e.def.Key, b.instanceType, engineClassSwapWaitMax)
		} else {
			return false, engineReasonClassSwapWait
		}
	} else {
		e.clearSwapWait()
	}
	e.logVramFit(ctx, cands[0])
	return true, ""
}

// swapWaitExpired reports whether the wait for the previous box has run past its bound, and
// starts the clock the first time it is asked.
func (e *engineRuntimeState) swapWaitExpired() bool {
	e.appliedMu.Lock()
	defer e.appliedMu.Unlock()
	if e.swapWaitSince.IsZero() {
		e.swapWaitSince = time.Now()
		return false
	}
	return time.Since(e.swapWaitSince) >= engineClassSwapWaitMax
}

func (e *engineRuntimeState) clearSwapWait() {
	e.appliedMu.Lock()
	e.swapWaitSince = time.Time{}
	e.appliedMu.Unlock()
}

// logVramFit writes the one line that makes a CUDA death readable afterwards (decision 6).
//
// It states the SOURCE of the number as well as the number: "at least 23,000 MiB (weights
// only)" and "23,000 MiB (measured)" are different claims, and an operator reading this after a
// failed start needs to know which one was being relied on.
func (e *engineRuntimeState) logVramFit(ctx context.Context, c engineClass) {
	need, source, id := engineVramDemand(e.catalog.list(ctx))
	switch {
	case source == engineVramUnknown:
		log.Printf("engines: %s: starting on %s (%d MiB VRAM declared); no model declares what it needs",
			e.def.Key, c.ID, c.VramMiB)
	case !engineClassFits(c, need):
		log.Printf("engines: %s: starting on %s (%d MiB VRAM declared) with %s wanting %d MiB (%s) — this may not fit",
			e.def.Key, c.ID, c.VramMiB, id, need, source)
	default:
		log.Printf("engines: %s: starting on %s (%d MiB VRAM declared); largest model %s wants %d MiB (%s)",
			e.def.Key, c.ID, c.VramMiB, id, need, source)
	}
}

// classStartHeld is startGate for the admin route: it asks the same question and says so in the
// log, but never turns a "cannot start yet" into a failed request. The administrator's intent is
// stored either way, and the controller acts on it as soon as the previous box has gone.
func (e *engineRuntimeState) classStartHeld(ctx context.Context) bool {
	if e == nil || len(e.classList()) == 0 {
		return false
	}
	ok, why := e.startGate(ctx)
	if !ok {
		log.Printf("engines: %s: mode=on stored, but the start waits (%s)", e.def.Key, why)
	}
	return !ok
}
