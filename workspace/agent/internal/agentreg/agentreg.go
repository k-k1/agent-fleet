// Package agentreg is the kind registry: every session kind's agents.Agent, built from one
// explicit list with no init() side effects.
//
// It imports every agent package, so the packages those import (bridge, mcpreg, transcript
// and the like) cannot import it; they consult internal/kinds, the capability-free list of
// the same kinds. registry_test.go holds the two equal. Everything above the agent packages
// (chatx, sessionx, mcpx, package main) can import this one.
package agentreg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/shell"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/ssm"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// registry is the kind → agents.Agent registry. Of falls back to claude for an unknown or
// empty kind, matching the historical default (a session with no recognized kind launches
// claude).
var registry = map[string]agents.Agent{
	session.KindClaude:   claude.New(),
	session.KindOpencode: opencode.New(),
	session.KindCodex:    codex.New(),
	session.KindCursor:   cursor.New(),
	session.KindKiro:     kiro.New(),
	session.KindAgy:      agy.New(),
	session.KindCopilot:  copilot.New(),
	session.KindShell:    shell.New(),
	session.KindSSM:      ssm.New(),
	session.KindLcpp:     lcpp.New(),
	session.KindMuse:     muse.New(),
}

// Of returns the Agent registered for kind, or claude's for an unknown or empty kind.
func Of(kind string) agents.Agent {
	if a, ok := registry[kind]; ok {
		return a
	}
	return registry[session.KindClaude]
}

// Lookup is Of without the claude fallback: the agent registered under exactly this key.
// Optional capabilities (agents.Instructions and the like) are resolved through it, so an
// unknown or alternate spelling never answers with claude's.
func Lookup(kind string) (agents.Agent, bool) {
	a, ok := registry[kind]
	return a, ok
}

// Kinds lists every kind in the registry, sorted. It is the source the kind catalog (GET
// /agents/kinds) and the drift guards iterate: a hand-kept list of kinds is exactly what
// drifted before. Of's claude fallback is not a kind, so an unknown or alternate spelling
// never appears here.
func Kinds() []string {
	kinds := make([]string, 0, len(registry))
	for k := range registry {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// Normalize maps a create request's kind onto a registered one, defaulting the
// unknown/empty/"claude" cases to claude (the historical create whitelist).
func Normalize(kind string) string {
	if _, ok := registry[kind]; ok {
		return kind
	}
	return session.KindClaude
}

// ValidateCreate is the create paths' gate: an empty kind means claude (the documented
// default), a registered kind passes, and anything else is refused with the accepted list.
// Matching is exact — "Codex", " codex" and "kiro-cli" are errors, never rewritten.
//
// It is separate from Normalize on purpose. Normalize is the READ-side fallback (a stored
// session with an empty or legacy kind must keep rendering as claude), and folding an unknown
// kind to claude at create time starts a different agent, model and billing route than the
// caller asked for, reporting only "kind":"claude" afterwards.
func ValidateCreate(kind string) error {
	if kind == "" {
		return nil
	}
	if _, ok := registry[kind]; ok {
		return nil
	}
	return fmt.Errorf("unknown kind %q; accepted kinds: %s (empty means claude)", kind, strings.Join(Kinds(), ", "))
}

// ManagedDefault reports whether a caller that cannot drive a pane (MCP create_session, the
// scheduler) should ask for the managed driver when it names none: the registry's
// Caps().ManagedDefault, plus every ManagedOnly kind, which has nothing else. The match is
// exact and never goes through Normalize — an alternate spelling ("Codex", "kiro-cli") is not
// a registered kind and gets no driver. It feeds the kind catalog's managedDefault and the
// in-container create_session.
func ManagedDefault(kind string) bool {
	a, ok := registry[kind]
	if !ok {
		return false
	}
	c := a.Caps()
	return c.ManagedOnly || c.ManagedDefault
}

// SwapForTest registers a under kind until the returned restore is called, which puts back
// the previous entry or removes the kind again. Tests only: the registry is not locked, and
// nothing else writes it after package initialisation.
func SwapForTest(kind string, a agents.Agent) (restore func()) {
	prev, had := registry[kind]
	registry[kind] = a
	return func() {
		if had {
			registry[kind] = prev
			return
		}
		delete(registry, kind)
	}
}
