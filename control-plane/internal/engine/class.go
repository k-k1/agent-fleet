package engine

// class.go — the declared GPU ladder and its offers (ADR 0074, ADR 0075, ADR 0077): the rung
// type, the parser for the `<role>Offers` / `<role>Classes` column and the pure predicates over
// a list of rungs. Nothing here touches the runtime state; the methods that read the stored
// rung stay in package main (engine_class.go) because they hang off engineRuntimeState.

import (
	"log"
	"strconv"
	"strings"
)

// Class is one rung: which boxes this role may buy, and how much VRAM the operator says
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
type Class struct {
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
	// all-on-demand offer list. Read through BuyOption(), never directly.
	Buy string
}

// The two purchase options. They are the operator's vocabulary in the engine table AND what the
// CP writes into `DefaultTargetCapacityType` (ADR 0077 decision 1), so the strings are load
// bearing in two places at once — and the second of them is on the box, as `af-engine-buy`.
const (
	BuyOnDemand = "od"
	BuySpot     = "spot"
)

// BuyOption is the offer's purchase option, defaulting to on-demand.
func (c Class) BuyOption() string {
	if c.Buy == BuySpot {
		return BuySpot
	}
	return BuyOnDemand
}

// DisplayLabel is what a person reads. Falls back to the id, never to an empty string: a select with
// a blank option is a control with no way to tell the rungs apart.
func (c Class) DisplayLabel() string {
	if s := strings.TrimSpace(c.Label); s != "" {
		return s
	}
	return c.ID
}

// ParseClasses reads the offer list one role declares (ADR 0074's ladder, with ADR 0075's
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
func ParseClasses(spec string) []Class {
	var out []Class
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
		c := Class{
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
		if c.VCpuMin, c.VCpuMax, ok = parseClassRange(parts[4]); !ok {
			log.Printf("engines: ignoring instance class %q: vcpu range %q is not min-max", entry, parts[4])
			continue
		}
		if c.MemMinMiB, c.MemMaxMiB, ok = parseClassRange(parts[5]); !ok {
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
			case "", BuyOnDemand:
				c.Buy = BuyOnDemand
			case BuySpot:
				c.Buy = BuySpot
			default:
				log.Printf("engines: ignoring offer %q: buy %q is neither %s nor %s", entry, parts[7], BuyOnDemand, BuySpot)
				continue
			}
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out
}

// parseClassRange reads "4-8". A single number means both ends, so a rung pinned to one
// size can say so without repeating itself.
func parseClassRange(s string) (int32, int32, bool) {
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

// ClassByID finds a rung. The empty id answers the DEFAULT — the first rung, which is the
// one the stack lists first — so "nothing chosen yet" and "chose the default" resolve the same
// way and no second parameter has to declare it.
func ClassByID(list []Class, id string) (Class, bool) {
	if len(list) == 0 {
		return Class{}, false
	}
	if id = strings.TrimSpace(id); id == "" {
		return list[0], true
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return Class{}, false
}

// The four answers to "how much VRAM does this model need". Which one was used travels with
// the number, because they are not equally strong and the panel must not print them alike.
const (
	// VramDeclared is the operator's own measurement (engine_models.vram_mib).
	VramDeclared = "declared"
	// VramFloor is the sum of the files' declared bytes: the WEIGHTS and nothing else.
	// No KV cache, no context, no CUDA context — so it can only ever say "at least this much".
	VramFloor = "floor"
	// VramWeightsKV is the weights PLUS the KV cache the declared context window needs,
	// computed from the model's own GGUF header (ADR 0074 open question 7). Still a floor —
	// the compute buffers are not in it (measured: 77–116 MiB CUDA0) — but a much closer one:
	// for a 30B at 32768 tokens the KV cache is 3072 MiB, which is the difference between
	// "4.8 GB spare on this card" and "1.8".
	VramWeightsKV = "weights_kv"
	// VramUnknown is nobody declared either. 🔴 It is NOT zero, and it must never be
	// drawn as "this fits": a 0 here means the question was not answered.
	VramUnknown = "unknown"
)

// VramStrength orders the answers, so that "the weakest evidence in this set" is one
// comparison rather than a chain of special cases. It exists because the set's verdict is not
// the winning row's — a panel that said "measured" because the LARGEST row happened to be
// measured would be describing a different model than the one that fails to load.
func VramStrength(source string) int {
	switch source {
	case VramDeclared:
		return 3
	case VramWeightsKV:
		return 2
	case VramFloor:
		return 1
	}
	return 0
}

// ClassFits reports whether the demand fits the rung, and is deliberately generous with
// the unknown: `false` here asks a human a question, and asking it about every model nobody has
// measured would train people to click through the one that matters.
func ClassFits(c Class, needMiB int) bool {
	return c.VramMiB <= 0 || needMiB <= 0 || needMiB <= c.VramMiB
}

// ClassSettingKey names the row holding the rung an administrator chose. Separate from
// engineSettings' three (mode, modeAt, demandAt) because those are the controller's and this is
// not: the VOICEVOX engine shares that struct and has no ladder.
func ClassSettingKey(key string) string { return "engine_" + key + "_class" }

// SpotSettingKey names the row holding whether this role may buy an INTERRUPTIBLE box.
//
// A second gate over the same offers the operator declared, and it is deliberate. Declaring a
// Spot row is a deployment-wide act performed once, in CloudFormation, by whoever stands the
// stack up; being interrupted is lived with by whoever runs the engine — and for the chat role
// the two are rarely the same person. So the declaration says what MAY be bought and this says
// whether the administrator accepts what it costs when the box is taken away: the answer in
// flight is lost, and the next one waits out a cold start (measured 527-586 s, which is what
// `<role>StartDeadlineSec` is sized for).
//
// Absent = NOT allowed. An engine only buys an interruptible box where somebody said so, which
// is the whole point of asking — with one consequence worth stating: a deployment that declared
// Spot offers before this existed keeps running and stops buying Spot until a super-admin ticks
// the box. That is visible on the panel (the rows are drawn, greyed, with the reason) rather
// than silent, and the alternative — defaulting to allowed because a row exists — would make
// the question unasked for exactly the deployments it is asked about.
func SpotSettingKey(key string) string { return "engine_" + key + "_spot" }

// ClassesEqual compares two ladders as DECLARATIONS: same rungs, same order, same
// numbers. Order counts because the first rung is the default (decision 1).
func ClassesEqual(a, b []Class) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || x.Label != y.Label || x.VramMiB != y.VramMiB || x.UsdPerHour != y.UsdPerHour ||
			x.BuyOption() != y.BuyOption() ||
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

// ClassesHaveSpot reports whether this role declares an interruptible offer at all. It is
// what decides whether the consent above is a question this deployment has — an all-on-demand
// list has nothing to consent to, and a panel asking anyway would be a control that changes
// nothing.
func ClassesHaveSpot(list []Class) bool {
	for _, c := range list {
		if c.BuyOption() == BuySpot {
			return true
		}
	}
	return false
}

// ClassHasType reports whether a running box belongs to this rung.
func ClassHasType(c Class, instanceType string) bool {
	for _, t := range c.Types {
		if t == instanceType {
			return true
		}
	}
	return false
}
