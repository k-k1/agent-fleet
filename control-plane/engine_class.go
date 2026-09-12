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
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineClass is one rung: which boxes this role may buy, and how much VRAM the operator says
// they have.
//
// VramMiB does two jobs on purpose (ADR 0074 decision 1). It is the filter that decides which
// cards qualify — under ADR 0071 as the capacity provider's `AcceleratorTotalMemoryMiB.Min`, and
// under ADR 0077 as the operator's own declaration, since a purchase names the instance types
// outright — and it is what a model's demand is compared against. It is
// the CARD'S PHYSICAL SIZE, read off the hardware (22000 for an L4, which reports
// `Total VRAM 22563 MB`) rather than a placement filter or a shaded-down cap. Measured
// 2026-09-11: a demand that includes the KV cache puts a 30B at 32k context at 20,712 MiB,
// which fits an L4 with 1.8 GB to spare and does not fit a rung that declared 21000.
//
// UsdPerHour is display-only and optional. Nothing computes with it, and 0 means "not
// declared", which the panel renders as no figure at all rather than as free.
type engineClass struct {
	ID        string
	Label     string
	VramMiB   int
	Types     []string
	VCpuMin   int32
	VCpuMax   int32
	MemMinMiB int32
	MemMaxMiB int32
	// UsdPerHour is what the operator says an hour of this rung costs. 0 = undeclared.
	//
	// ⚠️ ADR 0075 decision 1 turned the way it is written into a convention: it is the DEAREST
	// type the offer could buy. A row widened to three types has a price RANGE, not a price —
	// g6.xlarge at $0.58 and g6e.xlarge at $1.36 — and a figure written from the cheap end would
	// make the number useless for the comparison it exists for. Still display-only: nothing
	// computes with it, and the CP does not order offers by it.
	//
	// 🔴 It is the EC2 PRICE ITSELF (ADR 0077 decision 8). Under ADR 0071 the box came from
	// Managed Instances and the convention included its 7.80% management fee (measured); the CP
	// buys the instance directly now, so that fee is not charged and a figure carrying it would
	// overstate every offer. Writing the Cost Explorer figure stays the rule.
	UsdPerHour float64
	// Buy is the purchase option this offer asks for: "od" or "spot" (ADR 0075 decision 1).
	// Empty means on-demand — which is what makes an ADR 0074 ladder readable unchanged as an
	// all-on-demand offer list. Read through buy(), never directly.
	Buy string
}

// The two purchase options. They are the operator's vocabulary in the engine table AND what the
// CP writes into `DefaultTargetCapacityType` (ADR 0077 decision 1), so the strings are load
// bearing in two places at once — and the second of them is on the box, as `af-engine-buy`.
const (
	engineBuyOnDemand = "od"
	engineBuySpot     = "spot"
)

// buy is the offer's purchase option, defaulting to on-demand.
func (c engineClass) buy() string {
	if c.Buy == engineBuySpot {
		return engineBuySpot
	}
	return engineBuyOnDemand
}

// label is what a person reads. Falls back to the id, never to an empty string: a select with
// a blank option is a control with no way to tell the rungs apart.
func (c engineClass) label() string {
	if s := strings.TrimSpace(c.Label); s != "" {
		return s
	}
	return c.ID
}

