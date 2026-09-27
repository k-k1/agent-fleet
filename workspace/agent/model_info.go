package main

// What the model picker says about each model beyond its name (Issue #1021): list price,
// context window, release date and whether it is going away, served as ModelChoice.Info by
// GET /agents/{kind}/models?info=1.
//
// The price is the API LIST price on the route the kind is billed through — the same lookup
// the usage view and the recommendation use (usage_catalog.go), so the three can never name
// different numbers for one model. claude / codex / agy are usually subscription-billed, so
// the Console labels it as a list price, not a bill.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// modelInfoNone are the kinds nothing is said about. usageCatalogOrder falls back to the
// primary providers for any kind, so without this cursor's "gpt-5" would pick up openai's
// price: cursor bills through its own plan and has no row on models.dev, muse's vendor
// reports `cost: null` for every model (ADR 0095 decision 10), and lcpp runs on the
// deployment's own engines. A number there would be one nobody charges.
var modelInfoNone = map[string]bool{
	session.KindCursor: true,
	session.KindMuse:   true,
	session.KindLcpp:   true,
}

// claudeTierAliases are claude's fixed picker entries. Which model an alias runs depends on the
// installed CLI, so pricing "the newest model of that family" would be a guess; only a full id
// a member registered is looked up.
var claudeTierAliases = map[string]bool{"fable": true, "opus": true, "sonnet": true, "haiku": true}

// codexRetirement is codex.Retirement, a seam for tests: the real one answers from the last
// `codex debug models` run.
var codexRetirement = defaultCodexRetirement

var defaultCodexRetirement = codex.Retirement

// resolveModelInfo returns what is known about one (kind, model id), or nil when nothing is.
func resolveModelInfo(kind, id string) *agents.ModelInfo {
	if modelInfoNone[kind] || id == "" {
		return nil
	}
	if kind == session.KindClaude && claudeTierAliases[id] {
		return nil
	}
	info := &agents.ModelInfo{}
	if e, ok := usageCatalogLookupEntry(kind, recommendPriceID(kind, id)); ok {
		info.Price = &agents.ModelPrice{In: e.price.In, Out: e.price.Out, CacheRead: e.price.CacheRead}
		info.PriceFrom, _, _ = strings.Cut(e.ref, "/")
		info.Context = e.meta.context
		info.Released = e.meta.released
		info.Deprecated = e.deprecated
	}
	if kind == session.KindCodex {
		if r, ok := codexRetirement(id); ok {
			info.Retiring = &r
		}
	}
	if *info == (agents.ModelInfo{}) {
		return nil
	}
	return info
}
