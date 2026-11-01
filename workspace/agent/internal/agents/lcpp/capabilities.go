package lcpp

import (
	"context"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Optional agents capabilities of the lcpp kind.

// EngineModels is the chat-role engine catalog as a model menu (package main's lcppModels: a
// member's own connection first, else the deployment's "llm" engines). The engine catalog IS
// lcpp's model list (ADR 0093 decision 7), and only package main knows the Control Plane, so it
// sets this at startup, the same seam shape as harness.EngineToken. nil means no catalog.
var EngineModels func(ctx context.Context) []agents.ModelChoice

// ListModels is agents.ModelLister over EngineModels, unshaped.
func (agentImpl) ListModels(q agents.ModelQuery) agents.ModelListing {
	var list []agents.ModelChoice
	if EngineModels != nil {
		list = EngineModels(q.Ctx)
	}
	return agents.ModelListing{Models: list, Enumerated: -1}
}

// SkillsLayout is agents.SkillsLayouter: lcpp drives no CLI, so skills are foreign only (ADR 0093
// decision 5), the same set harness.SystemPrompt's foreignSkillsPrompt reads. Its file tools
// refuse any path outside the session's CWD (harness resolvePath), hence FenceToCWD.
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{Native: agents.SkillsForeignOnly, FenceToCWD: true}
}

// ManagedLifecycle is agents.ManagedLifecycler: this package's ReconcileManaged, AbortManaged
// and Shutdown.
func (agentImpl) ManagedLifecycle() agents.ManagedLifecycle {
	return agents.ManagedLifecycle{Reconcile: ReconcileManaged, Abort: AbortManaged, Shutdown: Shutdown}
}

// ConnStatus is the lcpp entry of GET /connections (package main's lcppStatus: the display
// setting, whether the member's LAN connection is configured, never its API key, and its last
// observed reachability, never a fresh dial). It reads state only package main holds, so main
// sets it at startup, the same seam shape as EngineModels. nil means no entry.
var ConnStatus func(s *secrets.Data) map[string]any

// ConnectionStatus is agents.ConnectionStatuser over ConnStatus.
func (agentImpl) ConnectionStatus(in agents.StatusInputs) map[string]any {
	if ConnStatus == nil {
		return nil
	}
	return ConnStatus(in.Secrets)
}

// UsageMeasured is agents.UsageMeasurer: usagex.MeasuredExact.
// lcpp (ADR 0093 decision 8): llama-server's own `usage` (prompt_tokens/completion_tokens)
// is exact and rides the stream (CP gateway's askForStreamUsage forces
// stream_options.include_usage), and this kind never runs it through usagex.WindowGuess or
// any other estimator: it is the executor, so its own store's KindUsage record (store.go AppendUsage) is the number the
// engine reported, not a derived one.
func (agentImpl) UsageMeasured() string { return usagex.MeasuredExact }