// parseEngineClasses reads the offer list one role declares (ADR 0074's ladder, with ADR 0075's
// eighth column):
//
//	id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour|buy
//
// separated by ";" or a newline (a CloudFormation parameter is one line; an env file is easier
// to read over several). The last two fields are optional and so are their whole columns: a
// SEVEN-field row is a valid on-demand offer, which is what lets an existing `<role>Classes`
// ladder move to `<role>Offers` with nothing edited.
//
// A malformed rung is DROPPED with a log line rather than defaulted. Every field here becomes
// an instance requirement, and a rung that quietly lost its VRAM floor would buy a cheaper card
// than the operator asked for — which is invisible until a model fails to load on it.
func parseEngineClasses(spec string) []engineClass {
	var out []engineClass
	seen := map[string]bool{}
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "|")
		if len(parts) < 6 {
			log.Printf("engines: ignoring instance class %q (want id|label|vramMiB|types|vcpuMin-vcpuMax|memMinMiB-memMaxMiB[|usdPerHour])", entry)
			continue
		}
		c := engineClass{
			ID:    strings.TrimSpace(parts[0]),
			Label: strings.TrimSpace(parts[1]),
		}
		if c.ID == "" {
			log.Printf("engines: ignoring instance class %q: no id", entry)
			continue
		}
		if seen[c.ID] {
			// Two rungs of the same id would make "which one is selected" unanswerable, and
			// the setting stores the id.
			log.Printf("engines: ignoring instance class %q: duplicate id %s", entry, c.ID)
			continue
		}
		vram, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil || vram < 0 {
			log.Printf("engines: ignoring instance class %q: vramMiB %q is not a number", entry, parts[2])
			continue
		}
		c.VramMiB = vram
		for _, t := range strings.Split(parts[3], ",") {
			if t = strings.TrimSpace(t); t != "" {
				c.Types = append(c.Types, t)
			}
		}
		if len(c.Types) == 0 {
			log.Printf("engines: ignoring instance class %q: no instance type", entry)
			continue
		}
		var ok bool
		if c.VCpuMin, c.VCpuMax, ok = parseEngineClassRange(parts[4]); !ok {
			log.Printf("engines: ignoring instance class %q: vcpu range %q is not min-max", entry, parts[4])
			continue
		}
		if c.MemMinMiB, c.MemMaxMiB, ok = parseEngineClassRange(parts[5]); !ok {
			log.Printf("engines: ignoring instance class %q: memory range %q is not min-max", entry, parts[5])
			continue
		}
		// The price is the one field a typo must not cost a rung: it is a label, and dropping
		// the rung over it would take the hardware away to protect a number nothing computes
		// with (the same rule the slot ladder's optional vCPU follows).
		if len(parts) > 6 {
			if usd, err := strconv.ParseFloat(strings.TrimSpace(parts[6]), 64); err == nil && usd > 0 {
				c.UsdPerHour = usd
			} else if strings.TrimSpace(parts[6]) != "" {
				log.Printf("engines: instance class %s: ignoring the price %q", c.ID, parts[6])
			}
		}
		// The purchase option, unlike the price, IS worth dropping a row over. It decides how the
		// box is bought, so a value nobody recognises cannot be defaulted: reading an unknown
		// word as `od` would buy on-demand for an operator who wrote `sport` meaning Spot, and
		// the bill is the only place that shows.
		if len(parts) > 7 {
			switch v := strings.ToLower(strings.TrimSpace(parts[7])); v {
			case "", engineBuyOnDemand:
				c.Buy = engineBuyOnDemand
			case engineBuySpot:
				c.Buy = engineBuySpot
			default:
				log.Printf("engines: ignoring offer %q: buy %q is neither %s nor %s", entry, parts[7], engineBuyOnDemand, engineBuySpot)
				continue
			}
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out
}

// parseEngineClassRange reads "4-8". A single number means both ends, so a rung pinned to one
// size can say so without repeating itself.
func parseEngineClassRange(s string) (int32, int32, bool) {
	s = strings.TrimSpace(s)
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		hi = lo
	}
	a, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil || a <= 0 {
		return 0, 0, false
	}
	b, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil || b < a {
		return 0, 0, false
	}
	return int32(a), int32(b), true
}

