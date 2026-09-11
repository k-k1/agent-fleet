package main

// engine_table_reload.go — the engine TABLE, re-read while the Control Plane runs
// (ADR 0074, the gap PR #520 measured).
//
// "Models are the catalogue, the chassis is the table" is the split ADR 0072 decision 7 drew:
// what an engine may load is a database row an administrator edits, and what the engine IS —
// its service, its health path, its GPU ladder — is the table 60-engines publishes to SSM. The
// catalogue half was made live. The table half was read ONCE, in newEngineRegistry, so
// changing a rung with a CloudFormation update reached a running CP only after a
// `force-new-deployment` of the CP itself: about 100 seconds of blue/green for a change that
// costs CloudFormation one API call. An exception to the whole point of the split, and an
// expensive one to discover — nothing says the ladder on screen is the ladder in the table.
//
// So the table is polled, on the same 10-second cadence the pending reader uses, and compared
// as TEXT first: an unchanged parameter costs one GetParameter and no parsing, which is what
// makes six calls a minute the right price.
//
// 🔴 What is taken live is the LADDER and the capacity provider's NAME, and nothing else. Those
// two are alike in the way that matters: both are what this process SAYS to ECS, and neither is
// something an object was built around. The name had to join the ladder because replacing a
// capacity provider renames it — the Spot swap — and a CP still addressing the old name applies
// the rung to a provider that no longer exists, matches no box, and shows a card the engine is
// not on (#536 step 4).
//
// Everything else in a row is wired into objects that were built once and are being used right
// now: the ECS client and cluster of engineECS, the controller's own goroutine and its
// intervals, the capacity client that is attached only when a ladder existed at start, the
// demand window. Swapping those means
// replacing the runtime state, which throws away what only this process knows — the demand
// counter the controller stops the engine on, the warm model, the rung this process last
// applied — and starting a second controller for the same engine. A change to any of them is
// LOGGED as needing a restart instead. Saying so is the honest half; pretending otherwise
// would put a panel's word against a deployment's behaviour, which is the defect above.

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// engineTableReloader re-reads one SSM parameter and carries the ladder into the registry.
type engineTableReloader struct {
	api  engineSSMAPI
	name string
	reg  *engineRegistry
	// last is the RAW value this reloader has already acted on. Text, not a parsed table: the
	// common case by far is "nothing changed", and that has to cost a string compare.
	//
	// A value that does not parse is recorded here too, so a broken table is reported once
	// rather than every ten seconds — and the registry keeps what it had, because a table
	// nobody can read is not a table that says there are no engines.
	last string
}

func newEngineTableReloader(api engineSSMAPI, name string, reg *engineRegistry, current string) *engineTableReloader {
	if api == nil || strings.TrimSpace(name) == "" || reg == nil {
		// No SSM (an inline AF_ENGINES_JSON table, a dev CP): there is nothing to re-read, and
		// an inline table changes only when the process is restarted anyway.
		return nil
	}
	return &engineTableReloader{api: api, name: strings.TrimSpace(name), reg: reg, last: current}
}

// run polls until the context is cancelled.
func (r *engineTableReloader) run(ctx context.Context) {
	if r == nil {
		return
	}
	t := time.NewTicker(engineCatalogCacheTTL)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

// tick reads the parameter once and applies what can be applied. It reports whether anything
// about the registry changed, which is what a test asserts on.
func (r *engineTableReloader) tick(ctx context.Context) bool {
	if r == nil {
		return false
	}
	out, err := r.api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(r.name)})
	if err != nil {
		// Not "there are no engines". A CP that lost SSM for a minute keeps the table it has;
		// the alternative is an engine list that empties itself over a transient API error.
		return false
	}
	raw := aws.ToString(out.Parameter.Value)
	if raw == r.last {
		return false
	}
	table, perr := parseEngineTable(raw)
	if perr != nil {
		r.last = raw // once, not every tick
		log.Printf("engines: the table at %s changed and cannot be read (%v) - keeping the one this process started with", r.name, perr)
		return false
	}
	r.last = raw
	return r.apply(table)
}

