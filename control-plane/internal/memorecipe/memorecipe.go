// Package memorecipe is the run recipe a queue message may carry in place of its category's
// settings (ADR 0137, addendum 2026-11-23): the agent, model, effort and route of its run. The
// recipe is stored as one canonical JSON object in memo.recipe ("" for none) and is part of the
// content a release is bound to. The spend cap stays the category's.
package memorecipe

import (
	"encoding/json"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Recipe is the override a message carries.
type Recipe struct {
	AgentKind string `json:"agentKind,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Route     string `json:"route,omitempty"`
}

// Empty says whether the recipe overrides nothing.
func (r Recipe) Empty() bool { return r == Recipe{} }

// Encode is the column's value: canonical (fixed key order, omitted empties), "" for none, so two
// equal recipes are equal strings and a message without one hashes as it did before recipes.
func (r Recipe) Encode() string {
	if r.Empty() {
		return ""
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// Of reads a stored recipe. A value that does not parse reads as none: the column is written only
// by Encode.
func Of(m store.Memo) Recipe {
	var r Recipe
	if m.Recipe != "" {
		_ = json.Unmarshal([]byte(m.Recipe), &r)
	}
	return r
}

// Trim strips the space around every field.
func Trim(r Recipe) Recipe {
	return Recipe{AgentKind: strings.TrimSpace(r.AgentKind), Model: strings.TrimSpace(r.Model),
		Effort: strings.TrimSpace(r.Effort), Route: strings.TrimSpace(r.Route)}
}

// Set is m with the given fields of its recipe replaced: a nil pointer leaves a field, a pointer to
// "" clears it.
func Set(m store.Memo, kind, model, effort, route *string) store.Memo {
	r := Of(m)
	if kind != nil {
		r.AgentKind = *kind
	}
	if model != nil {
		r.Model = *model
	}
	if effort != nil {
		r.Effort = *effort
	}
	if route != nil {
		r.Route = *route
	}
	m.Recipe = Trim(r).Encode()
	return m
}

func kindOf(kind string) string {
	if kind == "" {
		return "claude"
	}
	return kind
}

// Effective is what a message's run uses: the message's recipe over the category's, field
// by field. A message that names another agent than the category's does not inherit the category's
// model, effort or route: they belong to the other agent and would fail its create.
func Effective(q store.MemoQueue, m store.Memo) Recipe {
	r := Of(m)
	out := Recipe{AgentKind: kindOf(q.AgentKind), Model: q.Model, Effort: q.Effort, Route: q.Route}
	if r.AgentKind != "" && kindOf(r.AgentKind) != out.AgentKind {
		out = Recipe{AgentKind: r.AgentKind}
	}
	if r.Model != "" {
		out.Model = r.Model
	}
	if r.Effort != "" {
		out.Effort = r.Effort
	}
	if r.Route != "" {
		out.Route = r.Route
	}
	return out
}

// WithRecipe is the queue's settings with the effective recipe of m in the run fields.
func WithRecipe(q store.MemoQueue, m store.Memo) store.MemoQueue {
	e := Effective(q, m)
	q.AgentKind, q.Model, q.Effort, q.Route = e.AgentKind, e.Model, e.Effort, e.Route
	return q
}

// HashParts is appended to the content hash of a message that carries a recipe, and
// nothing otherwise, so the release hash of every message without one is unchanged.
func HashParts(m store.Memo) []string {
	if m.Recipe == "" {
		return nil
	}
	return []string{"recipe", m.Recipe}
}
