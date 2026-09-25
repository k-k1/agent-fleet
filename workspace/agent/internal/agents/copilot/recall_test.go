package copilot

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestRecallFrom(t *testing.T) {
	// Shapes as copilot 1.0.88 writes them (trimmed).
	cases := []struct {
		name  string
		lines []string
		want  agents.RecalledSettings
	}{
		{"nothing recorded", []string{`{"type":"user.message","data":{"content":"hi"}}`}, agents.RecalledSettings{}},
		{"launch settings", []string{
			`{"type":"session.start","data":{"selectedModel":"gpt-5-mini","reasoningEffort":"low"}}`,
		}, agents.RecalledSettings{Model: "gpt-5-mini", Effort: "low"}},
		{"requested model refused, fell back to auto", []string{
			`{"type":"session.model_change","data":{"source":"startup","newModel":"gpt-5-mini","previousModel":"gpt-5-mini","reasoningEffort":"low"}}`,
			`{"type":"session.model_change","data":{"cause":"initial_resolution","source":"automatic","newModel":"auto","previousModel":"gpt-5-mini"}}`,
		}, agents.RecalledSettings{Model: "auto", ClearEffort: true}},
		{"picker switch with an effort", []string{
			`{"type":"session.resume","data":{"selectedModel":"auto","reasoningEffort":null,"autoTier":"efficiency"}}`,
			`{"type":"session.model_change","data":{"previousModel":"auto","newModel":"claude-sonnet-5","reasoningEffort":"high","source":"model_picker"}}`,
		}, agents.RecalledSettings{Model: "claude-sonnet-5", Effort: "high"}},
		{"mode toggles", []string{
			`{"type":"session.mode_changed","data":{"previousMode":"interactive","newMode":"plan"}}`,
			`{"type":"session.mode_changed","data":{"previousMode":"plan","newMode":"autopilot"}}`,
		}, agents.RecalledSettings{Mode: "normal"}},
		{"plan kept", []string{
			`{"type":"session.mode_changed","data":{"previousMode":"interactive","newMode":"plan"}}`,
			`{"type":"session.info","data":{"message":"Auto routing profile change to Intelligence requested.","infoType":"model"}}`,
		}, agents.RecalledSettings{Mode: "plan"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recallFrom(strings.NewReader(strings.Join(c.lines, "\n"))); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
