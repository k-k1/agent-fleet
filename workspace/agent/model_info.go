package main

// What the model picker says about each model beyond its name (Issue #1021): list price,
// context window, release date and whether it is going away, served as ModelChoice.Info by
// GET /agents/{kind}/models?info=1.
//
// The price is the API LIST price on the route the kind is billed through, read from the same
// catalog the usage view prices from (internal/usagesvc/usage_catalog.go), but on the kind's own providers only:
// the ledger's fallback to other primary providers would put a price no route bills next to a
// model. claude / codex / agy are usually subscription-billed, so the Console labels it as a
// list price, not a bill.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagesvc"
	"slices"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// modelInfoNone are the kinds nothing is said about, stated here rather than left to their
// absence from usageCatalogProviders, which the ledger may one day fill for its own reasons:
// cursor bills through its own plan and has no row on models.dev, muse's vendor
// reports `cost: null` for every model (ADR 0095 decision 10), and lcpp runs on the
// deployment's own engines. A number there would be one nobody charges.
var modelInfoNone = map[string]bool{
	session.KindCursor: true,
	session.KindMuse:   true,
	session.KindLcpp:   true,
}

// resolveModelInfo returns what is known about one (kind, model id), or nil when nothing is.
func resolveModelInfo(kind, id string) *agents.ModelInfo {
	if modelInfoNone[kind] || id == "" {
		return nil
	}
	// A kind's tier aliases run whatever the installed CLI maps them to, so only a full id a
	// member registered is looked up.
	if a, ok := agentreg.Lookup(kind); ok {
		if s, ok := a.(agents.ModelInfoSuppressor); ok && s.SuppressesModelInfo(id) {
			return nil
		}
	}
	info := &agents.ModelInfo{}
	usagesvc.FillCatalogModelInfo(info, kind, recommendPriceID(kind, id))
	if a, ok := agentreg.Lookup(kind); ok {
		if rt, ok := a.(agents.ModelRetirer); ok {
			if r, ok := rt.ModelRetirement(id); ok {
				info.Retiring = &r
			}
		}
	}
	if *info == (agents.ModelInfo{}) {
		return nil
	}
	return info
}

// withModelDetail fills Provider and, when asked, Info on a COPY of list. The lists come
// straight from each kind's cache (codex.Models returns its cached slice, and the hidden-model
// filter passes it through untouched when nothing is hidden), so writing into them would make
// one ?info=1 answer leak Info into every later answer, MCP list_models included.
func withModelDetail(kind string, list []agents.ModelChoice, withInfo bool) []agents.ModelChoice {
	out := slices.Clone(list)
	for i := range out {
		out[i].Provider = resolveModelProvider(kind, out[i].ID)
		out[i].Info = nil
		if withInfo {
			out[i].Info = resolveModelInfo(kind, out[i].ID)
		}
	}
	return out
}