// apply carries the new table into the registry. Every branch either changes something or says
// why it did not.
func (r *engineTableReloader) apply(table engineTable) bool {
	changed := false
	seen := map[string]bool{}
	for _, d := range table.Engines {
		seen[d.Key] = true
		e := r.reg.get(d.Key)
		if e == nil {
			// A role added by a CloudFormation update. Registering it here would mean building
			// an ECS client, a controller and its goroutine from inside a poll, and the engine
			// would come up with none of the state the rest of this process assumes.
			log.Printf("engines: the table now declares %s, which this process does not serve - restart the Control Plane to pick it up", d.Key)
			continue
		}
		if e.def.external() {
			// An engine somebody else runs has no ladder to carry, no capacity provider to
			// rename and no service to ask for a restart over (ADR 0076 decision 2). Silently:
			// the synthesised AF_COMFY_URL row is not in this table at all, so every branch
			// below would fire on every change of any OTHER row and write a restart request
			// about a row the table never mentioned.
			continue
		}
		next := parseEngineClasses(d.offersSpec())
		// 🔴 Adopting a ladder (or dropping the last rung) is not a rung change: the capacity
		// client and the controller's start gate are attached at construction only when a
		// ladder exists, so a ladder that appears here would be a list the panel shows and
		// nothing enforces — the exact "the gate is bypassed and it lands quietly on the old
		// card" failure ADR 0074 decision 4 is about.
		if (len(next) == 0) != (len(e.classList()) == 0) {
			log.Printf("engines: %s went from %d to %d instance class(es) in the table - restart the Control Plane to pick that up",
				d.Key, len(e.classList()), len(next))
		} else if e.setClasses(next) {
			changed = true
			log.Printf("engines: %s instance classes re-read from %s: %s", d.Key, r.name, engineClassIDs(next))
		}
		// The capacity provider NAMES, live, as a pair (ADR 0075 decision 3). What a rename must
		// not do is leave the rung this process applied to the old provider standing as a note —
		// setCapacityProviders forgets it, and the next start re-applies the rung to the new one
		// (startGate). The Spot name travels the same way for the same reason, and it arrives
		// this way on every deployment that adopts ADR 0075 without replacing its CP.
		if e.setCapacityProviders(d.CapacityProvider, d.SpotCapacityProvider) {
			changed = true
			od, spot := e.ecs.providers()
			log.Printf("engines: %s capacity providers re-read from %s: %s / %s (spot)",
				d.Key, r.name, engineProviderLabel(od), engineProviderLabel(spot))
		}
		if why := engineDefDriftedBeyondClasses(e.def, d); why != "" {
			log.Printf("engines: %s changed in the table in a way this process cannot take live (%s) - restart the Control Plane", d.Key, why)
		}
	}
	for _, e := range r.reg.list() {
		if e.def.external() {
			// It was never IN this table — an external row comes from the environment — so its
			// absence says nothing (ADR 0076 decision 2).
			continue
		}
		if !seen[e.def.Key] {
			// Deliberately still registered and still controlled (the peer decision on this
			// change): stopping a role because a table stopped mentioning it would take a GPU
			// away from whatever is using it, on the strength of one poll. The operator's own
			// route out is `mode=off`, which is a decision rather than an inference.
			log.Printf("engines: the table no longer declares %s - it stays registered until the Control Plane restarts", e.def.Key)
		}
	}
	return changed
}

// engineDefDriftedBeyondClasses names the first field of a row that changed and cannot be
// carried into a running process, or "" when only the ladder and the capacity provider moved.
//
// 🔴 `capacityProvider` is deliberately NOT in this list. It used to be, and that is what made
// the Spot swap need a Control Plane replacement: replacing a capacity provider renames it, the
// table said the new name, and the running CP kept addressing the old one — the rung apply went
// to a name that no longer existed (400), `box` matched nothing so the panel reported the card
// the engine was NOT on, and only `update-service --force-new-deployment` (217 seconds) cleared
// it (#536 step 4, the same shape as ADR 0074 decision 4). Unlike everything below, the name is
// a destination string, not something an object was built around.
func engineDefDriftedBeyondClasses(was, now engineDef) string {
	for _, c := range []struct{ what, a, b string }{
		{"service", was.Service, now.Service},
		{"url", was.URL, now.URL},
		{"health", was.Health, now.Health},
		{"provider", was.Provider, now.Provider},
		{"api", was.API, now.API},
		{"api key parameter", was.APIKeyParam, now.APIKeyParam},
	} {
		if strings.TrimSpace(c.a) != strings.TrimSpace(c.b) {
			return c.what
		}
	}
	// The numbers the controller was built with. Its intervals are computed once in
	// engineControlCfgFor and live in the goroutine that is running.
	if was.IdleSec != now.IdleSec {
		return "idle"
	}
	if was.StartDeadlineSec != now.StartDeadlineSec {
		return "start deadline"
	}
	// The per-offer budget is read off the row this process started with (ADR 0075 decision 5),
	// like the two above and for the same reason: nothing re-reads the row itself, only the
	// offer list and the provider names are carried live.
	if was.OfferBudgetSec != now.OfferBudgetSec {
		return "offer budget"
	}
	return ""
}

// engineProviderLabel names the capacity provider for the reload log. Empty is a real value —
// the row went to Fargate — and printing nothing there reads as a truncated line.
func engineProviderLabel(name string) string {
	if name == "" {
		return "(none - Fargate)"
	}
	return name
}

// engineClassIDs is the ladder in one line, for the log that says a reload happened. The ids
// in declaration order, because the first is the default.
func engineClassIDs(classes []engineClass) string {
	ids := make([]string, 0, len(classes))
	for _, c := range classes {
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}