// engineClassByID finds a rung. The empty id answers the DEFAULT — the first rung, which is the
// one the stack lists first — so "nothing chosen yet" and "chose the default" resolve the same
// way and no second parameter has to declare it.
func engineClassByID(list []engineClass, id string) (engineClass, bool) {
	if len(list) == 0 {
		return engineClass{}, false
	}
	if id = strings.TrimSpace(id); id == "" {
		return list[0], true
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return engineClass{}, false
}

// The four answers to "how much VRAM does this model need". Which one was used travels with
// the number, because they are not equally strong and the panel must not print them alike.
const (
	// engineVramDeclared is the operator's own measurement (engine_models.vram_mib).
	engineVramDeclared = "declared"
	// engineVramFloor is the sum of the files' declared bytes: the WEIGHTS and nothing else.
	// No KV cache, no context, no CUDA context — so it can only ever say "at least this much".
	engineVramFloor = "floor"
	// engineVramWeightsKV is the weights PLUS the KV cache the declared context window needs,
	// computed from the model's own GGUF header (ADR 0074 open question 7). Still a floor —
	// the compute buffers are not in it (measured: 77–116 MiB CUDA0) — but a much closer one:
	// for a 30B at 32768 tokens the KV cache is 3072 MiB, which is the difference between
	// "4.8 GB spare on this card" and "1.8".
	engineVramWeightsKV = "weights_kv"
	// engineVramUnknown is nobody declared either. 🔴 It is NOT zero, and it must never be
	// drawn as "this fits": a 0 here means the question was not answered.
	engineVramUnknown = "unknown"
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
	if kv := engineKVCacheMiB(engineKVGeometry{
		Layers: m.KVLayers, HeadsKV: m.KVHeadsKV, KeyLen: m.KVKeyLen, ValLen: m.KVValueLen,
	}, m.ContextTokens); kv > 0 {
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

// engineVramStrength orders the answers, so that "the weakest evidence in this set" is one
// comparison rather than a chain of special cases. It exists because the set's verdict is not
// the winning row's — a panel that said "measured" because the LARGEST row happened to be
// measured would be describing a different model than the one that fails to load.
func engineVramStrength(source string) int {
	switch source {
	case engineVramDeclared:
		return 3
	case engineVramWeightsKV:
		return 2
	case engineVramFloor:
		return 1
	}
	return 0
}

// engineClassFits reports whether the demand fits the rung, and is deliberately generous with
// the unknown: `false` here asks a human a question, and asking it about every model nobody has
// measured would train people to click through the one that matters.
func engineClassFits(c engineClass, needMiB int) bool {
	return c.VramMiB <= 0 || needMiB <= 0 || needMiB <= c.VramMiB
}

// engineClassSettingKey names the row holding the rung an administrator chose. Separate from
// engineSettings' three (mode, modeAt, demandAt) because those are the controller's and this is
// not: the VOICEVOX engine shares that struct and has no ladder.
func engineClassSettingKey(key string) string { return "engine_" + key + "_class" }

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

// engineClassesEqual compares two ladders as DECLARATIONS: same rungs, same order, same
// numbers. Order counts because the first rung is the default (decision 1).
func engineClassesEqual(a, b []engineClass) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || x.Label != y.Label || x.VramMiB != y.VramMiB || x.UsdPerHour != y.UsdPerHour ||
			x.buy() != y.buy() ||
			x.VCpuMin != y.VCpuMin || x.VCpuMax != y.VCpuMax || x.MemMinMiB != y.MemMinMiB || x.MemMaxMiB != y.MemMaxMiB {
			return false
		}
		if len(x.Types) != len(y.Types) {
			return false
		}
		for j := range x.Types {
			if x.Types[j] != y.Types[j] {
				return false
			}
		}
	}
	return true
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

// engineClassHasType reports whether a running box belongs to this rung.
func engineClassHasType(c engineClass, instanceType string) bool {
	for _, t := range c.Types {
		if t == instanceType {
			return true
		}
	}
	return false
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
// came. What is left is the two gates that are about hardware, not about ECS:
//
//  1. a start already in flight is not re-judged. The offer is chosen, the box is bought, and
//     re-running the gate every five seconds would begin the walk again and buy a second one;
//  2. a box of a DIFFERENT rung still registered means the previous one has not gone. Starting
//     now either exceeds the vCPU quota or places the task straight back onto the old card
//     (ADR 0071 decision 7's drain wait, inherited by ADR 0077 decision 5);
//  3. and then the one line of evidence: what is about to be loaded against what the card holds,
//     written to the log BEFORE the start. CUDA does not fail in a diagnosable shape, so the one
//     place this can be recorded is in front of it.
func (e *engineRuntimeState) startGate(ctx context.Context) (bool, string) {
	if e == nil || len(e.classList()) == 0 {
		return true, ""
	}
	if e.offers.startInFlight() {
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
