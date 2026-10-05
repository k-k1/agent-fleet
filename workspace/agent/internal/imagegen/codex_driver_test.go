package imagegen

import (
	"context"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/modelfallback"
)

func stubCatalog(t *testing.T, ids ...string) {
	t.Helper()
	old := codexCatalog
	t.Cleanup(func() { codexCatalog = old })
	codexCatalog = func() []agents.ModelChoice {
		var out []agents.ModelChoice
		for _, id := range ids {
			out = append(out, agents.ModelChoice{ID: id})
		}
		return out
	}
}

// The driver comes from the signed-in account's catalog, not from a pinned id: gpt-5.4-mini was
// rejected with HTTP 400 on a ChatGPT login (#1711).
func TestCodexDriverFollowsTheLiveCatalog(t *testing.T) {
	t.Setenv("AF_IMAGEGEN_CODEX_MODEL", "")
	stubCatalog(t, "gpt-6.1-sol", "gpt-5.6-luna", "gpt-6-luna", "gpt-5.5")
	if got := codexDriver(); got != "gpt-6-luna" {
		t.Fatalf("driver = %q, want the newest listed -luna", got)
	}
	// With no catalog the last resort is the registry's id, never the rejected one.
	stubCatalog(t)
	if got := codexDriver(); got != modelfallback.ChatCodex || got == "gpt-5.4-mini" {
		t.Fatalf("driver = %q, want %q", got, modelfallback.ChatCodex)
	}
}

func TestCodexDriverEnvOverrideWins(t *testing.T) {
	t.Setenv("AF_IMAGEGEN_CODEX_MODEL", "pinned-x")
	stubCatalog(t, "gpt-6-luna")
	if got := codexDriver(); got != "pinned-x" {
		t.Fatalf("driver = %q", got)
	}
}

// The turn must actually be started with the discovered model.
func TestCodexGenerateRunsOnTheDiscoveredDriver(t *testing.T) {
	t.Setenv("AF_IMAGEGEN_CODEX_MODEL", "")
	stubCatalog(t, "gpt-6-luna")
	home := t.TempDir()
	p := &codexProvider{home: home}
	p.exe = fakeCodex(t, home, nil, `{"type":"thread.started","thread_id":"th-1"}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, 0)
	res, _ := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"})
	if res.Model != "gpt-6-luna" {
		t.Fatalf("ran on %q", res.Model)
	}
}

// A 400 "model not supported" must name the setting that is the way out.
func TestCodexModelRejectionNamesTheOverride(t *testing.T) {
	home := t.TempDir()
	p := &codexProvider{model: "gpt-x", home: home}
	p.exe = fakeCodex(t, home, nil, `{"type":"thread.started","thread_id":"th-1"}
{"type":"turn.failed","error":{"message":"{\"status\":400,\"error\":{\"message\":\"The 'gpt-x' model is not supported when using Codex with a ChatGPT account.\"}}"}}
{"type":"error","message":"{\"status\":400,\"error\":{\"message\":\"The 'gpt-x' model is not supported when using Codex with a ChatGPT account.\"}}"}`, 1)
	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"})
	if err == nil || !strings.Contains(err.Error(), "AF_IMAGEGEN_CODEX_MODEL") {
		t.Fatalf("err = %v, want a hint naming AF_IMAGEGEN_CODEX_MODEL", err)
	}
	// An unrelated failure carries no such hint.
	if h := modelHint("rate limited", "m"); h != "" {
		t.Fatalf("hint on unrelated error: %q", h)
	}
}
