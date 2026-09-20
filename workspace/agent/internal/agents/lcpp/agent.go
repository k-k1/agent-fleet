// Package lcpp is the vertical package for the lcpp kind (ADR 0093): a harness that talks
// directly to llama-server instead of driving a vendor CLI. This file wires only the
// read-layer agents.Agent contract into the kind registry, as the first step of ADR 0093
// stage 2 — the harness core (internal/harness), the managed driver (agents.Driver) and the
// transcript writer are separate, later work and are not connected here.
//
// lcpp is the first kind with no Terminal(CLI) route at all (ADR 0093 decision 2): there is no
// program to put in a tmux pane, so BuildLaunch always fails and the kind is managed-only
// (agents.Caps.ManagedOnly).
package lcpp

import (
	"errors"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ErrNoTerminalRoute is BuildLaunch's unconditional refusal: lcpp has no tmux pane program, so
// this kind can never launch on the tui route (ADR 0093 decision 2).
var ErrNoTerminalRoute = errors.New("llama.cpp セッションには Terminal(CLI) 実行方式がありません（managed 専用の kind です）")

// New returns the lcpp Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl embeds NoGenericTranscript: the harness's own transcript store (JSONL,
// AgentStateDir()/lcpp/sessions/<sid>.jsonl per ADR 0093 decision 3, docs/log/99) doesn't wire
// into anything here yet, so Transcript() correctly answers ok=false until a later stage wires
// it up.
type agentImpl struct{ agents.NoGenericTranscript }

func (agentImpl) Kind() string { return session.KindLcpp }

// Caps: only ManagedOnly is true at this stage. The other flags name capabilities the harness
// doesn't have yet (own transcript store, approval loop) — leaving them false is correct until
// the work that makes them real lands, per docs/log/76's "never claim an unverified cap".
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{ManagedOnly: true}
}

// BuildLaunch always fails: lcpp has no tmux pane program (ADR 0093 decision 2).
func (agentImpl) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, ErrNoTerminalRoute
}

// WireLive has nothing to report yet — the managed driver that would supply live state
// (status, context fill, last-say) is a later stage of ADR 0093.
func (agentImpl) WireLive(session.Meta, bool) agents.LiveInfo {
	return agents.LiveInfo{}
}

// ClearResume is a no-op: the resume-id store this would clear doesn't exist yet.
func (agentImpl) ClearResume(string) {}
